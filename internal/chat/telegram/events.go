package telegram

import (
	"context"

	"aurora-capcompute/aurora"
	"aurora-k8s-agent/internal/chat/telegram/state"
	chattimers "aurora-k8s-agent/internal/chat/timers"
	"aurora-k8s-agent/internal/transport/telegram"
)

func (s *Service) handleEvent(ctx context.Context, conversation state.Conversation, event aurora.Event) {
	s.logger.Info("handleEvent", "type", event.Type, "thread", conversation.ThreadID)
	switch event.Type {
	case "run.updated":
		var run aurora.RunSnapshot
		if decodeEvent(event.Data, &run) == nil {
			if terminal(run.Status) {
				s.timers.CancelRun(run.ID)
			}
			s.updateRunMessage(ctx, run)
		}
	case "task.created":
		var task aurora.TaskSnapshot
		if decodeEvent(event.Data, &task) == nil {
			if chattimers.IsTimerTask(task) {
				s.scheduleTimer(ctx, conversation, task)
			} else {
				s.createTaskMessage(ctx, conversation, task)
			}
		}
	case "task.updated":
		var task aurora.TaskSnapshot
		if decodeEvent(event.Data, &task) == nil {
			s.updateTaskMessage(ctx, task)
		}
	case "progress":
		var progress aurora.ProgressEvent
		if decodeEvent(event.Data, &progress) == nil {
			s.updateRunProgress(ctx, progress)
		}
	}
}

func (s *Service) updateRunProgress(ctx context.Context, progress aurora.ProgressEvent) {
	message, found, err := s.store.RunMessage(ctx, progress.RunID)
	if err != nil || !found || message.State != string(aurora.RunRunning) {
		return
	}
	text := "🧠 <b>Working…</b>\n\n" + escape(progress.Message)
	if err := s.client.EditMessage(ctx, message.ChatID, message.MessageID, text, stopKeyboard(progress.RunID)); err != nil {
		s.logger.Debug("edit progress", "run_id", progress.RunID, "error", err)
	}
}

func (s *Service) updateRunMessage(ctx context.Context, run aurora.RunSnapshot) {
	message, found, err := s.store.RunMessage(ctx, run.ID)
	if err != nil || !found {
		return
	}
	previousState := message.State
	text, keyboard := renderRun(run)
	if run.Status == aurora.RunWaitingTask {
		if fireAt, ok := s.timers.FireAtFor(run.ID); ok {
			text = renderTimerWaiting(fireAt)
		}
	}
	longAnswer := run.Status == aurora.RunCompleted && len(run.Answer) > 3000
	if longAnswer {
		text = "✅ <b>Completed</b>\nThe full answer follows in separate messages."
	}
	if err := s.client.EditMessage(ctx, message.ChatID, message.MessageID, text, keyboard); err != nil {
		s.logger.Debug("edit run message", "run_id", run.ID, "error", err)
	}
	_ = s.store.SaveRunMessage(ctx, state.RunMessage{
		RunID: run.ID, UserID: message.UserID, ChatID: message.ChatID,
		MessageID: message.MessageID, State: string(run.Status),
	})
	if longAnswer && previousState != string(aurora.RunCompleted) {
		for _, chunk := range chunks(run.Answer, 3500) {
			if _, err := s.client.SendMessage(ctx, message.ChatID, escape(chunk), nil); err != nil {
				s.logger.Warn("send completed answer chunk", "run_id", run.ID, "error", err)
				break
			}
		}
	}
}

func (s *Service) createTaskMessage(
	ctx context.Context,
	conversation state.Conversation,
	task aurora.TaskSnapshot,
) {
	if _, found, _ := s.store.TaskMessage(ctx, task.ID); found {
		return
	}
	text := renderTask(task)
	keyboard := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "✅ Approve", CallbackData: "task:a:" + task.ID},
		{Text: "⛔ Deny", CallbackData: "task:d:" + task.ID},
	}}}
	sent, err := s.client.SendMessage(ctx, conversation.ChatID, text, keyboard)
	if err != nil {
		s.logger.Error("send approval card", "task_id", task.ID, "error", err)
		return
	}
	_ = s.store.SaveTaskMessage(ctx, state.TaskMessage{
		TaskID: task.ID, RunID: task.RunID, UserID: conversation.UserID,
		ChatID: conversation.ChatID, MessageID: sent.MessageID,
		Token: task.WebhookToken, State: string(task.State),
	})
}

// scheduleTimer arms a timer task. Unlike approval tasks there is no card to
// approve; the run's own status message reflects the waiting-for-timer state and
// the scheduler resolves the task when it fires. Arming is idempotent, so this is
// safe to call from both the live event and restart recovery.
func (s *Service) scheduleTimer(_ context.Context, _ state.Conversation, task aurora.TaskSnapshot) {
	s.timers.Schedule(task)
}

func (s *Service) updateTaskMessage(ctx context.Context, task aurora.TaskSnapshot) {
	message, found, err := s.store.TaskMessage(ctx, task.ID)
	if err != nil || !found {
		return
	}
	if task.State != aurora.TaskStatePending {
		_ = s.client.EditMessage(ctx, message.ChatID, message.MessageID,
			decisionIcon(task.State)+" <b>"+escape(title(string(task.State)))+"</b>\n"+
				escape(task.Summary), nil)
	}
	_ = s.store.SetTaskState(ctx, task.ID, string(task.State))
}

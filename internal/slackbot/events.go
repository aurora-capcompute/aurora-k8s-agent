package slackbot

import (
	"context"
	"encoding/json"

	"aurora-capcompute/aurora"
	"aurora-k8s-agent/internal/slack"
	state "aurora-k8s-agent/internal/slackstate"
)

func (s *Service) handleEvent(ctx context.Context, conversation state.Conversation, event aurora.Event) {
	switch event.Type {
	case "run.updated":
		var run aurora.RunSnapshot
		if decodeEvent(event.Data, &run) == nil {
			if terminal(run.Status) {
				s.timers.cancelRun(run.ID)
			}
			s.updateRunMessage(ctx, run)
		}
	case "task.created":
		var task aurora.TaskSnapshot
		if decodeEvent(event.Data, &task) == nil {
			if isTimerTask(task) {
				s.timers.schedule(task)
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

func (s *Service) updateRunMessage(ctx context.Context, run aurora.RunSnapshot) {
	message, found, err := s.store.RunMessage(ctx, run.ID)
	if err != nil || !found {
		return
	}
	text, buttons := renderRun(run)
	if run.Status == aurora.RunWaitingTask {
		if fireAt, ok := s.timers.fireAtFor(run.ID); ok {
			text = renderTimerWaiting(fireAt)
		}
	}
	if err := s.client.UpdateMessage(ctx, message.ChannelID, message.MessageTS, text, buttons); err != nil {
		s.logger.Debug("update run message", "run_id", run.ID, "error", err)
	}
	_ = s.store.SaveRunMessage(ctx, state.RunMessage{
		RunID: run.ID, UserID: message.UserID, ChannelID: message.ChannelID,
		MessageTS: message.MessageTS, State: string(run.Status),
	})
}

func (s *Service) updateRunProgress(ctx context.Context, progress aurora.ProgressEvent) {
	message, found, err := s.store.RunMessage(ctx, progress.RunID)
	if err != nil || !found || message.State != string(aurora.RunRunning) {
		return
	}
	text := "🧠 *Working…*\n" + mrkdwn(progress.Message)
	if err := s.client.UpdateMessage(ctx, message.ChannelID, message.MessageTS, text, runButtons(progress.RunID)); err != nil {
		s.logger.Debug("update progress", "run_id", progress.RunID, "error", err)
	}
}

func (s *Service) createTaskMessage(ctx context.Context, conversation state.Conversation, task aurora.TaskSnapshot) {
	if _, found, _ := s.store.TaskMessage(ctx, task.ID); found {
		return
	}
	channelID := conversation.ChannelID
	threadTS := ""
	if runMsg, found, _ := s.store.RunMessage(ctx, task.RunID); found {
		channelID = runMsg.ChannelID
		threadTS = runMsg.MessageTS
	}
	buttons := []slack.Button{
		{Text: "✅ Approve", ActionID: actionApprove, Value: task.ID, Style: "primary"},
		{Text: "⛔ Deny", ActionID: actionDeny, Value: task.ID, Style: "danger"},
	}
	ts, err := s.client.PostMessage(ctx, channelID, threadTS, renderTask(task), buttons)
	if err != nil {
		s.logger.Error("post approval card", "task_id", task.ID, "error", err)
		return
	}
	_ = s.store.SaveTaskMessage(ctx, state.TaskMessage{
		TaskID: task.ID, RunID: task.RunID, UserID: conversation.UserID,
		ChannelID: channelID, MessageTS: ts, Token: task.WebhookToken, State: string(task.State),
	})
}

func (s *Service) updateTaskMessage(ctx context.Context, task aurora.TaskSnapshot) {
	message, found, err := s.store.TaskMessage(ctx, task.ID)
	if err != nil || !found {
		return
	}
	if task.State != aurora.TaskStatePending {
		_ = s.client.UpdateMessage(ctx, message.ChannelID, message.MessageTS,
			decisionIcon(task.State)+" *"+title(string(task.State))+"* — "+mrkdwn(task.Summary), nil)
	}
	_ = s.store.SetTaskState(ctx, task.ID, string(task.State))
}

func decodeEvent(value any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

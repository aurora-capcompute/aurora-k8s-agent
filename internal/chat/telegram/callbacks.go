package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/telegram"
)

func (s *Service) handleCallback(ctx context.Context, query *telegram.CallbackQuery) (resultErr error) {
	claimed, err := s.store.ClaimCallback(ctx, query.ID)
	if err != nil || !claimed {
		return err
	}
	defer func() {
		if resultErr == nil {
			resultErr = s.store.CompleteCallback(ctx, query.ID)
		}
	}()
	if query.Message == nil {
		return s.client.AnswerCallback(ctx, query.ID, "This action is no longer available.", true)
	}
	user, ok := s.authorize(query.From.ID, query.Message.Chat.ID)
	if !ok {
		return s.client.AnswerCallback(ctx, query.ID, "Not authorized.", true)
	}
	_ = user

	parts := strings.Split(query.Data, ":")
	switch {
	case len(parts) == 3 && parts[0] == "task":
		return s.resolveTask(ctx, query, parts[1], parts[2])
	case len(parts) == 2 && parts[0] == "stop":
		return s.stopRun(ctx, query, parts[1])
	default:
		return s.client.AnswerCallback(ctx, query.ID, "Unknown or expired action.", true)
	}
}

func (s *Service) resolveTask(
	ctx context.Context,
	query *telegram.CallbackQuery,
	action, taskID string,
) error {
	task, found, err := s.store.TaskMessage(ctx, taskID)
	if err != nil {
		return err
	}
	if !found || task.UserID != query.From.ID || task.ChatID != query.Message.Chat.ID {
		return s.client.AnswerCallback(ctx, query.ID, "This approval does not belong to you.", true)
	}
	decision := aurora.TaskStateApproved
	reason := "Approved through Telegram"
	if action == "d" {
		decision = aurora.TaskStateDenied
		reason = "Denied through Telegram"
	} else if action != "a" {
		return s.client.AnswerCallback(ctx, query.ID, "Unknown decision.", true)
	}
	_, err = s.runtime.ResolveTask(taskID, task.Token, aurora.Resolution{
		Decision: decision, Actor: fmt.Sprintf("telegram:%d", query.From.ID), Reason: reason,
	})
	if err != nil {
		return s.client.AnswerCallback(ctx, query.ID, "Resolution failed: "+err.Error(), true)
	}
	_ = s.store.SetTaskState(ctx, taskID, string(decision))
	_ = s.client.EditMessage(ctx, task.ChatID, task.MessageID,
		decisionIcon(decision)+" <b>"+escape(title(string(decision)))+"</b>\nTask <code>"+
			escape(taskID)+"</code>", nil)
	_ = s.client.AnswerCallback(ctx, query.ID, title(string(decision))+".", false)
	return nil
}

func (s *Service) stopRun(ctx context.Context, query *telegram.CallbackQuery, runID string) error {
	runMessage, found, err := s.store.RunMessage(ctx, runID)
	if err != nil {
		return err
	}
	if !found || runMessage.UserID != query.From.ID || runMessage.ChatID != query.Message.Chat.ID {
		return s.client.AnswerCallback(ctx, query.ID, "This session does not belong to you.", true)
	}
	if _, err := s.runtime.Stop(runID); err != nil {
		return s.client.AnswerCallback(ctx, query.ID, "Could not cancel session.", true)
	}
	_ = s.client.AnswerCallback(ctx, query.ID, "Cancellation requested.", false)
	return nil
}

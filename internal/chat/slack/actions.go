package slack

import (
	"context"
	"fmt"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/slack"
)

// HandleAction implements slack.Handler for interactive button clicks.
func (s *Service) HandleAction(ctx context.Context, action slack.BlockAction) {
	claimed, err := s.store.ClaimEvent(ctx, action.EnvelopeID)
	if err != nil || !claimed {
		return
	}
	defer func() { _ = s.store.CompleteEvent(ctx, action.EnvelopeID) }()

	switch action.ActionID {
	case actionApprove, actionDeny:
		s.resolveTask(ctx, action)
	case actionCancel:
		s.stopRun(ctx, action)
	}
}

func (s *Service) resolveTask(ctx context.Context, action slack.BlockAction) {
	taskID := action.Value
	message, found, err := s.store.TaskMessage(ctx, taskID)
	if err != nil || !found {
		return
	}
	if message.UserID != action.UserID {
		s.logger.Warn("task resolution by non-owner",
			"task_id", taskID, "actor", action.UserID, "owner", message.UserID)
		return
	}
	decision := aurora.TaskStateApproved
	reason := "Approved through Slack"
	if action.ActionID == actionDeny {
		decision = aurora.TaskStateDenied
		reason = "Denied through Slack"
	}
	if _, err := s.runtime.ResolveTask(taskID, message.Token, aurora.Resolution{
		Decision: decision, Actor: fmt.Sprintf("slack:%s", action.UserID), Reason: reason,
	}); err != nil {
		s.logger.Error("resolve task", "task_id", taskID, "error", err)
		return
	}
	_ = s.store.SetTaskState(ctx, taskID, string(decision))
	_ = s.client.UpdateMessage(ctx, message.ChannelID, message.MessageTS,
		decisionIcon(decision)+" *"+title(string(decision))+"* — task `"+taskID+"`", nil)
}

func (s *Service) stopRun(ctx context.Context, action slack.BlockAction) {
	runID := action.Value
	message, found, err := s.store.RunMessage(ctx, runID)
	if err != nil || !found {
		return
	}
	if message.UserID != action.UserID {
		s.logger.Warn("run cancel by non-owner",
			"run_id", runID, "actor", action.UserID, "owner", message.UserID)
		return
	}
	if _, err := s.runtime.Stop(runID); err != nil {
		s.logger.Error("stop run", "run_id", runID, "error", err)
	}
}

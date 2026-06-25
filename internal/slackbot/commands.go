package slackbot

import (
	"context"
	"fmt"
	"strings"

	"aurora-k8s-agent/internal/slack"
	policy "aurora-k8s-agent/internal/slackpolicy"
	state "aurora-k8s-agent/internal/slackstate"
)

// HandleSlash implements slack.Handler for the configured slash command
// (e.g. "/aurora new").
func (s *Service) HandleSlash(ctx context.Context, command slack.SlashCommand) {
	claimed, err := s.store.ClaimEvent(ctx, command.EnvelopeID)
	if err != nil || !claimed {
		return
	}
	defer func() { _ = s.store.CompleteEvent(ctx, command.EnvelopeID) }()

	user, ok := s.authorize(command.UserID, command.ChannelID)
	if !ok {
		_, _ = s.client.PostMessage(ctx, command.ChannelID, "", "You're not authorized to use Aurora here.", nil)
		return
	}
	fields := strings.Fields(command.Text)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "", "help":
		_, _ = s.client.PostMessage(ctx, command.ChannelID, "", helpText(), nil)
	case "new":
		conversation, err := s.newConversation(ctx, user, command.ChannelID)
		if err != nil {
			s.logger.Error("rotate conversation", "error", err)
			return
		}
		_, _ = s.client.PostMessage(ctx, command.ChannelID, "",
			"✅ *New conversation* — thread `"+conversation.ThreadID+"`", nil)
	case "status":
		s.postStatus(ctx, user, command.ChannelID)
	case "cancel":
		s.cancelActive(ctx, user, command.ChannelID)
	default:
		_, _ = s.client.PostMessage(ctx, command.ChannelID, "",
			"Unknown subcommand. Try `help`, `new`, `status`, or `cancel`.", nil)
	}
}

func (s *Service) handlePrompt(ctx context.Context, user policy.User, channelID, threadTS, text string) error {
	conversation, err := s.ensureConversation(ctx, user, channelID)
	if err != nil {
		return err
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if thread.ActiveRunID != "" {
		_, _ = s.client.PostMessage(ctx, channelID, threadTS,
			"⏳ I'm still working on the active request. Use `/aurora status` or `/aurora cancel`.", nil)
		return nil
	}
	s.subscribe(ctx, conversation)

	ts, err := s.client.PostMessage(ctx, channelID, threadTS, "🧠 *Starting…*", nil)
	if err != nil {
		return err
	}
	run, err := s.runtime.CreateRun(conversation.ThreadID, text, nil)
	if err != nil {
		_ = s.client.UpdateMessage(ctx, channelID, ts, "❌ *Could not start* — "+err.Error(), nil)
		return err
	}
	if err := s.store.SaveRunMessage(ctx, state.RunMessage{
		RunID: run.ID, UserID: user.ID, ChannelID: channelID, MessageTS: ts, State: string(run.Status),
	}); err != nil {
		return err
	}
	if current, err := s.runtime.GetRun(run.ID); err == nil {
		s.updateRunMessage(ctx, current)
	}
	return nil
}

func (s *Service) postStatus(ctx context.Context, user policy.User, channelID string) {
	conversation, found, err := s.store.Conversation(ctx, user.ID, channelID)
	if err != nil || !found {
		_, _ = s.client.PostMessage(ctx, channelID, "", "No conversation yet — send a message to begin.", nil)
		return
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		_, _ = s.client.PostMessage(ctx, channelID, "", "Could not read thread state.", nil)
		return
	}
	if thread.ActiveRunID == "" {
		_, _ = s.client.PostMessage(ctx, channelID, "",
			fmt.Sprintf("✅ Idle — thread `%s`", thread.ID), nil)
		return
	}
	run, err := s.runtime.GetRun(thread.ActiveRunID)
	if err != nil {
		_, _ = s.client.PostMessage(ctx, channelID, "", "Could not read run state.", nil)
		return
	}
	text, _ := renderRun(run)
	_, _ = s.client.PostMessage(ctx, channelID, "", text, nil)
}

func (s *Service) cancelActive(ctx context.Context, user policy.User, channelID string) {
	conversation, found, err := s.store.Conversation(ctx, user.ID, channelID)
	if err != nil || !found {
		_, _ = s.client.PostMessage(ctx, channelID, "", "Nothing to cancel.", nil)
		return
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil || thread.ActiveRunID == "" {
		_, _ = s.client.PostMessage(ctx, channelID, "", "Nothing to cancel.", nil)
		return
	}
	if _, err := s.runtime.Stop(thread.ActiveRunID); err != nil {
		_, _ = s.client.PostMessage(ctx, channelID, "", "Could not cancel: "+err.Error(), nil)
		return
	}
	_, _ = s.client.PostMessage(ctx, channelID, "", "🛑 Cancellation requested.", nil)
}

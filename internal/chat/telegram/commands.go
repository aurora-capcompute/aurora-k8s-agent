package telegram

import (
	"context"
	"fmt"
	"strings"

	"aurora-capcompute/aurora"
	"aurora-k8s-agent/internal/chat/telegram/policy"
	"aurora-k8s-agent/internal/chat/telegram/state"
	"aurora-k8s-agent/internal/transport/telegram"
)

func (s *Service) handleCommand(
	ctx context.Context,
	user policy.User,
	message *telegram.Message,
	text string,
) error {
	command := strings.Fields(strings.SplitN(text, "@", 2)[0])[0]
	switch command {
	case "/start", "/help":
		return s.send(ctx, message.Chat.ID, helpText(), nil)
	case "/new":
		conversation, err := s.rotateConversation(ctx, user, message.Chat.ID)
		if err != nil {
			return err
		}
		return s.send(ctx, message.Chat.ID,
			"✅ <b>New conversation</b>\nThread <code>"+escape(conversation.ThreadID)+"</code>", nil)
	case "/status":
		return s.sendStatus(ctx, user, message.Chat.ID)
	case "/history":
		return s.sendHistory(ctx, user, message.Chat.ID)
	case "/journal":
		return s.sendJournal(ctx, user, message.Chat.ID)
	case "/cancel":
		return s.cancelActive(ctx, user, message.Chat.ID)
	case "/retry":
		return s.retryActive(ctx, user, message.Chat.ID)
	default:
		return s.send(ctx, message.Chat.ID, "Unknown command. Use /help.", nil)
	}
}

func (s *Service) handlePrompt(
	ctx context.Context,
	user policy.User,
	message *telegram.Message,
	text string,
) error {
	conversation, err := s.ensureConversation(ctx, user, message.Chat.ID)
	if err != nil {
		return err
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if thread.ActiveRunID != "" {
		if active, getErr := s.runtime.GetRun(thread.ActiveRunID); getErr == nil && active.Message == text {
			if _, found, _ := s.store.RunMessage(ctx, active.ID); !found {
				sent, sendErr := s.client.SendMessage(ctx, message.Chat.ID,
					"🔄 <b>Recovered request</b>\nReattached to the active session.", stopKeyboard(active.ID))
				if sendErr == nil {
					_ = s.store.SaveRunMessage(ctx, state.RunMessage{
						RunID: active.ID, UserID: user.ID, ChatID: message.Chat.ID,
						MessageID: sent.MessageID, State: string(active.Status),
					})
				}
			}
			return nil
		}
		return s.send(ctx, message.Chat.ID,
			"⏳ I'm still working on the active session. Use /status or /cancel.", nil)
	}
	s.subscribe(ctx, conversation)

	sent, err := s.client.SendMessage(ctx, message.Chat.ID, "🧠 <b>Starting session…</b>", nil)
	if err != nil {
		return err
	}
	run, err := s.runtime.CreateRun(conversation.ThreadID, text, nil)
	if err != nil {
		_ = s.client.EditMessage(ctx, message.Chat.ID, sent.MessageID,
			"❌ <b>Could not start session</b>\n"+escape(err.Error()), nil)
		return err
	}
	if err := s.store.SaveRunMessage(ctx, state.RunMessage{
		RunID: run.ID, UserID: user.ID, ChatID: message.Chat.ID,
		MessageID: sent.MessageID, State: string(run.Status),
	}); err != nil {
		return err
	}
	if current, err := s.runtime.GetRun(run.ID); err == nil {
		s.updateRunMessage(ctx, current)
	}
	return nil
}

func (s *Service) sendStatus(ctx context.Context, user policy.User, chatID int64) error {
	conversation, found, err := s.store.Conversation(ctx, user.ID, chatID)
	if err != nil {
		return err
	}
	if !found {
		return s.send(ctx, chatID, "No conversation yet. Send a request to begin.", nil)
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if thread.ActiveRunID == "" {
		return s.send(ctx, chatID,
			fmt.Sprintf("✅ Idle\nThread <code>%s</code> · %d runs", escape(thread.ID), len(thread.Runs)), nil)
	}
	run, err := s.runtime.GetRun(thread.ActiveRunID)
	if err != nil {
		return err
	}
	text, keyboard := renderRun(run)
	return s.send(ctx, chatID, text, keyboard)
}

func (s *Service) sendHistory(ctx context.Context, user policy.User, chatID int64) error {
	conversation, found, err := s.store.Conversation(ctx, user.ID, chatID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return s.send(ctx, chatID, "No conversation history yet.", nil)
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	var lines []string
	start := len(thread.History) - 6
	if start < 0 {
		start = 0
	}
	for _, item := range thread.History[start:] {
		lines = append(lines, "<b>"+escape(item.Role)+":</b> "+escape(shorten(item.Content, 500)))
	}
	if len(lines) == 0 {
		lines = append(lines, "No messages yet.")
	}
	return s.send(ctx, chatID, strings.Join(lines, "\n\n"), nil)
}

func (s *Service) sendJournal(ctx context.Context, user policy.User, chatID int64) error {
	conversation, found, err := s.store.Conversation(ctx, user.ID, chatID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return s.send(ctx, chatID, "No conversation yet.", nil)
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if len(thread.Runs) == 0 {
		return s.send(ctx, chatID, "No runs yet.", nil)
	}
	latestRun := thread.Runs[len(thread.Runs)-1]
	entries, err := s.runtime.Journal(latestRun.ID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return s.send(ctx, chatID, fmt.Sprintf(
			"Run <code>%s</code> (%s) — empty journal.", escape(latestRun.ID), escape(string(latestRun.Status))), nil)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf(
		"📋 <b>Journal</b> — run <code>%s</code> (%s)\n",
		escape(latestRun.ID), escape(string(latestRun.Status))))
	for _, entry := range entries {
		status := string(entry.Outcome.Status)
		line := fmt.Sprintf("%d. <code>%s</code> → %s", entry.Index+1, escape(entry.Call.Name), escape(status))
		if entry.Outcome.Message != "" {
			line += ": " + escape(shorten(entry.Outcome.Message, 200))
		}
		lines = append(lines, line)
	}
	return s.send(ctx, chatID, strings.Join(lines, "\n"), nil)
}

func (s *Service) cancelActive(ctx context.Context, user policy.User, chatID int64) error {
	conversation, found, err := s.store.Conversation(ctx, user.ID, chatID)
	if err != nil || !found {
		return err
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if thread.ActiveRunID == "" {
		return s.send(ctx, chatID, "No active session.", nil)
	}
	_, err = s.runtime.Stop(thread.ActiveRunID)
	if err != nil {
		return err
	}
	return s.send(ctx, chatID, "🛑 Cancellation requested.", nil)
}

func (s *Service) retryActive(ctx context.Context, user policy.User, chatID int64) error {
	conversation, found, err := s.store.Conversation(ctx, user.ID, chatID)
	if err != nil || !found {
		return err
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if len(thread.Runs) == 0 {
		return s.send(ctx, chatID, "There is no session to retry.", nil)
	}
	run := thread.Runs[len(thread.Runs)-1]
	_, err = s.runtime.Retry(run.ID, aurora.RetryResume, nil)
	if err != nil {
		return s.send(ctx, chatID, "Could not retry: "+escape(err.Error()), nil)
	}
	return s.send(ctx, chatID, "🔄 Session retry started.", nil)
}

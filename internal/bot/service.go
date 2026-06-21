package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"aurora-capcompute/aurora"
	"aurora-k8s-agent/internal/policy"
	"aurora-k8s-agent/internal/state"
	"aurora-k8s-agent/internal/telegram"
)

type Service struct {
	runtime  aurora.Runtime
	client   *telegram.Client
	store    *state.Store
	policies *policy.Set
	identity telegram.BotIdentity
	logger   *slog.Logger

	mu            sync.Mutex
	subscriptions map[string]func()
}

func New(
	runtime aurora.Runtime,
	client *telegram.Client,
	store *state.Store,
	policies *policy.Set,
	identity telegram.BotIdentity,
	logger *slog.Logger,
) *Service {
	return &Service{
		runtime: runtime, client: client, store: store, policies: policies,
		identity: identity, logger: logger, subscriptions: make(map[string]func()),
	}
}

func (s *Service) Run(ctx context.Context) error {
	defer s.unsubscribeAll()

	backoff := time.Second
	for {
		if err := s.processPending(ctx); err != nil {
			s.logger.Error("process Telegram update inbox", "error", err)
		}
		offset, err := s.store.Offset(ctx)
		if err != nil {
			return err
		}
		updates, err := s.client.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.Warn("poll Telegram", "error", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, update := range updates {
			raw, err := json.Marshal(update)
			if err != nil {
				return err
			}
			if err := s.store.EnqueueUpdate(ctx, update.UpdateID, raw); err != nil {
				return err
			}
		}
	}
}

func (s *Service) processPending(ctx context.Context) error {
	for {
		updates, err := s.store.PendingUpdates(ctx, 50)
		if err != nil {
			return err
		}
		if len(updates) == 0 {
			return nil
		}
		for _, stored := range updates {
			var update telegram.Update
			processErr := json.Unmarshal(stored.Payload, &update)
			if processErr == nil {
				processErr = s.handleUpdate(ctx, update)
			}
			if err := s.store.CompleteUpdate(ctx, stored.ID, processErr); err != nil {
				return err
			}
			if processErr != nil {
				s.logger.Warn("process Telegram update", "update_id", stored.ID, "error", processErr)
			}
		}
	}
}

func (s *Service) handleUpdate(ctx context.Context, update telegram.Update) error {
	if update.CallbackQuery != nil {
		return s.handleCallback(ctx, update.CallbackQuery)
	}
	if update.Message == nil || update.Message.From == nil {
		return nil
	}
	message := update.Message
	user, ok := s.policies.Authorize(message.From.ID, message.Chat.ID)
	if !ok {
		return nil
	}
	text, ok := s.acceptedText(message)
	if !ok {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		return s.handleCommand(ctx, user, message, text)
	}
	return s.handlePrompt(ctx, user, message, text)
}

func (s *Service) acceptedText(message *telegram.Message) (string, bool) {
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return "", false
	}
	if message.Chat.Type == "private" {
		return text, true
	}
	mention := "@" + strings.ToLower(s.identity.Username)
	lower := strings.ToLower(text)
	replied := message.ReplyToMessage != nil &&
		message.ReplyToMessage.From != nil &&
		message.ReplyToMessage.From.ID == s.identity.ID
	if strings.HasPrefix(text, "/") || replied || strings.Contains(lower, mention) {
		text = strings.TrimSpace(strings.ReplaceAll(text, mention, ""))
		text = strings.TrimSpace(strings.ReplaceAll(text, "@"+s.identity.Username, ""))
		return text, text != ""
	}
	return "", false
}

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
	case "/privileges":
		return s.sendPrivileges(ctx, user, message.Chat.ID)
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
			"⏳ I’m still working on the active session. Use /status or /cancel.", nil)
	}
	s.subscribe(ctx, conversation)

	var overrides []aurora.CapabilityConfig
	var profileLabel string
	elevation, found, err := s.store.Elevation(ctx, user.ID, message.Chat.ID)
	if err != nil {
		return err
	}
	if found {
		if elevation.State != "armed" || !time.Now().Before(elevation.ExpiresAt) {
			_ = s.store.ClearElevation(ctx, user.ID, message.Chat.ID)
		} else if profile, ok := user.ElevationProfiles[elevation.Profile]; ok {
			overrides = profile.Overrides
			profileLabel = profile.Label
		}
	}

	status := "🧠 <b>Starting session…</b>"
	if profileLabel != "" {
		if err := s.store.BeginElevation(ctx, user.ID, message.Chat.ID); err != nil {
			return err
		}
		status += "\n🔐 Elevated profile: <b>" + escape(profileLabel) + "</b>"
	}
	sent, err := s.client.SendMessage(ctx, message.Chat.ID, status, nil)
	if err != nil {
		return err
	}
	run, err := s.runtime.CreateRun(conversation.ThreadID, text, overrides)
	if err != nil {
		if profileLabel != "" {
			_ = s.store.ClearElevation(ctx, user.ID, message.Chat.ID)
		}
		_ = s.client.EditMessage(ctx, message.Chat.ID, sent.MessageID,
			"❌ <b>Could not start session</b>\n"+escape(err.Error()), nil)
		return err
	}
	if profileLabel != "" {
		if err := s.store.BindElevation(ctx, user.ID, message.Chat.ID, run.ID); err != nil {
			_, _ = s.runtime.Stop(run.ID)
			return err
		}
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
	user, ok := s.policies.Authorize(query.From.ID, query.Message.Chat.ID)
	if !ok {
		return s.client.AnswerCallback(ctx, query.ID, "Not authorized.", true)
	}
	return s.handleClaimedCallback(ctx, query, user)
}

func (s *Service) handleClaimedCallback(
	ctx context.Context,
	query *telegram.CallbackQuery,
	user policy.User,
) error {
	parts := strings.Split(query.Data, ":")
	switch {
	case len(parts) == 2 && parts[0] == "priv":
		return s.confirmPrivilege(ctx, query, user, parts[1])
	case len(parts) == 2 && parts[0] == "privc":
		return s.armPrivilege(ctx, query, user, parts[1])
	case query.Data == "privx":
		_ = s.store.ClearElevation(ctx, user.ID, query.Message.Chat.ID)
		_ = s.client.EditMessage(ctx, query.Message.Chat.ID, query.Message.MessageID,
			"🔓 Privilege selection cancelled.", nil)
		return s.client.AnswerCallback(ctx, query.ID, "Cancelled.", false)
	case len(parts) == 3 && parts[0] == "task":
		return s.resolveTask(ctx, query, parts[1], parts[2])
	case len(parts) == 2 && parts[0] == "stop":
		return s.stopRun(ctx, query, parts[1])
	default:
		return s.client.AnswerCallback(ctx, query.ID, "Unknown or expired action.", true)
	}
}

func (s *Service) sendPrivileges(ctx context.Context, user policy.User, chatID int64) error {
	if len(user.ElevationProfiles) == 0 {
		return s.send(ctx, chatID, "No elevation profiles are configured for your account.", nil)
	}
	keyboard := &telegram.InlineKeyboardMarkup{}
	text := "🔐 <b>Session privileges</b>\nChoose a profile for your next capcompute session."
	names := make([]string, 0, len(user.ElevationProfiles))
	for name := range user.ElevationProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := user.ElevationProfiles[name]
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []telegram.InlineKeyboardButton{{
			Text: profile.Label, CallbackData: "priv:" + name,
		}})
	}
	return s.send(ctx, chatID, text, keyboard)
}

func (s *Service) confirmPrivilege(
	ctx context.Context,
	query *telegram.CallbackQuery,
	user policy.User,
	name string,
) error {
	profile, ok := user.ElevationProfiles[name]
	if !ok {
		return s.client.AnswerCallback(ctx, query.ID, "Profile is unavailable.", true)
	}
	text := "⚠️ <b>Confirm elevated session</b>\n\n<b>" + escape(profile.Label) + "</b>"
	if profile.Description != "" {
		text += "\n" + escape(profile.Description)
	}
	text += "\n\nApplies to the next session only and expires unused in " + escape(profile.TTL.String()) + "."
	keyboard := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "Confirm", CallbackData: "privc:" + name},
		{Text: "Cancel", CallbackData: "privx"},
	}}}
	if err := s.client.EditMessage(ctx, query.Message.Chat.ID, query.Message.MessageID, text, keyboard); err != nil {
		return err
	}
	return s.client.AnswerCallback(ctx, query.ID, "Review the profile before confirming.", false)
}

func (s *Service) armPrivilege(
	ctx context.Context,
	query *telegram.CallbackQuery,
	user policy.User,
	name string,
) error {
	profile, ok := user.ElevationProfiles[name]
	if !ok {
		return s.client.AnswerCallback(ctx, query.ID, "Profile is unavailable.", true)
	}
	conversation, err := s.ensureConversation(ctx, user, query.Message.Chat.ID)
	if err != nil {
		return err
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if thread.ActiveRunID != "" {
		return s.client.AnswerCallback(ctx, query.ID, "Cancel the active session first.", true)
	}
	if err := s.store.ArmElevation(ctx, state.Elevation{
		UserID: user.ID, ChatID: query.Message.Chat.ID, Profile: name,
		ExpiresAt: time.Now().Add(profile.TTL),
	}); err != nil {
		return err
	}
	text := "🔐 <b>" + escape(profile.Label) + " armed</b>\nSend your next request within " +
		escape(profile.TTL.String()) + ". It applies to that session only."
	if err := s.client.EditMessage(ctx, query.Message.Chat.ID, query.Message.MessageID, text, nil); err != nil {
		return err
	}
	return s.client.AnswerCallback(ctx, query.ID, "Profile armed.", false)
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

func (s *Service) ensureConversation(ctx context.Context, user policy.User, chatID int64) (state.Conversation, error) {
	conversation, found, err := s.store.Conversation(ctx, user.ID, chatID)
	if err != nil {
		return state.Conversation{}, err
	}
	if found && conversation.PolicyDigest == user.Digest {
		return conversation, nil
	}
	if found {
		if thread, getErr := s.runtime.GetThread(conversation.ThreadID); getErr == nil && thread.ActiveRunID != "" {
			_, _ = s.runtime.Stop(thread.ActiveRunID)
		}
	}
	return s.newConversation(ctx, user, chatID)
}

func (s *Service) newConversation(ctx context.Context, user policy.User, chatID int64) (state.Conversation, error) {
	thread, err := s.runtime.CreateThread(user.Manifest)
	if err != nil {
		return state.Conversation{}, err
	}
	_ = s.store.ClearElevation(ctx, user.ID, chatID)
	conversation := state.Conversation{
		UserID: user.ID, ChatID: chatID, ThreadID: thread.ID, PolicyDigest: user.Digest,
	}
	if err := s.store.SaveConversation(ctx, conversation); err != nil {
		return state.Conversation{}, err
	}
	s.subscribe(ctx, conversation)
	return conversation, nil
}

func (s *Service) rotateConversation(ctx context.Context, user policy.User, chatID int64) (state.Conversation, error) {
	if current, found, err := s.store.Conversation(ctx, user.ID, chatID); err != nil {
		return state.Conversation{}, err
	} else if found {
		if thread, getErr := s.runtime.GetThread(current.ThreadID); getErr == nil && thread.ActiveRunID != "" {
			_, _ = s.runtime.Stop(thread.ActiveRunID)
		}
	}
	return s.newConversation(ctx, user, chatID)
}

func (s *Service) subscribe(ctx context.Context, conversation state.Conversation) {
	s.mu.Lock()
	if _, exists := s.subscriptions[conversation.ThreadID]; exists {
		s.mu.Unlock()
		return
	}
	snapshot, events, unsubscribe, err := s.runtime.Subscribe(conversation.ThreadID)
	if err != nil {
		s.mu.Unlock()
		s.logger.Warn("subscribe thread", "thread_id", conversation.ThreadID, "error", err)
		return
	}
	s.subscriptions[conversation.ThreadID] = unsubscribe
	s.mu.Unlock()
	go func() {
		s.handleEvent(context.Background(), conversation, snapshot)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				s.handleEvent(context.Background(), conversation, event)
			}
		}
	}()
}

func (s *Service) handleEvent(ctx context.Context, conversation state.Conversation, event aurora.Event) {
	switch event.Type {
	case "run.updated":
		var run aurora.RunSnapshot
		if decodeEvent(event.Data, &run) == nil {
			s.updateRunMessage(ctx, run)
		}
	case "task.created":
		var task aurora.TaskSnapshot
		if decodeEvent(event.Data, &task) == nil {
			s.createTaskMessage(ctx, conversation, task)
		}
	case "task.updated":
		var task aurora.TaskSnapshot
		if decodeEvent(event.Data, &task) == nil {
			s.updateTaskMessage(ctx, task)
		}
	}
}

func (s *Service) updateRunMessage(ctx context.Context, run aurora.RunSnapshot) {
	message, found, err := s.store.RunMessage(ctx, run.ID)
	if err != nil || !found {
		return
	}
	previousState := message.State
	text, keyboard := renderRun(run)
	longAnswer := run.Status == aurora.RunCompleted && len(run.Answer) > 3000
	if longAnswer {
		text = "✅ <b>Completed</b>\nThe full answer follows in separate messages.\n\n🔓 Session privileges revoked."
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
	if terminal(run.Status) {
		_ = s.store.ClearElevation(ctx, message.UserID, message.ChatID)
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
	profileLabel := ""
	if elevation, found, _ := s.store.Elevation(ctx, conversation.UserID, conversation.ChatID); found &&
		elevation.RunID == task.RunID {
		if user, ok := s.policies.Authorize(conversation.UserID, conversation.ChatID); ok {
			if profile, ok := user.ElevationProfiles[elevation.Profile]; ok {
				profileLabel = profile.Label
			}
		}
	}
	text := renderTask(task, profileLabel)
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

func (s *Service) Recover(ctx context.Context) error {
	conversations, err := s.store.Conversations(ctx)
	if err != nil {
		return err
	}
	for _, conversation := range conversations {
		s.subscribe(ctx, conversation)
		thread, err := s.runtime.GetThread(conversation.ThreadID)
		if err != nil {
			continue
		}
		elevation, hasElevation, _ := s.store.Elevation(ctx, conversation.UserID, conversation.ChatID)
		if thread.ActiveRunID == "" {
			if hasElevation && elevation.State == "consuming" {
				_ = s.store.ClearElevation(ctx, conversation.UserID, conversation.ChatID)
			}
			continue
		}
		run, err := s.runtime.GetRun(thread.ActiveRunID)
		if err != nil {
			continue
		}
		if hasElevation && elevation.State == "consuming" {
			_ = s.store.BindElevation(ctx, conversation.UserID, conversation.ChatID, run.ID)
		}
		if _, found, _ := s.store.RunMessage(ctx, run.ID); !found {
			sent, sendErr := s.client.SendMessage(ctx, conversation.ChatID,
				"🔄 <b>Recovered session</b>\nReconciling persisted state…", stopKeyboard(run.ID))
			if sendErr == nil {
				_ = s.store.SaveRunMessage(ctx, state.RunMessage{
					RunID: run.ID, UserID: conversation.UserID, ChatID: conversation.ChatID,
					MessageID: sent.MessageID, State: "recovering",
				})
			}
		}
		if run.Status == aurora.RunInterrupted {
			if _, err := s.runtime.Retry(run.ID, aurora.RetryResume, nil); err != nil {
				s.logger.Warn("resume interrupted run", "run_id", run.ID, "error", err)
			} else if current, getErr := s.runtime.GetRun(run.ID); getErr == nil {
				run = current
			}
		}
		s.updateRunMessage(ctx, run)
		runtimeTasks, taskErr := s.runtime.Tasks(run.ID)
		if taskErr == nil {
			for _, task := range runtimeTasks {
				if task.State == aurora.TaskStatePending {
					s.createTaskMessage(ctx, conversation, task)
				}
			}
		}
	}
	tasks, err := s.store.PendingTaskMessages(ctx)
	if err != nil {
		return err
	}
	for _, message := range tasks {
		runtimeTasks, err := s.runtime.Tasks(message.RunID)
		if err != nil {
			continue
		}
		for _, task := range runtimeTasks {
			if task.ID == message.TaskID {
				s.updateTaskMessage(ctx, task)
				break
			}
		}
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

func (s *Service) cancelActive(ctx context.Context, user policy.User, chatID int64) error {
	_ = s.store.ClearElevation(ctx, user.ID, chatID)
	conversation, found, err := s.store.Conversation(ctx, user.ID, chatID)
	if err != nil || !found {
		return err
	}
	thread, err := s.runtime.GetThread(conversation.ThreadID)
	if err != nil {
		return err
	}
	if thread.ActiveRunID == "" {
		return s.send(ctx, chatID, "No active session. Any armed privilege profile was cleared.", nil)
	}
	_, err = s.runtime.Stop(thread.ActiveRunID)
	if err != nil {
		return err
	}
	return s.send(ctx, chatID, "🛑 Cancellation requested. Elevated privileges are revoked.", nil)
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

func (s *Service) send(ctx context.Context, chatID int64, text string, keyboard *telegram.InlineKeyboardMarkup) error {
	for _, chunk := range chunks(text, 3900) {
		if _, err := s.client.SendMessage(ctx, chatID, chunk, keyboard); err != nil {
			return err
		}
		keyboard = nil
	}
	return nil
}

func (s *Service) unsubscribeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.subscriptions {
		cancel()
	}
	s.subscriptions = make(map[string]func())
}

func renderRun(run aurora.RunSnapshot) (string, *telegram.InlineKeyboardMarkup) {
	switch run.Status {
	case aurora.RunQueued:
		return "⏳ <b>Queued</b>", stopKeyboard(run.ID)
	case aurora.RunRunning:
		return "🧠 <b>Thinking and inspecting the cluster…</b>", stopKeyboard(run.ID)
	case aurora.RunWaitingTask:
		return "✋ <b>Waiting for your approval</b>\nReview the approval card below.", stopKeyboard(run.ID)
	case aurora.RunInterrupted:
		return "⚠️ <b>Session interrupted</b>\nUse /retry to reconstruct and resume it.", nil
	case aurora.RunCompleted:
		return "✅ <b>Completed</b>\n\n" + escape(run.Answer) + "\n\n🔓 Session privileges revoked.", nil
	case aurora.RunStopped:
		return "🛑 <b>Cancelled</b>\n🔓 Session privileges revoked.", nil
	case aurora.RunFailed:
		return "❌ <b>Failed</b>\n" + escape(run.Error) + "\n\nUse /retry or /new.", nil
	default:
		return "ℹ️ <b>" + escape(string(run.Status)) + "</b>", nil
	}
}

func renderTask(task aurora.TaskSnapshot, profileLabel string) string {
	text := "⚠️ <b>Approval required</b>\n\n" +
		"<b>Operation:</b> <code>" + escape(task.Call.Name) + "</code>\n" +
		"<b>Request:</b> <code>" + escape(shorten(redactedJSON(task.Call.Args), 1200)) + "</code>\n"
	if profileLabel != "" {
		text += "<b>Privilege profile:</b> " + escape(profileLabel) + "\n"
	}
	return text + "\n" + escape(task.Summary) + "\n\nTask expires " + formatExpiry(task.ExpiresAt) + "."
}

func stopKeyboard(runID string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "🛑 Cancel session", CallbackData: "stop:" + runID},
	}}}
}

func terminal(status aurora.RunStatus) bool {
	switch status {
	case aurora.RunCompleted, aurora.RunStopped, aurora.RunFailed:
		return true
	default:
		return false
	}
}

func decodeEvent(value any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func decisionIcon(state aurora.TaskState) string {
	if state == aurora.TaskStateApproved || state == aurora.TaskStateCompleted || state == aurora.TaskStateExecuted {
		return "✅"
	}
	return "⛔"
}

func formatExpiry(value *time.Time) string {
	if value == nil {
		return "later"
	}
	return "<code>" + escape(value.Local().Format(time.RFC822)) + "</code>"
}

func helpText() string {
	return `🤖 <b>Aurora Kubernetes agent</b>

Send a request to inspect Kubernetes resources or Helm releases.

/status — current session
/history — recent conversation
/privileges — arm a configured profile for one session
/cancel — stop the session and revoke elevation
/retry — reconstruct and retry the latest interrupted session
/new — start a fresh conversation

Mutating operations always show an approval card unless your administrator explicitly configured otherwise.`
}

func escape(value string) string { return html.EscapeString(value) }

func shorten(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func chunks(value string, limit int) []string {
	if len(value) <= limit {
		return []string{value}
	}
	var result []string
	for len(value) > limit {
		cut := strings.LastIndex(value[:limit], "\n")
		if cut < limit/2 {
			cut = limit
		}
		result = append(result, value[:cut])
		value = strings.TrimLeft(value[cut:], "\n")
	}
	if value != "" {
		result = append(result, value)
	}
	return result
}

func title(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func redactedJSON(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	redactValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func redactValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") ||
				strings.Contains(lower, "token") ||
				strings.Contains(lower, "secret") ||
				strings.Contains(lower, "api_key") ||
				strings.Contains(lower, "apikey") {
				typed[key] = "[REDACTED]"
				continue
			}
			redactValue(item)
		}
	case []any:
		for _, item := range typed {
			redactValue(item)
		}
	}
}

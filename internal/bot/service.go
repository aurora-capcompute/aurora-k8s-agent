package bot

import (
	"context"
	"encoding/json"
	"log/slog"
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
		if thread.ActiveRunID == "" {
			continue
		}
		run, err := s.runtime.GetRun(thread.ActiveRunID)
		if err != nil {
			continue
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

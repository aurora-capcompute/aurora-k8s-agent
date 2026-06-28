// Package telegram is the Telegram chat adapter: it connects a Telegram bot to
// the Aurora runtime, turning messages into agent runs, rendering run and
// approval state back into chats, and resolving durable approval tasks from
// inline-keyboard callbacks. It owns Telegram-shaped presentation and command
// handling; the raw Bot API client lives in transport/telegram, and per-user
// authorization in the policy subpackage.
package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/policy"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/state"
	chattimers "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/timers"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/telegram"
)

type Service struct {
	runtime  aurora.Runtime
	client   *telegram.Client
	store    *state.Store
	policies atomic.Pointer[policy.Set]
	identity telegram.BotIdentity
	logger   *slog.Logger
	timers   *chattimers.Scheduler
	subs     *chat.Subscriptions
	// runProgress accumulates progress lines per run so all tool calls remain
	// visible in the status message (not just the latest one).
	runProgress sync.Map // runID string → []string
}

func New(
	runtime aurora.Runtime,
	client *telegram.Client,
	store *state.Store,
	policies *policy.Set,
	identity telegram.BotIdentity,
	logger *slog.Logger,
) *Service {
	s := &Service{
		runtime: runtime, client: client, store: store,
		identity: identity, logger: logger,
		timers: chattimers.NewScheduler(runtime, logger),
		subs:   chat.NewSubscriptions(runtime, logger),
	}
	s.policies.Store(policies)
	return s
}

// SetPolicies atomically swaps the authorization set, so the control plane can
// reroute a live bridge when bindings change without dropping the transport.
func (s *Service) SetPolicies(p *policy.Set) { s.policies.Store(p) }

// authorize routes a subject through the current policy set, tolerating a nil set
// (no bindings yet) as "not authorized".
func (s *Service) authorize(userID, chatID int64) (policy.User, bool) {
	p := s.policies.Load()
	if p == nil {
		return policy.User{}, false
	}
	return p.Authorize(userID, chatID)
}

// Kind identifies this source. Implements source.Source.
func (s *Service) Kind() string { return "telegram" }

// Start reconciles persisted sessions and then serves Telegram updates until
// ctx is cancelled. Implements source.Source.
func (s *Service) Start(ctx context.Context) error {
	if err := s.Recover(ctx); err != nil {
		return err
	}
	return s.Run(ctx)
}

func (s *Service) Run(ctx context.Context) error {
	defer s.unsubscribeAll()
	defer s.timers.StopAll()

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
	user, ok := s.authorize(message.From.ID, message.Chat.ID)
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
		// Policy changed: stop active run before rotating to a fresh thread.
		if thread, getErr := s.runtime.GetThread(conversation.ThreadID); getErr == nil && thread.ActiveRunID != "" {
			_, _ = s.runtime.Stop(thread.ActiveRunID)
		}
	}
	return s.newConversation(ctx, user, chatID)
}

func (s *Service) newConversation(ctx context.Context, user policy.User, chatID int64) (state.Conversation, error) {
	thread, err := s.runtime.CreateThread(user.Manifest, nil)
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
	s.subs.Add(ctx, conversation.ThreadID, func(event aurora.Event) {
		s.handleEvent(context.Background(), conversation, event)
	})
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
		runtimeTasks, taskErr := s.runtime.Tasks(run.ID)
		if taskErr == nil {
			for _, task := range runtimeTasks {
				if task.State != aurora.TaskStatePending {
					continue
				}
				if chattimers.IsTimerTask(task) {
					s.scheduleTimer(ctx, conversation, task)
					continue
				}
				s.createTaskMessage(ctx, conversation, task)
			}
		}
		s.updateRunMessage(ctx, run)
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

func (s *Service) unsubscribeAll() { s.subs.CloseAll() }

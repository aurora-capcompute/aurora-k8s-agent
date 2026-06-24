// Package bridge connects a Slack workspace to the Aurora runtime: it turns
// Slack messages into agent runs, renders run/approval state back into Slack, and
// resolves durable approval tasks from interactive buttons.
package slackbot

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"aurora-capcompute/aurora"
	"aurora-k8s-agent/internal/slack"
	policy "aurora-k8s-agent/internal/slackpolicy"
	state "aurora-k8s-agent/internal/slackstate"
)

type Service struct {
	runtime  aurora.Runtime
	client   *slack.Client
	store    *state.Store
	policies *policy.Set
	logger   *slog.Logger
	timers   *timerScheduler

	mu            sync.Mutex
	subscriptions map[string]func()
}

func New(
	runtime aurora.Runtime,
	client *slack.Client,
	store *state.Store,
	policies *policy.Set,
	logger *slog.Logger,
) *Service {
	return &Service{
		runtime: runtime, client: client, store: store, policies: policies, logger: logger,
		timers:        newTimerScheduler(runtime, logger),
		subscriptions: make(map[string]func()),
	}
}

// Kind identifies this source. Implements source.Source.
func (s *Service) Kind() string { return "slack" }

// Start serves the Slack source until ctx is cancelled. Run already identifies
// the bot and recovers persisted state. Implements source.Source.
func (s *Service) Start(ctx context.Context) error { return s.Run(ctx) }

// Run identifies the bot, reconciles persisted state, then serves Socket Mode
// events until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	defer s.unsubscribeAll()
	defer s.timers.stopAll()
	if _, err := s.client.Identify(ctx); err != nil {
		return err
	}
	if err := s.Recover(ctx); err != nil {
		return err
	}
	return s.client.Run(ctx, s)
}

// HandleMessage implements slack.Handler: a DM or @mention becomes a prompt.
func (s *Service) HandleMessage(ctx context.Context, event slack.MessageEvent) {
	claimed, err := s.store.ClaimEvent(ctx, event.EventID)
	if err != nil || !claimed {
		return
	}
	text := cleanMentions(event.Text)
	if text == "" {
		_ = s.store.CompleteEvent(ctx, event.EventID)
		return
	}
	user, ok := s.policies.Authorize(event.UserID, event.ChannelID)
	if !ok {
		_ = s.store.CompleteEvent(ctx, event.EventID)
		return
	}
	threadTS := firstNonEmpty(event.ThreadTS, event.TS)
	if err := s.handlePrompt(ctx, user, event.ChannelID, threadTS, text); err != nil {
		s.logger.Error("handle prompt", "user", event.UserID, "error", err)
		return
	}
	_ = s.store.CompleteEvent(ctx, event.EventID)
}

func (s *Service) ensureConversation(ctx context.Context, user policy.User, channelID string) (state.Conversation, error) {
	conversation, found, err := s.store.Conversation(ctx, user.ID, channelID)
	if err != nil {
		return state.Conversation{}, err
	}
	if found && conversation.PolicyDigest == user.Digest {
		return conversation, nil
	}
	if found {
		// Policy changed: stop any active run before rotating to a fresh thread so
		// a revoked capability cannot linger in the old conversation.
		if thread, getErr := s.runtime.GetThread(conversation.ThreadID); getErr == nil && thread.ActiveRunID != "" {
			_, _ = s.runtime.Stop(thread.ActiveRunID)
		}
	}
	return s.newConversation(ctx, user, channelID)
}

func (s *Service) newConversation(ctx context.Context, user policy.User, channelID string) (state.Conversation, error) {
	thread, err := s.runtime.CreateThread(user.Manifest)
	if err != nil {
		return state.Conversation{}, err
	}
	conversation := state.Conversation{
		UserID: user.ID, ChannelID: channelID, ThreadID: thread.ID, PolicyDigest: user.Digest,
	}
	if err := s.store.SaveConversation(ctx, conversation); err != nil {
		return state.Conversation{}, err
	}
	s.subscribe(ctx, conversation)
	return conversation, nil
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

// Recover re-subscribes to persisted conversations, re-arms pending timers, and
// repaints any in-flight approval cards after a restart.
func (s *Service) Recover(ctx context.Context) error {
	conversations, err := s.store.Conversations(ctx)
	if err != nil {
		return err
	}
	for _, conversation := range conversations {
		s.subscribe(ctx, conversation)
		thread, err := s.runtime.GetThread(conversation.ThreadID)
		if err != nil || thread.ActiveRunID == "" {
			continue
		}
		run, err := s.runtime.GetRun(thread.ActiveRunID)
		if err != nil {
			continue
		}
		if run.Status == aurora.RunInterrupted {
			if _, err := s.runtime.Retry(run.ID, aurora.RetryResume, nil); err != nil {
				s.logger.Warn("resume interrupted run", "run_id", run.ID, "error", err)
			} else if current, getErr := s.runtime.GetRun(run.ID); getErr == nil {
				run = current
			}
		}
		tasks, taskErr := s.runtime.Tasks(run.ID)
		if taskErr == nil {
			for _, task := range tasks {
				if task.State != aurora.TaskStatePending {
					continue
				}
				if isTimerTask(task) {
					s.timers.schedule(task)
					continue
				}
				s.createTaskMessage(ctx, conversation, task)
			}
		}
		s.updateRunMessage(ctx, run)
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

var mentionPattern = regexp.MustCompile(`<@[^>]+>`)

// cleanMentions strips Slack user mentions (e.g. the leading "<@U0BOT>") and
// trims surrounding whitespace.
func cleanMentions(text string) string {
	return strings.TrimSpace(mentionPattern.ReplaceAllString(text, ""))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

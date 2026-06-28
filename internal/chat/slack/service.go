// Package slack is the Slack chat adapter: it connects a Slack workspace to the
// Aurora runtime, turning Slack messages into agent runs, rendering run and
// approval state back into Slack, and resolving durable approval tasks from
// interactive buttons. It owns Slack-shaped presentation and command handling;
// the raw Socket Mode/Web API client lives in transport/slack, and per-user
// authorization in the policy subpackage.
package slack

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack/policy"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack/state"
	chattimers "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/timers"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/slack"
)

type Service struct {
	runtime  aurora.Runtime
	client   *slack.Client
	store    *state.Store
	policies atomic.Pointer[policy.Set]
	logger   *slog.Logger
	timers   *chattimers.Scheduler
	subs     *chat.Subscriptions
}

func New(
	runtime aurora.Runtime,
	client *slack.Client,
	store *state.Store,
	policies *policy.Set,
	logger *slog.Logger,
) *Service {
	s := &Service{
		runtime: runtime, client: client, store: store, logger: logger,
		timers: chattimers.NewScheduler(runtime, logger),
		subs:   chat.NewSubscriptions(runtime, logger),
	}
	s.policies.Store(policies)
	return s
}

// SetPolicies atomically swaps the authorization set, so the control plane can
// reroute a live bridge when bindings change without dropping the socket.
func (s *Service) SetPolicies(p *policy.Set) { s.policies.Store(p) }

// authorize routes a subject through the current policy set, tolerating a nil set
// (no bindings yet) as "not authorized".
func (s *Service) authorize(userID, channelID string) (policy.User, bool) {
	p := s.policies.Load()
	if p == nil {
		return policy.User{}, false
	}
	return p.Authorize(userID, channelID)
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
	defer s.timers.StopAll()
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
	user, ok := s.authorize(event.UserID, event.ChannelID)
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
	thread, err := s.runtime.CreateThread(user.Manifest, nil)
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
	s.subs.Add(ctx, conversation.ThreadID, func(event aurora.Event) {
		s.handleEvent(context.Background(), conversation, event)
	})
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
				if chattimers.IsTimerTask(task) {
					s.timers.Schedule(task)
					continue
				}
				s.createTaskMessage(ctx, conversation, task)
			}
		}
		s.updateRunMessage(ctx, run)
	}
	return nil
}

func (s *Service) unsubscribeAll() { s.subs.CloseAll() }

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

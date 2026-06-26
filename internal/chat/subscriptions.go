// Package chat holds the transport-agnostic pieces shared by every chat bridge
// (Telegram, Slack, …). Rendering, ingestion, commands, and persistence are
// genuinely platform-specific and live in the per-transport adapters; what lives
// here is the logic that is identical across them and dangerous to let drift.
package chat

import (
	"context"
	"log/slog"
	"sync"

	"aurora-capcompute/aurora"
)

// Subscriber is the slice of the runtime the subscription set needs.
// aurora.Runtime satisfies it.
type Subscriber interface {
	Subscribe(threadID string) (aurora.Event, <-chan aurora.Event, func(), error)
}

// Subscriptions tracks one live runtime subscription per thread and fans its
// events to a handler. It is shared by every bridge: the snapshot-then-stream
// pattern, idempotent arming, and teardown are identical across transports.
type Subscriptions struct {
	runtime Subscriber
	logger  *slog.Logger

	mu     sync.Mutex
	active map[string]func()
}

// NewSubscriptions builds an empty subscription set bound to runtime.
func NewSubscriptions(runtime Subscriber, logger *slog.Logger) *Subscriptions {
	return &Subscriptions{runtime: runtime, logger: logger, active: make(map[string]func())}
}

// Add subscribes to a thread and streams its events to handle until ctx is
// cancelled or the runtime closes the channel. Arming is idempotent: a thread
// that is already subscribed is a no-op, so Add is safe to call from both live
// conversation creation and restart recovery. handle is invoked first with the
// current snapshot, then with each subsequent event.
func (s *Subscriptions) Add(ctx context.Context, threadID string, handle func(aurora.Event)) {
	s.mu.Lock()
	if _, exists := s.active[threadID]; exists {
		s.mu.Unlock()
		return
	}
	snapshot, events, unsubscribe, err := s.runtime.Subscribe(threadID)
	if err != nil {
		s.mu.Unlock()
		s.logger.Warn("subscribe thread", "thread_id", threadID, "error", err)
		return
	}
	s.active[threadID] = unsubscribe
	s.mu.Unlock()

	go func() {
		handle(snapshot)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				handle(event)
			}
		}
	}()
}

// CloseAll cancels every live subscription and resets the set.
func (s *Subscriptions) CloseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.active {
		cancel()
	}
	s.active = make(map[string]func())
}

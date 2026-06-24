// Package source models a first-class *caller* of the agent. A Source owns a
// transport, identifies a subject, drives runs against the shared runtime, and
// renders output. Telegram and Slack are interactive sources today; a Kubernetes
// informer is the intended non-interactive source. See the Sources & Bindings
// RFC (docs/rfc-sources-and-bindings.md).
package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// Source is one caller bound to the agent runtime.
type Source interface {
	// Kind is the short identifier used in config and logs (e.g. "telegram").
	Kind() string
	// Start reconciles persisted state and then serves the source until ctx is
	// cancelled. It must return promptly once ctx is done.
	Start(ctx context.Context) error
}

// Run starts every source concurrently against the shared runtime. The first
// source to fail cancels the rest; Run returns that first non-cancel error, or
// nil on a clean (ctx-cancelled) shutdown.
func Run(ctx context.Context, logger *slog.Logger, sources ...Source) error {
	if len(sources) == 0 {
		return errors.New("no sources configured")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for _, src := range sources {
		wg.Add(1)
		go func(src Source) {
			defer wg.Done()
			logger.Info("source started", "kind", src.Kind())
			if err := src.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				once.Do(func() { firstErr = fmt.Errorf("source %s: %w", src.Kind(), err) })
				cancel()
			}
		}(src)
	}
	wg.Wait()
	return firstErr
}

package source

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSource struct {
	kind    string
	started chan struct{}
	failary error
	running atomic.Bool
}

func (f *fakeSource) Kind() string { return f.kind }

func (f *fakeSource) Start(ctx context.Context) error {
	f.running.Store(true)
	defer f.running.Store(false)
	if f.started != nil {
		close(f.started)
	}
	if f.failary != nil {
		return f.failary
	}
	<-ctx.Done()
	return ctx.Err()
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRunRequiresAtLeastOneSource(t *testing.T) {
	if err := Run(context.Background(), discardLogger()); err == nil {
		t.Fatal("expected an error with no sources")
	}
}

func TestRunCleanShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &fakeSource{kind: "a", started: make(chan struct{})}
	b := &fakeSource{kind: "b", started: make(chan struct{})}

	done := make(chan error, 1)
	go func() { done <- Run(ctx, discardLogger(), a, b) }()

	<-a.started
	<-b.started
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunPropagatesFirstErrorAndCancelsOthers(t *testing.T) {
	boom := errors.New("boom")
	failing := &fakeSource{kind: "failing", failary: boom}
	blocking := &fakeSource{kind: "blocking", started: make(chan struct{})}

	err := Run(context.Background(), discardLogger(), failing, blocking)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
	if blocking.running.Load() {
		t.Fatal("blocking source should have been cancelled")
	}
}

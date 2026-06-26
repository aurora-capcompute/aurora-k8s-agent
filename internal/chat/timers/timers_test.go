package timers

import (
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"aurora-capcompute/aurora"
	"capcompute/dispatcher"
)

type recordingResolver struct {
	fired chan string
}

func (r *recordingResolver) ResolveTask(taskID, _ string, res aurora.Resolution) (aurora.TaskSnapshot, error) {
	if res.Decision == aurora.TaskStateCompleted {
		r.fired <- taskID
	}
	return aurora.TaskSnapshot{}, nil
}

func newTestScheduler(resolver TaskResolver, now time.Time) *Scheduler {
	s := NewScheduler(resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return now }
	return s
}

func timerTask(id string, createdAt time.Time, durationSeconds int) aurora.TaskSnapshot {
	return aurora.TaskSnapshot{
		ID:           id,
		RunID:        "run-1",
		WebhookToken: "token-" + id,
		CreatedAt:    createdAt,
		Call: dispatcher.Call{
			Name: "timer.set",
			Args: []byte(`{"duration_seconds":` + strconv.Itoa(durationSeconds) + `}`),
		},
	}
}

func TestSchedulerFiresElapsedTimer(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	resolver := &recordingResolver{fired: make(chan string, 1)}
	sched := newTestScheduler(resolver, now)

	// Created an hour ago with a 1s timer => already elapsed => fires immediately.
	sched.Schedule(timerTask("task-1", now.Add(-time.Hour), 1))

	select {
	case id := <-resolver.fired:
		if id != "task-1" {
			t.Fatalf("fired task = %q, want task-1", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timer did not fire")
	}
}

func TestSchedulerCancelPreventsFiring(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	resolver := &recordingResolver{fired: make(chan string, 1)}
	sched := newTestScheduler(resolver, now)

	sched.Schedule(timerTask("task-1", now, 3600)) // fires in an hour
	sched.Cancel("task-1")

	select {
	case id := <-resolver.fired:
		t.Fatalf("cancelled timer fired: %q", id)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSchedulerCancelRun(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	resolver := &recordingResolver{fired: make(chan string, 1)}
	sched := newTestScheduler(resolver, now)

	sched.Schedule(timerTask("task-1", now, 3600))
	if _, ok := sched.FireAtFor("run-1"); !ok {
		t.Fatal("expected an armed timer for run-1")
	}
	sched.CancelRun("run-1")
	if _, ok := sched.FireAtFor("run-1"); ok {
		t.Fatal("timer should have been cancelled")
	}
}

func TestSchedulerIgnoresNonTimerTask(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	resolver := &recordingResolver{fired: make(chan string, 1)}
	sched := newTestScheduler(resolver, now)

	task := aurora.TaskSnapshot{
		ID: "task-1", RunID: "run-1", CreatedAt: now,
		Call: dispatcher.Call{Name: "k8s.apply", Args: []byte(`{}`)},
	}
	sched.Schedule(task)
	if _, ok := sched.FireAtFor("run-1"); ok {
		t.Fatal("non-timer task should not be armed")
	}
}

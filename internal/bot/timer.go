package bot

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"aurora-capcompute/aurora"
	"aurora-dispatchers/timer"
)

// taskResolver is the slice of the runtime the timer scheduler needs. aurora.Runtime
// satisfies it.
type taskResolver interface {
	ResolveTask(taskID, token string, resolution aurora.Resolution) (aurora.TaskSnapshot, error)
}

// timerScheduler fires durable timer.set tasks. When a timer task is created the
// scheduler arms an in-process timer; when it elapses the task is resolved with
// Completed, which resumes the waiting run. Fire times are derived from the
// persisted task (created_at + duration) so they are restart-safe: Recover re-arms
// pending timers, firing immediately for any that already elapsed.
type timerScheduler struct {
	resolver taskResolver
	logger   *slog.Logger
	now      func() time.Time

	mu     sync.Mutex
	timers map[string]*scheduledTimer
}

type scheduledTimer struct {
	timer  *time.Timer
	runID  string
	fireAt time.Time
}

func newTimerScheduler(resolver taskResolver, logger *slog.Logger) *timerScheduler {
	return &timerScheduler{
		resolver: resolver,
		logger:   logger,
		now:      time.Now,
		timers:   make(map[string]*scheduledTimer),
	}
}

// schedule arms a timer for the task. It is idempotent: arming an already-armed
// task is a no-op, so it is safe to call from both the task.created event and
// restart recovery.
func (s *timerScheduler) schedule(task aurora.TaskSnapshot) {
	fireAt, label, ok := timerFireAt(task)
	if !ok {
		s.logger.Warn("ignore malformed timer task", "task_id", task.ID)
		return
	}
	delay := fireAt.Sub(s.now())
	if delay < 0 {
		delay = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.timers[task.ID]; exists {
		return
	}
	taskID, token, runID := task.ID, task.WebhookToken, task.RunID
	s.timers[task.ID] = &scheduledTimer{
		timer:  time.AfterFunc(delay, func() { s.fire(taskID, token, label) }),
		runID:  runID,
		fireAt: fireAt,
	}
}

// fireAtFor returns the fire time of the timer currently armed for a run, if any.
func (s *timerScheduler) fireAtFor(runID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.timers {
		if entry.runID == runID {
			return entry.fireAt, true
		}
	}
	return time.Time{}, false
}

func (s *timerScheduler) fire(taskID, token, label string) {
	s.mu.Lock()
	delete(s.timers, taskID)
	s.mu.Unlock()

	data, err := json.Marshal(map[string]string{"status": "fired", "label": label})
	if err != nil {
		s.logger.Error("marshal timer result", "task_id", taskID, "error", err)
		return
	}
	if _, err := s.resolver.ResolveTask(taskID, token, aurora.Resolution{
		Decision: aurora.TaskStateCompleted, Data: data, Actor: "timer",
	}); err != nil {
		// The run may have been stopped or the task already resolved; that is a
		// benign no-op rather than an error worth surfacing to the user.
		s.logger.Info("timer resolution skipped", "task_id", taskID, "error", err)
	}
}

// cancel stops a single armed timer.
func (s *timerScheduler) cancel(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.timers[taskID]; ok {
		entry.timer.Stop()
		delete(s.timers, taskID)
	}
}

// cancelRun stops every timer armed for a run. Called when a run reaches a
// terminal state so a pending timer does not fire against a finished run.
func (s *timerScheduler) cancelRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.timers {
		if entry.runID == runID {
			entry.timer.Stop()
			delete(s.timers, id)
		}
	}
}

func (s *timerScheduler) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.timers {
		entry.timer.Stop()
		delete(s.timers, id)
	}
}

func isTimerTask(task aurora.TaskSnapshot) bool {
	return task.Call.Name == timer.Capability
}

// timerFireAt derives the absolute fire time and label from a timer task. It
// returns false for any task that is not a well-formed timer.
func timerFireAt(task aurora.TaskSnapshot) (time.Time, string, bool) {
	if !isTimerTask(task) {
		return time.Time{}, "", false
	}
	var request timer.SetRequest
	if err := json.Unmarshal(task.Call.Args, &request); err != nil || request.DurationSeconds <= 0 {
		return time.Time{}, "", false
	}
	fireAt := task.CreatedAt.Add(time.Duration(request.DurationSeconds) * time.Second)
	return fireAt, request.Label, true
}

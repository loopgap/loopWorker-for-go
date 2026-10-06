package dispatcher

import (
	"context"
	"errors"
	"sync"
	"testing"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
)

// startFailProvider is a TaskProvider whose StartTask always fails, standing in
// for a store that rejects the queued -> running transition.
type startFailProvider struct {
	*scheduler.Scheduler
	mu        sync.Mutex
	startErr  error
	failedIDs []string
}

// StartTask fails while startErr is set, and otherwise delegates to the real
// scheduler so a "recovered" task still enters `running` normally.
func (p *startFailProvider) StartTask(ctx context.Context, taskID, workerID string) error {
	p.mu.Lock()
	err := p.startErr
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return p.Scheduler.StartTask(ctx, taskID, workerID)
}

func (p *startFailProvider) FailTask(ctx context.Context, taskID, workerID, errMsg string) error {
	p.mu.Lock()
	p.failedIDs = append(p.failedIDs, taskID)
	p.mu.Unlock()
	return p.Scheduler.FailTask(ctx, taskID, workerID, errMsg)
}

func (p *startFailProvider) failed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.failedIDs...)
}

// TestDispatchDoesNotLoseTaskWhenStartFails is the F2 regression guard. If the
// task is popped from the queue and StartTask then fails, the task must still
// reach a terminal state: either it is recorded failed, or it is back in the
// queue. Silently dropping it strands the task in `running` forever.
func TestDispatchDoesNotLoseTaskWhenStartFails(t *testing.T) {
	bus := testBus(t)
	s := scheduler.NewScheduler(bus)
	t.Cleanup(func() { _ = s.Close() })

	provider := &startFailProvider{Scheduler: s, startErr: errors.New("store rejected start")}
	d := NewDispatcher(provider, bus)

	if err := d.RegisterWorker(context.Background(), "w-1", "echo"); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	task, err := s.CreateTask(context.Background(), "echo", nil, []byte("payload"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.QueueTask(context.Background(), task.ID); err != nil {
		t.Fatalf("queue task: %v", err)
	}

	// Dispatch must report the failure instead of returning a task nobody owns.
	dispatched, _, err := d.Dispatch(context.Background())
	if err == nil {
		t.Fatalf("Dispatch returned no error after StartTask failed (task=%v)", dispatched)
	}
	if dispatched != nil {
		t.Errorf("Dispatch handed back task %s despite a failed start", dispatched.ID)
	}

	// The worker must not stay busy on a task that never started.
	if got := d.AvailableWorkerCount(); got != 1 {
		t.Errorf("worker was left busy after a failed start: available=%d, want 1", got)
	}

	failed := provider.failed()
	if len(failed) != 1 || failed[0] != task.ID {
		t.Errorf("expected the orphaned task %s to be recorded failed, got %v", task.ID, failed)
	}

	// The orphaned task must remain visible in the store rather than vanish.
	if _, ok := s.GetTask(task.ID); !ok {
		t.Fatalf("task %s vanished from the store", task.ID)
	}
}

// TestOrphanedQueuedTaskIsRecoveredInProcess is the scheduler-side half of F2.
// Scheduler.FailTask used to accept only `running`, so a task orphaned by a
// failed StartTask stayed `queued` and invisible to every counter until the
// process restarted and re-adopted it. FailTask now accepts `queued` too, so
// the failure is recorded in-process and the task earns another attempt.
//
// The invariant under test: a task must never sit outside the queue while the
// store still claims it is `queued` - that state is invisible to QueueSize, to
// the health check and to every drain path.
func TestOrphanedQueuedTaskIsRecoveredInProcess(t *testing.T) {
	bus := testBus(t)
	s := scheduler.NewScheduler(bus)
	provider := &startFailProvider{Scheduler: s, startErr: errors.New("store rejected start")}
	d := NewDispatcher(provider, bus)

	if err := d.RegisterWorker(context.Background(), "w-1", "echo"); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	task, err := s.CreateTask(context.Background(), "echo", nil, []byte("payload"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.QueueTask(context.Background(), task.ID); err != nil {
		t.Fatalf("queue task: %v", err)
	}

	if _, _, err := d.Dispatch(context.Background()); err == nil {
		t.Fatal("expected Dispatch to report the failed start")
	}

	// The dispatcher released the worker and reported the error, so nothing is
	// silently lost on the dispatcher side.
	if got := d.AvailableWorkerCount(); got != 1 {
		t.Errorf("available workers = %d, want 1", got)
	}

	// The task was dequeued by Dispatch but never entered `running`. FailTask
	// must still record the failure and put it back on the queue, otherwise it
	// is stranded in a state no counter or health check can see.
	orphaned, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatalf("task %s vanished from the store", task.ID)
	}
	if orphaned.State != scheduler.StateQueued {
		t.Fatalf("orphan state = %s, want queued: a failed start must be retryable, not terminal", orphaned.State)
	}
	if orphaned.Retry != 1 {
		t.Errorf("orphan retry = %d, want 1: the failed attempt must be counted or the retry budget is infinite", orphaned.Retry)
	}
	if s.QueueSize() != 1 {
		t.Errorf("queue size = %d, want 1: the orphaned task must be back on the queue, not stranded", s.QueueSize())
	}

	// The point of the fix: recovery no longer requires a restart.
	if err := s.FailTask(context.Background(), task.ID, "w-1", "orphan again"); err != nil {
		t.Fatalf("FailTask on a never-started task: %v", err)
	}
	if again, _ := s.GetTask(task.ID); again.Retry != 2 {
		t.Errorf("retry = %d after the second failure, want 2", again.Retry)
	}
}

// TestDispatchReleasesWorkerWhenStartFails pins the worker-availability invariant
// on its own: a failed start must never consume a worker slot, or a poison task
// can shrink the pool to zero and starve every healthy task behind it.
func TestDispatchReleasesWorkerWhenStartFails(t *testing.T) {
	bus := testBus(t)
	s := scheduler.NewScheduler(bus)
	t.Cleanup(func() { _ = s.Close() })

	provider := &startFailProvider{Scheduler: s, startErr: errors.New("store rejected start")}
	d := NewDispatcher(provider, bus)

	for _, id := range []string{"w-1", "w-2"} {
		if err := d.RegisterWorker(context.Background(), id, "echo"); err != nil {
			t.Fatalf("register worker %s: %v", id, err)
		}
	}

	// Two poison tasks in the queue: both starts fail.
	for i := 0; i < 2; i++ {
		task, err := s.CreateTask(context.Background(), "echo", nil, []byte("poison"))
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		if err := s.QueueTask(context.Background(), task.ID); err != nil {
			t.Fatalf("queue task: %v", err)
		}
	}

	if _, _, err := d.Dispatch(context.Background()); err == nil {
		t.Fatal("expected Dispatch to report the failed start")
	}
	if _, _, err := d.Dispatch(context.Background()); err == nil {
		t.Fatal("expected Dispatch to report the failed start")
	}

	if got := d.AvailableWorkerCount(); got != 2 {
		t.Errorf("available workers = %d, want 2: poison tasks must not starve the pool", got)
	}
}

// TestDispatchRecoversAfterTransientStartFailure proves the worker pool keeps
// working once the underlying fault clears, i.e. a transient store error does
// not permanently degrade throughput.
func TestDispatchRecoversAfterTransientStartFailure(t *testing.T) {
	bus := testBus(t)
	s := scheduler.NewScheduler(bus)
	t.Cleanup(func() { _ = s.Close() })

	provider := &startFailProvider{Scheduler: s, startErr: errors.New("transient")}
	d := NewDispatcher(provider, bus)

	if err := d.RegisterWorker(context.Background(), "w-1", "echo"); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	poison, err := s.CreateTask(context.Background(), "echo", nil, []byte("poison"))
	if err != nil {
		t.Fatalf("create poison task: %v", err)
	}
	if err := s.QueueTask(context.Background(), poison.ID); err != nil {
		t.Fatalf("queue poison task: %v", err)
	}
	if _, _, err := d.Dispatch(context.Background()); err == nil {
		t.Fatal("expected the poison dispatch to fail")
	}

	// The fault clears.
	provider.mu.Lock()
	provider.startErr = nil
	provider.mu.Unlock()

	// The scheduler heap is a priority queue, and the failed start put the
	// poison task back on it with Retry=1. So the next dispatch may hand out
	// the poison task again now that the fault has cleared. Drain until the
	// healthy task comes out.
	healthy, err := s.CreateTask(context.Background(), "echo", nil, []byte("healthy"))
	if err != nil {
		t.Fatalf("create healthy task: %v", err)
	}
	if err := s.QueueTask(context.Background(), healthy.ID); err != nil {
		t.Fatalf("queue healthy task: %v", err)
	}

	var task *scheduler.Task
	var worker *WorkerInfo
	for i := 0; i < 8; i++ {
		candidate, w, err := d.Dispatch(context.Background())
		if err != nil {
			t.Fatalf("dispatch after recovery: %v", err)
		}
		if candidate.ID == healthy.ID {
			task, worker = candidate, w
			break
		}
		// Not ours: fail it so the next round reaches the healthy task. This
		// must go through the Dispatcher, not the provider - only the
		// Dispatcher marks the worker free again, which is what the executor
		// does in production (executor.go:409).
		if err := d.FailTask(context.Background(), candidate.ID, w.ID, "not the target"); err != nil {
			t.Fatalf("fail unrelated task %s: %v", candidate.ID, err)
		}
	}
	if task == nil {
		t.Fatal("healthy task was never dispatched")
	}
	if worker == nil || worker.ID != "w-1" {
		t.Fatalf("expected worker w-1, got %v", worker)
	}
	if err := d.CompleteTask(context.Background(), task.ID, worker.ID, []byte("done")); err != nil {
		t.Errorf("complete task: %v", err)
	}

	final, _ := s.GetTask(healthy.ID)
	if final.State != scheduler.StateCompleted {
		t.Errorf("healthy task state = %s, want completed", final.State)
	}
}

func testBus(t *testing.T) *event.EventBus {
	t.Helper()
	bus := event.NewEventBus(nil)
	t.Cleanup(bus.Close)
	return bus
}

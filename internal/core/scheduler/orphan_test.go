package scheduler

import (
	"context"
	"errors"
	"testing"

	lwerrors "loopworker/pkg/errors"
)

func queuedTask(t *testing.T, s *Scheduler, taskType string) *Task {
	t.Helper()
	ctx := context.Background()
	task, err := s.CreateTask(ctx, taskType, nil, []byte("payload"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.QueueTask(ctx, task.ID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	return task
}

// A task the dispatcher popped but the worker never started stays queued. Without
// this the task is invisible to every counter and only comes back on restart.
func TestFailTaskAcceptsATaskThatWasNeverStarted(t *testing.T) {
	ctx := context.Background()
	s := openTestScheduler(t, t.TempDir(), nil)
	task := queuedTask(t, s, "orphan")

	if popped := s.DequeueTask(); popped == nil || popped.ID != task.ID {
		t.Fatalf("DequeueTask = %v, want the task we queued", popped)
	}
	if got := s.QueueSize(); got != 0 {
		t.Fatalf("queue size after dequeue = %d, want 0", got)
	}

	if err := s.FailTask(ctx, task.ID, "w-1", "worker refused the task"); err != nil {
		t.Fatalf("FailTask on a never-started task: %v", err)
	}
	if got := s.QueueSize(); got != 1 {
		t.Fatalf("queue size after failing a popped task = %d, want 1 (it must be retried)", got)
	}
	stored, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatalf("task %s disappeared", task.ID)
	}
	if stored.Retry != 1 {
		t.Fatalf("retry = %d, want 1", stored.Retry)
	}
}

// insertByPriority is a bare heap.Push. Failing a task that is still in the heap
// would therefore run the same task twice, at the same time.
func TestFailTaskDoesNotQueueATaskTwice(t *testing.T) {
	ctx := context.Background()
	s := openTestScheduler(t, t.TempDir(), nil)
	task := queuedTask(t, s, "still-queued")

	if err := s.FailTask(ctx, task.ID, "w-1", "first failure"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	if got := s.QueueSize(); got != 1 {
		t.Fatalf("queue size = %d, want 1: the task was already queued and must not be pushed twice", got)
	}

	// Exhaust the retry budget; the task must end up dead-lettered, not duplicated.
	for i := 0; i < 8; i++ {
		if err := s.FailTask(ctx, task.ID, "w-1", "repeated failure"); err != nil {
			t.Fatalf("FailTask iteration %d: %v", i, err)
		}
		stored, _ := s.GetTask(task.ID)
		if stored.State == StateDeadLetter {
			break
		}
		if got := s.QueueSize(); got != 1 {
			t.Fatalf("queue size at iteration %d = %d, want 1", i, got)
		}
	}
	stored, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatalf("task %s disappeared", task.ID)
	}
	if stored.State != StateDeadLetter {
		t.Fatalf("state = %s after %d retries, want %s", stored.State, stored.Retry, StateDeadLetter)
	}
	if got := s.QueueSize(); got != 0 {
		t.Fatalf("queue size = %d, want 0 once the task is dead-lettered", got)
	}
}

// A self-edge can never become ready, and unlike a user-supplied cycle it is
// never legitimate: pkg/api rejects those before they get here.
func TestAddDependencyRefusesASelfEdge(t *testing.T) {
	ctx := context.Background()
	s := openTestScheduler(t, t.TempDir(), nil)
	task := queuedTask(t, s, "self-edge")

	err := s.AddDependency(ctx, task.ID, task.ID)
	if !errors.Is(err, lwerrors.ErrWorkflowCycle) {
		t.Fatalf("AddDependency(task, task) error = %v, want ErrWorkflowCycle", err)
	}
	stored, _ := s.GetTask(task.ID)
	if len(stored.Dependencies) != 0 {
		t.Fatalf("dependencies = %v, want none persisted", stored.Dependencies)
	}
}

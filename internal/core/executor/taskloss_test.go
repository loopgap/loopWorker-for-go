package executor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"loopworker/internal/core/scheduler"
)

// TestExecutorRunDoesNotLoseTasksUnderBurst is the diagnostic companion to
// TestExecutorRunDrivesTasksToCompleted. That test fails on roughly one run in
// five with 1-6 of 100 tasks dead-lettered, and the assertion only prints the
// state counts. This one prints *why* the task was failed, which is the only
// thing that makes the flake fixable.
func TestExecutorRunDoesNotLoseTasksUnderBurst(t *testing.T) {
	rt := newRuntime(t, "echo", func(ctx context.Context, input []byte) ([]byte, error) {
		return input, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rt.executor.Run(ctx) }()

	const workers, tasks = 4, 100
	for i := 0; i < workers; i++ {
		if err := rt.executor.StartWorker(ctx, fmt.Sprintf("w-%d", i), "echo"); err != nil {
			t.Fatalf("start worker: %v", i)
		}
	}
	for i := 0; i < tasks; i++ {
		task, err := rt.sched.CreateTask(ctx, "echo", nil, []byte(fmt.Sprintf("payload-%d", i)))
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		if err := rt.sched.QueueTask(ctx, task.ID); err != nil {
			t.Fatalf("queue task: %v", err)
		}
	}

	counts := rt.stateCounts(t, tasks, 20*time.Second)
	if counts[scheduler.StateCompleted] == tasks {
		return // the common case; nothing to report
	}

	for _, task := range rt.allTasks(t) {
		if task.State == scheduler.StateCompleted {
			continue
		}
		t.Errorf("task %s ended in state %s after %d retries: %s (input %q)",
			task.ID, task.State, task.Retry, task.Error, task.Input)
	}
	t.Errorf("state counts: %v", counts)
}

func (rt *runtime) allTasks(t *testing.T) []*scheduler.Task {
	t.Helper()
	tasks, err := rt.sched.ListTasksErr(scheduler.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasksErr: %v", err)
	}
	return tasks
}

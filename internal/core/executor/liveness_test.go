package executor

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
)

// runtime wires the real production object graph (scheduler -> dispatcher ->
// sandbox -> executor) so a test exercises the same path the server does.
type runtime struct {
	bus      *event.EventBus
	sched    *scheduler.Scheduler
	disp     *dispatcher.Dispatcher
	sandbox  *sandbox.Sandbox
	executor *Executor
}

func newRuntime(t *testing.T, pluginName string, fn func(context.Context, []byte) ([]byte, error)) *runtime {
	t.Helper()

	bus := event.NewEventBus(nil)
	t.Cleanup(bus.Close)

	sched := scheduler.NewScheduler(bus)
	disp := dispatcher.NewDispatcher(sched, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})

	if err := sb.LoadPlugin(pluginName, sandbox.NewMockPlugin(pluginName, "1.0",
		func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
			return fn(ctx, input)
		})); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	return &runtime{bus: bus, sched: sched, disp: disp, sandbox: sb, executor: NewExecutor(sched, disp, sb, bus, nil)}
}

// stateCounts polls the scheduler until stop reports every task terminal or the
// deadline expires. Polling the store (not sleeping) is what makes this a liveness
// assertion: it fails if the engine never advances a task past `queued`.
func (rt *runtime) stateCounts(t *testing.T, total int, timeout time.Duration) map[scheduler.TaskState]int {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var counts map[scheduler.TaskState]int
	for time.Now().Before(deadline) {
		tasks, err := rt.sched.ListTasksErr(scheduler.TaskFilter{})
		if err != nil {
			t.Fatalf("list tasks: %v", err)
		}
		counts = map[scheduler.TaskState]int{}
		terminal := 0
		for _, task := range tasks {
			counts[task.State]++
			switch task.State {
			case scheduler.StateCompleted, scheduler.StateFailed,
				scheduler.StateCancelled, scheduler.StateDeadLetter:
				terminal++
			}
		}
		if len(tasks) == total && terminal == total {
			return counts
		}
		time.Sleep(10 * time.Millisecond)
	}
	return counts
}

// TestExecutorRunDrivesTasksToCompleted is the F1 regression guard: Executor.Run
// must be able to move a burst of tasks all the way from queued to completed.
// If the dispatch loop stops being pumped, every task is stranded in `queued`
// and this fails with the state breakdown.
func TestExecutorRunDrivesTasksToCompleted(t *testing.T) {
	rt := newRuntime(t, "echo", func(ctx context.Context, input []byte) ([]byte, error) {
		return input, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rt.executor.Run(ctx) }()

	const workers, tasks = 4, 100
	for i := 0; i < workers; i++ {
		if err := rt.executor.StartWorker(ctx, fmt.Sprintf("w-%d", i), "echo"); err != nil {
			t.Fatalf("start worker: %v", err)
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
	if counts[scheduler.StateCompleted] != tasks {
		t.Errorf("expected %d completed, got %d (states: %v)", tasks, counts[scheduler.StateCompleted], counts)
	}
	if stuck := counts[scheduler.StateQueued] + counts[scheduler.StateRunning] + counts[scheduler.StatePending]; stuck != 0 {
		t.Errorf("%d tasks never reached a terminal state (states: %v)", stuck, counts)
	}

	if err := rt.executor.StopAllWorkers(context.Background()); err != nil {
		t.Errorf("stop workers: %v", err)
	}
}

// TestExecutorRunAcceptsTasksQueuedBeforeWorkers proves the wake-up path is not
// order dependent: a task queued while zero workers are free must still run once
// a worker shows up. A lost wake-up signal strands the task in `queued`.
func TestExecutorRunAcceptsTasksQueuedBeforeWorkers(t *testing.T) {
	rt := newRuntime(t, "echo", func(ctx context.Context, input []byte) ([]byte, error) {
		return input, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rt.executor.Run(ctx) }()

	task, err := rt.sched.CreateTask(ctx, "echo", nil, []byte("early"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := rt.sched.QueueTask(ctx, task.ID); err != nil {
		t.Fatalf("queue task: %v", err)
	}

	// No workers yet: the pump has nothing to hand the task to.
	if err := rt.executor.StartWorker(ctx, "late", "echo"); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	counts := rt.stateCounts(t, 1, 10*time.Second)
	if counts[scheduler.StateCompleted] != 1 {
		t.Errorf("expected 1 completed, got %d (states: %v)", counts[scheduler.StateCompleted], counts)
	}
}

// TestExecutorRunRespectsDependencyOrder verifies a dependent task is not run
// before its upstream completes. With a single worker this can only pass if the
// engine actually sequences work instead of draining the queue flat.
func TestExecutorRunRespectsDependencyOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string

	rt := newRuntime(t, "step", func(ctx context.Context, input []byte) ([]byte, error) {
		mu.Lock()
		order = append(order, string(input))
		mu.Unlock()
		return input, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rt.executor.Run(ctx) }()

	if err := rt.executor.StartWorker(ctx, "w-0", "step"); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	upstream, err := rt.sched.CreateTask(ctx, "step", nil, []byte("upstream"))
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	downstream, err := rt.sched.CreateTask(ctx, "step", nil, []byte("downstream"))
	if err != nil {
		t.Fatalf("create downstream: %v", err)
	}
	if err := rt.sched.AddDependency(ctx, downstream.ID, upstream.ID); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	if err := rt.sched.QueueTask(ctx, upstream.ID); err != nil {
		t.Fatalf("queue upstream: %v", err)
	}

	counts := rt.stateCounts(t, 2, 10*time.Second)
	if counts[scheduler.StateCompleted] != 2 {
		t.Errorf("expected 2 completed, got %d (states: %v)", counts[scheduler.StateCompleted], counts)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "upstream" || order[1] != "downstream" {
		t.Errorf("expected execution order [upstream downstream], got %v", order)
	}
}

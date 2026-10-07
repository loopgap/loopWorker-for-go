package executor

import (
	"context"
	"testing"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
)

// Every worker spawn and every worker exit reached subscribers twice: the
// executor publishes the lifecycle event itself and then calls
// dispatcher.RegisterWorker / UnregisterWorker, which publish the same event
// again. The visible symptom was an event log in which each worker appeared to
// spawn twice and exit twice, and - because shutdown cancels the context that
// Run passes to StopAllWorkers - a drain in which every one of those duplicates
// was rejected by the event store with "context canceled" instead.
//
// The existing lifecycle test only asserted that at least one event arrived,
// which a duplicate satisfies, so it could never have caught this.

func TestWorkerLifecycleEventsFireExactlyOnce(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)

	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})
	_ = sb.LoadPlugin("test", plugin)

	spawnSub := bus.Subscribe(event.EventWorkerSpawned, 16)
	exitSub := bus.Subscribe(event.EventWorkerExited, 16)

	if err := e.StartWorker(context.Background(), "w1", "test"); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	if err := e.StopWorker(context.Background(), "w1"); err != nil {
		t.Fatalf("StopWorker: %v", err)
	}

	if got := drainEvents(spawnSub.Chan()); got != 1 {
		t.Errorf("worker.spawned fired %d time(s) for one worker, want exactly 1", got)
	}
	if got := drainEvents(exitSub.Chan()); got != 1 {
		t.Errorf("worker.exited fired %d time(s) for one worker, want exactly 1", got)
	}
}

// drainEvents counts what is already queued, after giving the publishers a
// moment: Publish is synchronous, but the subscriber buffer is fed from a
// fan-out goroutine.
func drainEvents(ch <-chan event.Event) int {
	time.Sleep(150 * time.Millisecond)
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

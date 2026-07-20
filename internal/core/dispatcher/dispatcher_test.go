package dispatcher

import (
	"context"
	"testing"
	"time"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
)

func TestRegisterWorker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	if err := d.RegisterWorker(context.Background(), "worker-1", "plugin-a"); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if d.WorkerCount() != 1 {
		t.Errorf("expected 1 worker, got %d", d.WorkerCount())
	}
}

func TestRegisterDuplicateWorker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")
	if err := d.RegisterWorker(context.Background(), "worker-1", "plugin-a"); err == nil {
		t.Error("expected error registering duplicate worker")
	}
}

func TestUnregisterWorker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")
	if err := d.UnregisterWorker(context.Background(), "worker-1"); err != nil {
		t.Fatalf("unregister worker: %v", err)
	}
	if d.WorkerCount() != 0 {
		t.Errorf("expected 0 workers after unregister, got %d", d.WorkerCount())
	}
}

func TestUnregisterNonexistentWorker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	if err := d.UnregisterWorker(context.Background(), "nonexistent"); err == nil {
		t.Error("expected error unregistering nonexistent worker")
	}
}

func TestDispatch(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)

	dispatched, worker, err := d.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if dispatched.ID != task.ID {
		t.Errorf("expected task %s, got %s", task.ID, dispatched.ID)
	}
	if worker.ID != "worker-1" {
		t.Errorf("expected worker worker-1, got %s", worker.ID)
	}
	if !worker.Busy {
		t.Error("worker should be busy after dispatch")
	}
}

func TestDispatchNoTasks(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")

	_, _, err := d.Dispatch(context.Background())
	if err == nil {
		t.Error("expected error dispatching with no tasks")
	}
}

func TestDispatchNoWorkers(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)

	_, _, err := d.Dispatch(context.Background())
	if err == nil {
		t.Error("expected error dispatching with no workers")
	}
}

func TestCompleteTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_, worker, _ := d.Dispatch(context.Background())

	if err := d.CompleteTask(context.Background(), task.ID, worker.ID, []byte("done")); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != scheduler.StateCompleted {
		t.Errorf("expected state completed, got %s", task.State)
	}
	if worker.Busy {
		t.Error("worker should not be busy after complete")
	}
}

func TestFailTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_, worker, _ := d.Dispatch(context.Background())

	if err := d.FailTask(context.Background(), task.ID, worker.ID, "error"); err != nil {
		t.Fatalf("fail task: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != scheduler.StateQueued {
		t.Errorf("expected state queued (for retry), got %s", task.State)
	}
	if worker.Busy {
		t.Error("worker should not be busy after fail")
	}
}

func TestListWorkers(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")
	_ = d.RegisterWorker(context.Background(), "worker-2", "plugin-b")

	workers := d.ListWorkers()
	if len(workers) != 2 {
		t.Errorf("expected 2 workers, got %d", len(workers))
	}
}

func TestAvailableWorkerCount(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")
	_ = d.RegisterWorker(context.Background(), "worker-2", "plugin-b")

	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_, _, _ = d.Dispatch(context.Background())

	if d.AvailableWorkerCount() != 1 {
		t.Errorf("expected 1 available worker, got %d", d.AvailableWorkerCount())
	}
}

func TestDispatcherEventPublishing(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventWorkerSpawned, 10)
	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)

	_ = d.RegisterWorker(context.Background(), "worker-1", "plugin-a")

	select {
	case <-sub.Chan():
	case <-time.After(time.Second):
		t.Error("timeout waiting for worker spawned event")
	}
}

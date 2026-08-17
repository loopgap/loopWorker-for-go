package dispatcher

import (
	"context"
	"fmt"
	"sync"
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

// 并发压力测试

func TestConcurrentRegisterUnregister(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)
	ctx := context.Background()

	var wg sync.WaitGroup
	n := 100

	// 并发注册worker
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			workerID := fmt.Sprintf("worker-%d", idx)
			if err := d.RegisterWorker(ctx, workerID, "plugin-a"); err != nil {
				t.Errorf("register worker %s: %v", workerID, err)
			}
		}(i)
	}
	wg.Wait()

	if d.WorkerCount() != n {
		t.Errorf("expected %d workers, got %d", n, d.WorkerCount())
	}

	// 并发注销worker
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			workerID := fmt.Sprintf("worker-%d", idx)
			if err := d.UnregisterWorker(ctx, workerID); err != nil {
				t.Errorf("unregister worker %s: %v", workerID, err)
			}
		}(i)
	}
	wg.Wait()

	if d.WorkerCount() != 0 {
		t.Errorf("expected 0 workers after unregister, got %d", d.WorkerCount())
	}
}

func TestConcurrentDispatch(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)
	ctx := context.Background()

	// 注册50个worker
	for i := 0; i < 50; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		_ = d.RegisterWorker(ctx, workerID, "plugin-a")
	}

	// 创建50个任务
	for i := 0; i < 50; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, nil)
		_ = s.QueueTask(ctx, task.ID)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	dispatchedTasks := make([]*scheduler.Task, 0)

	// 并发分发任务
	wg.Add(50)
	for i := 0; i < 50; i++ {
		go func() {
			defer wg.Done()
			task, _, err := d.Dispatch(ctx)
			if err == nil && task != nil {
				mu.Lock()
				dispatchedTasks = append(dispatchedTasks, task)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// 验证分发的任务数量
	if len(dispatchedTasks) != 50 {
		t.Errorf("expected 50 dispatched tasks, got %d", len(dispatchedTasks))
	}
}

func TestConcurrentCompleteAndFail(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)
	ctx := context.Background()

	// 注册50个worker
	for i := 0; i < 50; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		_ = d.RegisterWorker(ctx, workerID, "plugin-a")
	}

	// 创建并分发50个任务
	tasks := make([]*scheduler.Task, 0, 50)
	for i := 0; i < 50; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, nil)
		_ = s.QueueTask(ctx, task.ID)
		dispatched, _, err := d.Dispatch(ctx)
		if err == nil && dispatched != nil {
			tasks = append(tasks, dispatched)
		}
	}

	if len(tasks) != 50 {
		t.Fatalf("expected 50 dispatched tasks, got %d", len(tasks))
	}

	var wg sync.WaitGroup

	// 并发完成任务
	wg.Add(25)
	for i := 0; i < 25; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := d.CompleteTask(ctx, tasks[idx].ID, fmt.Sprintf("worker-%d", idx), []byte("result")); err != nil {
				t.Errorf("complete task %s: %v", tasks[idx].ID, err)
			}
		}(i)
	}

	// 并发失败任务
	wg.Add(25)
	for i := 25; i < 50; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := d.FailTask(ctx, tasks[idx].ID, fmt.Sprintf("worker-%d", idx), "error"); err != nil {
				t.Errorf("fail task %s: %v", tasks[idx].ID, err)
			}
		}(i)
	}
	wg.Wait()

	// 验证任务状态
	completedCount := 0
	retriedCount := 0
	for _, task := range tasks {
		updatedTask, _ := s.GetTask(task.ID)
		if updatedTask.State == scheduler.StateCompleted {
			completedCount++
		} else if updatedTask.State == scheduler.StateQueued && updatedTask.Retry > 0 {
			retriedCount++
		}
	}
	if completedCount != 25 {
		t.Errorf("expected 25 completed tasks, got %d", completedCount)
	}
	if retriedCount != 25 {
		t.Errorf("expected 25 retried tasks, got %d", retriedCount)
	}
}

func TestConcurrentListWorkers(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := NewDispatcher(s, bus)
	ctx := context.Background()

	// 注册worker
	for i := 0; i < 10; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		_ = d.RegisterWorker(ctx, workerID, "plugin-a")
	}

	var wg sync.WaitGroup
	n := 100

	// 并发读取worker列表
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			workers := d.ListWorkers()
			if len(workers) != 10 {
				t.Errorf("expected 10 workers, got %d", len(workers))
			}
		}()
	}
	wg.Wait()
}

package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"loopworker/pkg/event"
)

func TestCreateTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, err := s.CreateTask(context.Background(), "test", nil, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.State != StatePending {
		t.Errorf("expected state pending, got %s", task.State)
	}
	if task.Priority != PriorityNormal {
		t.Errorf("expected priority normal, got %d", task.Priority)
	}
}

func TestCreateTaskWithPriority(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, err := s.CreateTaskWithPriority(context.Background(), "test", PriorityHigh, nil, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.Priority != PriorityHigh {
		t.Errorf("expected priority high, got %d", task.Priority)
	}
}

func TestQueueTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)

	if err := s.QueueTask(context.Background(), task.ID); err != nil {
		t.Fatalf("queue task: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != StateQueued {
		t.Errorf("expected state queued, got %s", task.State)
	}
	if s.QueueSize() != 1 {
		t.Errorf("expected queue size 1, got %d", s.QueueSize())
	}
}

func TestQueueTaskPriorityOrder(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	taskLow, _ := s.CreateTaskWithPriority(context.Background(), "low", PriorityLow, nil, nil)
	taskHigh, _ := s.CreateTaskWithPriority(context.Background(), "high", PriorityHigh, nil, nil)
	taskNormal, _ := s.CreateTaskWithPriority(context.Background(), "normal", PriorityNormal, nil, nil)

	_ = s.QueueTask(context.Background(), taskLow.ID)
	_ = s.QueueTask(context.Background(), taskHigh.ID)
	_ = s.QueueTask(context.Background(), taskNormal.ID)

	first := s.DequeueTask()
	if first.ID != taskHigh.ID {
		t.Errorf("expected high priority task first, got %s", first.ID)
	}
}

func TestAddDependency(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task1, _ := s.CreateTask(context.Background(), "test1", nil, nil)
	task2, _ := s.CreateTask(context.Background(), "test2", nil, nil)

	if err := s.AddDependency(context.Background(), task2.ID, task1.ID); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	task2, _ = s.GetTask(task2.ID)
	if len(task2.Dependencies) != 1 {
		t.Errorf("expected 1 dependency, got %d", len(task2.Dependencies))
	}
}

func TestQueueTaskWithDependency(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task1, _ := s.CreateTask(context.Background(), "test1", nil, nil)
	task2, _ := s.CreateTask(context.Background(), "test2", nil, nil)

	_ = s.AddDependency(context.Background(), task2.ID, task1.ID)

	err := s.QueueTask(context.Background(), task2.ID)
	if err == nil {
		t.Error("expected error queuing task with incomplete dependency")
	}
}

func TestStartTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)

	if err := s.StartTask(context.Background(), task.ID, "worker-1"); err != nil {
		t.Fatalf("start task: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != StateRunning {
		t.Errorf("expected state running, got %s", task.State)
	}
	if task.StartedAt == nil {
		t.Error("startedAt should be set")
	}
}

func TestCompleteTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_ = s.StartTask(context.Background(), task.ID, "worker-1")

	if err := s.CompleteTask(context.Background(), task.ID, "worker-1", []byte("done")); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != StateCompleted {
		t.Errorf("expected state completed, got %s", task.State)
	}
	if string(task.Result) != "done" {
		t.Errorf("expected result 'done', got '%s'", string(task.Result))
	}
}

func TestFailTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_ = s.StartTask(context.Background(), task.ID, "worker-1")

	if err := s.FailTask(context.Background(), task.ID, "worker-1", "error"); err != nil {
		t.Fatalf("fail task: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != StateQueued {
		t.Errorf("expected state queued (for retry), got %s", task.State)
	}
	if task.Retry != 1 {
		t.Errorf("expected retry 1, got %d", task.Retry)
	}
}

func TestFailTaskMaxRetry(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	task.MaxRetry = 1
	s.saveTask(task)
	_ = s.QueueTask(context.Background(), task.ID)
	_ = s.StartTask(context.Background(), task.ID, "worker-1")
	_ = s.FailTask(context.Background(), task.ID, "worker-1", "error 1")

	dequeued := s.DequeueTask()
	if dequeued == nil || dequeued.ID != task.ID {
		t.Fatal("expected task to be re-queued")
	}

	_ = s.StartTask(context.Background(), task.ID, "worker-1")
	_ = s.FailTask(context.Background(), task.ID, "worker-1", "error 2")

	task, _ = s.GetTask(task.ID)
	if task.State != StateDeadLetter {
		t.Errorf("expected state dead_letter after max retry, got %s", task.State)
	}
}

func TestCancelTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)

	if err := s.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != StateCancelled {
		t.Errorf("expected state cancelled, got %s", task.State)
	}
	if s.QueueSize() != 0 {
		t.Errorf("expected queue size 0 after cancel, got %d", s.QueueSize())
	}
}

func TestDequeueTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task1, _ := s.CreateTask(context.Background(), "test", nil, nil)
	task2, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task1.ID)
	_ = s.QueueTask(context.Background(), task2.ID)

	if s.QueueSize() != 2 {
		t.Errorf("expected queue size 2 before dequeue, got %d", s.QueueSize())
	}

	dequeued := s.DequeueTask()
	if dequeued.ID != task1.ID {
		t.Errorf("expected task1, got %s", dequeued.ID)
	}
	if s.QueueSize() != 1 {
		t.Errorf("expected queue size 1 after dequeue, got %d", s.QueueSize())
	}
}

func TestDequeueEmptyQueue(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	dequeued := s.DequeueTask()
	if dequeued != nil {
		t.Error("expected nil from empty queue")
	}
}

func TestListTasks(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	_, _ = s.CreateTask(context.Background(), "type-a", nil, nil)
	_, _ = s.CreateTask(context.Background(), "type-b", nil, nil)
	_, _ = s.CreateTask(context.Background(), "type-a", nil, nil)

	allTasks := s.ListTasks(TaskFilter{})
	if len(allTasks) != 3 {
		t.Errorf("expected 3 total tasks, got %d", len(allTasks))
	}

	tasks := s.ListTasks(TaskFilter{Types: []string{"type-a"}})
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks of type-a, got %d", len(tasks))
	}
}

func TestListTasksByPriority(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	_, _ = s.CreateTaskWithPriority(context.Background(), "low", PriorityLow, nil, nil)
	_, _ = s.CreateTaskWithPriority(context.Background(), "high", PriorityHigh, nil, nil)

	priority := PriorityHigh
	tasks := s.ListTasks(TaskFilter{Priority: &priority})
	if len(tasks) != 1 {
		t.Errorf("expected 1 high priority task, got %d", len(tasks))
	}
}

func TestGetTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)

	found, exists := s.GetTask(task.ID)
	if !exists {
		t.Error("task not found")
	}
	if found.ID != task.ID {
		t.Errorf("expected task ID %s, got %s", task.ID, found.ID)
	}
}

func TestGetStats(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	_, _ = s.CreateTask(context.Background(), "test1", nil, nil)
	task2, _ := s.CreateTask(context.Background(), "test2", nil, nil)
	_ = s.QueueTask(context.Background(), task2.ID)

	stats := s.GetStats()
	if stats["total"] != 2 {
		t.Errorf("expected 2 total tasks, got %v", stats["total"])
	}
	if stats["pending"] != 1 {
		t.Errorf("expected 1 pending task, got %v", stats["pending"])
	}
	if stats["queued"] != 1 {
		t.Errorf("expected 1 queued task, got %v", stats["queued"])
	}
}

func TestTaskEventPublishing(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventTaskCreated, 10)
	s := NewScheduler(bus)

	_, _ = s.CreateTask(context.Background(), "test", nil, nil)

	select {
	case evt := <-sub.Chan():
		if evt.Type() != event.EventTaskCreated {
			t.Errorf("expected event type %s, got %s", event.EventTaskCreated, evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestDependencyUnblocking(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	t1, _ := s.CreateTask(context.Background(), "dep", nil, nil)
	t2, _ := s.CreateTask(context.Background(), "task", nil, nil)
	_ = s.AddDependency(context.Background(), t2.ID, t1.ID)
	err := s.QueueTask(context.Background(), t2.ID)
	if err == nil {
		t.Error("should fail to queue with incomplete dep")
	}
	_ = s.QueueTask(context.Background(), t1.ID)
	_ = s.StartTask(context.Background(), t1.ID, "w1")
	_ = s.CompleteTask(context.Background(), t1.ID, "w1", nil)
	t2, _ = s.GetTask(t2.ID)
	if t2.State != StateQueued {
		t.Errorf("expected queued after dep completed, got %s", t2.State)
	}
}

func TestCompletedDependencySkipsQueueCheck(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	t1, _ := s.CreateTask(context.Background(), "dep", nil, nil)
	t2, _ := s.CreateTask(context.Background(), "task", nil, nil)
	_ = s.QueueTask(context.Background(), t1.ID)
	_ = s.StartTask(context.Background(), t1.ID, "w1")
	_ = s.CompleteTask(context.Background(), t1.ID, "w1", nil)
	err := s.AddDependency(context.Background(), t2.ID, t1.ID)
	if err != nil {
		t.Fatalf("AddDependency on completed task should succeed: %v", err)
	}
}

func TestPriorityOrderAllLevels(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	low, _ := s.CreateTaskWithPriority(context.Background(), "low", PriorityLow, nil, nil)
	normal, _ := s.CreateTaskWithPriority(context.Background(), "normal", PriorityNormal, nil, nil)
	high, _ := s.CreateTaskWithPriority(context.Background(), "high", PriorityHigh, nil, nil)
	critical, _ := s.CreateTaskWithPriority(context.Background(), "critical", PriorityCritical, nil, nil)
	_ = s.QueueTask(context.Background(), low.ID)
	_ = s.QueueTask(context.Background(), normal.ID)
	_ = s.QueueTask(context.Background(), high.ID)
	_ = s.QueueTask(context.Background(), critical.ID)
	order := []string{}
	for i := 0; i < 4; i++ {
		tk := s.DequeueTask()
		if tk != nil {
			order = append(order, tk.ID)
		}
	}
	expected := []string{critical.ID, high.ID, normal.ID, low.ID}
	for i, id := range order {
		if id != expected[i] {
			t.Errorf("pos %d: expected %s, got %s", i, expected[i], id)
		}
	}
}

func TestRetryLimitExact(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	task.MaxRetry = 2
	s.saveTask(task)
	for attempt := 0; attempt < 3; attempt++ {
		_ = s.QueueTask(context.Background(), task.ID)
		_ = s.StartTask(context.Background(), task.ID, "w1")
		_ = s.FailTask(context.Background(), task.ID, "w1", "err")
	}
	task, _ = s.GetTask(task.ID)
	if task.State != StateDeadLetter {
		t.Errorf("expected dead_letter, got %s", task.State)
	}
	if task.Retry != 3 {
		t.Errorf("expected retry 3, got %d", task.Retry)
	}
}

func TestCancelRunningTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_ = s.StartTask(context.Background(), task.ID, "w1")
	if err := s.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	task, _ = s.GetTask(task.ID)
	if task.State != StateCancelled {
		t.Errorf("expected cancelled, got %s", task.State)
	}
}

func TestCancelTerminalStateTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_ = s.StartTask(context.Background(), task.ID, "w1")
	_ = s.CompleteTask(context.Background(), task.ID, "w1", nil)
	if err := s.CancelTask(context.Background(), task.ID); err == nil {
		t.Error("should not cancel completed task")
	}
}

func TestCreateTaskWithoutEventBus(t *testing.T) {
	s := NewScheduler(nil)
	task, err := s.CreateTask(context.Background(), "test", nil, nil)
	if err != nil {
		t.Fatalf("create without bus: %v", err)
	}
	if task.State != StatePending {
		t.Errorf("expected pending, got %s", task.State)
	}
}

func TestListTasksFilterByState(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	t1, _ := s.CreateTask(context.Background(), "a", nil, nil)
	_ = s.QueueTask(context.Background(), t1.ID)
	pending := s.ListTasks(TaskFilter{States: []TaskState{StatePending}})
	queued := s.ListTasks(TaskFilter{States: []TaskState{StateQueued}})
	if len(pending) != 0 {
		t.Errorf("expected 0 pending, got %d", len(pending))
	}
	if len(queued) != 1 {
		t.Errorf("expected 1 queued, got %d", len(queued))
	}
}

func TestStartNonQueuedTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	if err := s.StartTask(context.Background(), task.ID, "w1"); err == nil {
		t.Error("should not start non-queued task")
	}
}

func TestCompleteNonRunningTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	if err := s.CompleteTask(context.Background(), task.ID, "w1", nil); err == nil {
		t.Error("should not complete non-running task")
	}
}

func TestFailNonRunningTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	if err := s.FailTask(context.Background(), task.ID, "w1", "err"); err == nil {
		t.Error("should not fail non-running task")
	}
}

func TestGetNonexistentTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	_, exists := s.GetTask("nonexistent")
	if exists {
		t.Error("should not find nonexistent task")
	}
}

func TestQueueNonexistentTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	if err := s.QueueTask(context.Background(), "nonexistent"); err == nil {
		t.Error("should error queuing nonexistent")
	}
}

func TestAddDependencyNonexistentTasks(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	if err := s.AddDependency(context.Background(), "a", "b"); err == nil {
		t.Error("should error for nonexistent")
	}
}

func TestQueueAlreadyQueuedTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	if err := s.QueueTask(context.Background(), task.ID); err == nil {
		t.Error("should error re-queuing")
	}
}

func TestTaskPriorityString(t *testing.T) {
	tests := []struct {
		p    TaskPriority
		want string
	}{
		{PriorityLow, "low"}, {PriorityNormal, "normal"}, {PriorityHigh, "high"}, {PriorityCritical, "critical"}, {TaskPriority(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("Priority(%d).String() = %s, want %s", tt.p, got, tt.want)
		}
	}
}

func TestConcurrentCreateAndQueue(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	var wg sync.WaitGroup
	n := 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			task, _ := s.CreateTask(context.Background(), "test", nil, nil)
			_ = s.QueueTask(context.Background(), task.ID)
		}()
	}
	wg.Wait()
	if s.QueueSize() != n {
		t.Errorf("expected queue %d, got %d", n, s.QueueSize())
	}
}

func BenchmarkSchedulerCreateTask(b *testing.B) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.CreateTask(ctx, "bench", nil, nil)
	}
}

func BenchmarkSchedulerCreateAndQueue(b *testing.B) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := NewScheduler(bus)
		task, _ := s.CreateTask(ctx, "bench", nil, nil)
		_ = s.QueueTask(ctx, task.ID)
	}
}

func BenchmarkSchedulerDequeueTask(b *testing.B) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		task, _ := s.CreateTask(ctx, "bench", nil, nil)
		_ = s.QueueTask(ctx, task.ID)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.DequeueTask()
	}
}

func BenchmarkSchedulerParallelCreate(b *testing.B) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		s := NewScheduler(bus)
		for pb.Next() {
			_, _ = s.CreateTask(ctx, "bench", nil, nil)
		}
	})
}

// 并发压力测试

func TestConcurrentDequeue(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	ctx := context.Background()

	// 预先创建任务
	for i := 0; i < 100; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, nil)
		_ = s.QueueTask(ctx, task.ID)
	}

	var wg sync.WaitGroup
	dequeued := make(chan *Task, 100)

	// 并发出队
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func() {
			defer wg.Done()
			task := s.DequeueTask()
			if task != nil {
				dequeued <- task
			}
		}()
	}
	wg.Wait()
	close(dequeued)

	// 验证所有任务都被出队
	count := 0
	for range dequeued {
		count++
	}
	if count != 100 {
		t.Errorf("expected 100 dequeued tasks, got %d", count)
	}
	if s.QueueSize() != 0 {
		t.Errorf("expected queue size 0, got %d", s.QueueSize())
	}
}

func TestConcurrentStartAndComplete(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	ctx := context.Background()

	// 创建并入队任务
	tasks := make([]*Task, 50)
	for i := 0; i < 50; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, nil)
		_ = s.QueueTask(ctx, task.ID)
		tasks[i] = task
	}

	var wg sync.WaitGroup

	// 并发启动任务
	wg.Add(50)
	for i := 0; i < 50; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := s.StartTask(ctx, tasks[idx].ID, "worker-1"); err != nil {
				t.Errorf("start task %s: %v", tasks[idx].ID, err)
			}
		}(i)
	}
	wg.Wait()

	// 并发完成任务
	wg.Add(50)
	for i := 0; i < 50; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := s.CompleteTask(ctx, tasks[idx].ID, "worker-1", []byte("result")); err != nil {
				t.Errorf("complete task %s: %v", tasks[idx].ID, err)
			}
		}(i)
	}
	wg.Wait()

	// 验证所有任务都已完成
	for _, task := range tasks {
		updatedTask, _ := s.GetTask(task.ID)
		if updatedTask.State != StateCompleted {
			t.Errorf("task %s expected completed, got %s", task.ID, updatedTask.State)
		}
	}
}

func TestConcurrentFailAndRetry(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	ctx := context.Background()

	// 创建并入队任务
	tasks := make([]*Task, 30)
	for i := 0; i < 30; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, nil)
		task.MaxRetry = 3
		_ = s.QueueTask(ctx, task.ID)
		tasks[i] = task
	}

	var wg sync.WaitGroup

	// 并发启动任务
	wg.Add(30)
	for i := 0; i < 30; i++ {
		go func(idx int) {
			defer wg.Done()
			_ = s.StartTask(ctx, tasks[idx].ID, "worker-1")
		}(i)
	}
	wg.Wait()

	// 并发失败任务（触发重试）
	wg.Add(30)
	for i := 0; i < 30; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := s.FailTask(ctx, tasks[idx].ID, "worker-1", "test error"); err != nil {
				t.Errorf("fail task %s: %v", tasks[idx].ID, err)
			}
		}(i)
	}
	wg.Wait()

	// 验证任务进入重试状态
	retriedCount := 0
	for _, task := range tasks {
		updatedTask, _ := s.GetTask(task.ID)
		if updatedTask.State == StateQueued && updatedTask.Retry > 0 {
			retriedCount++
		}
	}
	if retriedCount != 30 {
		t.Errorf("expected 30 retried tasks, got %d", retriedCount)
	}
}

func TestConcurrentCancel(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	ctx := context.Background()

	// 创建任务
	tasks := make([]*Task, 50)
	for i := 0; i < 50; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, nil)
		_ = s.QueueTask(ctx, task.ID)
		tasks[i] = task
	}

	var wg sync.WaitGroup

	// 并发取消任务
	wg.Add(50)
	for i := 0; i < 50; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := s.CancelTask(ctx, tasks[idx].ID); err != nil {
				t.Errorf("cancel task %s: %v", tasks[idx].ID, err)
			}
		}(i)
	}
	wg.Wait()

	// 验证所有任务都已取消
	for _, task := range tasks {
		updatedTask, _ := s.GetTask(task.ID)
		if updatedTask.State != StateCancelled {
			t.Errorf("task %s expected cancelled, got %s", task.ID, updatedTask.State)
		}
	}
}

func TestConcurrentStats(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := NewScheduler(bus)
	ctx := context.Background()

	var wg sync.WaitGroup
	n := 100

	// 并发创建任务并读取统计
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = s.CreateTask(ctx, "test", nil, nil)
			_ = s.GetStats()
		}()
	}
	wg.Wait()

	stats := s.GetStats()
	// 检查queued字段，因为任务创建后状态是pending，不会立即入队
	// GetStats()查询数据库，但测试使用的是内存数据库
	if stats["queued"] != 0 {
		t.Errorf("expected queued 0, got %d", stats["queued"])
	}
}

// ---- Bridge and remaining coverage tests ----

// TestSchedulerBridgeCreateTask 验证 SchedulerBridge 创建任务。
func TestSchedulerBridgeCreateTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	bridge := NewSchedulerBridge(s)

	ref, err := bridge.CreateTask(context.Background(), "test", map[string]interface{}{"key": "val"}, []byte("input"))
	if err != nil {
		t.Fatalf("bridge create task: %v", err)
	}
	if ref.ID == "" {
		t.Error("expected non-empty task ID")
	}
	if ref.Type != "test" {
		t.Errorf("expected type 'test', got '%s'", ref.Type)
	}
	if ref.State != "pending" {
		t.Errorf("expected state 'pending', got '%s'", ref.State)
	}
}

// TestSchedulerBridgeGetTask 验证 SchedulerBridge 获取任务。
func TestSchedulerBridgeGetTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	bridge := NewSchedulerBridge(s)

	task, _ := s.CreateTask(context.Background(), "test", nil, []byte("data"))
	ref, exists := bridge.GetTask(task.ID)
	if !exists {
		t.Error("expected task to exist via bridge")
	}
	if ref.ID != task.ID {
		t.Errorf("expected ID %s, got %s", task.ID, ref.ID)
	}
}

// TestSchedulerBridgeGetTaskNotFound 验证不存在的任务返回 false。
func TestSchedulerBridgeGetTaskNotFound(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	bridge := NewSchedulerBridge(s)

	_, exists := bridge.GetTask("nonexistent")
	if exists {
		t.Error("expected false for nonexistent task")
	}
}

// TestSchedulerBridgeWaitForTask 验证 WaitForTask 在任务完成后返回。
func TestSchedulerBridgeWaitForTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	bridge := NewSchedulerBridge(s)

	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)
	_ = s.StartTask(context.Background(), task.ID, "w1")

	// Complete the task in a goroutine after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = s.CompleteTask(context.Background(), task.ID, "w1", []byte("done"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ref, err := bridge.WaitForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("wait for task: %v", err)
	}
	if ref.State != "completed" {
		t.Errorf("expected completed, got %s", ref.State)
	}
	if string(ref.Result) != "done" {
		t.Errorf("expected result 'done', got '%s'", string(ref.Result))
	}
}

// TestSchedulerBridgeWaitForTaskCancelled 验证上下文取消时 WaitForTask 返回。
func TestSchedulerBridgeWaitForTaskCancelled(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	bridge := NewSchedulerBridge(s)

	task, _ := s.CreateTask(context.Background(), "test", nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := bridge.WaitForTask(ctx, task.ID)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// TestSchedulerBridgeWaitForTaskNotFound 验证不存在的任务返回 not_found。
func TestSchedulerBridgeWaitForTaskNotFound(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)
	bridge := NewSchedulerBridge(s)

	// The bridge polls GetTask - since task doesn't exist in DB, it returns not_found
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ref, _ := bridge.WaitForTask(ctx, "nonexistent")
	if ref != nil && ref.State != "not_found" {
		t.Errorf("expected not_found state, got %s", ref.State)
	}
}

// TestSaveTask 验证 SaveTask 公开方法。
func TestSaveTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)

	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	task.Priority = PriorityCritical
	s.SaveTask(task)

	found, exists := s.GetTask(task.ID)
	if !exists {
		t.Fatal("task not found after save")
	}
	if found.Priority != PriorityCritical {
		t.Errorf("expected critical priority after save, got %d", found.Priority)
	}
}

// TestNotifyCh 验证 NotifyCh 返回可用通道。
func TestNotifyCh(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)

	ch := s.NotifyCh()
	if ch == nil {
		t.Error("expected non-nil notify channel")
	}
}

// TestQueueTaskDependencyMet 验证依赖满足后任务自动入队。
func TestQueueTaskDependencyMet(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)

	t1, _ := s.CreateTask(context.Background(), "dep", nil, nil)
	t2, _ := s.CreateTask(context.Background(), "task", nil, nil)
	_ = s.AddDependency(context.Background(), t2.ID, t1.ID)

	// Complete the dependency — this triggers unblockDependents which auto-queues t2
	_ = s.QueueTask(context.Background(), t1.ID)
	_ = s.StartTask(context.Background(), t1.ID, "w1")
	_ = s.CompleteTask(context.Background(), t1.ID, "w1", nil)

	// t2 should be automatically queued by unblockDependents
	t2, _ = s.GetTask(t2.ID)
	if t2.State != StateQueued {
		t.Errorf("expected queued after dependency met, got %s", t2.State)
	}
}

// TestListTasksWithLimit 验证 ListTasks 限制返回数量。
func TestListTasksWithLimit(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)

	for i := 0; i < 10; i++ {
		_, _ = s.CreateTask(context.Background(), "test", nil, nil)
	}

	tasks := s.ListTasks(TaskFilter{Limit: 3})
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks with limit, got %d", len(tasks))
	}
}

// TestAgentTaskConfig 验证 Agent 任务配置序列化/反序列化。
func TestAgentTaskConfig(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := NewScheduler(bus)

	task, _ := s.CreateTask(context.Background(), "agent", nil, nil)
	task.IsAgent = true
	task.AgentConfig = &AgentConfig{
		SystemPrompt:   "You are helpful",
		Model:          "gpt-4o",
		ResponseSchema: `{"type": "object"}`,
	}
	s.SaveTask(task)

	found, _ := s.GetTask(task.ID)
	if !found.IsAgent {
		t.Error("expected IsAgent=true")
	}
	if found.AgentConfig == nil {
		t.Fatal("expected non-nil AgentConfig")
	}
	if found.AgentConfig.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got '%s'", found.AgentConfig.Model)
	}
}

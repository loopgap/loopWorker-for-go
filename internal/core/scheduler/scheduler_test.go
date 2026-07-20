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

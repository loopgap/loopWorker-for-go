package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/ai"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
)

func TestStartWorker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)

	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return input, nil
	})
	_ = sb.LoadPlugin("test", plugin)

	if err := e.StartWorker(context.Background(), "worker-1", "test"); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	if e.WorkerCount() != 1 {
		t.Errorf("expected 1 worker, got %d", e.WorkerCount())
	}

	stats := e.GetStats()
	if stats.ActiveWorkers != 1 {
		t.Errorf("expected 1 active worker, got %d", stats.ActiveWorkers)
	}
}

func TestStartDuplicateWorker(t *testing.T) {
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

	_ = e.StartWorker(context.Background(), "worker-1", "test")
	if err := e.StartWorker(context.Background(), "worker-1", "test"); err == nil {
		t.Error("expected error starting duplicate worker")
	}
}

func TestStopWorker(t *testing.T) {
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

	_ = e.StartWorker(context.Background(), "worker-1", "test")
	time.Sleep(50 * time.Millisecond)

	if err := e.StopWorker(context.Background(), "worker-1"); err != nil {
		t.Fatalf("stop worker: %v", err)
	}
	if e.WorkerCount() != 0 {
		t.Errorf("expected 0 workers after stop, got %d", e.WorkerCount())
	}

	stats := e.GetStats()
	if stats.ActiveWorkers != 0 {
		t.Errorf("expected 0 active workers, got %d", stats.ActiveWorkers)
	}
}

func TestStopNonexistentWorker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)

	if err := e.StopWorker(context.Background(), "nonexistent"); err == nil {
		t.Error("expected error stopping nonexistent worker")
	}
}

func TestStopAllWorkers(t *testing.T) {
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

	_ = e.StartWorker(context.Background(), "worker-1", "test")
	_ = e.StartWorker(context.Background(), "worker-2", "test")
	time.Sleep(50 * time.Millisecond)

	if err := e.StopAllWorkers(context.Background()); err != nil {
		t.Fatalf("stop all workers: %v", err)
	}
	if e.WorkerCount() != 0 {
		t.Errorf("expected 0 workers after stop all, got %d", e.WorkerCount())
	}
}

func TestExecuteTask(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	plugin := sandbox.NewMockPlugin("echo", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return input, nil
	})
	_ = sb.LoadPlugin("echo", plugin)
	_ = e.StartWorker(context.Background(), "worker-1", "echo")

	task, _ := s.CreateTask(context.Background(), "test", nil, []byte("hello"))
	_ = s.QueueTask(context.Background(), task.ID)

	// Wait for task to complete by checking stats
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := e.GetStats()
		if stats.TotalTasksRun >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	stats := e.GetStats()
	if stats.TotalTasksRun < 1 {
		t.Error("expected at least 1 task run")
	}
}

func TestExecuteTaskFailure(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	plugin := sandbox.NewMockPlugin("fail", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, fmt.Errorf("test error")
	})
	_ = sb.LoadPlugin("fail", plugin)
	_ = e.StartWorker(context.Background(), "worker-1", "fail")

	task, _ := s.CreateTask(context.Background(), "test", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)

	// Wait for task to fail by checking stats
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := e.GetStats()
		if stats.TotalTasksFailed >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	stats := e.GetStats()
	if stats.TotalTasksFailed < 1 {
		t.Error("expected at least 1 task failed")
	}
}

func TestListWorkers(t *testing.T) {
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

	_ = e.StartWorker(context.Background(), "worker-1", "test")
	_ = e.StartWorker(context.Background(), "worker-2", "test")

	workers := e.ListWorkers()
	if len(workers) != 2 {
		t.Errorf("expected 2 workers, got %d", len(workers))
	}
}

func TestWorkerState(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		time.Sleep(100 * time.Millisecond)
		return input, nil
	})
	_ = sb.LoadPlugin("test", plugin)

	_ = e.StartWorker(context.Background(), "worker-1", "test")
	time.Sleep(100 * time.Millisecond)

	workers := e.ListWorkers()
	if len(workers) != 1 {
		t.Fatal("expected 1 worker")
	}

	w := workers[0]
	if w.State() != workerIdle {
		t.Errorf("expected worker idle, got %v", w.State())
	}

	task, _ := s.CreateTask(context.Background(), "test", nil, []byte("test"))
	_ = s.QueueTask(context.Background(), task.ID)

	time.Sleep(200 * time.Millisecond)

	if w.TasksRun() != 1 {
		t.Errorf("expected 1 task run, got %d", w.TasksRun())
	}
}

func TestMultiWorkerConcurrentExecution(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	plugin := sandbox.NewMockPlugin("echo", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		time.Sleep(50 * time.Millisecond)
		return input, nil
	})
	_ = sb.LoadPlugin("echo", plugin)
	for i := 0; i < 3; i++ {
		_ = e.StartWorker(context.Background(), fmt.Sprintf("w-%d", i), "echo")
	}
	for i := 0; i < 6; i++ {
		task, _ := s.CreateTask(context.Background(), "test", nil, []byte("data"))
		_ = s.QueueTask(context.Background(), task.ID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := e.GetStats()
		if stats.TotalTasksRun >= 6 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stats := e.GetStats()
	if stats.TotalTasksRun < 6 {
		t.Errorf("expected 6 tasks run, got %d", stats.TotalTasksRun)
	}
	_ = e.StopAllWorkers(context.Background())
}

func TestWorkerStopCleanup(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) { return nil, nil })
	_ = sb.LoadPlugin("test", plugin)
	_ = e.StartWorker(context.Background(), "w1", "test")
	_ = e.StartWorker(context.Background(), "w2", "test")
	time.Sleep(100 * time.Millisecond)
	if e.WorkerCount() != 2 {
		t.Errorf("expected 2 workers, got %d", e.WorkerCount())
	}
	_ = e.StopWorker(context.Background(), "w1")
	if e.WorkerCount() != 1 {
		t.Errorf("expected 1 worker after stop, got %d", e.WorkerCount())
	}
	_ = e.StopWorker(context.Background(), "w2")
	if e.WorkerCount() != 0 {
		t.Errorf("expected 0 workers, got %d", e.WorkerCount())
	}
}

func TestWorkerListAfterStop(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) { return nil, nil })
	_ = sb.LoadPlugin("test", plugin)
	_ = e.StartWorker(context.Background(), "w1", "test")
	time.Sleep(100 * time.Millisecond)
	workers := e.ListWorkers()
	if len(workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(workers))
	}
	_ = e.StopWorker(context.Background(), "w1")
	workers = e.ListWorkers()
	if len(workers) != 0 {
		t.Errorf("expected 0 workers after stop, got %d", len(workers))
	}
}

func TestWorkerEvents(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) { return nil, nil })
	_ = sb.LoadPlugin("test", plugin)
	spawnSub := bus.Subscribe(event.EventWorkerSpawned, 10)
	exitSub := bus.Subscribe(event.EventWorkerExited, 10)
	_ = e.StartWorker(context.Background(), "w1", "test")
	select {
	case evt := <-spawnSub.Chan():
		if evt.Type() != event.EventWorkerSpawned {
			t.Errorf("expected worker.spawned, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for spawn event")
	}
	time.Sleep(100 * time.Millisecond)
	_ = e.StopWorker(context.Background(), "w1")
	select {
	case evt := <-exitSub.Chan():
		if evt.Type() != event.EventWorkerExited {
			t.Errorf("expected worker.exited, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for exit event")
	}
}

func TestExecuteAgentTask(t *testing.T) {
	mockResponse := `{"choices": [{"message": {"role": "assistant", "content": "{\"summary\": \"Task 1 completed successfully\"}"}}], "usage": {}}`

	var receivedBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			data, _ := io.ReadAll(r.Body)
			receivedBody = string(data)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(mockResponse))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})

	llm := ai.NewLLMClient(ts.URL, "mock-api-key")
	e := NewExecutor(s, d, sb, bus, nil).WithLLMClient(llm)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	plugin := sandbox.NewMockPlugin("task1-plugin", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("Task 1 output data"), nil
	})
	_ = sb.LoadPlugin("task1-plugin", plugin)
	_ = e.StartWorker(context.Background(), "w-1", "task1-plugin")

	task1, _ := s.CreateTask(context.Background(), "task1-plugin", nil, []byte("start"))
	_ = s.QueueTask(context.Background(), task1.ID)

	schema := `{"type": "object", "properties": {"summary": {"type": "string"}}}`
	task2, _ := s.CreateTask(context.Background(), "agent-plugin", nil, []byte("Summarize upstream output"))

	// Set task2 as agent
	task2.IsAgent = true
	task2.AgentConfig = &scheduler.AgentConfig{
		SystemPrompt:   "You summarize outputs.",
		Model:          "mock-model",
		ResponseSchema: schema,
	}

	s.SaveTask(task2)

	// Save to DB to update columns
	// Since scheduler has a local SQLite, we can update directly via s.CreateTask/saveTask
	// We'll queue it next
	_ = s.AddDependency(context.Background(), task2.ID, task1.ID)
	_ = s.QueueTask(context.Background(), task2.ID)

	_ = e.StartWorker(context.Background(), "w-2", "agent-plugin")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := e.GetStats()
		if stats.TotalTasksRun >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	stats := e.GetStats()
	if stats.TotalTasksRun < 2 {
		t.Fatalf("expected 2 tasks completed, got %d. Failed count: %d", stats.TotalTasksRun, stats.TotalTasksFailed)
	}

	completedTask2, ok := s.GetTask(task2.ID)
	if !ok {
		t.Fatal("failed to get task2")
	}

	if completedTask2.State != scheduler.StateCompleted {
		t.Errorf("expected task2 completed, got %v", completedTask2.State)
	}

	expectedResult := `{"summary": "Task 1 completed successfully"}`
	if string(completedTask2.Result) != expectedResult {
		t.Errorf("expected Result %s, got %s", expectedResult, string(completedTask2.Result))
	}

	if receivedBody == "" {
		t.Fatal("mock server did not receive completions request")
	}

	if !strings.Contains(receivedBody, "Task 1 output data") {
		t.Error("LLM prompt did not contain upstream Task 1 output")
	}

	if !strings.Contains(receivedBody, "json_schema") {
		t.Error("LLM request did not include response_format json_schema")
	}
}

func TestLLMCircuitBreaker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{MaxMemoryMB: 64, MaxCPUSeconds: 5, MaxOutputMB: 1, MaxConcurrent: 2})
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	e := NewExecutor(s, d, sb, bus, sh)

	// Simulate LLM failures to trigger circuit breaker
	for i := 0; i < 5; i++ {
		if e.llmCircuitBreaker == nil {
			e.llmCircuitBreaker = sh.GetCircuitBreaker("llm")
		}
		e.llmCircuitBreaker.RecordFailure()
	}

	// Circuit breaker should be open now
	if e.llmCircuitBreaker.AllowRequest() {
		t.Error("circuit breaker should be open after 5 failures")
	}

	// Verify circuit breaker state
	state := e.llmCircuitBreaker.GetState()
	if state != selfheal.CircuitOpen {
		t.Errorf("expected circuit open, got %v", state)
	}
}

func TestLLMCircuitBreakerRecovery(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	e := NewExecutor(s, d, sb, bus, sh)

	cb := sh.GetCircuitBreaker("llm")
	e.llmCircuitBreaker = cb

	// Trip the circuit breaker
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if cb.AllowRequest() {
		t.Error("circuit breaker should be open")
	}

	// Reset the circuit breaker
	cb.Reset()
	if !cb.AllowRequest() {
		t.Error("circuit breaker should be closed after reset")
	}

	// Record successes to stabilize
	for i := 0; i < 3; i++ {
		cb.RecordSuccess()
	}
	state := cb.GetState()
	if state != selfheal.CircuitClosed {
		t.Errorf("expected circuit closed after successes, got %v", state)
	}
}

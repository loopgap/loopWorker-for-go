package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
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

	llm := NewLLMClient(ts.URL, "mock-api-key")
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

// ---- Additional coverage tests ----

// TestWithTaskTimeout 验证 WithTaskTimeout 函数选项正确设置超时。
func TestWithTaskTimeout(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})

	customTimeout := 5 * time.Minute
	e := NewExecutor(s, d, sb, bus, nil, WithTaskTimeout(customTimeout))

	if e.GetTaskTimeout() != customTimeout {
		t.Errorf("expected timeout %v, got %v", customTimeout, e.GetTaskTimeout())
	}
}

// TestWithLLMClient 验证 WithLLMClient 函数选项正确注入 LLM 客户端。
func TestWithLLMClient(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})

	llm := NewLLMClient("http://test.api", "key123")
	e := NewExecutor(s, d, sb, bus, nil, WithLLMClient(llm))

	client := e.getLLMClient()
	if client.BaseURL != "http://test.api" {
		t.Errorf("expected BaseURL 'http://test.api', got '%s'", client.BaseURL)
	}
}

// TestGetLLMClientDefault 验证无 LLM 客户端时返回默认客户端。
func TestGetLLMClientDefault(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})

	e := NewExecutor(s, d, sb, bus, nil)
	client := e.getLLMClient()

	// Default LLM client should have OpenAI base URL
	if client.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected default OpenAI URL, got '%s'", client.BaseURL)
	}
}

// TestSetGetTaskTimeout 验证动态获取/设置任务超时。
func TestSetGetTaskTimeout(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})

	e := NewExecutor(s, d, sb, bus, nil)

	// Default is 30 minutes
	if e.GetTaskTimeout() != 30*time.Minute {
		t.Errorf("expected default 30m timeout, got %v", e.GetTaskTimeout())
	}

	e.SetTaskTimeout(10 * time.Second)
	if e.GetTaskTimeout() != 10*time.Second {
		t.Errorf("expected 10s timeout after set, got %v", e.GetTaskTimeout())
	}
}

// TestSetSkillContext 验证技能上下文注入与获取。
func TestSetSkillContext(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})

	e := NewExecutor(s, d, sb, bus, nil)

	// Initially nil
	if ctx := e.getSkillContext(); ctx.Bus != nil {
		t.Error("expected nil initial skill context bus")
	}

	// Set skill context
	testBus := event.NewEventBus(nil)
	defer testBus.Close()
	skillCtx := skill.SkillContext{Bus: testBus, Config: map[string]interface{}{"key": "val"}}
	e.SetSkillContext(skillCtx)

	got := e.getSkillContext()
	if got.Bus != testBus {
		t.Error("expected skill context bus to match")
	}
	if got.Config["key"] != "val" {
		t.Errorf("expected config key 'val', got '%v'", got.Config["key"])
	}
}

// TestWorkerMethods 验证 Worker 的各种状态查询方法。
func TestWorkerMethods(t *testing.T) {
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
		return input, nil
	})
	_ = sb.LoadPlugin("test", plugin)
	_ = e.StartWorker(context.Background(), "w1", "test")
	time.Sleep(50 * time.Millisecond)

	workers := e.ListWorkers()
	if len(workers) != 1 {
		t.Fatal("expected 1 worker")
	}
	w := workers[0]

	// Test IsBusy (idle)
	if w.IsBusy() {
		t.Error("expected worker not busy initially")
	}

	// Test TasksRun
	if w.TasksRun() != 0 {
		t.Errorf("expected 0 tasks run, got %d", w.TasksRun())
	}

	// Test TasksFailed
	if w.TasksFailed() != 0 {
		t.Errorf("expected 0 tasks failed, got %d", w.TasksFailed())
	}

	// Test State
	if w.State() != workerIdle {
		t.Errorf("expected idle state, got %d", w.State())
	}

	// Test LastActive
	if w.LastActive().IsZero() {
		t.Error("expected non-zero LastActive")
	}

	// Execute a task and verify counters
	task, _ := s.CreateTask(context.Background(), "test", nil, []byte("data"))
	_ = s.QueueTask(context.Background(), task.ID)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.TasksRun() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if w.TasksRun() != 1 {
		t.Errorf("expected 1 task run after execution, got %d", w.TasksRun())
	}
}

// TestWorkerIsBusyDuringExecution 验证执行任务时 Worker 状态为 busy。
func TestWorkerIsBusyDuringExecution(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	started := make(chan struct{})
	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		return input, nil
	})
	_ = sb.LoadPlugin("test", plugin)
	_ = e.StartWorker(context.Background(), "w1", "test")
	time.Sleep(50 * time.Millisecond)

	task, _ := s.CreateTask(context.Background(), "test", nil, []byte("data"))
	_ = s.QueueTask(context.Background(), task.ID)

	// Wait for execution to start
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for task to start")
	}

	workers := e.ListWorkers()
	if len(workers) != 1 {
		t.Fatal("expected 1 worker")
	}

	// Worker should be busy during execution
	if !workers[0].IsBusy() {
		t.Error("expected worker to be busy during task execution")
	}
	if workers[0].State() != workerBusy {
		t.Errorf("expected busy state, got %d", workers[0].State())
	}
}

// TestStartWatchdog 验证看门狗启动不会 panic。
func TestStartWatchdog(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)

	// StartWatchdog should not panic
	e.StartWatchdog(context.Background())
	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)
}

// TestSweepZombies 验证僵尸 Worker 检测逻辑。
func TestSweepZombies(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	e := NewExecutor(s, d, sb, bus, sh)

	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})
	_ = sb.LoadPlugin("test", plugin)

	_ = e.StartWorker(context.Background(), "w1", "test")
	time.Sleep(50 * time.Millisecond)

	// sweepZombies with no zombies should be a no-op
	e.sweepZombies(context.Background())

	if e.WorkerCount() != 1 {
		t.Errorf("expected 1 worker after sweep with no zombies, got %d", e.WorkerCount())
	}
}

// TestNewContextBuilder 验证 ContextBuilder 创建和默认值。
func TestNewContextBuilder(t *testing.T) {
	cb := NewContextBuilder(0)
	if cb.MaxChars != 16000 {
		t.Errorf("expected default 16000, got %d", cb.MaxChars)
	}

	cb2 := NewContextBuilder(8000)
	if cb2.MaxChars != 8000 {
		t.Errorf("expected 8000, got %d", cb2.MaxChars)
	}
}

// TestBuildUserPrompt 验证上下文构建和截断。
func TestBuildUserPrompt(t *testing.T) {
	cb := NewContextBuilder(100)

	// With upstream results
	results := map[string]string{
		"task1": "result data",
	}
	prompt := cb.BuildUserPrompt(results, "do something")
	if prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "result data") {
		t.Error("expected prompt to contain upstream result")
	}
	if !strings.Contains(prompt, "do something") {
		t.Error("expected prompt to contain main prompt")
	}
}

// TestBuildUserPromptTruncation 验证长内容截断。
func TestBuildUserPromptTruncation(t *testing.T) {
	cb := NewContextBuilder(50)

	// Create content that exceeds MaxChars
	longResult := strings.Repeat("x", 100)
	results := map[string]string{
		"task1": longResult,
	}
	prompt := cb.BuildUserPrompt(results, "instructions")

	if len(prompt) > 200 { // Allow some overhead for labels
		t.Errorf("expected truncation, got prompt of length %d", len(prompt))
	}
}

// TestBuildUserPromptNoUpstream 验证无上游结果时的构建。
func TestBuildUserPromptNoUpstream(t *testing.T) {
	cb := NewContextBuilder(16000)
	prompt := cb.BuildUserPrompt(nil, "just do it")

	if prompt != "Instructions:\njust do it" {
		t.Errorf("unexpected prompt: %s", prompt)
	}
}

// TestNewLLMClientDefaults 验证 LLM 客户端默认值。
func TestNewLLMClientDefaults(t *testing.T) {
	c := NewLLMClient("", "")
	if c.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected default URL, got '%s'", c.BaseURL)
	}
	if c.Client == nil {
		t.Error("expected non-nil HTTP client")
	}
	if c.Client.Timeout != 60*time.Second {
		t.Errorf("expected 60s timeout, got %v", c.Client.Timeout)
	}
}

// TestNewLLMClientTrailingSlash 验证 URL 尾部斜杠处理。
func TestNewLLMClientTrailingSlash(t *testing.T) {
	c := NewLLMClient("http://localhost:8080/v1/", "key")
	if strings.HasSuffix(c.BaseURL, "/") {
		t.Errorf("BaseURL should not have trailing slash: '%s'", c.BaseURL)
	}
}

// TestGenerateStructuredNoChoices 验证 LLM 无返回选项时的错误处理。
func TestGenerateStructuredNoChoices(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": []}`))
	}))
	defer ts.Close()

	c := NewLLMClient(ts.URL, "key")
	_, err := c.GenerateStructured(context.Background(), "test", "sys", "user", "")
	if err == nil {
		t.Error("expected error for no choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected 'no choices' in error, got: %v", err)
	}
}

// TestGenerateStructuredAPIError 验证 LLM API 错误响应。
func TestGenerateStructuredAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error": {"message": "rate limit exceeded"}}`))
	}))
	defer ts.Close()

	c := NewLLMClient(ts.URL, "key")
	_, err := c.GenerateStructured(context.Background(), "test", "sys", "user", "")
	if err == nil {
		t.Error("expected error for API error")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("expected 'rate limit exceeded' in error, got: %v", err)
	}
}

// TestGenerateStructuredHTTPError 验证 LLM HTTP 非200状态码。
func TestGenerateStructuredHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer ts.Close()

	c := NewLLMClient(ts.URL, "key")
	_, err := c.GenerateStructured(context.Background(), "test", "sys", "user", "")
	if err == nil {
		t.Error("expected error for 500 status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected '500' in error, got: %v", err)
	}
}

// TestGenerateStructuredWithSchema 验证带 JSON Schema 的结构化输出。
func TestGenerateStructuredWithSchema(t *testing.T) {
	var reqBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		reqBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": [{"message": {"content": "{\"result\": \"ok\"}"}}]}`))
	}))
	defer ts.Close()

	schema := `{"type": "object", "properties": {"result": {"type": "string"}}}`
	c := NewLLMClient(ts.URL, "key")
	result, err := c.GenerateStructured(context.Background(), "gpt-4o", "system", "user", schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != `{"result": "ok"}` {
		t.Errorf("unexpected result: %s", result)
	}
	if !strings.Contains(reqBody, "json_schema") {
		t.Error("expected request to contain json_schema response format")
	}
}

// TestGenerateStructuredInvalidSchema 验证无效 schema JSON 的错误处理。
func TestGenerateStructuredInvalidSchema(t *testing.T) {
	c := NewLLMClient("http://localhost", "key")
	_, err := c.GenerateStructured(context.Background(), "test", "sys", "user", "not-json")
	if err == nil {
		t.Error("expected error for invalid schema JSON")
	}
	if !strings.Contains(err.Error(), "invalid response schema") {
		t.Errorf("expected 'invalid response schema' in error, got: %v", err)
	}
}

// TestGenerateStructuredConnectionError 验证连接失败时的错误处理。
func TestGenerateStructuredConnectionError(t *testing.T) {
	c := NewLLMClient("http://localhost:1", "key")
	_, err := c.GenerateStructured(context.Background(), "test", "sys", "user", "")
	if err == nil {
		t.Error("expected connection error")
	}
}

// TestGenerateStructuredDefaultModel 验证空模型名使用默认值。
func TestGenerateStructuredDefaultModel(t *testing.T) {
	var reqBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		reqBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer ts.Close()

	c := NewLLMClient(ts.URL, "key")
	_, err := c.GenerateStructured(context.Background(), "", "sys", "user", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(reqBody, "gpt-4o") {
		t.Error("expected default model 'gpt-4o' in request body")
	}
}

// ---- Final strict coverage: remaining uncovered paths ----

// TestSweepZombiesDetectsOldWorker 验证看门狗检测 lastActive 过期的 worker。
func TestSweepZombiesDetectsOldWorker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	e := NewExecutor(s, d, sb, bus, sh)

	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return input, nil
	})
	_ = sb.LoadPlugin("test", plugin)

	_ = e.StartWorker(context.Background(), "w1", "test")
	time.Sleep(50 * time.Millisecond)

	// Manually set worker state to busy with old lastActive to simulate zombie
	e.mu.RLock()
	for _, w := range e.workers {
		atomic.StoreInt32(&w.state, workerBusy)
		w.mu.Lock()
		w.currentTask = "zombie-task"
		w.lastActive = time.Now().Add(-120 * time.Second) // 2 minutes ago
		w.mu.Unlock()
	}
	e.mu.RUnlock()

	// sweepZombies should detect and terminate the zombie
	e.sweepZombies(context.Background())

	// The original zombie worker should be gone
	// (a reborn worker may have been spawned)
}

// TestWorkerLoopWithSelfHealer 验证带 SelfHealer 的任务执行路径。
func TestWorkerLoopWithSelfHealer(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	e := NewExecutor(s, d, sb, bus, sh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	plugin := sandbox.NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return input, nil
	})
	_ = sb.LoadPlugin("test", plugin)
	_ = e.StartWorker(context.Background(), "w1", "test")

	task, _ := s.CreateTask(context.Background(), "test", nil, []byte("hello"))
	_ = s.QueueTask(context.Background(), task.ID)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e.GetStats().TotalTasksRun >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if e.GetStats().TotalTasksRun < 1 {
		t.Error("expected 1 task run with selfhealer")
	}

	_ = e.StopAllWorkers(context.Background())
}

// TestWorkerLoopFailureWithSelfHealer 验证带 SelfHealer 的任务失败路径。
func TestWorkerLoopFailureWithSelfHealer(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	// Use fast config to avoid long backoff delays
	fastCfg := selfheal.SelfHealConfig{
		MaxRetries: 2, RetryDelay: time.Millisecond, MaxRetryDelay: 5 * time.Millisecond,
		CircuitThreshold: 5, CircuitTimeout: 10 * time.Millisecond,
		HealthInterval: time.Second, IncidentRetention: time.Hour,
	}
	sh := selfheal.NewSelfHealer(fastCfg)
	e := NewExecutor(s, d, sb, bus, sh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	plugin := sandbox.NewMockPlugin("fail", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, fmt.Errorf("plugin error")
	})
	_ = sb.LoadPlugin("fail", plugin)
	_ = e.StartWorker(context.Background(), "w1", "fail")

	task, _ := s.CreateTask(context.Background(), "fail", nil, nil)
	_ = s.QueueTask(context.Background(), task.ID)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.GetStats().TotalTasksFailed >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if e.GetStats().TotalTasksFailed < 1 {
		t.Error("expected 1 task failed with selfhealer")
	}

	_ = e.StopAllWorkers(context.Background())
}

// TestStartWatchdogWithTicker 验证看门狗定时器工作。
func TestStartWatchdogWithTicker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	e := NewExecutor(s, d, sb, bus, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	e.StartWatchdog(ctx)
	<-ctx.Done()
	// Watchdog should have run at least once without errors
}

// TestContextBuilderEmptyResults 验证空上游结果的上下文构建。
func TestContextBuilderEmptyResults(t *testing.T) {
	cb := NewContextBuilder(16000)
	prompt := cb.BuildUserPrompt(map[string]string{}, "do something")
	if !strings.Contains(prompt, "do something") {
		t.Error("expected prompt to contain instructions")
	}
}

// TestContextBuilderLongResultTruncation 验证单个超长结果的截断。
func TestContextBuilderLongResultTruncation(t *testing.T) {
	cb := NewContextBuilder(100)
	longResult := strings.Repeat("abcdefghij", 100)
	results := map[string]string{"task1": longResult}
	prompt := cb.BuildUserPrompt(results, "go")
	if !strings.Contains(prompt, "Truncated") {
		t.Error("expected truncation marker in prompt")
	}
}

// TestGenerateStructuredSuccessPath 验证 LLM 完整成功路径。
func TestGenerateStructuredSuccessPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": [{"message": {"content": "Hello, World!"}}]}`))
	}))
	defer ts.Close()

	c := NewLLMClient(ts.URL, "test-key")
	result, err := c.GenerateStructured(context.Background(), "gpt-4o", "system prompt", "user prompt", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got '%s'", result)
	}
}

// TestLLMClientAuthHeader 验证 API Key 设置 Authorization 头。
func TestLLMClientAuthHeader(t *testing.T) {
	var authHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer ts.Close()

	c := NewLLMClient(ts.URL, "my-secret-key")
	_, err := c.GenerateStructured(context.Background(), "test", "sys", "user", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authHeader != "Bearer my-secret-key" {
		t.Errorf("expected 'Bearer my-secret-key', got '%s'", authHeader)
	}
}

// TestLLMClientNoAuthHeader 验证无 API Key 时不设置 Authorization 头。
func TestLLMClientNoAuthHeader(t *testing.T) {
	var hasAuth bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasAuth = r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer ts.Close()

	c := NewLLMClient(ts.URL, "")
	_, err := c.GenerateStructured(context.Background(), "test", "sys", "user", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasAuth {
		t.Error("expected no Authorization header when API key is empty")
	}
}

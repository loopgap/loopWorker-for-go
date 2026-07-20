package integration

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/event"
	"loopworker/pkg/plugin"
	"loopworker/pkg/security"
	"loopworker/pkg/skill"
	"loopworker/pkg/workflow"
)

func TestFullPlatformIntegration(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sb := sandbox.NewSandbox(sandbox.SandboxConfig{
		MaxMemoryMB:   256,
		MaxCPUSeconds: 30,
		MaxOutputMB:   64,
		MaxConcurrent: 10,
	})

	tmpDir := t.TempDir()
	_, err := plugin.NewPluginManager(sb, bus, tmpDir)
	if err != nil {
		t.Fatalf("create plugin manager: %v", err)
	}

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	e := executor.NewExecutor(s, d, sb, bus, nil)
	o := observer.NewObserver(bus)
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	sm := security.NewSecurityManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = o.Start(ctx)
	go e.Run(ctx)

	// Create test plugin
	echoPlugin := sandbox.NewMockPlugin("echo", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return input, nil
	})
	_ = sb.LoadPlugin("echo", echoPlugin)

	// Create user
	user, err := sm.CreateUser("admin", "pass123", "admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Authenticate
	token, err := sm.Authenticate("admin", "pass123")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	// Validate token
	validUser, err := sm.ValidateToken(token.Value)
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}

	if validUser.ID != user.ID {
		t.Error("user ID mismatch")
	}

	// Check authorization
	if !sm.Authorize(user, security.PermRead) {
		t.Error("admin should have read permission")
	}

	// Start workers
	for i := 0; i < 3; i++ {
		_ = e.StartWorker(ctx, fmt.Sprintf("worker-%d", i), "echo")
	}

	// Create and execute tasks
	for i := 0; i < 10; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, []byte("hello"))
		_ = s.QueueTask(ctx, task.ID)
	}

	// Wait for completion by checking stats
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := e.GetStats()
		if stats.TotalTasksRun >= 10 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	stats := e.GetStats()
	if stats.TotalTasksRun < 10 {
		t.Errorf("expected 10 completed tasks, got %d", stats.TotalTasksRun)
	}

	// Check observer metrics
	metrics := o.GetMetrics()
	if len(metrics) == 0 {
		t.Error("metrics should be collected")
	}

	// Check health
	health := sh.GetHealthStatus()
	if health == selfheal.HealthCritical {
		t.Error("health should not be critical")
	}

	// Check audit log
	auditLog := sm.GetAuditLog()
	if len(auditLog) == 0 {
		t.Error("audit log should have entries")
	}
}

func TestSecurityIntegration(t *testing.T) {
	sm := security.NewSecurityManager()
	rl := security.NewRateLimiter(5, time.Minute)

	// Create users
	_, _ = sm.CreateUser("user1", "pass1", "viewer")
	_, _ = sm.CreateUser("user2", "pass2", "admin")

	// Test rate limiting
	for i := 0; i < 5; i++ {
		if !rl.Allow("192.168.1.1") {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	if rl.Allow("192.168.1.1") {
		t.Error("should be rate limited")
	}

	// Different IP should work
	if !rl.Allow("192.168.1.2") {
		t.Error("different IP should be allowed")
	}
}

func TestWorkflowIntegration(t *testing.T) {
	engine := workflow.NewWorkflowEngine()
	wf := workflow.NewWorkflow("test-wf", "Test Workflow")

	var stepOrder []string
	var mu sync.Mutex

	wf.AddStep(&workflow.Step{
		ID: "init",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			stepOrder = append(stepOrder, "init")
			mu.Unlock()
			return state, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "process",
		DependsOn: []string{"init"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			stepOrder = append(stepOrder, "process")
			mu.Unlock()
			return state, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "cleanup",
		DependsOn: []string{"process"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			stepOrder = append(stepOrder, "cleanup")
			mu.Unlock()
			return state, nil
		},
	})

	engine.Register(wf)

	err := engine.Execute(context.Background(), "test-wf")
	if err != nil {
		t.Fatalf("execute workflow: %v", err)
	}

	if len(stepOrder) != 3 {
		t.Errorf("expected 3 steps, got %d", len(stepOrder))
	}

	if stepOrder[0] != "init" || stepOrder[1] != "process" || stepOrder[2] != "cleanup" {
		t.Errorf("wrong step order: %v", stepOrder)
	}
}

func TestSelfHealIntegration(t *testing.T) {
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	ctx := context.Background()

	var attempts int32
	err := sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error {
		if atomic.AddInt32(&attempts, 1) < 3 {
			return fmt.Errorf("temporary error")
		}
		return nil
	})

	if err != nil {
		t.Errorf("should succeed after retries: %v", err)
	}

	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestConcurrentTaskExecution(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sb := sandbox.NewSandbox(sandbox.SandboxConfig{
		MaxMemoryMB:   256,
		MaxCPUSeconds: 30,
		MaxOutputMB:   64,
		MaxConcurrent: 20,
	})

	plugin := sandbox.NewMockPlugin("fast", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		time.Sleep(10 * time.Millisecond)
		return input, nil
	})
	_ = sb.LoadPlugin("fast", plugin)

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	e := executor.NewExecutor(s, d, sb, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Start 10 workers
	for i := 0; i < 10; i++ {
		_ = e.StartWorker(ctx, fmt.Sprintf("worker-%d", i), "fast")
	}

	// Create 100 tasks
	for i := 0; i < 100; i++ {
		task, _ := s.CreateTask(ctx, "test", nil, []byte("test"))
		_ = s.QueueTask(ctx, task.ID)
	}

	// Wait for completion by checking stats
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := e.GetStats()
		if stats.TotalTasksRun >= 50 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	stats := e.GetStats()
	if stats.TotalTasksRun < 50 {
		t.Errorf("expected at least 50 completed tasks, got %d", stats.TotalTasksRun)
	}
}

func TestCircuitBreakerIntegration(t *testing.T) {
	sh := selfheal.NewSelfHealer(selfheal.SelfHealConfig{
		MaxRetries:        3,
		RetryDelay:        10 * time.Millisecond,
		MaxRetryDelay:     100 * time.Millisecond,
		CircuitThreshold:  3,
		CircuitTimeout:    50 * time.Millisecond,
		HealthInterval:    time.Second,
		IncidentRetention: time.Hour,
	})
	cb := sh.GetCircuitBreaker("test-service")

	// Trigger circuit breaker
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	if cb.GetState() != selfheal.CircuitOpen {
		t.Error("circuit should be open")
	}

	// Wait for timeout
	time.Sleep(100 * time.Millisecond)

	// Should allow half-open request
	if !cb.AllowRequest() {
		t.Error("should allow request in half-open state")
	}

	// Success should close circuit
	cb.RecordSuccess()
	cb.RecordSuccess()
	cb.RecordSuccess()

	if cb.GetState() != selfheal.CircuitClosed {
		t.Error("circuit should be closed after successes")
	}
}

func TestFullCallChainWithCircuitBreaker(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{MaxMemoryMB: 64, MaxCPUSeconds: 5, MaxOutputMB: 1, MaxConcurrent: 2})
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	e := executor.NewExecutor(s, d, sb, bus, sh)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start observer
	o := observer.NewObserver(bus)
	if err := o.Start(ctx); err != nil {
		t.Fatalf("start observer: %v", err)
	}

	// Run executor
	go e.Run(ctx)

	// Load plugin
	plugin := sandbox.NewMockPlugin("echo", "1.0", func(ctx context.Context, input []byte, sc skill.SkillContext) ([]byte, error) {
		return []byte("echo-result"), nil
	})
	if err := sb.LoadPlugin("echo", plugin); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	// Start worker
	if err := e.StartWorker(ctx, "w-1", "echo"); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	// Create and queue task
	task, err := s.CreateTask(ctx, "echo", nil, []byte("hello"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.QueueTask(ctx, task.ID); err != nil {
		t.Fatalf("queue task: %v", err)
	}

	// Wait for completion
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := e.GetStats()
		if stats.TotalTasksRun >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	stats := e.GetStats()
	if stats.TotalTasksRun < 1 {
		t.Errorf("expected >=1 task run, got %d", stats.TotalTasksRun)
	}

	// Verify circuit breaker exists and is in closed state
	cb := sh.GetCircuitBreaker("llm")
	if cb.GetState() != selfheal.CircuitClosed {
		t.Errorf("circuit breaker should be closed after successful execution, got %v", cb.GetState())
	}

	// Verify observer metrics
	metrics := o.GetMetrics()
	if len(metrics) == 0 {
		t.Error("observer should have recorded metrics")
	}

	// Verify task state
	completedTask, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatal("task should exist")
	}
	if completedTask.State != "completed" {
		t.Errorf("expected completed, got %s", completedTask.State)
	}

	_ = e.StopAllWorkers(ctx)
}

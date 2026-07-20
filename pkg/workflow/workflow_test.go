package workflow

import (
	"fmt"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loopworker/pkg/event"
)

func TestNewWorkflow(t *testing.T) {
	wf := NewWorkflow("test-1", "Test Workflow")
	if wf == nil {
		t.Fatal("workflow should not be nil")
	}
	if wf.ID != "test-1" {
		t.Errorf("expected ID test-1, got %s", wf.ID)
	}
	if wf.Status != WorkflowPending {
		t.Errorf("expected status pending, got %d", wf.Status)
	}
}

func TestAddStep(t *testing.T) {
	wf := NewWorkflow("test-1", "Test")
	step := &Step{
		ID:   "step-1",
		Name: "Step 1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return state, nil
		},
	}
	wf.AddStep(step)

	if len(wf.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(wf.Steps))
	}
	if wf.StepStatus["step-1"] != StepPending {
		t.Error("step should be pending")
	}
}

func TestWorkflowEngineExecute(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")

	var executed int32
	wf.AddStep(&Step{
		ID: "step-1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			atomic.AddInt32(&executed, 1)
			return state, nil
		},
	})

	engine.Register(wf)
	err := engine.Execute(context.Background(), "test-1")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if atomic.LoadInt32(&executed) != 1 {
		t.Error("step should be executed")
	}
	if wf.GetStatus() != WorkflowCompleted {
		t.Error("workflow should be completed")
	}
}

func TestWorkflowWithDependencies(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")

	var order []string
	var mu sync.Mutex

	wf.AddStep(&Step{
		ID: "step-1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			order = append(order, "step-1")
			mu.Unlock()
			return state, nil
		},
	})

	wf.AddStep(&Step{
		ID:        "step-2",
		DependsOn: []string{"step-1"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			order = append(order, "step-2")
			mu.Unlock()
			return state, nil
		},
	})

	engine.Register(wf)
	_ = engine.Execute(context.Background(), "test-1")

	if len(order) != 2 || order[0] != "step-1" || order[1] != "step-2" {
		t.Errorf("steps should execute in order, got %v", order)
	}
}

func TestWorkflowWithRetry(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")

	var attempts int32
	wf.AddStep(&Step{
		ID: "step-1",
		RetryPolicy: &RetryPolicy{
			MaxRetries:  3,
			InitialWait: 10 * time.Millisecond,
			MaxWait:     100 * time.Millisecond,
			Multiplier:  2,
		},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			if atomic.AddInt32(&attempts, 1) < 3 {
				return nil, errors.New("temporary error")
			}
			return state, nil
		},
	})

	engine.Register(wf)
	err := engine.Execute(context.Background(), "test-1")

	if err != nil {
		t.Errorf("expected success after retries, got %v", err)
	}
}

func TestWorkflowStepFailure(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")

	wf.AddStep(&Step{
		ID: "step-1",
		RetryPolicy: &RetryPolicy{
			MaxRetries:  0,
			InitialWait: 10 * time.Millisecond,
			MaxWait:     100 * time.Millisecond,
			Multiplier:  2,
		},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return nil, errors.New("permanent error")
		},
	})

	engine.Register(wf)
	err := engine.Execute(context.Background(), "test-1")

	if err == nil {
		t.Error("expected error when step fails")
	}
	if wf.GetStatus() != WorkflowFailed {
		t.Error("workflow should be failed")
	}
}

func TestWorkflowCondition(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")

	var executed int32
	wf.AddStep(&Step{
		ID: "step-1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			atomic.AddInt32(&executed, 1)
			return state, nil
		},
	})

	wf.AddStep(&Step{
		ID: "step-2",
		Condition: func(state map[string]interface{}) bool {
			return false
		},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			atomic.AddInt32(&executed, 1)
			return state, nil
		},
	})

	engine.Register(wf)
	_ = engine.Execute(context.Background(), "test-1")

	if atomic.LoadInt32(&executed) != 1 {
		t.Error("only step-1 should be executed")
	}
}

func TestWorkflowState(t *testing.T) {
	wf := NewWorkflow("test-1", "Test")
	wf.SetState("key1", "value1")

	val, ok := wf.GetState("key1")
	if !ok || val != "value1" {
		t.Error("state should be set correctly")
	}
}

func TestWorkflowCancellation(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")

	wf.AddStep(&Step{
		ID: "step-1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return state, nil
			}
		},
	})

	engine.Register(wf)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := engine.Execute(ctx, "test-1")
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestDAGWorkflow(t *testing.T) {
	dag := NewDAGWorkflow("dag-1", "DAG Test")

	dag.AddStep(&Step{ID: "a"})
	dag.AddStep(&Step{ID: "b"})
	dag.AddStep(&Step{ID: "c"})

	dag.AddEdge("a", "b")
	dag.AddEdge("b", "c")

	sorted, err := dag.TopologicalSort()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if len(sorted) != 3 {
		t.Errorf("expected 3 steps, got %d", len(sorted))
	}
}

func TestDAGCycleDetection(t *testing.T) {
	dag := NewDAGWorkflow("dag-1", "DAG Test")

	dag.AddStep(&Step{ID: "a"})
	dag.AddStep(&Step{ID: "b"})

	dag.AddEdge("a", "b")
	dag.AddEdge("b", "a")

	_, err := dag.TopologicalSort()
	if err == nil {
		t.Error("expected cycle detection error")
	}
}

func TestParallelWorkflow(t *testing.T) {
	pw := NewParallelWorkflow("par-1", "Parallel Test", 2)

	var count int32
	for i := 0; i < 4; i++ {
		pw.AddStep(&Step{
			ID: string(rune('a' + i)),
			Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&count, 1)
				return state, nil
			},
		})
	}

	err := pw.ExecuteParallel(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if atomic.LoadInt32(&count) != 4 {
		t.Error("all steps should be executed")
	}
}

func TestGetWorkflow(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")
	engine.Register(wf)

	found, exists := engine.GetWorkflow("test-1")
	if !exists || found.ID != "test-1" {
		t.Error("should find workflow")
	}

	_, exists = engine.GetWorkflow("nonexistent")
	if exists {
		t.Error("should not find nonexistent workflow")
	}
}

func TestListWorkflows(t *testing.T) {
	engine := NewWorkflowEngine()
	engine.Register(NewWorkflow("w1", "Workflow 1"))
	engine.Register(NewWorkflow("w2", "Workflow 2"))

	workflows := engine.ListWorkflows()
	if len(workflows) != 2 {
		t.Errorf("expected 2 workflows, got %d", len(workflows))
	}
}

func TestStepStatus(t *testing.T) {
	tests := []struct {
		s    StepStatus
		want string
	}{
		{StepPending, "pending"},
		{StepRunning, "running"},
		{StepCompleted, "completed"},
		{StepFailed, "failed"},
		{StepSkipped, "skipped"},
		{StepRetrying, "retrying"},
	}

	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("StepStatus.String() = %s, want %s", got, tt.want)
		}
	}
}

func TestDAGCycleDetectionThreeNodes(t *testing.T) {
	dag := NewDAGWorkflow("dag-1", "DAG Test")
	dag.AddStep(&Step{ID: "a"})
	dag.AddStep(&Step{ID: "b"})
	dag.AddStep(&Step{ID: "c"})
	dag.AddEdge("a", "b")
	dag.AddEdge("b", "c")
	dag.AddEdge("c", "a")
	_, err := dag.TopologicalSort()
	if err == nil {
		t.Error("expected cycle detection error for 3-node cycle")
	}
}

func TestDAGNoCycleLinear(t *testing.T) {
	dag := NewDAGWorkflow("dag-1", "DAG Test")
	dag.AddStep(&Step{ID: "a"})
	dag.AddStep(&Step{ID: "b"})
	dag.AddStep(&Step{ID: "c"})
	dag.AddEdge("a", "b")
	dag.AddEdge("b", "c")
	sorted, err := dag.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 3 {
		t.Errorf("expected 3, got %d", len(sorted))
	}
	if sorted[0] != "a" || sorted[2] != "c" {
		t.Errorf("expected a,b,c order, got %v", sorted)
	}
}

func TestWorkflowEngineExecuteNotFound(t *testing.T) {
	engine := NewWorkflowEngine()
	err := engine.Execute(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent workflow")
	}
}

func TestWorkflowStepTimeout(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")
	wf.AddStep(&Step{
		ID:      "step-1",
		Timeout: 50 * time.Millisecond,
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return state, nil
			}
		},
	})
	engine.Register(wf)
	err := engine.Execute(context.Background(), "test-1")
	if err == nil {
		t.Error("expected timeout error")
	}
	if wf.GetStatus() != WorkflowFailed {
		t.Error("workflow should be failed")
	}
}

func TestWorkflowStepWithOnFailure(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("test-1", "Test")
	var onFailureCalled int32
	wf.AddStep(&Step{
		ID:          "step-1",
		RetryPolicy: &RetryPolicy{MaxRetries: 0, InitialWait: time.Millisecond, MaxWait: time.Millisecond, Multiplier: 1},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return nil, errors.New("fail")
		},
		OnFailure: func(ctx context.Context, err error) error { atomic.AddInt32(&onFailureCalled, 1); return nil },
	})
	engine.Register(wf)
	_ = engine.Execute(context.Background(), "test-1")
	if atomic.LoadInt32(&onFailureCalled) != 1 {
		t.Errorf("expected OnFailure called once, got %d", onFailureCalled)
	}
}

func TestParallelWorkflowWithDependency(t *testing.T) {
	pw := NewParallelWorkflow("par-1", "Parallel Dep Test", 4)
	var order []string
	var mu sync.Mutex
	pw.AddStep(&Step{
		ID: "a",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			order = append(order, "a")
			mu.Unlock()
			return state, nil
		},
	})
	pw.AddStep(&Step{
		ID:        "b",
		DependsOn: []string{"a"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			order = append(order, "b")
			mu.Unlock()
			return state, nil
		},
	})
	err := pw.ExecuteParallel(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("expected a,b order, got %v", order)
	}
	mu.Unlock()
}

func TestParallelWorkflowDefaultConcurrency(t *testing.T) {
	pw := NewParallelWorkflow("par-1", "Test", 0)
	if pw.maxConcurrency != 4 {
		t.Errorf("expected default concurrency 4, got %d", pw.maxConcurrency)
	}
}

func TestWorkflowIsComplete(t *testing.T) {
	wf := NewWorkflow("test", "Test")
	if wf.IsComplete() {
		t.Error("new workflow should not be complete")
	}
	wf.Status = WorkflowCompleted
	if !wf.IsComplete() {
		t.Error("completed workflow should be complete")
	}
	wf.Status = WorkflowFailed
	if !wf.IsComplete() {
		t.Error("failed workflow should be complete")
	}
	wf.Status = WorkflowCancelled
	if !wf.IsComplete() {
		t.Error("cancelled workflow should be complete")
	}
}

func TestStepStatusUnknown(t *testing.T) {
	s := StepStatus(99)
	if s.String() != "unknown" {
		t.Errorf("expected unknown, got %s", s.String())
	}
}

func TestParallelWorkflowFailure(t *testing.T) {
	pw := NewParallelWorkflow("par-1", "Test", 2)
	pw.AddStep(&Step{
		ID: "fail",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return nil, errors.New("boom")
		},
	})
	pw.AddStep(&Step{
		ID: "ok",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			time.Sleep(100 * time.Millisecond)
			return state, nil
		},
	})
	err := pw.ExecuteParallel(context.Background())
	if err == nil {
		t.Error("expected error from parallel workflow")
	}
	if pw.GetStatus() != WorkflowFailed {
		t.Error("workflow should be failed")
	}
}

func TestParallelWorkflowContextCancellation(t *testing.T) {
	pw := NewParallelWorkflow("par-1", "Test", 2)
	pw.AddStep(&Step{
		ID: "slow",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return state, nil
			}
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := pw.ExecuteParallel(ctx)
	if err == nil {
		t.Error("expected timeout error")
	}
}
func TestWorkflowEngineWithEventBus(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventWorkflowStepCompleted, 10)

	engine := NewWorkflowEngine(WithEventBus(bus))
	wf := NewWorkflow("wf-1", "Test")

	wf.AddStep(&Step{
		ID: "step-1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"result": "ok"}, nil
		},
	})

	engine.Register(wf)
	err := engine.Execute(context.Background(), "wf-1")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Should receive step completed event
	select {
	case evt := <-sub.Chan():
		if evt.Type() != event.EventWorkflowStepCompleted {
			t.Errorf("expected step completed event, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for step event")
	}
}

func TestWorkflowEngineWorkflowEvents(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	startedSub := bus.Subscribe(event.EventWorkflowStarted, 10)
	completedSub := bus.Subscribe(event.EventWorkflowCompleted, 10)

	engine := NewWorkflowEngine(WithEventBus(bus))
	wf := NewWorkflow("wf-events", "Event Test")

	wf.AddStep(&Step{
		ID: "step-1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return state, nil
		},
	})

	engine.Register(wf)
	err := engine.Execute(context.Background(), "wf-events")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Should receive workflow started event
	select {
	case evt := <-startedSub.Chan():
		if evt.Type() != event.EventWorkflowStarted {
			t.Errorf("expected started event, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for started event")
	}

	// Should receive workflow completed event
	select {
	case evt := <-completedSub.Chan():
		if evt.Type() != event.EventWorkflowCompleted {
			t.Errorf("expected completed event, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for completed event")
	}
}

func TestWorkflowEngineStepFailureEvent(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventWorkflowStepCompleted, 10)

	engine := NewWorkflowEngine(WithEventBus(bus))
	wf := NewWorkflow("wf-fail", "Fail Test")

	wf.AddStep(&Step{
		ID: "step-fail",
		RetryPolicy: &RetryPolicy{
			MaxRetries:  0,
			InitialWait: time.Millisecond,
		},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return nil, errors.New("permanent failure")
		},
	})

	engine.Register(wf)
	err := engine.Execute(context.Background(), "wf-fail")
	if err == nil {
		t.Fatal("expected error from failed step")
	}

	// Should receive step failed event (Success: false)
	select {
	case evt := <-sub.Chan():
		if evt.Type() != event.EventWorkflowStepCompleted {
			t.Errorf("expected step event, got %s", evt.Type())
		}
		payload, ok := evt.Payload().(event.WorkflowStepCompletedPayload)
		if !ok {
			t.Fatal("expected WorkflowStepCompletedPayload")
		}
		if payload.Success {
			t.Error("expected step event with Success=false")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for failed step event")
	}
}

func TestWorkflowEngineFailedEvent(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventWorkflowFailed, 10)

	engine := NewWorkflowEngine(WithEventBus(bus))
	wf := NewWorkflow("wf-cycle", "Cycle Test")

	// Create a cycle to trigger workflow failed event
	wf.AddStep(&Step{ID: "a", DependsOn: []string{"b"}})
	wf.AddStep(&Step{ID: "b", DependsOn: []string{"a"}})

	engine.Register(wf)
	err := engine.Execute(context.Background(), "wf-cycle")
	if err == nil {
		t.Fatal("expected error from cyclic workflow")
	}

	select {
	case evt := <-sub.Chan():
		if evt.Type() != event.EventWorkflowFailed {
			t.Errorf("expected workflow failed event, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for failed event")
	}
}

func TestWorkflowEngineSetDispatcher(t *testing.T) {
	engine := NewWorkflowEngine()

	// Initially nil
	if engine.GetDispatcher() != nil {
		t.Error("expected nil dispatcher initially")
	}

	// Set dispatcher
	mockDisp := &mockTaskDispatcher{}
	engine.SetDispatcher(mockDisp)

	if engine.GetDispatcher() != mockDisp {
		t.Error("expected dispatcher to be set")
	}
}

func TestWorkflowEngineWithDispatcherOption(t *testing.T) {
	mockDisp := &mockTaskDispatcher{}
	engine := NewWorkflowEngine(WithDispatcher(mockDisp))

	if engine.GetDispatcher() != mockDisp {
		t.Error("expected dispatcher set via option")
	}
}

// mockTaskDispatcher implements workflow.TaskDispatcher for testing.
type mockTaskDispatcher struct{}

func (m *mockTaskDispatcher) CreateTask(ctx context.Context, taskType string, config map[string]interface{}, input []byte) (*TaskRef, error) {
	return &TaskRef{ID: "mock-1", Type: taskType, State: "pending"}, nil
}

func (m *mockTaskDispatcher) WaitForTask(ctx context.Context, taskID string) (*TaskRef, error) {
	return &TaskRef{ID: taskID, State: "completed"}, nil
}

func (m *mockTaskDispatcher) GetTask(taskID string) (*TaskRef, bool) {
	return &TaskRef{ID: taskID, State: "completed"}, true
}

func TestWorkflowEngineConcurrentExecution(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventWorkflowStepCompleted, 500)

	engine := NewWorkflowEngine(WithEventBus(bus))

	const workflows = 20
	const stepsPerWF = 5

	var wg sync.WaitGroup
	wg.Add(workflows)

	for i := 0; i < workflows; i++ {
		go func(wfID int) {
			defer wg.Done()
			wf := NewWorkflow(fmt.Sprintf("concurrent-wf-%d", wfID), fmt.Sprintf("WF %d", wfID))
			for s := 0; s < stepsPerWF; s++ {
				wf.AddStep(&Step{
					ID: fmt.Sprintf("step-%d", s),
					Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
						return state, nil
					},
				})
			}
			engine.Register(wf)
			_ = engine.Execute(context.Background(), wf.ID)
		}(i)
	}

	wg.Wait()

	// Collect events
	var count int
	timeout := time.After(3 * time.Second)
collect:
	for {
		select {
		case evt := <-sub.Chan():
			if evt.Type() == event.EventWorkflowStepCompleted {
				count++
			}
		case <-timeout:
			break collect
		}
	}

	expected := workflows * stepsPerWF
	if count != expected {
		t.Errorf("expected %d step events, got %d (possible race or drop)", expected, count)
	}
}

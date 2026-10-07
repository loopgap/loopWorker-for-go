package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentExecuteOfTheSameWorkflowIsRefused pins the invariant that makes
// Register usable from a server: a registered Workflow is a single shared
// instance, and executeWorkflow writes workflow.State and workflow.StepStatus
// from inside the step action. Two overlapping Execute calls on one instance are
// two goroutines writing the same map, which is a process-killing "concurrent
// map writes", not a fixable error.
//
// So the engine refuses a second overlapping run instead of corrupting the first.
// Until workflows were registered at startup this was unreachable: nothing in
// production ever called Execute.
func TestConcurrentExecuteOfTheSameWorkflowIsRefused(t *testing.T) {
	engine := NewWorkflowEngine()
	release := make(chan struct{})
	entered := make(chan struct{})
	var countMu sync.Mutex
	runs := 0

	wf := NewWorkflow("shared", "Shared")
	wf.AddStep(&Step{
		ID: "slow",
		Action: func(_ context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			// Only the first invocation parks; a second one returns at once so
			// the test fails with an assertion instead of a deadlock.
			countMu.Lock()
			runs++
			first := runs == 1
			countMu.Unlock()
			if first {
				close(entered)
				<-release
			}
			return map[string]interface{}{"touched": true}, nil
		},
	})
	engine.Register(wf)

	done := make(chan error, 1)
	go func() { done <- engine.Execute(context.Background(), "shared") }()
	<-entered // the first run now owns the workflow and is parked mid-step

	// Deterministic overlap: the first run cannot finish until release closes,
	// and this call happens before it does.
	second := engine.Execute(context.Background(), "shared")
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the run that was allowed to proceed must still complete: %v", err)
	}

	if second == nil {
		t.Fatal("a second overlapping run of the same workflow must be refused, not executed")
	}
	if wf.GetStatus() != WorkflowCompleted {
		t.Errorf("status after the surviving run = %v, want completed", wf.GetStatus())
	}
}

// TestStepDependencyCheckDoesNotRaceWithStepRegistration pins the locking
// discipline of checkDependencies. It reads Workflow.StepStatus, the same map
// that AddStep writes when it registers a step under w.mu, so an unlocked read
// is a genuine data race against any caller that adds a step while a run is in
// flight.
//
// AddStep is exported and takes no execMu, so "add a step during a run" is
// reachable from outside the package today - the server itself only registers at
// startup. This test drives exactly that interleaving so the read stays locked.
//
// Run under -race. Before checkDependencies read through GetStepStatus this
// reported a race between workflow.go:439 (the read) and workflow.go:229
// (AddStep's write).
func TestStepDependencyCheckDoesNotRaceWithStepRegistration(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("raced-deps", "Raced")
	// A dependent step is what forces checkDependencies to read the map at all;
	// without DependsOn the loop body never executes.
	wf.AddStep(&Step{
		ID:        "second",
		DependsOn: []string{"first"},
		Action:    func(context.Context, map[string]interface{}) (map[string]interface{}, error) { return nil, nil },
	})
	wf.AddStep(&Step{
		ID:     "first",
		Action: func(context.Context, map[string]interface{}) (map[string]interface{}, error) { return nil, nil },
	})
	engine.Register(wf)

	// Repeatedly run the workflow while a second goroutine registers more steps
	// on the same instance. Both touch wf.StepStatus: the run reads it in
	// checkDependencies, the registration writes it in AddStep.
	// Both loops are bounded on purpose: an unbounded adder grows StepOrder
	// faster than computeExecutionOrder can walk it, and the test hangs instead
	// of racing. 400 registrations against 40 runs keeps the two goroutines
	// interleaved without letting the workflow grow without limit.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			wf.AddStep(&Step{
				ID:     fmt.Sprintf("extra-%d", i),
				Action: func(context.Context, map[string]interface{}) (map[string]interface{}, error) { return nil, nil },
			})
		}
	}()

	for i := 0; i < 40; i++ {
		if err := engine.Execute(context.Background(), "raced-deps"); err != nil {
			wg.Wait()
			t.Fatalf("run %d: %v", i, err)
		}
	}
	wg.Wait()
}

// TestWorkflowRunsAgainAfterFinishing proves the guard is a concurrency lock, not
// a one-shot latch: an operator re-running a workflow over the API must still
// work once the previous run reached a terminal state.
func TestWorkflowRunsAgainAfterFinishing(t *testing.T) {
	engine := NewWorkflowEngine()
	wf := NewWorkflow("repeat", "Repeat")
	wf.AddStep(&Step{
		ID:     "noop",
		Action: func(context.Context, map[string]interface{}) (map[string]interface{}, error) { return nil, nil },
	})
	engine.Register(wf)

	for i := 1; i <= 2; i++ {
		if err := engine.Execute(context.Background(), "repeat"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}

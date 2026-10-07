package workflow

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestExecuteParallelSurvivesConcurrentMutation is the regression guard for the
// crash this file used to be one line away from. ExecuteParallel read Steps,
// StepOrder, StepStatus and State with no lock at all, and wrote State under
// completedMu while SetState wrote the same map under mu - two mutexes, one map.
// The result was a possible "fatal error: concurrent map writes", which kills the
// process instead of returning an error, so only -race can prove this is fixed.
func TestExecuteParallelSurvivesConcurrentMutation(t *testing.T) {
	pw := NewParallelWorkflow("race-1", "race", 4)

	release := make(chan struct{})
	pw.AddStep(&Step{
		ID:   "s1",
		Name: "blocker",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			<-release
			return map[string]interface{}{"s1": "done"}, nil
		},
	})
	pw.AddStep(&Step{
		ID:        "s2",
		Name:      "follower",
		DependsOn: []string{"s1"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			// s2 must see s1's merged result, which is what proves the snapshot is
			// taken per-step rather than once for the whole run.
			return map[string]interface{}{"s2": state["s1"]}, nil
		},
	})

	done := make(chan error, 1)
	go func() { done <- pw.ExecuteParallel(context.Background()) }()

	// Mutate the way an operator would while a run is in flight.
	for i := 0; i < 500; i++ {
		pw.SetState(fmt.Sprintf("key-%d", i), i)
		pw.AddStep(&Step{
			ID:   fmt.Sprintf("late-%d", i),
			Name: "late",
			Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
				return nil, nil
			},
		})
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteParallel: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ExecuteParallel did not finish")
	}

	if got := pw.GetStepStatus("s1"); got != StepCompleted {
		t.Errorf("s1 status = %v, want completed", got)
	}
	if got := pw.GetStepStatus("s2"); got != StepCompleted {
		t.Errorf("s2 status = %v, want completed", got)
	}
	if v, ok := pw.GetState("s2"); !ok || v != "done" {
		t.Errorf("s2 saw %v, want the value s1 returned - a dependent step must observe its dependency's merged result", v)
	}
}

// TestStateSnapshotIsIsolated proves the copy handed to a step's Action is not
// the live map, which is what removes the unguarded read at the call site.
func TestStateSnapshotIsIsolated(t *testing.T) {
	wf := NewWorkflow("snap-1", "snap")
	wf.SetState("a", "original")

	snap := wf.StateSnapshot()
	if snap["a"] != "original" {
		t.Fatalf("snapshot lost state: %v", snap)
	}

	snap["a"] = "mutated"
	snap["injected"] = true

	if v, _ := wf.GetState("a"); v != "original" {
		t.Errorf("mutating the snapshot changed workflow state to %v", v)
	}
	if _, ok := wf.GetState("injected"); ok {
		t.Error("writing to the snapshot leaked a key into workflow state")
	}
}

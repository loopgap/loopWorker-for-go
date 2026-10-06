package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"loopworker/pkg/workflow"
)

var errBoom = errors.New("boom")

// A registered Workflow is one shared instance for the whole process:
// executeWorkflow writes Status, StartedAt, CompletedAt and Error while holding
// the workflow's own mutex. GET /api/v1/workflow/{id} projects that same struct
// onto the wire, so the projection has to read it through the workflow's lock -
// reading the exported fields directly is a data race that only shows up once a
// workflow is really running while somebody polls it.
//
// Run under -race. Before newWorkflowView stopped touching wf.Error directly
// this reported three races (handlers_system.go:163/164/205 against
// workflow.go:277/342/344) within six executions.
func TestWorkflowStatusEndpointDoesNotRaceWithExecution(t *testing.T) {
	env := newTestEnv(t)

	reachedStep := make(chan struct{}, 1)
	wf := workflow.NewWorkflow("raced", "Raced")
	wf.AddStep(&workflow.Step{
		ID: "boom",
		Action: func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
			select {
			case reachedStep <- struct{}{}:
			default:
			}
			return nil, errBoom
		},
	})
	env.wfe.Register(wf)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				env.call(roleOperator, http.MethodGet, "/api/v1/workflow/raced", "")
			}
		}()
	}
	defer func() {
		close(stop)
		readers.Wait()
	}()

	const wantRuns = 200
	completed := 0
	for attempt := 0; completed < wantRuns && attempt < 20*wantRuns; attempt++ {
		w := env.call(roleOperator, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"raced"}`)
		if w.Code != http.StatusAccepted {
			t.Fatalf("execute = %d, want 202: %s", w.Code, w.Body.String())
		}
		// A run still in flight refuses the next one, so a POST whose step never
		// ran is a retry rather than a failure.
		select {
		case <-reachedStep:
		case <-time.After(100 * time.Millisecond):
			continue
		}
		// That run has now written Status, StartedAt, Error and CompletedAt.
		deadline := time.Now().Add(5 * time.Second)
		for wf.GetStatus() != workflow.WorkflowFailed {
			if time.Now().After(deadline) {
				t.Fatal("a workflow that reached its step never reached a failed terminal state")
			}
		}
		completed++
	}
	if completed < wantRuns {
		t.Fatalf("only %d of %d executions completed", completed, wantRuns)
	}
}

// The projection must report a failed run's error rather than the zero value,
// which is what a locked read is for: the accessor and a direct field read
// agree on the outcome, so the assertion pins the behaviour the lock protects.
func TestWorkflowViewReportsTheRunError(t *testing.T) {
	env := newTestEnv(t)

	wf := workflow.NewWorkflow("failing", "Failing")
	wf.AddStep(&workflow.Step{
		ID: "boom",
		Action: func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
			return nil, errBoom
		},
	})
	env.wfe.Register(wf)

	w := env.call(roleOperator, http.MethodGet, "/api/v1/workflow/failing", "")
	if got := env.data(w)["error"]; got != "" {
		t.Fatalf("a workflow that has not run has no error, got %v", got)
	}

	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"failing"}`),
		http.StatusAccepted)
	deadline := time.Now().Add(5 * time.Second)
	for wf.GetStatus() != workflow.WorkflowFailed {
		if time.Now().After(deadline) {
			t.Fatal("the workflow never reached a failed terminal state")
		}
	}

	w = env.call(roleOperator, http.MethodGet, "/api/v1/workflow/failing", "")
	view := env.data(w)
	if !strings.Contains(fmt.Sprint(view["error"]), "boom") {
		t.Errorf("a failed run must report its step error, got %v", view["error"])
	}
	if view["status"] != "failed" {
		t.Errorf("status: got %v, want failed", view["status"])
	}
}

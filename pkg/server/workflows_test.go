package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loopworker/internal/config"
	"loopworker/pkg/workflow"
)

// newRegisteredServer builds a server and registers its workflows.
//
// Registration is a separate call from New on purpose: the production call site
// is in BuildComponents (pkg/server/server.go), which this wave's ownership
// matrix assigns to the integrator. Every test here goes through this helper so
// the tests stay green against the current tree while still exercising the real
// registration path and the real HTTP surface.
func newRegisteredServer(t *testing.T, cfg *config.Config) (*Server, *WorkflowRun) {
	t.Helper()
	srv := newTestServer(t, cfg)
	// A rejected definition is reported, not fatal: one bad file must not stop a
	// server whose built-in workflows are fine. Tests that do not expect a
	// rejection assert on run.Failed instead of on a nil error.
	run, _ := RegisterWorkflows(srv.cfg, srv.GetComponents())
	return srv, run
}

// TestFreshServerHasRegisteredWorkflows is the reproduction of the sellability
// gap: with nothing registered, GET /api/v1/workflow/list is always empty and
// POST /api/v1/workflow/execute always answers 404 WORKFLOW_NOT_FOUND.
func TestFreshServerHasRegisteredWorkflows(t *testing.T) {
	srv, _ := newRegisteredServer(t, testConfig(t))

	registered := srv.WorkflowEngine().ListWorkflows()
	if len(registered) == 0 {
		t.Fatal("a server with no configuration registered zero workflows: " +
			"GET /api/v1/workflow/list would be empty and execute would 404")
	}
	for _, wf := range registered {
		if len(wf.StepOrder) == 0 {
			t.Errorf("registered workflow %q has no steps", wf.ID)
		}
	}
}

// TestBuiltinWorkflowsNeedNoConfiguration pins the promise that a fresh install
// can execute something: both built-ins must be registered from an empty config
// with no plugins, no API key and no definition files.
func TestBuiltinWorkflowsNeedNoConfiguration(t *testing.T) {
	cfg := testConfig(t)
	if cfg.Security.APIKey != "" || cfg.LLM.APIKey != "" {
		t.Fatal("this test is only meaningful with no credentials configured")
	}
	if _, err := os.Stat(cfg.Plugins.Dir); err == nil {
		entries, _ := os.ReadDir(cfg.Plugins.Dir)
		if len(entries) > 0 {
			t.Fatal("this test is only meaningful with an empty plugins dir")
		}
	}
	srv, run := newRegisteredServer(t, cfg)
	if len(run.Failed) != 0 {
		t.Fatalf("a fresh install must reject nothing: %+v", run.Failed)
	}

	for _, id := range []string{AnomalyReviewID, PluginSmokeID} {
		if _, ok := srv.WorkflowEngine().GetWorkflow(id); !ok {
			t.Errorf("%s must be registered on a fresh install, got %v", id, workflowIDs(srv))
		}
	}
}

// TestWorkflowListOverHTTPIsNotEmpty is the same gap seen from the API surface
// a customer actually calls.
func TestWorkflowListOverHTTPIsNotEmpty(t *testing.T) {
	srv, _ := newRegisteredServer(t, testConfig(t))

	w := callAPI(t, srv, http.MethodGet, "/api/v1/workflow/list", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/workflow/list = %d, body %s", w.Code, w.Body.String())
	}
	for _, want := range []string{`"workflows"`, `"steps"`, AnomalyReviewID, PluginSmokeID} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("workflow list body has no %s: %s", want, w.Body.String())
		}
	}
}

// TestBuiltinAnomalyReviewExecutesWithNoConfiguration is the load-bearing test:
// a fresh install with no plugin, no API key and no definition file must be able
// to run a multi-step DAG to completion.
func TestBuiltinAnomalyReviewExecutesWithNoConfiguration(t *testing.T) {
	srv, _ := newRegisteredServer(t, testConfig(t))
	wf, ok := srv.WorkflowEngine().GetWorkflow(AnomalyReviewID)
	if !ok {
		t.Fatalf("%s is not registered", AnomalyReviewID)
	}
	// A DAG, not a chain: the join must depend on both branches, and the steps
	// must be declared out of topological order so the engine's sort has work to
	// do.
	if got := len(wf.StepOrder); got != 3 {
		t.Fatalf("want 3 steps, got %d", got)
	}
	if wf.StepOrder[0] != "verify" {
		t.Errorf("steps must be declared out of order to prove the sort runs, got %v", wf.StepOrder)
	}
	join := wf.Steps["verify"]
	if len(join.DependsOn) != 2 || join.DependsOn[0] != "steady" || join.DependsOn[1] != "spike" {
		t.Fatalf("verify must join both branches, depends_on = %v", join.DependsOn)
	}

	if err := srv.WorkflowEngine().Execute(context.Background(), AnomalyReviewID); err != nil {
		t.Fatalf("execute %s: %v", AnomalyReviewID, err)
	}
	if wf.GetStatus() != workflow.WorkflowCompleted {
		t.Fatalf("status = %v, error = %v", wf.GetStatus(), wf.GetError())
	}
	for _, id := range wf.StepOrder {
		if got := wf.GetStepStatus(id); got != workflow.StepCompleted {
			t.Errorf("step %s = %v, want completed", id, got)
		}
	}
	// The branches really ran and produced different findings, and the join
	// really ran last on the union of both. research.anomaly reports `count` as
	// the number of input values and lists the outliers separately.
	steady, _ := wf.GetState("steady")
	if !strings.Contains(fmt.Sprint(steady), `"anomalies":[]`) {
		t.Errorf("the steady branch must find no anomaly: %v", steady)
	}
	spike, _ := wf.GetState("spike")
	if !strings.Contains(fmt.Sprint(spike), `"index":4`) {
		t.Errorf("the spike branch must find the outlier at index 4: %v", spike)
	}
	verify, _ := wf.GetState("verify")
	if !strings.Contains(fmt.Sprint(verify), `"index":5`) {
		t.Errorf("the join must find the outlier at index 5 of the combined series: %v", verify)
	}
}

// TestExecuteOverHTTPReturns202ThenReachesTerminalState walks the whole path a
// customer walks: POST /execute, read the poll path out of the response, poll it.
func TestExecuteOverHTTPReturns202ThenReachesTerminalState(t *testing.T) {
	srv, _ := newRegisteredServer(t, testConfig(t))

	w := callAPI(t, srv, http.MethodPost, "/api/v1/workflow/execute",
		fmt.Sprintf(`{"workflow_id":%q}`, AnomalyReviewID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /workflow/execute = %d, want 202: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			WorkflowID string `json:"workflow_id"`
			Poll       string `json:"poll"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode 202 body: %v (%s)", err, w.Body.String())
	}
	if env.Data.WorkflowID != AnomalyReviewID {
		t.Errorf("workflow_id = %q", env.Data.WorkflowID)
	}
	if want := "/api/v1/workflow/" + AnomalyReviewID; env.Data.Poll != want {
		t.Errorf("poll path = %q, want %q", env.Data.Poll, want)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		poll := callAPI(t, srv, http.MethodGet, env.Data.Poll, "")
		if poll.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", env.Data.Poll, poll.Code, poll.Body.String())
		}
		var view struct {
			Data struct {
				Status string `json:"status"`
				Steps  []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"steps"`
			} `json:"data"`
		}
		if err := json.Unmarshal(poll.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode poll body: %v", err)
		}
		if view.Data.Status == "completed" || view.Data.Status == "failed" {
			if view.Data.Status != "completed" {
				t.Fatalf("workflow failed: %s", poll.Body.String())
			}
			if len(view.Data.Steps) != 3 {
				t.Errorf("poll must project every step, got %d: %s", len(view.Data.Steps), poll.Body.String())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workflow never reached a terminal state within 15s")
}

// TestDefinitionFileFromConfigIsRegistered covers the configuration path: a JSON
// or YAML definition file the operator dropped in the workflow directory.
func TestDefinitionFileFromConfigIsRegistered(t *testing.T) {
	cfg := testConfig(t)
	dir := writeWorkflowFile(t, cfg, "custom.yaml", `
id: custom-audit
name: Custom audit
steps:
  - id: run
    action: plugin
    input: "hello"
`)

	srv, _ := newRegisteredServer(t, cfg)
	wf, ok := srv.WorkflowEngine().GetWorkflow("custom-audit")
	if !ok {
		t.Fatalf("custom-audit was not registered; registered = %v", workflowIDs(srv))
	}
	if wf.Name != "Custom audit" || len(wf.StepOrder) != 1 {
		t.Errorf("definition not applied faithfully: %+v", wf)
	}
	if wf.Steps["run"].Action == nil {
		t.Error("the step has no action, so it would report success without running anything")
	}
	if dir == "" {
		t.Error("writeWorkflowFile returned no directory")
	}
}

// TestDefinitionFileExecutesOverHTTP proves a configured workflow is not merely
// listed but actually runnable through the API. The 202 is asynchronous, so
// driving Execute() here as well would be a second concurrent run of the same
// workflow - which the engine refuses on purpose.
func TestDefinitionFileExecutesOverHTTP(t *testing.T) {
	cfg := testConfig(t)
	writeWorkflowFile(t, cfg, "runnable.json", `{
	  "id": "runnable",
	  "name": "Runnable from a file",
	  "steps": [{"id":"only","action":"skill:research.anomaly","input":"[1,2,3]"}]
	}`)
	srv, _ := newRegisteredServer(t, cfg)

	w := callAPI(t, srv, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"runnable"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /workflow/execute = %d: %s", w.Code, w.Body.String())
	}
	wf, _ := srv.WorkflowEngine().GetWorkflow("runnable")

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if wf.IsComplete() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if wf.GetStatus() != workflow.WorkflowCompleted {
		t.Fatalf("status = %v, error = %v", wf.GetStatus(), wf.GetError())
	}
	result, _ := wf.GetState("only")
	if !strings.Contains(fmt.Sprint(result), `"count":3`) {
		t.Errorf("the file-driven step produced no analysis: %v", result)
	}
}

func TestDefinitionFileCanOverrideABuiltin(t *testing.T) {
	cfg := testConfig(t)
	writeWorkflowFile(t, cfg, "override.json", `{
	  "id": "`+AnomalyReviewID+`",
	  "name": "Retuned anomaly review",
	  "steps": [{"id":"only","action":"skill:research.anomaly","input":"[1,2,3]"}]
	}`)
	srv, _ := newRegisteredServer(t, cfg)

	wf, ok := srv.WorkflowEngine().GetWorkflow(AnomalyReviewID)
	if !ok {
		t.Fatal("override removed the workflow instead of replacing it")
	}
	if wf.Name != "Retuned anomaly review" || len(wf.StepOrder) != 1 {
		t.Errorf("the file must win over the built-in, got %+v", wf)
	}
}

// TestRejectedDefinitionDoesNotTakeDownTheServer: one bad file is an operator
// problem with one file, exactly like one bad plugin directory.
func TestRejectedDefinitionDoesNotTakeDownTheServer(t *testing.T) {
	cfg := testConfig(t)
	writeWorkflowFile(t, cfg, "broken.yaml",
		"id: broken\nsteps:\n  - {id: a, action: plugin, depends_on: [ghost]}")
	srv, run := newRegisteredServer(t, cfg)

	assertRejected(t, srv, run, "broken.yaml", "broken", "not a step of this workflow")
	if _, ok := srv.WorkflowEngine().GetWorkflow(AnomalyReviewID); !ok {
		t.Errorf("built-ins must survive a rejected file, registered = %v", workflowIDs(srv))
	}
}

// TestUnknownActionIsRejectedAtRegistration: a step whose action nobody
// implements is a ghost success waiting to happen.
func TestUnknownActionIsRejectedAtRegistration(t *testing.T) {
	cfg := testConfig(t)
	writeWorkflowFile(t, cfg, "unknown.yaml",
		"id: unknown-action\nsteps:\n  - {id: a, action: telepathy}")
	srv, run := newRegisteredServer(t, cfg)

	assertRejected(t, srv, run, "unknown.yaml", "unknown-action", `unknown action "telepathy"`)
}

// TestUnknownSkillIsRejectedAtRegistration names the skills that do exist, so the
// operator does not have to read the source to find the typo.
func TestUnknownSkillIsRejectedAtRegistration(t *testing.T) {
	cfg := testConfig(t)
	writeWorkflowFile(t, cfg, "skill.yaml",
		"id: bad-skill\nsteps:\n  - {id: a, action: skill:research.anomly, input: '[1,2]'}")
	srv, run := newRegisteredServer(t, cfg)

	assertRejected(t, srv, run, "skill.yaml", "bad-skill",
		`skill "research.anomly" is not registered`)
}

// assertRejected pins the contract for a rejected definition: it is not
// registered, it did not take the server down, and the operator is told which
// file and why.
func assertRejected(t *testing.T, srv *Server, run *WorkflowRun, file, id, reason string) {
	t.Helper()
	if _, ok := srv.WorkflowEngine().GetWorkflow(id); ok {
		t.Errorf("workflow %q must not be registered when its definition was rejected", id)
	}
	if len(run.Failed) != 1 {
		t.Fatalf("want exactly 1 rejected definition, got %d: %+v", len(run.Failed), run.Failed)
	}
	if !strings.Contains(run.Failed[0].Source, file) {
		t.Errorf("rejection must name the file %s, got %q", file, run.Failed[0].Source)
	}
	if !strings.Contains(run.Failed[0].Reason, reason) {
		t.Errorf("rejection reason %q does not contain %q", run.Failed[0].Reason, reason)
	}
	if !strings.Contains(run.Summary(), "rejected") {
		t.Errorf("summary must report the rejection: %q", run.Summary())
	}
}

func TestWorkflowRunSummaryNamesTheWorkflows(t *testing.T) {
	cfg := testConfig(t)
	srv := newTestServer(t, cfg)
	run, err := RegisterWorkflows(cfg, srv.GetComponents())
	if err != nil {
		t.Fatalf("RegisterWorkflows: %v", err)
	}
	summary := run.Summary()
	// Operator-facing text: it must name both built-ins and the directory an
	// operator adds files to.
	for _, want := range []string{AnomalyReviewID, PluginSmokeID, "workflows"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q is missing %q", summary, want)
		}
	}
	if run.Dir != filepath.Join(cfg.WorkDir, "workflows") {
		t.Errorf("definition dir = %q", run.Dir)
	}
}

// fakeDispatcher records what a workflow step asked the scheduler to do. When it
// can queue, the task completes; otherwise the task stays in "pending", which is
// exactly the state a created-but-never-queued task is left in.
type fakeDispatcher struct {
	mu       sync.Mutex
	canQueue bool
	created  []string
	queued   []string
}

func (f *fakeDispatcher) CreateTask(_ context.Context, taskType string, _ map[string]interface{}, _ []byte) (*workflow.TaskRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, taskType)
	return &workflow.TaskRef{ID: "task-1", Type: taskType, State: "pending"}, nil
}

func (f *fakeDispatcher) QueueTask(_ context.Context, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, taskID)
	return nil
}

func (f *fakeDispatcher) WaitForTask(_ context.Context, taskID string) (*workflow.TaskRef, error) {
	return &workflow.TaskRef{ID: taskID, State: "completed", Result: []byte("hello from wasm: " + taskID)}, nil
}

func (f *fakeDispatcher) GetTask(taskID string) (*workflow.TaskRef, bool) {
	return &workflow.TaskRef{ID: taskID, State: "completed"}, true
}

func TestPluginStepQueuesTheTaskItCreates(t *testing.T) {
	// A created task is not a runnable task. The scheduler's REST path pairs
	// CreateTask with QueueTask (pkg/api/handlers_task_mutate.go); a workflow step
	// that skipped the second half would block in WaitForTask until its context
	// expired, looking like it was still working.
	srv, _ := newRegisteredServer(t, testConfig(t))
	c := srv.GetComponents()
	d := &fakeDispatcher{canQueue: true}
	c.WorkflowEngine.SetDispatcher(d)

	action := pluginStep(c, workflow.StepDefinition{ID: "run", Input: "payload"})
	out, err := action(context.Background(), nil)
	if err != nil {
		t.Fatalf("plugin step: %v", err)
	}
	if len(d.created) != 1 || d.created[0] != workflowTaskType {
		t.Errorf("created = %v, want one %q task", d.created, workflowTaskType)
	}
	if len(d.queued) != 1 || d.queued[0] != "task-1" {
		t.Errorf("queued = %v, want the created task to be queued", d.queued)
	}
	if got := fmt.Sprint(out["run"]); !strings.Contains(got, "hello from wasm") {
		t.Errorf("step output must carry the task result, got %v", out)
	}
}

// TestPluginStepRefusesADispatcherThatCannotQueue is the fail-fast half: rather
// than hanging for the step timeout, the operator is told exactly which
// capability the dispatcher is missing.
func TestPluginStepRefusesADispatcherThatCannotQueue(t *testing.T) {
	srv, _ := newRegisteredServer(t, testConfig(t))
	c := srv.GetComponents()
	c.WorkflowEngine.SetDispatcher(noQueueDispatcher{})

	action := pluginStep(c, workflow.StepDefinition{ID: "run", Input: "payload"})
	done := make(chan error, 1)
	go func() {
		_, err := action(context.Background(), nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a dispatcher that cannot queue must not report success")
		}
		for _, want := range []string{"stays in \"pending\"", "QueueTask", "SchedulerBridge"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the step must fail fast, not block until its timeout")
	}
}

// noQueueDispatcher can create and inspect tasks but not run them.
type noQueueDispatcher struct{}

func (noQueueDispatcher) CreateTask(context.Context, string, map[string]interface{}, []byte) (*workflow.TaskRef, error) {
	return &workflow.TaskRef{ID: "task-1", State: "pending"}, nil
}
func (noQueueDispatcher) WaitForTask(context.Context, string) (*workflow.TaskRef, error) {
	return &workflow.TaskRef{ID: "task-1", State: "completed"}, nil
}
func (noQueueDispatcher) GetTask(string) (*workflow.TaskRef, bool) {
	return &workflow.TaskRef{State: "completed"}, true
}

func callAPI(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	key, _ := srv.components.APIServer.Authenticator().BootstrapKey()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	return w
}

func workflowIDs(srv *Server) []string {
	var ids []string
	for _, wf := range srv.WorkflowEngine().ListWorkflows() {
		ids = append(ids, wf.ID)
	}
	return ids
}

func writeWorkflowFile(t *testing.T, cfg *config.Config, name, body string) string {
	t.Helper()
	dir := filepath.Join(cfg.WorkDir, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

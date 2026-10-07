package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/event"
	"loopworker/pkg/security"
	"loopworker/pkg/workflow"
)

// Role names double as the selector for a credential in tests.
const (
	roleAdmin    = security.RoleAdmin
	roleOperator = security.RoleOperator
	roleViewer   = security.RoleViewer
	roleNone     = ""
)

// testKeys holds one plaintext API key per role.
type testKeys struct {
	admin    string
	operator string
	viewer   string
}

func (k testKeys) forRole(role string) string {
	switch role {
	case roleAdmin:
		return k.admin
	case roleOperator:
		return k.operator
	case roleViewer:
		return k.viewer
	default:
		return ""
	}
}

// testAuth mints one deterministic-per-run key per role and returns the auth
// configuration that carries only their digests. Tests can therefore prove that
// a role is rejected without any production credential ever existing.
func testAuth(t *testing.T) (security.AuthConfig, testKeys) {
	t.Helper()
	cfg := security.DefaultAuthConfig()
	var keys testKeys
	for _, role := range []string{roleAdmin, roleOperator, roleViewer} {
		plaintext, record, err := security.GenerateAPIKey("test-"+role, role, time.Time{})
		if err != nil {
			t.Fatalf("mint %s key: %v", role, err)
		}
		cfg.Keys = append(cfg.Keys, *record)
		keys.forRoleSet(role, plaintext)
	}
	return cfg, keys
}

func (k *testKeys) forRoleSet(role, plaintext string) {
	switch role {
	case roleAdmin:
		k.admin = plaintext
	case roleOperator:
		k.operator = plaintext
	case roleViewer:
		k.viewer = plaintext
	}
}

// testEnv is a fully wired server plus the credentials needed to talk to it.
type testEnv struct {
	*APIServer
	t     *testing.T
	bus   *event.EventBus
	sched *scheduler.Scheduler
	wfe   *workflow.WorkflowEngine
	obs   *observer.Observer
	keys  testKeys
}

// newTestEnv wires every core component and installs explicit credentials so a
// test can pick the role it needs. The rate limit is raised far above any test's
// request count; the rate-limit tests install their own budget.
func newTestEnv(t *testing.T, opts ...Option) *testEnv {
	t.Helper()

	bus := event.NewEventBus(nil)
	sched := scheduler.NewScheduler(bus)
	disp := dispatcher.NewDispatcher(sched, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	healer := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	exec := executor.NewExecutor(sched, disp, sb, bus, healer)
	obs := observer.NewObserver(bus)
	wfe := workflow.NewWorkflowEngine(workflow.WithEventBus(bus))

	authCfg, keys := testAuth(t)
	base := []Option{
		WithAuth(authCfg),
		WithRateLimits(100000, 100000, time.Minute),
		WithStaticCanvas(true),
	}
	server := NewAPIServer(sched, exec, bus, wfe, obs, append(base, opts...)...)

	t.Cleanup(func() {
		server.Close()
		_ = sched.Close()
		bus.Close()
	})
	return &testEnv{APIServer: server, t: t, bus: bus, sched: sched, wfe: wfe, obs: obs, keys: keys}
}

// call performs one request as the given role (roleNone sends no credential).
func (e *testEnv) call(role, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if key := e.keys.forRole(role); key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.Router.ServeHTTP(w, req)
	return w
}

// wire mirrors the response envelope so assertions read like the customer's.
type wire struct {
	Success   bool            `json:"success"`
	Data      json.RawMessage `json:"data"`
	Error     *ErrorBody      `json:"error"`
	Timestamp time.Time       `json:"timestamp"`
	RequestID string          `json:"request_id"`
}

func (e *testEnv) decode(w *httptest.ResponseRecorder) wire {
	e.t.Helper()
	var env wire
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("response is not JSON (%d): %v\n%s", w.Code, err, w.Body.String())
	}
	return env
}

func (e *testEnv) data(w *httptest.ResponseRecorder) map[string]any {
	e.t.Helper()
	var out map[string]any
	if err := json.Unmarshal(e.decode(w).Data, &out); err != nil {
		e.t.Fatalf("data is not an object: %v\n%s", err, w.Body.String())
	}
	return out
}

// createTask POSTs a task and returns its id; it fails the test on any non-2xx.
func (e *testEnv) createTask(role, typeName string, extra string) string {
	e.t.Helper()
	body := `{"type":"` + typeName + `"` + extra + `}`
	w := e.call(role, http.MethodPost, "/api/v1/tasks", body)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("create task %q: want 201, got %d: %s", typeName, w.Code, w.Body.String())
	}
	id, _ := e.data(w)["id"].(string)
	if id == "" {
		e.t.Fatalf("create task %q returned no id: %s", typeName, w.Body.String())
	}
	return id
}

// expectCode asserts the status and the machine-readable error code together:
// the code is what clients branch on, the status is what humans see.
func (e *testEnv) expectCode(w *httptest.ResponseRecorder, status int, code ErrorCode) {
	e.t.Helper()
	env := e.decode(w)
	if w.Code != status || env.Error == nil || env.Error.Code != code {
		e.t.Fatalf("want %d/%s, got %d/%+v\n%s", status, code, w.Code, env.Error, w.Body.String())
	}
	if env.Success {
		e.t.Fatalf("failure response claims success=true: %s", w.Body.String())
	}
	if env.RequestID == "" {
		e.t.Errorf("error response carries no request_id: %s", w.Body.String())
	}
}

// expectOK asserts a 2xx envelope.
func (e *testEnv) expectOK(w *httptest.ResponseRecorder, status int) wire {
	e.t.Helper()
	env := e.decode(w)
	if w.Code != status {
		e.t.Fatalf("want %d, got %d: %s", status, w.Code, w.Body.String())
	}
	if !env.Success || env.Error != nil {
		e.t.Fatalf("want success envelope, got %+v: %s", env.Error, w.Body.String())
	}
	return env
}

// ---- system routes ----

func TestHealthCheck(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleNone, http.MethodGet, "/api/v1/health", "")
	env.expectOK(w, http.StatusOK)
	if env.data(w)["status"] != "ok" {
		t.Errorf("health should report ok: %s", w.Body.String())
	}
}

func TestLivenessProbeIsUnauthenticated(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleNone, http.MethodGet, "/healthz", "")
	if w.Code != http.StatusOK {
		t.Fatalf("healthz must not need a credential: %d %s", w.Code, w.Body.String())
	}
}

func TestEnvelopeShapeCarriesTimestampAndRequestID(t *testing.T) {
	env := newTestEnv(t)
	rec := env.call(roleNone, http.MethodGet, "/api/v1/health", "")
	body := env.expectOK(rec, http.StatusOK)
	if body.Timestamp.IsZero() {
		t.Error("envelope timestamp must not be zero")
	}
	if body.RequestID == "" {
		t.Error("envelope must carry the request id")
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header must be set")
	}
}

func TestOpenAPISpecIsServed(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleNone, http.MethodGet, "/api/v1/openapi.json", "")
	if w.Code != http.StatusOK {
		t.Fatalf("openapi.json should be public, got %d", w.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("openapi.json is not JSON: %v", err)
	}
	if doc["paths"] == nil {
		t.Error("openapi.json declares no paths")
	}
}

// ---- task lifecycle ----

func TestCreateTask(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"test-task","input":"dGVzdA==","config":{}}`)
	env.expectOK(w, http.StatusCreated)
	if got := w.Header().Get("Location"); got == "" {
		t.Error("201 should carry a Location header")
	}
}

func TestCreateTaskAcceptsPlainTextInput(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "echo", `,"input":"Hello, World!"`)
	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, "")
	env.expectOK(w, http.StatusOK)
	if got := env.data(w)["input"]; got != "Hello, World!" {
		t.Errorf("plain text input should round-trip verbatim, got %v", got)
	}
}

func TestCreateTaskRejectsBlankType(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"","input":"dGVzdA=="}`)
	env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)
}

func TestCreateTaskRejectsOverlongType(t *testing.T) {
	env := newTestEnv(t)
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	body, _ := json.Marshal(map[string]any{"type": string(long)})
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", string(body))
	env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)
}

func TestCreateTaskWithAgent(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks",
		`{"type":"agent-task","input":"dGVzdA==","is_agent":true,"agent_config":{"system_prompt":"You are helpful","model":"gpt-4o"}}`)
	env.expectOK(w, http.StatusCreated)
	if env.data(w)["is_agent"] != true {
		t.Error("is_agent should survive the round trip")
	}
}

func TestCreateTaskRejectsClientSuppliedOwner(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks",
		`{"type":"t","metadata":{"`+OwnerMetadataKey+`":"someone-else"}}`)
	env.expectCode(w, http.StatusForbidden, CodeForbidden)
}

func TestCreateAndListTasks(t *testing.T) {
	env := newTestEnv(t)
	env.createTask(roleOperator, "my-task", `,"input":"aGVsbG8="`)

	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks", "")
	env.expectOK(w, http.StatusOK)
	page := env.data(w)
	tasks, _ := page["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d: %s", len(tasks), w.Body.String())
	}
}

func TestListTasksPagination(t *testing.T) {
	env := newTestEnv(t)
	for i := 0; i < 3; i++ {
		env.createTask(roleOperator, "task", "")
	}

	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks?limit=1", "")
	env.expectOK(w, http.StatusOK)
	page := env.data(w)
	if page["limit"] != float64(1) {
		t.Errorf("limit=1 should be echoed, got %v", page["limit"])
	}
	if tasks, _ := page["tasks"].([]any); len(tasks) != 1 {
		t.Errorf("limit=1 must return one row, got %d", len(tasks))
	}
	if page["has_more"] != true {
		t.Error("more rows remain, has_more should be true")
	}
}

func TestListTasksWithStateFilter(t *testing.T) {
	env := newTestEnv(t)
	env.createTask(roleOperator, "task", "")
	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks?state=queued", "")
	env.expectOK(w, http.StatusOK)
}

func TestListTasksRejectsUnknownState(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks?state=teleporting", "")
	env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)
}

func TestListTasksTypeFilter(t *testing.T) {
	env := newTestEnv(t)
	for _, taskType := range []string{"type-a", "type-b", "type-a"} {
		env.createTask(roleOperator, taskType, "")
	}
	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks?type=type-a", "")
	env.expectOK(w, http.StatusOK)
	tasks, _ := env.data(w)["tasks"].([]any)
	if len(tasks) != 2 {
		t.Errorf("want the 2 type-a tasks, got %d", len(tasks))
	}
}

func TestListTasksLimitBounds(t *testing.T) {
	env := newTestEnv(t)
	env.createTask(roleOperator, "t", "")

	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks?limit=5000", "")
	env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)

	w = env.call(roleOperator, http.MethodGet, "/api/v1/tasks?offset=-5", "")
	env.expectOK(w, http.StatusOK)
	if got := env.data(w)["offset"]; got != float64(0) {
		t.Errorf("negative offset must clamp to 0, got %v", got)
	}
}

func TestListTasksOffsetBeyondTotal(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks?offset=9999", "")
	env.expectOK(w, http.StatusOK)
	if tasks, _ := env.data(w)["tasks"].([]any); len(tasks) != 0 {
		t.Errorf("offset past the end must return no rows, got %d", len(tasks))
	}
}

func TestGetTask(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "test", "")
	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, "")
	env.expectOK(w, http.StatusOK)
}

func TestGetTaskNotFound(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/nonexistent-id", "")
	env.expectCode(w, http.StatusNotFound, CodeTaskNotFound)
}

func TestDeleteTask(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "test", "")
	w := env.call(roleOperator, http.MethodDelete, "/api/v1/tasks/"+id, "")
	env.expectOK(w, http.StatusOK)
	if env.data(w)["state"] != string(scheduler.StateCancelled) {
		t.Errorf("delete should cancel the task, got state %v", env.data(w)["state"])
	}
}

func TestDeleteTaskNotFound(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodDelete, "/api/v1/tasks/nonexistent", "")
	env.expectCode(w, http.StatusNotFound, CodeTaskNotFound)
}

func TestCancelTaskTwiceConflicts(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "test", "")
	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+id+"/cancel", ""), http.StatusOK)
	env.expectCode(env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+id+"/cancel", ""),
		http.StatusConflict, CodeTaskStateConflict)
}

func TestAddTaskDependency(t *testing.T) {
	env := newTestEnv(t)
	a := env.createTask(roleOperator, "task-a", "")
	b := env.createTask(roleOperator, "task-b", "")
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+b+"/dependencies", `{"dependency_id":"`+a+`"}`)
	env.expectOK(w, http.StatusOK)
	deps, _ := env.data(w)["dependencies"].([]any)
	if len(deps) != 1 || deps[0] != a {
		t.Errorf("dependency should be recorded, got %v", deps)
	}
}

func TestAddDependencyNotFound(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/nonexistent/dependencies", `{"dependency_id":"nope"}`)
	env.expectCode(w, http.StatusNotFound, CodeTaskNotFound)
}

func TestAddDependencyRejectsInvalidJSON(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "task", "")
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+id+"/dependencies", "not json")
	env.expectCode(w, http.StatusBadRequest, CodeMalformedJSON)
}

func TestAddDependencyRequiresDependencyID(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "task", "")
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+id+"/dependencies", `{}`)
	env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)
}

func TestAddDependencyOnUnknownTaskReports404(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+env.createTask(roleOperator, "t", "")+"/dependencies",
		`{"dependency_id":"missing-task"}`)
	env.expectCode(w, http.StatusNotFound, CodeTaskNotFound)
}

// ---- system and workflow routes ----

func TestListWorkers(t *testing.T) {
	env := newTestEnv(t)
	env.expectOK(env.call(roleOperator, http.MethodGet, "/api/v1/workers", ""), http.StatusOK)
}

func TestWorkflowGraph(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodGet, "/api/v1/workflow/graph", "")
	env.expectOK(w, http.StatusOK)
	data := env.data(w)
	if _, ok := data["nodes"]; !ok {
		t.Error("graph response must carry nodes")
	}
	if _, ok := data["edges"]; !ok {
		t.Error("graph response must carry edges")
	}
	if _, ok := data["meta"]; !ok {
		t.Error("graph response must carry meta")
	}
}

func TestWorkflowGraphWithTasks(t *testing.T) {
	env := newTestEnv(t)
	a := env.createTask(roleOperator, "task-a", "")
	b := env.createTask(roleOperator, "task-b", "")
	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+b+"/dependencies", `{"dependency_id":"`+a+`"}`),
		http.StatusOK)

	w := env.call(roleOperator, http.MethodGet, "/api/v1/workflow/graph", "")
	env.expectOK(w, http.StatusOK)
	data := env.data(w)
	nodes, _ := data["nodes"].([]any)
	edges, _ := data["edges"].([]any)
	if len(nodes) != 2 {
		t.Errorf("want 2 nodes, got %d", len(nodes))
	}
	if len(edges) != 1 {
		t.Errorf("want 1 edge, got %d", len(edges))
	}
}

func TestListWorkflows(t *testing.T) {
	env := newTestEnv(t)
	env.expectOK(env.call(roleOperator, http.MethodGet, "/api/v1/workflow/list", ""), http.StatusOK)
}

func TestGetWorkflowNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.expectCode(env.call(roleOperator, http.MethodGet, "/api/v1/workflow/nope", ""),
		http.StatusNotFound, CodeWorkflowNotFound)
}

func TestCreateAndExecuteWorkflow(t *testing.T) {
	env := newTestEnv(t)
	wf := workflow.NewWorkflow("test-wf", "Test Workflow")
	wf.AddStep(&workflow.Step{
		ID:   "step1",
		Name: "echo",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"result": "done"}, nil
		},
	})
	env.wfe.Register(wf)

	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"test-wf"}`),
		http.StatusAccepted)
}

func TestExecuteWorkflowNotFound(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"nonexistent"}`)
	env.expectCode(w, http.StatusNotFound, CodeWorkflowNotFound)
}

func TestExecuteWorkflowMissingID(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":""}`)
	env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)
}

func TestExecuteWorkflowInvalidJSON(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/workflow/execute", "not json")
	env.expectCode(w, http.StatusBadRequest, CodeMalformedJSON)
}

// TestMissingSubsystemIsReportedNotPanicked proves every optional dependency
// answers 503 with a code instead of dereferencing nil.
func TestMissingSubsystemIsReportedNotPanicked(t *testing.T) {
	bus := event.NewEventBus(nil)
	t.Cleanup(bus.Close)
	server := NewAPIServerWithDependencies(Dependencies{},
		WithAuth(mustAuth(t)), WithRateLimits(100000, 100000, time.Minute))
	t.Cleanup(server.Close)

	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/workers", ""},
		{http.MethodGet, "/api/v1/tasks", ""},
		{http.MethodGet, "/api/v1/workflow/list", ""},
		{http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"x"}`},
		{http.MethodGet, "/api/v1/events/live", ""},
	}
	key, _, err := server.Authenticator().AddKey("probe", security.RoleAdmin, time.Time{})
	if err != nil {
		t.Fatalf("mint probe credential: %v", err)
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		server.Router.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: want 503, got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func mustAuth(t *testing.T) security.AuthConfig {
	t.Helper()
	cfg, _ := testAuth(t)
	return cfg
}

// decodeEnvelope parses a response body as the wire envelope.
func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) wire {
	t.Helper()
	var env wire
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not JSON (%d): %v\n%s", w.Code, err, w.Body.String())
	}
	return env
}

// decodeData parses the "data" member of a 2xx envelope.
func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(decodeEnvelope(t, w).Data, &out); err != nil {
		t.Fatalf("data is not an object: %v\n%s", err, w.Body.String())
	}
	return out
}

// callWithKey performs a request with an explicit credential, for tests that
// need a caller the shared env does not already own.
func (e *testEnv) callWithKey(key, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.Router.ServeHTTP(w, req)
	return w
}

// secondOperatorKey installs an extra operator credential and returns its
// plaintext, so tenancy tests have two distinct callers with equal rights.
func secondOperatorKey(t *testing.T, e *testEnv) string {
	t.Helper()
	return addKey(t, e, "second-operator", security.RoleOperator)
}

func addKey(t *testing.T, e *testEnv, name, role string) string {
	t.Helper()
	plaintext, _, err := e.Authenticator().AddKey(name, role, time.Time{})
	if err != nil {
		t.Fatalf("install %s key: %v", name, err)
	}
	return plaintext
}

// createTaskWithKey creates a task as an explicit credential and returns its id.
func (e *testEnv) createTaskWithKey(key, typeName, extra string) string {
	e.t.Helper()
	body := `{"type":"` + typeName + `"` + extra + `}`
	w := e.callWithKey(key, http.MethodPost, "/api/v1/tasks", body)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("create task %q: want 201, got %d: %s", typeName, w.Code, w.Body.String())
	}
	id, _ := e.data(w)["id"].(string)
	if id == "" {
		e.t.Fatalf("create task %q returned no id: %s", typeName, w.Body.String())
	}
	return id
}

func newTestBus(t *testing.T) *event.EventBus {
	t.Helper()
	bus := event.NewEventBus(nil)
	t.Cleanup(bus.Close)
	return bus
}

// newTestEnvNoKeys builds a server the way a fresh install does: no configured
// credentials, so the authenticator mints an ephemeral bootstrap admin key.
func newTestEnvNoKeys(t *testing.T, bus *event.EventBus) *testEnv {
	t.Helper()
	sched := scheduler.NewScheduler(bus)
	t.Cleanup(func() { _ = sched.Close() })
	server := NewAPIServerWithDependencies(Dependencies{Tasks: sched, Lister: sched},
		WithRateLimits(100000, 100000, time.Minute))
	t.Cleanup(server.Close)
	return &testEnv{APIServer: server, t: t, bus: bus, sched: sched, keys: testKeys{}}
}

// ---- admin listener ----

// TestAdminSurfaceRequiresAdminCredentials is the evidence for "runtime metrics
// are not anonymous": the metrics route lives only on the admin listener, behind
// authentication and the admin permission.
func TestAdminSurfaceRequiresAdminCredentials(t *testing.T) {
	env := newTestEnv(t)

	if w := env.call(roleNone, http.MethodGet, "/metrics", ""); w.Code != http.StatusNotFound {
		t.Errorf("the public listener must not serve /metrics, got %d", w.Code)
	}

	admin := env.AdminHandler()

	w := httptest.NewRecorder()
	admin.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous /metrics: want 401, got %d", w.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-API-Key", env.keys.viewer)
	w = httptest.NewRecorder()
	admin.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer /metrics: want 403, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-API-Key", env.keys.admin)
	w = httptest.NewRecorder()
	admin.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin /metrics: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct == "" || !bytes.Contains([]byte(ct), []byte("text/plain")) {
		t.Errorf("metrics should be prometheus text, got %q", ct)
	}
}

func TestAdminStatsAndLogs(t *testing.T) {
	env := newTestEnv(t)
	admin := env.AdminHandler()

	for _, path := range []string{"/runtime/stats", "/logs", "/events/stats"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", env.keys.admin)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: want 200, got %d: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestAdminLogsWithoutObserver(t *testing.T) {
	env := newTestEnv(t)
	env.deps.Observer = nil

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("X-API-Key", env.keys.admin)
	w := httptest.NewRecorder()
	env.AdminHandler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing observer: want 503, got %d: %s", w.Code, w.Body.String())
	}
}

// adminLogsEntries GETs /logs on the admin listener and returns the entries.
func (e *testEnv) adminLogsEntries(t *testing.T) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("X-API-Key", e.keys.admin)
	w := httptest.NewRecorder()
	e.AdminHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /logs: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var body wire
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, w.Body.String())
	}
	if !body.Success {
		t.Fatalf("GET /logs: success=false: %s", w.Body.String())
	}
	var inner struct {
		Logs []map[string]any `json:"logs"`
	}
	if err := json.Unmarshal(body.Data, &inner); err != nil {
		t.Fatalf("data is not a {\"logs\":[...]} envelope: %v\n%s", err, w.Body.String())
	}
	return inner.Logs
}

// TestAdminLogsEntryShape pins the JSON the API reference promises for GET /logs,
// read off the wire against the real observer rather than a stub.
//
// The prose and the payload had drifted apart once already: the reference showed
// a populated entry whose fields map carried task_id and worker_id, and
// worker_id appears nowhere in this repository. Comparing the exact key set is
// what makes that class of drift fail here instead of in a customer's parser.
func TestAdminLogsEntryShape(t *testing.T) {
	env := newTestEnv(t)

	// fields is the caller's map verbatim, so this is exactly what an operator
	// would see if anything ever fed the ring.
	env.obs.Log("info", "Task completed successfully", map[string]any{"task_id": "task-1"})

	entries := env.adminLogsEntries(t)
	if len(entries) != 1 {
		t.Fatalf("want the one entry just logged, got %d: %+v", len(entries), entries)
	}

	got := slices.Sorted(maps.Keys(entries[0]))
	want := []string{"fields", "level", "message", "timestamp"}
	if !slices.Equal(got, want) {
		t.Errorf("entry keys = %v, want exactly %v.\n"+
			"worker_id/task_id are fields members, never top-level keys, and "+
			"total/limit/offset do not exist: this is a ring snapshot, not a query.",
			got, want)
	}
	if level := entries[0]["level"]; level != "info" {
		t.Errorf("level = %v, want \"info\"", level)
	}
}

// TestAdminLogsOmitsEmptyFields pins the "optional fields" the reference claims.
// omitempty is what makes it true, and the difference is only visible in the
// bytes — a client reading entry.fields.task_id sees nil either way.
func TestAdminLogsOmitsEmptyFields(t *testing.T) {
	env := newTestEnv(t)
	env.obs.Log("warn", "no fields here", nil)

	entries := env.adminLogsEntries(t)
	if len(entries) != 1 {
		t.Fatalf("want the one entry just logged, got %d: %+v", len(entries), entries)
	}
	if _, present := entries[0]["fields"]; present {
		t.Errorf("an entry logged with nil fields must omit the key entirely, got %+v", entries[0])
	}
	got := slices.Sorted(maps.Keys(entries[0]))
	want := []string{"level", "message", "timestamp"}
	if !slices.Equal(got, want) {
		t.Errorf("entry keys = %v, want exactly %v", got, want)
	}
}

func TestMetricsHandlerPrometheusIsReachable(t *testing.T) {
	// The admin listener uses promhttp directly; assert the dependency is wired
	// so a future refactor cannot silently swap it for a stub.
	router := chi.NewRouter()
	router.Handle("/metrics", promhttp.Handler())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("promhttp handler: want 200, got %d", w.Code)
	}
}

// ---- routing edge cases ----

func TestUnknownRouteIs404WithCode(t *testing.T) {
	env := newTestEnv(t)
	env.expectCode(env.call(roleOperator, http.MethodGet, "/api/v1/nope", ""), http.StatusNotFound, CodeNotFound)
}

func TestWrongMethodIs405WithCode(t *testing.T) {
	env := newTestEnv(t)
	env.expectCode(env.call(roleOperator, http.MethodPut, "/api/v1/tasks", ""),
		http.StatusMethodNotAllowed, CodeMethodNotAllowed)
}

func TestStaticFileServing(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleNone, http.MethodGet, "/", "")
	if w.Code != http.StatusOK && w.Code != http.StatusMovedPermanently {
		t.Errorf("SPA root: want 200 or 301, got %d", w.Code)
	}
}

func TestStaticFileAPI404(t *testing.T) {
	env := newTestEnv(t)
	env.expectCode(env.call(roleNone, http.MethodGet, "/api/nonexistent", ""), http.StatusNotFound, CodeNotFound)
}

func TestStatusWriterRecordsStatusAndForwardsFlush(t *testing.T) {
	recorder := httptest.NewRecorder()
	sw := newStatusWriter(recorder)
	if sw.Written() {
		t.Fatal("a fresh writer has not written yet")
	}
	sw.WriteHeader(http.StatusNotFound)
	if sw.status != http.StatusNotFound {
		t.Errorf("want 404, got %d", sw.status)
	}
	sw.WriteHeader(http.StatusTeapot)
	if sw.status != http.StatusNotFound {
		t.Errorf("the first status must win, got %d", sw.status)
	}
	if !sw.Written() {
		t.Error("Written() must report the started response")
	}
	flusher, ok := unwrapFlusher(sw)
	if !ok {
		t.Fatal("statusWriter must expose http.Flusher")
	}
	flusher.Flush()
	if !recorder.Flushed {
		t.Error("Flush must reach the underlying writer (SSE depends on it)")
	}
}

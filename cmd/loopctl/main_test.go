package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"loopworker/version"
)

// TestStatusHitsRealEndpoint pins the --server flag to the client. It used to be
// declared but never bound, so serverURL was always "" and the only way to
// reach a non-default server was the LOOPWORKER_URL environment variable.
func TestStatusHitsRealEndpoint(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		// The real /api/v1/health answers an envelope, not a bare object.
		_, _ = w.Write([]byte(`{"success":true,"data":{"status":"ok","version":"0.1.0-beta"},"request_id":"req-test"}`))
	}))
	defer srv.Close()

	rootCmd.SetArgs([]string{"--server", srv.URL, "status"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("loopctl status against a live server failed: %v", err)
	}
	if want := "/api/v1/health"; got != want {
		t.Fatalf("status requested %q, want %q", got, want)
	}
}

// TestUnreachableServerFails guards against the ghost-success shape this CLI
// used to have: `config show` printed a hardcoded port and directory list when
// the endpoint 404'd, and exited 0. A dead server must be an error.
func TestUnreachableServerFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening on url any more

	rootCmd.SetArgs([]string{"--server", url, "status"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("status against a closed server returned no error")
	}
}

// --- fixtures -------------------------------------------------------------
//
// Every pkg/api route answers an envelope (pkg/api/respond.go writeJSON):
// {"success":true,"data":{...},"request_id":"..."} on 2xx and
// {"success":false,"error":{...},"request_id":"..."} otherwise. Fixtures must
// encode that shape: pkg/client used to be tested against bare JSON, so its
// green suite certified a contract the server never had.

func success(t *testing.T, w http.ResponseWriter, status int, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true, "data": data, "request_id": "req-test",
	}); err != nil {
		t.Errorf("encode success envelope: %v", err)
	}
}

func failure(t *testing.T, w http.ResponseWriter, status int, code, message string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success":    false,
		"error":      map[string]any{"code": code, "message": message, "request_id": "req-test"},
		"request_id": "req-test",
	}); err != nil {
		t.Errorf("encode failure envelope: %v", err)
	}
}

// --- harness --------------------------------------------------------------

// runCLI executes loopctl the way main() does and returns what it printed.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetFlags()
	rootCmd.SetArgs(args)
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	return out, err
}

// resetFlags clears flag state between runs. The command tree is a
// package-level singleton, so a --state set by one test would otherwise follow
// the next test and make it assert the wrong thing.
func resetFlags() {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

// captureStdout collects what the RunE closures print. Output is drained on a
// goroutine so a unexpectedly verbose command cannot fill the pipe buffer and
// deadlock the test binary.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	drained := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		drained <- string(out)
	}()

	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	fn()
	os.Stdout = saved
	_ = w.Close()
	out := <-drained
	_ = r.Close()
	return out
}

// printedObjects parses a printed JSON array, asserting the CLI shows the
// payload rather than the envelope that wrapped it.
func printedObjects(t *testing.T, out string) []map[string]any {
	t.Helper()
	var objects []map[string]any
	if err := json.Unmarshal([]byte(out), &objects); err != nil {
		t.Fatalf("output is not a JSON array (%v):\n%s", err, out)
	}
	return objects
}

func printedObject(t *testing.T, out string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not a JSON object (%v):\n%s", err, out)
	}
	return object
}

// --- task list ------------------------------------------------------------

func TestTaskListPrintsTheServerPage(t *testing.T) {
	var gotPath, gotState, gotType, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		q := r.URL.Query()
		gotState, gotType, gotLimit = q.Get("state"), q.Get("type"), q.Get("limit")
		success(t, w, http.StatusOK, map[string]any{
			"tasks":    []map[string]any{{"id": "t1", "state": "queued"}},
			"total":    1,
			"limit":    5,
			"has_more": false,
		})
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "list",
		"--state", "queued", "--type", "echo", "--limit", "5")
	if err != nil {
		t.Fatalf("task list failed: %v", err)
	}
	if gotPath != "/api/v1/tasks" {
		t.Errorf("task list requested %q, want /api/v1/tasks", gotPath)
	}
	if gotState != "queued" || gotType != "echo" || gotLimit != "5" {
		t.Errorf("task list sent state=%q type=%q limit=%q, want queued/echo/5", gotState, gotType, gotLimit)
	}

	printed := printedObjects(t, out)
	if len(printed) != 1 || printed[0]["id"] != "t1" {
		t.Fatalf("task list printed %v, want the single task t1 from the server page", printed)
	}
}

func TestTaskListUnreachableServerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	out, err := runCLI(t, "--server", url, "task", "list")
	if err == nil {
		t.Fatalf("task list against a closed server printed %q and returned no error", out)
	}
	if !strings.Contains(err.Error(), "list tasks") {
		t.Errorf("error %q does not name the command that failed", err)
	}
	if out != "" {
		t.Errorf("a failed task list printed %q, want nothing", out)
	}
}

func TestTaskListServerErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := runCLI(t, "--server", srv.URL, "task", "list"); err == nil {
		t.Fatal("task list against a 500 server returned no error")
	} else if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not report the status the server returned", err)
	}
}

// --- task create ----------------------------------------------------------

func TestTaskCreateSendsFlagsAndPrintsTheCreatedTask(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		success(t, w, http.StatusCreated, map[string]any{"id": "t-new", "state": "pending"})
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "create",
		"--type", "echo", "--input", "hi", "--priority", "3")
	if err != nil {
		t.Fatalf("task create failed: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/tasks" {
		t.Errorf("task create sent %s %s, want POST /api/v1/tasks", gotMethod, gotPath)
	}
	if gotBody["type"] != "echo" || gotBody["input"] != "hi" || gotBody["priority"] != float64(3) {
		t.Errorf("task create body is %v, want type=echo input=hi priority=3", gotBody)
	}
	if printedObject(t, out)["id"] != "t-new" {
		t.Errorf("task create printed %s, want the id the server assigned", out)
	}
}

func TestTaskCreateRejectedByTheServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failure(t, w, http.StatusBadRequest, "INVALID_REQUEST", "field \"type\" is not a known plugin")
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "create", "--type", "nope")
	if err == nil {
		t.Fatalf("task create against a rejecting server printed %q and returned no error", out)
	}
	for _, want := range []string{"create task", "400", "INVALID_REQUEST"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
	if out != "" {
		t.Errorf("a rejected task create printed %q, want nothing", out)
	}
}

// --- task get -------------------------------------------------------------

func TestTaskGetPrintsTheTask(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		success(t, w, http.StatusOK, map[string]any{"id": "t1", "type": "echo", "state": "completed"})
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "get", "t1")
	if err != nil {
		t.Fatalf("task get failed: %v", err)
	}
	if gotPath != "/api/v1/tasks/t1" {
		t.Errorf("task get requested %q, want /api/v1/tasks/t1", gotPath)
	}
	task := printedObject(t, out)
	if task["id"] != "t1" || task["state"] != "completed" {
		t.Errorf("task get printed %v, want the record the server sent", task)
	}
}

func TestTaskGetUnknownTaskReports404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failure(t, w, http.StatusNotFound, "TASK_NOT_FOUND", "no task with id \"missing\" is visible to this credential")
	}))
	defer srv.Close()

	_, err := runCLI(t, "--server", srv.URL, "task", "get", "missing")
	if err == nil {
		t.Fatal("task get for an unknown id returned no error")
	}
	for _, want := range []string{"get task", "404", "TASK_NOT_FOUND"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// --- task cancel ----------------------------------------------------------

func TestTaskCancelPostsToTheCancelRoute(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		success(t, w, http.StatusOK, map[string]any{"id": "t1", "state": "cancelled", "action": "cancelled"})
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "cancel", "t1")
	if err != nil {
		t.Fatalf("task cancel failed: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/tasks/t1/cancel" {
		t.Errorf("task cancel sent %s %s, want POST /api/v1/tasks/t1/cancel", gotMethod, gotPath)
	}
	if !strings.Contains(out, "t1") || !strings.Contains(out, "cancelled") {
		t.Errorf("task cancel printed %q, want it to name the cancelled task", out)
	}
}

func TestTaskCancelOnATerminalTaskReports409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failure(t, w, http.StatusConflict, "TASK_STATE_CONFLICT", "task \"t1\" already reached the terminal state \"completed\"")
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "cancel", "t1")
	if err == nil {
		t.Fatalf("cancelling a completed task printed %q and returned no error", out)
	}
	for _, want := range []string{"cancel task", "409", "TASK_STATE_CONFLICT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// --- task delete ----------------------------------------------------------

func TestTaskDeleteSendsDelete(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "delete", "t1")
	if err != nil {
		t.Fatalf("task delete failed: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/tasks/t1" {
		t.Errorf("task delete sent %s %s, want DELETE /api/v1/tasks/t1", gotMethod, gotPath)
	}
	if !strings.Contains(out, "t1") || !strings.Contains(out, "deleted") {
		t.Errorf("task delete printed %q, want it to name the deleted task", out)
	}
}

func TestTaskDeleteServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "task", "delete", "t1")
	if err == nil {
		t.Fatalf("task delete against a 500 server printed %q and returned no error", out)
	}
	if !strings.Contains(err.Error(), "delete task") {
		t.Errorf("error %q does not name the command that failed", err)
	}
}

// --- workflow list --------------------------------------------------------

func TestWorkflowListPrintsTheWorkflows(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		success(t, w, http.StatusOK, map[string]any{
			"total":     1,
			"workflows": []map[string]any{{"id": "wf1", "name": "nightly"}},
		})
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "workflow", "list")
	if err != nil {
		t.Fatalf("workflow list failed: %v", err)
	}
	if gotPath != "/api/v1/workflow/list" {
		t.Errorf("workflow list requested %q, want /api/v1/workflow/list", gotPath)
	}
	printed := printedObjects(t, out)
	if len(printed) != 1 || printed[0]["id"] != "wf1" {
		t.Fatalf("workflow list printed %v, want the workflow from the server page", printed)
	}
}

func TestWorkflowListWithoutTheWorkflowEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failure(t, w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "this deployment does not provide the workflows subsystem")
	}))
	defer srv.Close()

	_, err := runCLI(t, "--server", srv.URL, "workflow", "list")
	if err == nil {
		t.Fatal("workflow list against a server without the engine returned no error")
	}
	for _, want := range []string{"list workflows", "503", "SERVICE_UNAVAILABLE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// --- workflow get ---------------------------------------------------------

// The route GET /api/v1/workflow/{workflowID} has been registered all along.
// loopctl never called it, so a workflow could only be read with curl — the
// exact "how do I read this?" ticket a self-service product must not generate.
func TestWorkflowGetPrintsTheWorkflow(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		success(t, w, http.StatusOK, map[string]any{
			"id": "wf1", "name": "nightly", "status": "pending",
		})
	}))
	defer srv.Close()

	out, err := runCLI(t, "--server", srv.URL, "workflow", "get", "wf1")
	if err != nil {
		t.Fatalf("workflow get failed: %v", err)
	}
	if gotPath != "/api/v1/workflow/wf1" {
		t.Errorf("workflow get requested %q, want /api/v1/workflow/wf1", gotPath)
	}
	printed := printedObject(t, out)
	if printed["id"] != "wf1" || printed["name"] != "nightly" {
		t.Fatalf("workflow get printed %v, want the workflow the server returned", printed)
	}
}

// A miss must be an error, not an empty success: "the CLI said nothing wrong"
// is how an id typo becomes an unanswered ticket.
func TestWorkflowGetNotFoundIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failure(t, w, http.StatusNotFound, "WORKFLOW_NOT_FOUND", "no workflow named \"typo\" is registered in this process")
	}))
	defer srv.Close()

	_, err := runCLI(t, "--server", srv.URL, "workflow", "get", "typo")
	if err == nil {
		t.Fatal("workflow get for an unknown id returned no error")
	}
	for _, want := range []string{"get workflow", "404", "WORKFLOW_NOT_FOUND"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// Help text is the first thing a customer reads. It used to promise "create"
// (deliberately absent) and "get" (missing until now). Help that names a
// command the binary does not have is a ticket in itself.
func TestWorkflowHelpPromisesOnlyCommandsThatExist(t *testing.T) {
	out, err := runCLI(t, "workflow", "--help")
	if err != nil {
		t.Fatalf("workflow --help failed: %v", err)
	}
	if !strings.Contains(out, "get") {
		t.Errorf("workflow help does not mention the get command it registers:\n%s", out)
	}
	if strings.Contains(out, "create") {
		t.Errorf("workflow help promises create, which is deliberately absent:\n%s", out)
	}
}

// --- workflow execute -----------------------------------------------------

// TestWorkflowExecuteNeverSendsTheInputFlag pins the reason the -i flag is
// parsed but not forwarded: POST /api/v1/workflow/execute rejects any key but
// workflow_id with 400 UNKNOWN_FIELD, so leaking the flag would make every
// invocation with -i fail.
func TestWorkflowExecuteNeverSendsTheInputFlag(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/workflow/execute" {
			t.Errorf("workflow execute sent %s %s, want POST /api/v1/workflow/execute", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodies = append(bodies, body)
		success(t, w, http.StatusAccepted, map[string]any{
			"workflow_id": "wf1",
			"status":      "executing",
			"poll":        "/api/v1/workflow/wf1",
		})
	}))
	defer srv.Close()

	// A JSON document and a bare string exercise both -i branches.
	for _, input := range []string{`{"name":"x"}`, "not json at all"} {
		out, err := runCLI(t, "--server", srv.URL, "workflow", "execute", "wf1", "--input", input)
		if err != nil {
			t.Fatalf("workflow execute with --input %q failed: %v", input, err)
		}
		if printedObject(t, out)["status"] != "executing" {
			t.Errorf("workflow execute printed %s, want the 202 acknowledgement", out)
		}
	}

	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(bodies))
	}
	for i, body := range bodies {
		if body["workflow_id"] != "wf1" {
			t.Errorf("request %d body is %v, want workflow_id=wf1", i+1, body)
		}
		if len(body) != 1 {
			t.Errorf("request %d sent %v; the endpoint accepts workflow_id only", i+1, body)
		}
	}
}

func TestWorkflowExecuteUnknownWorkflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failure(t, w, http.StatusNotFound, "WORKFLOW_NOT_FOUND", "no workflow named \"nope\" is registered in this process")
	}))
	defer srv.Close()

	_, err := runCLI(t, "--server", srv.URL, "workflow", "execute", "nope")
	if err == nil {
		t.Fatal("executing an unregistered workflow returned no error")
	}
	for _, want := range []string{"execute workflow", "404", "WORKFLOW_NOT_FOUND"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// --- version --------------------------------------------------------------

// TestVersionPrintsTheSingleVersionSource keeps `loopctl version` on
// version.Version, the same value GET /api/v1/health reports.
func TestVersionPrintsTheSingleVersionSource(t *testing.T) {
	out, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version failed: %v", err)
	}
	if got, want := strings.TrimSpace(out), version.Get().String(); got != want {
		t.Errorf("version printed %q, want %q", got, want)
	}
}

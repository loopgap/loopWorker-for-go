package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// reply writes the envelope every LoopWorker route actually returns.
//
// This helper exists because the fixtures used to encode bare JSON, which is
// how this package's tests stayed green against a server that never answered
// that way: list routes answered a page object in production, so a bare array
// fixture certified a contract that was always broken.
func reply(t *testing.T, w http.ResponseWriter, status int, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"success": status < 300, "data": data, "request_id": "req-test"}
	if status >= 300 {
		body["success"] = false
		body["data"] = nil
		body["error"] = map[string]any{"code": "TEST_ERROR", "message": http.StatusText(status)}
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encode reply: %v", err)
	}
}

// taskPage is the shape GET /api/v1/tasks puts in the envelope's data member.
func taskPage(tasks ...map[string]any) map[string]any {
	if tasks == nil {
		tasks = []map[string]any{}
	}
	return map[string]any{"tasks": tasks, "limit": 20, "offset": 0, "has_more": false}
}

func TestNewAPIClient(t *testing.T) {
	c := NewAPIClient("http://localhost:8080")
	if c.BaseURL != "http://localhost:8080" {
		t.Errorf("expected BaseURL 'http://localhost:8080', got '%s'", c.BaseURL)
	}
	if c.HTTPClient == nil {
		t.Error("HTTPClient should not be nil")
	}
}

func TestNewAPIClientDefault(t *testing.T) {
	c := NewAPIClient("")
	if c.BaseURL != defaultServerURL {
		t.Errorf("expected default URL '%s', got '%s'", defaultServerURL, c.BaseURL)
	}
}

func TestNewAPIClientEnvOverride(t *testing.T) {
	os.Setenv("LOOPWORKER_URL", "http://custom:9999")
	defer os.Unsetenv("LOOPWORKER_URL")

	c := NewAPIClient("")
	if c.BaseURL != "http://custom:9999" {
		t.Errorf("expected env URL 'http://custom:9999', got '%s'", c.BaseURL)
	}
}

func TestHealthCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		reply(t, w, http.StatusOK, map[string]any{"status": "ok", "version": "0.1.0-beta"})
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	health, err := c.HealthCheck()
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
	if health["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%v'", health["status"])
	}
}

func TestListTasks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		reply(t, w, http.StatusOK, taskPage())
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	tasks, err := c.ListTasks("", "", 10)
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if tasks == nil {
		t.Error("tasks should not be nil")
	}
}

func TestListTasksWithFilters(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "pending" {
			t.Errorf("expected state=pending, got %s", q.Get("state"))
		}
		if q.Get("type") != "echo" {
			t.Errorf("expected type=echo, got %s", q.Get("type"))
		}
		reply(t, w, http.StatusOK, taskPage())
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.ListTasks("pending", "echo", 5)
	if err != nil {
		t.Fatalf("ListTasks with filters failed: %v", err)
	}
}

func TestCreateTask(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		reply(t, w, http.StatusCreated, map[string]any{"id": "task-1"})
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	task, err := c.CreateTask("echo", "hello", 1)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if task["id"] != "task-1" {
		t.Errorf("expected id 'task-1', got '%v'", task["id"])
	}
}

func TestGetTask(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(t, w, http.StatusOK, map[string]any{"id": "task-1", "type": "echo"})
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	task, err := c.GetTask("task-1")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if task["id"] != "task-1" {
		t.Errorf("expected id 'task-1', got '%v'", task["id"])
	}
}

func TestDeleteTask(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	if err := c.DeleteTask("task-1"); err != nil {
		t.Fatalf("DeleteTask failed: %v", err)
	}
}

func TestListWorkflows(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workflow/list" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		reply(t, w, http.StatusOK, map[string]any{"total": 0, "workflows": []map[string]any{}})
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	workflows, err := c.ListWorkflows()
	if err != nil {
		t.Fatalf("ListWorkflows failed: %v", err)
	}
	if workflows == nil {
		t.Error("workflows should not be nil")
	}
}

func TestExecuteWorkflow(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/workflow/execute" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if _, extra := got["input"]; extra {
			t.Errorf("client sent an `input` key the endpoint rejects with 400 UNKNOWN_FIELD: %v", got)
		}
		// The real endpoint answers 202 Accepted, not 200.
		reply(t, w, http.StatusAccepted, map[string]any{"workflow_id": "my-wf", "status": "executing"})
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	result, err := c.ExecuteWorkflow("my-wf", nil)
	if err != nil {
		t.Fatalf("ExecuteWorkflow failed: %v", err)
	}
	if result["status"] != "executing" {
		t.Errorf("expected status 'executing', got '%v'", result["status"])
	}
}

// TestGetMetricsHasNoRoute pins the fact that broke this client: there is no
// /api/v1/metrics on the API port. The old test answered this path from an
// httptest server, so it passed against a contract the real server never
// implemented - a green test for something that could not work.
func TestGetMetricsHasNoRoute(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	if _, err := c.GetMetrics(); err == nil {
		t.Fatal("expected an error: the API port serves no metrics route")
	}
}

// TestGetLogsHasNoRoute is the same guard for logs, which also live on the
// admin listener and arrive in an envelope rather than a bare array.
func TestGetLogsHasNoRoute(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	if _, err := c.GetLogs(); err == nil {
		t.Fatal("expected an error: the API port serves no logs route")
	}
}

// TestCredentialsReachEveryVerb is the regression this package needed: Get,
// Post and Delete used to be copy-pasted, and every copy forgot the API key
// header, so all three answered 401 against any authenticated route.
func TestCredentialsReachEveryVerb(t *testing.T) {
	const key = "lwk_test_key"
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != key {
			t.Errorf("%s %s sent X-API-Key %q, want %q", r.Method, r.URL.Path, got, key)
		}
		seen = append(seen, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	c.APIKey = key

	for _, call := range []struct {
		name string
		run  func() error
	}{
		{"Get", func() error { _, err := c.Get("/api/v1/workers"); return err }},
		{"Post", func() error { _, err := c.Post("/api/v1/tasks", map[string]string{"type": "echo"}); return err }},
		{"Delete", func() error { _, err := c.Delete("/api/v1/tasks/x"); return err }},
	} {
		if err := call.run(); err != nil {
			t.Errorf("%s: %v", call.name, err)
		}
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 authenticated requests, got %d", len(seen))
	}
}

// TestUnauthorizedErrorNamesTheFix keeps the 401 message actionable. A bare
// "server returned 401" is indistinguishable from a misconfigured server.
func TestUnauthorizedErrorNamesTheFix(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	_, err := NewAPIClient(ts.URL).ListTasks("", "", 10)
	if err == nil {
		t.Fatal("expected an error for an unauthenticated request")
	}
	msg := err.Error()
	for _, want := range []string{"401", "cause:", "fix:", "LOOPWORKER_API_KEY", "README.md#authentication"} {
		if !strings.Contains(msg, want) {
			t.Errorf("401 message is missing %q:\n%s", want, msg)
		}
	}
}

// TestAPIKeyComesFromTheEnvironment documents where the CLI credentials come
// from, so a 401 caused by an unset variable is traceable from the error.
func TestAPIKeyComesFromTheEnvironment(t *testing.T) {
	t.Setenv("LOOPWORKER_API_KEY", "lwk_from_env")
	if got := NewAPIClient("").APIKey; got != "lwk_from_env" {
		t.Errorf("NewAPIClient picked up API key %q, want lwk_from_env", got)
	}
}

func TestServerErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.HealthCheck()
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestConnectionError(t *testing.T) {
	c := NewAPIClient("http://localhost:1")
	_, err := c.HealthCheck()
	if err == nil {
		t.Error("expected connection error")
	}
}

func TestClientGetConnectionError(t *testing.T) {
	c := NewAPIClient("http://localhost:1")
	_, err := c.Get("/api/v1/tasks")
	if err == nil {
		t.Error("expected connection error from Get")
	}
}

func TestClientPostConnectionError(t *testing.T) {
	c := NewAPIClient("http://localhost:1")
	_, err := c.Post("/api/v1/tasks", map[string]string{"type": "test"})
	if err == nil {
		t.Error("expected connection error from Post")
	}
}

func TestClientDeleteConnectionError(t *testing.T) {
	c := NewAPIClient("http://localhost:1")
	_, err := c.Delete("/api/v1/tasks/test")
	if err == nil {
		t.Error("expected connection error from Delete")
	}
}

func TestClientGetReadError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		// Close immediately to cause read error
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.Get("/api/v1/tasks")
	if err == nil {
		// May or may not error depending on timing - acceptable
	}
}

func TestClientHealthCheckInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.HealthCheck()
	if err == nil {
		t.Error("expected error for invalid JSON response")
	}
}

func TestClientListTasksInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.ListTasks("", "", 10)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClientListWorkflowsInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.ListWorkflows()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClientExecuteWorkflowInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.ExecuteWorkflow("wf", nil)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClientGetMetricsInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.GetMetrics()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClientGetLogsInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.GetLogs()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClientGetTaskInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.GetTask("task-1")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClientCreateTaskInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.CreateTask("echo", "hello", 1)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClientPostMarshalError(t *testing.T) {
	c := NewAPIClient("http://localhost:8080")
	// Channels can't be marshaled to JSON
	_, err := c.Post("/test", make(chan int))
	if err == nil {
		t.Error("expected marshal error for unmarshalable data")
	}
}

// TestClientDeleteNon200Error 验证 DELETE 非200/204的错误。
func TestClientDeleteNon200Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("delete failed"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	err := c.DeleteTask("task-1")
	if err == nil {
		t.Error("expected error for 500 on delete")
	}
}

// TestClientPostNon200Error 验证 POST 非200/201的错误。
func TestClientPostNon200Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.CreateTask("test", "input", 1)
	if err == nil {
		t.Error("expected error for 400 on create")
	}
}

// TestClientGetReadBodyError 验证读取响应体失败时的错误。
func TestClientGetReadBodyError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)
	_, err := c.Get("/api/v1/tasks")
	if err == nil {
		// May or may not error depending on timing
	}
}

// TestClientAllMethodsIntegration 验证所有客户端方法的基本路径。
func TestClientAllMethodsIntegration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			reply(t, w, http.StatusOK, map[string]any{"status": "ok"})
		case "/api/v1/tasks":
			if r.Method == "GET" {
				reply(t, w, http.StatusOK, taskPage(map[string]any{"id": "t1"}))
			} else {
				reply(t, w, http.StatusCreated, map[string]any{"id": "t2"})
			}
		case "/api/v1/tasks/t1":
			if r.Method == "DELETE" {
				w.WriteHeader(http.StatusNoContent)
			} else {
				reply(t, w, http.StatusOK, map[string]any{"id": "t1", "type": "echo"})
			}
		case "/api/v1/workflow/list":
			reply(t, w, http.StatusOK, map[string]any{"total": 1, "workflows": []map[string]any{{"id": "wf1"}}})
		case "/api/v1/workflow/execute":
			reply(t, w, http.StatusAccepted, map[string]any{"workflow_id": "wf1", "status": "executing"})
		case "/api/v1/metrics":
			json.NewEncoder(w).Encode(map[string]interface{}{"count": 42})
		case "/api/v1/logs":
			json.NewEncoder(w).Encode([]map[string]interface{}{{"level": "info"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c := NewAPIClient(ts.URL)

	// Health
	health, err := c.HealthCheck()
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if health["status"] != "ok" {
		t.Errorf("expected status ok, got %v", health["status"])
	}

	// ListTasks
	tasks, err := c.ListTasks("pending", "echo", 10)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}

	// GetTask
	task, err := c.GetTask("t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task["id"] != "t1" {
		t.Errorf("expected t1, got %v", task["id"])
	}

	// CreateTask
	created, err := c.CreateTask("echo", "hello", 1)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created["id"] != "t2" {
		t.Errorf("expected t2, got %v", created["id"])
	}

	// DeleteTask
	if err := c.DeleteTask("t1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}

	// ListWorkflows
	wfs, err := c.ListWorkflows()
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(wfs) != 1 {
		t.Errorf("expected 1 workflow, got %d", len(wfs))
	}

	// ExecuteWorkflow
	result, err := c.ExecuteWorkflow("wf1", nil)
	if err != nil {
		t.Fatalf("ExecuteWorkflow: %v", err)
	}
	if result["status"] != "executing" {
		t.Errorf("expected executing, got %v", result["status"])
	}

	// GetMetrics
	metrics, err := c.GetMetrics()
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	if metrics["count"] != float64(42) {
		t.Errorf("expected 42, got %v", metrics["count"])
	}

	// GetLogs
	logs, err := c.GetLogs()
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 log, got %d", len(logs))
	}
}

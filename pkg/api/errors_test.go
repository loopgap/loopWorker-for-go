package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	lwerrors "loopworker/pkg/errors"
)

// newRecorder returns a flush-capable recorder so the recoverer path is
// exercised through the same writer type the middleware stack uses.
func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func requestFor(path string) *http.Request { return httptest.NewRequest(http.MethodGet, path, nil) }

var _ = errors.Is

// internalLeaks are substrings that must never reach a customer. A Go error
// string quoting a database file or a driver error is the SPEC 10-B7 defect:
// it turns a support question into an incident.
var internalLeaks = []string{
	"sql:", "SQLITE", "sqlite3", ".db", "sql.Open", "gorm",
	"runtime error", "panic:", "goroutine ", ".go:", "loopworker/internal",
	"no such file", "permission denied", "0x", "null pointer",
}

// assertNoLeak fails when a response body carries an internal diagnostic.
func assertNoLeak(t *testing.T, label, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, leak := range internalLeaks {
		if strings.Contains(lower, strings.ToLower(leak)) {
			t.Errorf("%s leaks internal detail %q:\n%s", label, leak, body)
		}
	}
}

// errorCase is one row of the SPEC 10-B7 table: a request, the status it must
// produce, and the code a client branches on.
type errorCase struct {
	name       string
	role       string
	method     string
	path       string
	body       string
	wantStatus int
	wantCode   ErrorCode
	// wantFix requires the message to carry a remedy ("Fix:").
	wantFix bool
}

func TestB7EveryErrorRouteMapsToTheRightStatusAndCode(t *testing.T) {
	env := newTestEnv(t)

	// Build the state the table needs before driving the requests.
	id := env.createTask(roleOperator, "b7-task", "")
	dep := env.createTask(roleOperator, "b7-dependency", "")
	unknownWorkflow := env.wfe

	cases := []errorCase{
		{
			name: "unknown task", role: roleOperator,
			method: http.MethodGet, path: "/api/v1/tasks/does-not-exist",
			wantStatus: http.StatusNotFound, wantCode: CodeTaskNotFound, wantFix: true,
		},
		{
			name: "unknown workflow", role: roleOperator,
			method: http.MethodGet, path: "/api/v1/workflow/does-not-exist",
			wantStatus: http.StatusNotFound, wantCode: CodeWorkflowNotFound, wantFix: true,
		},
		{
			name: "unknown dependency", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks/" + id + "/dependencies",
			body:       `{"dependency_id":"missing-task"}`,
			wantStatus: http.StatusNotFound, wantCode: CodeTaskNotFound, wantFix: true,
		},
		{
			name: "cancel a task twice is a conflict", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks/" + dep + "/cancel",
			wantStatus: http.StatusConflict, wantCode: CodeTaskStateConflict, wantFix: true,
		},
		{
			name: "malformed json", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks", body: "{not json",
			wantStatus: http.StatusBadRequest, wantCode: CodeMalformedJSON, wantFix: true,
		},
		{
			name: "empty body", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks", body: "",
			wantStatus: http.StatusBadRequest, wantCode: CodeMalformedJSON, wantFix: true,
		},
		{
			name: "array body", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks", body: `[{"type":"x"}]`,
			wantStatus: http.StatusBadRequest, wantCode: CodeMalformedJSON, wantFix: true,
		},
		{
			name: "wrong json type", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks", body: `{"type":42}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequest, wantFix: true,
		},
		{
			name: "unknown field", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks", body: `{"type":"x","typo":1}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeUnknownField, wantFix: true,
		},
		{
			name: "missing dependency_id", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/tasks/" + id + "/dependencies", body: `{}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequest, wantFix: true,
		},
		{
			name: "missing workflow_id", role: roleOperator,
			method: http.MethodPost, path: "/api/v1/workflow/execute", body: `{}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequest, wantFix: true,
		},
		{
			name: "bad duration", role: roleAdmin,
			method: http.MethodPost, path: "/api/v1/auth/keys", body: `{"name":"k","role":"viewer","ttl":"soon"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequest, wantFix: true,
		},
		{
			name: "bad role", role: roleAdmin,
			method: http.MethodPost, path: "/api/v1/auth/keys", body: `{"name":"k","role":"root"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequest, wantFix: true,
		},
		{
			name: "blank key name", role: roleAdmin,
			method: http.MethodPost, path: "/api/v1/auth/keys", body: `{"name":"  ","role":"viewer"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequest, wantFix: true,
		},
		{
			name: "revoke unknown key", role: roleAdmin,
			method: http.MethodDelete, path: "/api/v1/auth/keys/key-does-not-exist",
			wantStatus: http.StatusNotFound, wantCode: CodeNotFound, wantFix: true,
		},
		{
			name: "revoke the bootstrap key", role: roleAdmin,
			method: http.MethodDelete, path: "/api/v1/auth/keys/bootstrap",
			wantStatus: http.StatusConflict, wantCode: CodeTaskStateConflict, wantFix: true,
		},
		{
			name: "unknown event type filter", role: roleOperator,
			method: http.MethodGet, path: "/api/v1/events/live?types=not.an.event",
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequest, wantFix: true,
		},
		{
			name: "unknown route", role: roleOperator,
			method: http.MethodGet, path: "/api/v1/nonexistent",
			wantStatus: http.StatusNotFound, wantCode: CodeNotFound, wantFix: true,
		},
		{
			name: "wrong method", role: roleOperator,
			method: http.MethodPut, path: "/api/v1/tasks",
			wantStatus: http.StatusMethodNotAllowed, wantCode: CodeMethodNotAllowed, wantFix: true,
		},
		{
			name: "no credential", role: roleNone,
			method: http.MethodPost, path: "/api/v1/tasks", body: `{"type":"x"}`,
			wantStatus: http.StatusUnauthorized, wantCode: CodeUnauthorized, wantFix: true,
		},
		{
			name: "insufficient role", role: roleViewer,
			method: http.MethodPost, path: "/api/v1/tasks", body: `{"type":"x"}`,
			wantStatus: http.StatusForbidden, wantCode: CodeForbidden, wantFix: true,
		},
	}

	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+dep+"/cancel", ""), http.StatusOK)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := env.call(tc.role, tc.method, tc.path, tc.body)
			body := w.Body.String()

			if w.Code != tc.wantStatus {
				t.Fatalf("status: want %d, got %d\n%s", tc.wantStatus, w.Code, body)
			}
			envW := decodeEnvelope(t, w)
			if envW.Error == nil {
				t.Fatalf("no error object in body:\n%s", body)
			}
			if envW.Error.Code != tc.wantCode {
				t.Errorf("code: want %s, got %s\n%s", tc.wantCode, envW.Error.Code, body)
			}
			if envW.Success {
				t.Error("failure response claims success=true")
			}
			if strings.TrimSpace(envW.Error.Message) == "" {
				t.Error("error message must not be empty")
			}
			if tc.wantFix && !strings.Contains(envW.Error.Message, "Fix:") {
				t.Errorf("error message must carry a remedy:\n%s", envW.Error.Message)
			}
			if envW.Error.RequestID == "" {
				t.Error("error must carry the request id for correlation")
			}
			assertNoLeak(t, tc.name, body)
		})
	}
	_ = unknownWorkflow
}

// TestB7ErrorResponseAlwaysHasCodeAndFix is the contract sweep: every reachable
// 4xx/5xx in this package must carry a machine-readable code and a remedy.
func TestB7ErrorResponseAlwaysHasCodeAndFix(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "contract", "")

	probes := []struct {
		role, method, path, body string
	}{
		{roleOperator, http.MethodGet, "/api/v1/tasks/nope", ""},
		{roleOperator, http.MethodGet, "/api/v1/tasks/" + id, ""},
		{roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"ok"}`},
		{roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":""}`},
		{roleOperator, http.MethodPost, "/api/v1/tasks", `{`},
		{roleOperator, http.MethodPost, "/api/v1/tasks/" + id + "/dependencies", `{}`},
		{roleOperator, http.MethodPost, "/api/v1/tasks/" + id + "/dependencies", `{"dependency_id":"` + id + `"}`},
		{roleOperator, http.MethodPost, "/api/v1/workflow/execute", `{}`},
		{roleOperator, http.MethodGet, "/api/v1/workflow/nope", ""},
		{roleOperator, http.MethodGet, "/api/v1/events/live?types=bogus", ""},
		{roleOperator, http.MethodGet, "/api/v1/nope", ""},
		{roleOperator, http.MethodPatch, "/api/v1/tasks", ""},
		{roleNone, http.MethodGet, "/api/v1/tasks", ""},
		{roleViewer, http.MethodDelete, "/api/v1/tasks/" + id, ""},
		{roleAdmin, http.MethodGet, "/api/v1/auth/keys/nope", ""},
	}

	for _, p := range probes {
		w := env.call(p.role, p.method, p.path, p.body)
		if w.Code < 400 {
			continue // a 2xx/3xx is not an error response
		}
		label := p.role + " " + p.method + " " + p.path
		envW := decodeEnvelope(t, w)
		if envW.Error == nil || envW.Error.Code == "" {
			t.Errorf("%s -> %d has no error code:\n%s", label, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(envW.Error.Message, "Fix:") {
			t.Errorf("%s -> %d/%s message lacks a remedy: %q", label, w.Code, envW.Error.Code, envW.Error.Message)
		}
		assertNoLeak(t, label, w.Body.String())
	}
}

// TestB7UnknownErrorsCollapseToInternalError proves an unrecognised error never
// reaches the client verbatim.
func TestB7UnknownErrorsCollapseToInternalError(t *testing.T) {
	internal := errors.New("dial tcp 10.0.0.5:5432: connect: connection refused (sqlite3 open /srv/loopworker/tasks.db failed)")
	c := classify(internal)
	if c.status != http.StatusInternalServerError {
		t.Errorf("unknown error status: want 500, got %d", c.status)
	}
	if c.code != CodeInternalError {
		t.Errorf("unknown error code: want %s, got %s", CodeInternalError, c.code)
	}
	assertNoLeak(t, "classify(unknown)", c.message)
}

// TestB7CatalogueStatusMapping locks the sentinels onto their HTTP statuses: the
// SPEC defect was 404/409 collapsing into 500.
func TestB7CatalogueStatusMapping(t *testing.T) {
	cases := []struct {
		sentinel error
		status   int
		code     ErrorCode
	}{
		{lwerrors.ErrTaskNotFound, http.StatusNotFound, CodeTaskNotFound},
		{lwerrors.ErrTaskInvalid, http.StatusConflict, CodeTaskStateConflict},
		{lwerrors.ErrTaskAlreadyExists, http.StatusConflict, CodeTaskAlreadyExists},
		{lwerrors.ErrWorkflowNotFound, http.StatusNotFound, CodeWorkflowNotFound},
		{lwerrors.ErrWorkflowCycle, http.StatusConflict, CodeDependencyCycle},
		{lwerrors.ErrWorkerNotFound, http.StatusNotFound, CodeWorkerNotFound},
		{lwerrors.ErrPluginNotFound, http.StatusNotFound, CodePluginNotFound},
		{lwerrors.ErrQueueFull, http.StatusServiceUnavailable, CodeQueueFull},
		{lwerrors.ErrForbidden, http.StatusForbidden, CodeForbidden},
		{lwerrors.ErrUnauthorized, http.StatusUnauthorized, CodeUnauthorized},
		{lwerrors.ErrTokenInvalid, http.StatusUnauthorized, CodeTokenInvalid},
		{lwerrors.ErrTokenExpired, http.StatusUnauthorized, CodeTokenExpired},
		{lwerrors.ErrRateLimited, http.StatusTooManyRequests, CodeRateLimited},
	}
	for _, tc := range cases {
		got := classify(tc.sentinel)
		if got.status != tc.status || got.code != tc.code {
			t.Errorf("%v: want %d/%s, got %d/%s", tc.sentinel, tc.status, tc.code, got.status, got.code)
		}
		if !strings.Contains(got.message, "Fix:") {
			t.Errorf("%v: message lacks a remedy: %q", tc.sentinel, got.message)
		}
		assertNoLeak(t, tc.sentinel.Error(), got.message)
	}
}

// TestB7OversizedBodyIs413 proves the body limit reports itself through the
// contract instead of surfacing an io error.
func TestB7OversizedBodyIs413(t *testing.T) {
	env := newTestEnv(t, withSmallBodyLimit(1024))

	huge := strings.Repeat("a", 4096)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks",
		`{"type":"big","input_text":"`+huge+`"}`)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: want 413 or 400, got %d: %s", w.Code, w.Body.String())
	}
	envW := decodeEnvelope(t, w)
	if envW.Error == nil || envW.Error.Code != CodeRequestTooLarge {
		t.Errorf("want %s, got %+v", CodeRequestTooLarge, envW.Error)
	}
	if !strings.Contains(envW.Error.Message, "Fix:") {
		t.Errorf("413 must carry a remedy: %q", envW.Error.Message)
	}
}

// TestB7PanicBecomes500Envelope proves the recoverer replaces a stack dump with
// the contract.
func TestB7PanicBecomes500Envelope(t *testing.T) {
	router := chi.NewRouter()
	router.Use(middleware_Recoverer())
	router.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("internal detail: /srv/loopworker/tasks.db is locked")
	})

	w := newRecorder()
	router.ServeHTTP(w, requestFor("/boom"))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic: want 500, got %d", w.Code)
	}
	envW := decodeEnvelope(t, w)
	if envW.Error == nil || envW.Error.Code != CodeInternalError {
		t.Fatalf("want %s, got %+v", CodeInternalError, envW.Error)
	}
	assertNoLeak(t, "panic", w.Body.String())
}

// TestB7SuccessEnvelopesHaveNoErrorMember keeps the contract unambiguous.
func TestB7SuccessEnvelopesHaveNoErrorMember(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"ok"}`)
	env.expectOK(w, http.StatusCreated)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if _, present := raw["error"]; present {
		t.Error("a 2xx envelope must not carry an error member")
	}
	for _, key := range []string{"success", "timestamp", "request_id"} {
		if _, present := raw[key]; !present {
			t.Errorf("envelope is missing %q", key)
		}
	}
}

func withSmallBodyLimit(n int64) Option {
	return func(cfg *Config) { cfg.MaxBodyBytes = n }
}

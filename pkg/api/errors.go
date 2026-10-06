package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	lwerrors "loopworker/pkg/errors"
)

// ErrorCode is the stable, machine-readable member of the error contract. Clients
// switch on this value; the human message may be reworded at any time.
type ErrorCode string

// The full catalogue. Every error produced by this package uses one of these.
const (
	CodeInvalidRequest       ErrorCode = "INVALID_REQUEST"
	CodeUnknownField         ErrorCode = "UNKNOWN_FIELD"
	CodeMalformedJSON        ErrorCode = "MALFORMED_JSON"
	CodeInputEncodingInvalid ErrorCode = "INPUT_ENCODING_INVALID"
	CodeRequestTooLarge      ErrorCode = "REQUEST_TOO_LARGE"
	CodeUnauthorized         ErrorCode = "UNAUTHORIZED"
	CodeTokenInvalid         ErrorCode = "TOKEN_INVALID"
	CodeTokenExpired         ErrorCode = "TOKEN_EXPIRED"
	CodeForbidden            ErrorCode = "FORBIDDEN"
	CodeTaskNotFound         ErrorCode = "TASK_NOT_FOUND"
	CodeTaskAlreadyExists    ErrorCode = "TASK_ALREADY_EXISTS"
	CodeTaskStateConflict    ErrorCode = "TASK_STATE_CONFLICT"
	CodeWorkflowNotFound     ErrorCode = "WORKFLOW_NOT_FOUND"
	CodeWorkerNotFound       ErrorCode = "WORKER_NOT_FOUND"
	CodePluginNotFound       ErrorCode = "PLUGIN_NOT_FOUND"
	CodeDependencyCycle      ErrorCode = "DEPENDENCY_CYCLE"
	CodeDependencyInvalid    ErrorCode = "DEPENDENCY_INVALID"
	CodeGraphTooLarge        ErrorCode = "GRAPH_TOO_LARGE"
	CodeRateLimited          ErrorCode = "RATE_LIMITED"
	CodeStreamLimitReached   ErrorCode = "STREAM_LIMIT_REACHED"
	CodeQueueFull            ErrorCode = "QUEUE_FULL"
	CodeNotFound             ErrorCode = "NOT_FOUND"
	CodeMethodNotAllowed     ErrorCode = "METHOD_NOT_ALLOWED"
	CodeStreamingUnsupported ErrorCode = "STREAMING_UNSUPPORTED"
	CodeServiceUnavailable   ErrorCode = "SERVICE_UNAVAILABLE"
	CodeInternalError        ErrorCode = "INTERNAL_ERROR"
	CodeTimeout              ErrorCode = "TIMEOUT"
)

// ErrorBody is the error object inside every failed response.
type ErrorBody struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id"`
}

// Envelope is the single response shape for success and failure.
type Envelope struct {
	Success   bool       `json:"success"`
	Data      any        `json:"data,omitempty"`
	Error     *ErrorBody `json:"error,omitempty"`
	Timestamp time.Time  `json:"timestamp"`
	RequestID string     `json:"request_id"`
}

// FieldError reports a rejected field together with the fix for it.
type FieldError struct {
	Field   string         `json:"field"`
	Reason  string         `json:"reason"`
	Fix     string         `json:"fix"`
	Details map[string]any `json:"details,omitempty"`
	Status  int            `json:"-"`
	Code    ErrorCode      `json:"-"`
	Cause   error          `json:"-"`
}

func (e *FieldError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Field, e.Reason, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

func (e *FieldError) Unwrap() error { return e.Cause }

func fieldMessage(e *FieldError) string {
	msg := strings.TrimSpace(e.Reason)
	if e.Fix != "" {
		msg = msg + " Fix: " + e.Fix
	}
	return msg
}

// classification maps an error onto HTTP status + code + self-explanatory message.
type classification struct {
	status  int
	code    ErrorCode
	message string
	details map[string]any
}

// catalogue is the authority for status/code pairing and for the wording that
// makes a support ticket unnecessary: cause first, then the fix.
var catalogue = map[error]classification{}

func init() {
	register := func(sentinel error, status int, code ErrorCode, message string) {
		catalogue[sentinel] = classification{status: status, code: code, message: message}
	}

	register(lwerrors.ErrTaskNotFound, http.StatusNotFound, CodeTaskNotFound,
		"No task with that id exists on this server. Fix: list visible tasks with GET /api/v1/tasks, or create one with POST /api/v1/tasks. Task ids are returned in the response body of the create call.")
	register(lwerrors.ErrTaskInvalid, http.StatusConflict, CodeTaskStateConflict,
		"The task exists but its current state forbids this operation (for example it already reached a terminal state, or a dependency has not completed). Fix: GET /api/v1/tasks/{id} and read \"state\" before retrying; terminal states are completed, failed, cancelled and dead_letter.")
	register(lwerrors.ErrTaskAlreadyExists, http.StatusConflict, CodeTaskAlreadyExists,
		"A task with that identity already exists. Fix: reuse the existing task id, or POST /api/v1/tasks to let the server allocate a new one.")
	register(lwerrors.ErrWorkflowNotFound, http.StatusNotFound, CodeWorkflowNotFound,
		"No workflow with that id is registered. Fix: GET /api/v1/workflow/list shows every registered workflow id.")
	register(lwerrors.ErrWorkflowInvalid, http.StatusUnprocessableEntity, CodeInvalidRequest,
		"The workflow definition is not executable (missing steps, unknown step reference, or a cycle). Fix: GET /api/v1/workflow/graph and check the dependency edges.")
	register(lwerrors.ErrWorkflowCycle, http.StatusConflict, CodeDependencyCycle,
		"The dependency graph contains a cycle, so no task in it could ever unblock. Fix: remove the back edge; a task may only depend on tasks that can finish before it.")
	register(lwerrors.ErrWorkerNotFound, http.StatusNotFound, CodeWorkerNotFound,
		"No worker with that id is registered. Fix: GET /api/v1/workers lists live workers.")
	register(lwerrors.ErrPluginNotFound, http.StatusNotFound, CodePluginNotFound,
		"The requested plugin is not loaded. Fix: check the plugins directory and GET /api/v1/workers for capacity, then restart the server to load new .wasm plugins.")
	register(lwerrors.ErrQueueFull, http.StatusServiceUnavailable, CodeQueueFull,
		"The task queue is at capacity and rejected this submission. Fix: retry later with exponential backoff and honour the Retry-After header; the queue drains as workers complete tasks.")
	register(lwerrors.ErrSchedulerDown, http.StatusServiceUnavailable, CodeServiceUnavailable,
		"The scheduler is not running, so tasks cannot be queued yet. Fix: retry once the server reports healthy at GET /api/v1/health.")
	register(lwerrors.ErrNotRunning, http.StatusServiceUnavailable, CodeServiceUnavailable,
		"The subsystem needed by this endpoint is not running. Fix: GET /api/v1/health to see which component is down, then restart the server.")
	register(lwerrors.ErrUnauthorized, http.StatusUnauthorized, CodeUnauthorized,
		"No credentials were supplied. Fix: send \"Authorization: Bearer <token>\" or \"X-API-Key: lwk_...\". POST /api/v1/auth/token exchanges an API key for a short-lived bearer token.")
	register(lwerrors.ErrTokenInvalid, http.StatusUnauthorized, CodeTokenInvalid,
		"The credential is unknown, malformed or revoked. Fix: copy the key without truncation and confirm it was issued by this server instance; ephemeral bootstrap keys stop working when the process exits.")
	register(lwerrors.ErrTokenExpired, http.StatusUnauthorized, CodeTokenExpired,
		"The credential expired. Fix: request a fresh bearer token from POST /api/v1/auth/token, or install a permanent API key through LOOPWORKER_API_KEYS.")
	register(lwerrors.ErrForbidden, http.StatusForbidden, CodeForbidden,
		"The authenticated role is not allowed to perform this operation. Fix: use a key whose role covers the required permission (viewer=read, operator=read/write/execute, admin=all) or call a read-only endpoint.")
	register(lwerrors.ErrRateLimited, http.StatusTooManyRequests, CodeRateLimited,
		"Too many requests from this caller within the rate-limit window. Fix: honour the Retry-After header and back off; authenticated callers get a larger budget than anonymous ones.")
	register(lwerrors.ErrConfigInvalid, http.StatusInternalServerError, CodeInternalError,
		"The server was started with an invalid configuration. Fix: check the config file and LOOPWORKER_* environment variables against docs/USAGE.md, then restart.")
	register(lwerrors.ErrDatabaseError, http.StatusInternalServerError, CodeInternalError,
		"The task store failed. Fix: check that the data directory is writable and not held by another process; the request id in this response can be matched to the server log line.")
}

// classify translates any handler error into the wire contract. Unknown errors
// collapse to INTERNAL_ERROR so raw Go/stdlib strings never reach customers.
func classify(err error) classification {
	if err == nil {
		return classification{status: http.StatusInternalServerError, code: CodeInternalError, message: "The request failed for an unspecified reason. Fix: retry once; if it persists, report the request_id."}
	}

	var fieldErr *FieldError
	if errors.As(err, &fieldErr) {
		status := fieldErr.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		code := fieldErr.Code
		if code == "" {
			code = CodeInvalidRequest
		}
		return classification{status: status, code: code, message: fieldMessage(fieldErr), details: fieldErr.Details}
	}

	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return classification{
			status:  http.StatusRequestEntityTooLarge,
			code:    CodeRequestTooLarge,
			message: "Request body exceeds the configured limit. Fix: reduce the payload or raise api.max_body_bytes in the config file.",
		}
	}

	for sentinel, entry := range catalogue {
		if errors.Is(err, sentinel) {
			return entry
		}
	}

	var jsonSyntax *json.UnmarshalTypeError
	if errors.As(err, &jsonSyntax) {
		return classification{
			status: http.StatusBadRequest,
			code:   CodeInvalidRequest,
			message: fmt.Sprintf("Field %q has the wrong JSON type. Fix: see GET /api/v1/openapi.json for the schema of this endpoint.",
				jsonSyntax.Field),
		}
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return classification{
			status: http.StatusBadRequest,
			code:   CodeMalformedJSON,
			message: fmt.Sprintf("The request body is not valid JSON (offset %d). Fix: send a single JSON object with Content-Type: application/json; quote all keys and use double quotes for strings.",
				syntaxErr.Offset),
		}
	}

	return classification{
		status: http.StatusInternalServerError,
		code:   CodeInternalError,
		message: "The server failed while handling this request. Fix: retry once; if it persists, report this request_id " +
			"together with the endpoint and body - the server log entry has the full cause.",
	}
}

// newFieldError is the short constructor for the common "one field is wrong"
// case, so handlers do not repeat the same five literal fields.
func newFieldError(field, reason, fix string, code ErrorCode, status int) *FieldError {
	return &FieldError{Field: field, Reason: reason, Fix: fix, Code: code, Status: status}
}

// joinStrings renders a list for error messages ("a, b, c").
func joinStrings(items []string, sep string) string { return strings.Join(items, sep) }

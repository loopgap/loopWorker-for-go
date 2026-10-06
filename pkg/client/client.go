// Package client is the HTTP client the loopctl, loopdebug and loopwatch CLIs
// use to reach a LoopWorker server.
//
// # Credentials
//
// Every route except /api/v1/health, /api/v1/openapi.json and GET /healthz is
// behind the authenticator. Set LOOPWORKER_API_KEY (or APIClient.APIKey) or
// every task and workflow command answers 401 - which reads like a broken
// server rather than a missing key, so the 401 path says so explicitly.
//
// The metrics and logs endpoints are the exception and are not on this port;
// see GetMetrics and GetLogs.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultServerURL = "http://localhost:19527"

	// apiKeyHeader is the header pkg/api's authenticator reads.
	apiKeyHeader = "X-API-Key"

	// apiKeyEnv carries the credential when the caller has nothing better.
	apiKeyEnv = "LOOPWORKER_API_KEY"
)

// APIClient provides HTTP client for LoopWorker API.
type APIClient struct {
	BaseURL string
	// APIKey is sent as the apiKeyHeader header on every request. Empty means
	// unauthenticated, which the server accepts only for its three probes.
	APIKey     string
	HTTPClient *http.Client
}

// NewAPIClient creates a new API client. An empty baseURL falls back to
// LOOPWORKER_URL, then to defaultServerURL; the credential comes from
// LOOPWORKER_API_KEY unless the caller overrides the field.
func NewAPIClient(baseURL string) *APIClient {
	if baseURL == "" {
		if envURL := os.Getenv("LOOPWORKER_URL"); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = defaultServerURL
		}
	}
	return &APIClient{
		BaseURL: baseURL,
		APIKey:  os.Getenv(apiKeyEnv),
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// do sends req and returns the response body, treating any 2xx as success.
//
// Credentials, body reading and status checking live here so that no verb can
// forget any of them. They used to be copy-pasted across Get, Post and Delete,
// which is how the client shipped without ever sending a credential.
//
// Any 2xx counts rather than a per-verb list: this package used to enumerate
// 200/201 for Post, which silently rejected the 202 that
// POST /api/v1/workflow/execute actually returns. Deciding which 2xx a route
// picks is the server's business, and getting it wrong fails silently.
func (c *APIClient) do(req *http.Request) ([]byte, error) {
	if c.APIKey != "" {
		req.Header.Set(apiKeyHeader, c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s failed: %w", req.Method, req.URL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", req.URL, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%s %s: server returned %d: %s\n"+
			"  cause: the server requires an API key and the request carried none it accepted (expected in the %s header).\n"+
			"  fix:   export LOOPWORKER_API_KEY=<key>, or run `loopworker doctor` to see which keys the server loaded and what role they have.\n"+
			"  docs:  README.md#authentication",
			req.Method, req.URL, resp.StatusCode, strings.TrimSpace(string(body)), apiKeyHeader)
	}
	return nil, fmt.Errorf("%s %s: server returned %d: %s",
		req.Method, req.URL, resp.StatusCode, strings.TrimSpace(string(body)))
}

// Get performs a GET request.
func (c *APIClient) Get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	return c.do(req)
}

// Post performs a POST request.
func (c *APIClient) Post(path string, data interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

// Delete performs a DELETE request.
func (c *APIClient) Delete(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodDelete, c.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	return c.do(req)
}

// envelope mirrors the reply shape every route uses. It is redeclared rather
// than imported from pkg/api: a client should not depend on the server, and
// four wire fields are not worth dragging the whole package into every CLI.
type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// decode unwraps the response envelope into target.
//
// Every route answers {"success":true,"data":...,"request_id":...}. The old
// code unmarshalled the whole body straight into the caller's type, so list
// routes failed outright (a JSON object cannot fill a slice) and single-record
// routes succeeded while silently returning the envelope instead of the record
// - GetTask(taskID)["id"] was always nil. Every httptest fixture answered with
// bare JSON, so no test noticed. This is the same root cause as the missing
// API key header: the package was written against a server that never existed.
func decode(body []byte, target interface{}) error {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("unmarshal response envelope: %w", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		if env.Error != nil {
			return fmt.Errorf("server reported %s: %s (request_id %s)", env.Error.Code, env.Error.Message, env.RequestID)
		}
		return fmt.Errorf("response carried no data member (request_id %s)", env.RequestID)
	}
	if err := json.Unmarshal(env.Data, target); err != nil {
		return fmt.Errorf("unmarshal data member into %T: %w", target, err)
	}
	return nil
}

// HealthCheck checks server health.
func (c *APIClient) HealthCheck() (map[string]interface{}, error) {
	body, err := c.Get("/api/v1/health")
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := decode(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// ListTasks lists tasks with optional filters.
func (c *APIClient) ListTasks(state, taskType string, limit int) ([]map[string]interface{}, error) {
	path := fmt.Sprintf("/api/v1/tasks?limit=%d", limit)
	if state != "" {
		path += "&state=" + state
	}
	if taskType != "" {
		path += "&type=" + taskType
	}

	body, err := c.Get(path)
	if err != nil {
		return nil, err
	}

	// The list route answers a page object, not a bare array: {"tasks":[...],
	// "total":N,"has_more":bool,...}.
	var page struct {
		Tasks   []map[string]interface{} `json:"tasks"`
		Total   *int64                   `json:"total"`
		HasMore bool                     `json:"has_more"`
	}
	if err := decode(body, &page); err != nil {
		return nil, err
	}

	return page.Tasks, nil
}

// GetTask gets a specific task.
func (c *APIClient) GetTask(taskID string) (map[string]interface{}, error) {
	body, err := c.Get("/api/v1/tasks/" + taskID)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := decode(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// CreateTask creates a new task.
func (c *APIClient) CreateTask(taskType, input string, priority int) (map[string]interface{}, error) {
	data := map[string]interface{}{
		"type":     taskType,
		"input":    input,
		"priority": priority,
	}

	body, err := c.Post("/api/v1/tasks", data)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := decode(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// DeleteTask deletes a task.
func (c *APIClient) DeleteTask(taskID string) error {
	_, err := c.Delete("/api/v1/tasks/" + taskID)
	return err
}

// ListWorkflows lists all workflows.
func (c *APIClient) ListWorkflows() ([]map[string]interface{}, error) {
	body, err := c.Get("/api/v1/workflow/list")
	if err != nil {
		return nil, err
	}

	// {"total":N,"workflows":[...]} - not a bare array.
	var page struct {
		Total     *int64                   `json:"total"`
		Workflows []map[string]interface{} `json:"workflows"`
	}
	if err := decode(body, &page); err != nil {
		return nil, err
	}

	return page.Workflows, nil
}

// ExecuteWorkflow starts a workflow run and returns the 202 acknowledgement.
//
// input is accepted for source compatibility with older callers but is not
// sent: the endpoint's contract is {"workflow_id": ...} only, and an extra key
// is answered with 400 UNKNOWN_FIELD. Execution is asynchronous - the returned
// "poll" path is where progress and the result live.
func (c *APIClient) ExecuteWorkflow(workflowID string, input interface{}) (map[string]interface{}, error) {
	_ = input

	body, err := c.Post("/api/v1/workflow/execute", map[string]interface{}{"workflow_id": workflowID})
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := decode(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// GetMetrics gets server metrics.
//
// Deprecated: this cannot succeed against a LoopWorker server. There is no
// /api/v1/metrics route. Metrics live on the loopback admin listener
// (server.admin_port, 19528 by default), behind admin permission, and are
// served as Prometheus text - so the JSON decode below would fail even if the
// path were right. Kept only so existing callers keep compiling; deletion is a
// product decision, not this package's to make. Use a Prometheus client
// against the admin listener instead.
func (c *APIClient) GetMetrics() (map[string]interface{}, error) {
	body, err := c.Get("/api/v1/metrics")
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// GetLogs gets server logs.
//
// Deprecated: same problem as GetMetrics. There is no /api/v1/logs route; logs
// live on the admin listener (server.admin_port) and arrive inside a
// {"data":{"logs":[...]}} envelope, not as the bare array this decodes.
func (c *APIClient) GetLogs() ([]map[string]interface{}, error) {
	body, err := c.Get("/api/v1/logs")
	if err != nil {
		return nil, err
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	defaultServerURL = "http://localhost:19527"
)

// APIClient provides HTTP client for LoopWorker API.
type APIClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewAPIClient creates a new API client.
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
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// get performs a GET request.
func (c *APIClient) get(path string) ([]byte, error) {
	resp, err := c.HTTPClient.Get(c.BaseURL + path)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// post performs a POST request.
func (c *APIClient) post(path string, data interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.HTTPClient.Post(c.BaseURL+path, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// delete performs a DELETE request.
func (c *APIClient) delete(path string) ([]byte, error) {
	req, err := http.NewRequest("DELETE", c.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// HealthCheck checks server health.
func (c *APIClient) HealthCheck() (map[string]interface{}, error) {
	body, err := c.get("/api/v1/health")
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
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

	body, err := c.get(path)
	if err != nil {
		return nil, err
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// GetTask gets a specific task.
func (c *APIClient) GetTask(taskID string) (map[string]interface{}, error) {
	body, err := c.get("/api/v1/tasks/" + taskID)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
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

	body, err := c.post("/api/v1/tasks", data)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// DeleteTask deletes a task.
func (c *APIClient) DeleteTask(taskID string) error {
	_, err := c.delete("/api/v1/tasks/" + taskID)
	return err
}

// ListWorkflows lists all workflows.
func (c *APIClient) ListWorkflows() ([]map[string]interface{}, error) {
	body, err := c.get("/api/v1/workflow/list")
	if err != nil {
		return nil, err
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// ExecuteWorkflow executes a workflow.
func (c *APIClient) ExecuteWorkflow(workflowID string, input interface{}) (map[string]interface{}, error) {
	data := map[string]interface{}{
		"workflow_id": workflowID,
		"input":       input,
	}

	body, err := c.post("/api/v1/workflow/execute", data)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// GetMetrics gets server metrics.
func (c *APIClient) GetMetrics() (map[string]interface{}, error) {
	body, err := c.get("/api/v1/metrics")
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
func (c *APIClient) GetLogs() ([]map[string]interface{}, error) {
	body, err := c.get("/api/v1/logs")
	if err != nil {
		return nil, err
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestHealthCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
		json.NewEncoder(w).Encode([]map[string]interface{}{})
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

func TestCreateTask(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "task-1"})
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
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "task-1", "type": "echo"})
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

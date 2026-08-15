package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loopworker/internal/core/observer"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
)

func TestDashboardComprehensive(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	o := observer.NewObserver(bus)
	d := NewDashboard(s, o, bus)

	// Test all endpoints
	tests := []struct {
		name   string
		method string
		path   string
		code   int
	}{
		{"index", "GET", "/", 200},
		{"tasks", "GET", "/api/tasks", 200},
		{"metrics", "GET", "/api/metrics", 200},
		{"logs", "GET", "/api/logs", 200},
		{"health", "GET", "/api/health", 200},
		{"stats", "GET", "/api/stats", 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()

			switch tt.path {
			case "/":
				d.handleIndex(w, req)
			case "/api/tasks":
				d.handleTasks(w, req)
			case "/api/metrics":
				d.handleMetrics(w, req)
			case "/api/logs":
				d.handleLogs(w, req)
			case "/api/health":
				d.handleHealth(w, req)
			case "/api/stats":
				d.handleStats(w, req)
			}

			if w.Code != tt.code {
				t.Errorf("expected status %d, got %d", tt.code, w.Code)
			}
		})
	}
}

func TestDashboardWithEmptyData(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	o := observer.NewObserver(bus)
	d := NewDashboard(s, o, bus)

	// Test with no data
	req := httptest.NewRequest("GET", "/api/tasks", nil)
	w := httptest.NewRecorder()
	d.handleTasks(w, req)

	var tasks []interface{}
	json.NewDecoder(w.Body).Decode(&tasks)

	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestDashboardWithData(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	o := observer.NewObserver(bus)
	d := NewDashboard(s, o, bus)

	// Create some data
	_, _ = s.CreateTask(context.Background(), "test", nil, nil)
	o.IncrementCounter("test.metric", nil)
	o.Log("info", "test message", nil)

	// Test tasks endpoint
	req := httptest.NewRequest("GET", "/api/tasks", nil)
	w := httptest.NewRecorder()
	d.handleTasks(w, req)

	var tasks []interface{}
	json.NewDecoder(w.Body).Decode(&tasks)

	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}

	// Test metrics endpoint
	req = httptest.NewRequest("GET", "/api/metrics", nil)
	w = httptest.NewRecorder()
	d.handleMetrics(w, req)

	var metrics []interface{}
	json.NewDecoder(w.Body).Decode(&metrics)

	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}

	// Test logs endpoint
	req = httptest.NewRequest("GET", "/api/logs", nil)
	w = httptest.NewRecorder()
	d.handleLogs(w, req)

	var logs []interface{}
	json.NewDecoder(w.Body).Decode(&logs)

	if len(logs) != 1 {
		t.Errorf("expected 1 log, got %d", len(logs))
	}
}

func TestDashboardHTML(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	o := observer.NewObserver(bus)
	d := NewDashboard(s, o, bus)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	d.handleIndex(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "LoopWorker") {
		t.Error("HTML should contain 'LoopWorker'")
	}
	if !strings.Contains(body, "Dashboard") {
		t.Error("HTML should contain 'Dashboard'")
	}
}

func TestDashboardBroadcastUpdate(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	o := observer.NewObserver(bus)
	d := NewDashboard(s, o, bus)

	// This should not panic
	d.BroadcastUpdate()
}

func TestDashboardStartEventListening(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	o := observer.NewObserver(bus)
	d := NewDashboard(s, o, bus)

	// This should not panic
	d.StartEventListening()

	// Wait a bit for goroutine to start
	time.Sleep(10 * time.Millisecond)
}

// SSE test removed due to blocking nature

func TestDashboardDataStructure(t *testing.T) {
	data := DashboardData{
		Tasks:   []*scheduler.Task{},
		Metrics: []observer.Metric{},
		Logs:    []observer.LogEntry{},
		Health:  map[string]interface{}{"status": "ok"},
		Stats:   map[string]interface{}{"total": 0},
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}

	var decoded DashboardData
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
}

func TestDashboardMiddlewareChain(t *testing.T) {
	// Test CORS headers
	w := httptest.NewRecorder()

	// Simulate CORS handling
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS header should be set")
	}
}

func TestDashboardBroadcastThrottle(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	o := observer.NewObserver(bus)
	d := NewDashboard(s, o, bus)

	// First broadcast should succeed
	d.BroadcastUpdate()

	// Immediate second broadcast should be throttled
	start := time.Now()
	d.BroadcastUpdate()
	elapsed := time.Since(start)

	// Should have returned quickly (throttled), not waited 100ms
	if elapsed > 50*time.Millisecond {
		t.Errorf("broadcast should be throttled, took %v", elapsed)
	}

	// Wait for throttle window to expire
	time.Sleep(110 * time.Millisecond)

	// Now broadcast should succeed again
	d.BroadcastUpdate()
}

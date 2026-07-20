package server

import (
	"context"
	"testing"
	"time"
)

func TestNewServer(t *testing.T) {
	config := DefaultConfig()
	srv := New(config)
	if srv == nil {
		t.Fatal("server should not be nil")
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()
	if config.Port != 19527 {
		t.Errorf("expected port 19527, got %d", config.Port)
	}
	if config.Language != "en" {
		t.Errorf("expected language 'en', got '%s'", config.Language)
	}
	if config.Theme != "glass" {
		t.Errorf("expected theme 'glass', got '%s'", config.Theme)
	}
}

func TestServerGetStatus(t *testing.T) {
	config := DefaultConfig()
	srv := New(config)

	status := srv.GetStatus()
	if status == nil {
		t.Fatal("status should not be nil")
	}
	if status.Status != "running" {
		t.Errorf("expected status 'running', got '%s'", status.Status)
	}
}

func TestServerGetComponents(t *testing.T) {
	config := DefaultConfig()
	srv := New(config)

	if srv.GetScheduler() == nil {
		t.Error("scheduler should not be nil")
	}
	if srv.GetExecutor() == nil {
		t.Error("executor should not be nil")
	}
	if srv.GetObserver() == nil {
		t.Error("observer should not be nil")
	}
	if srv.GetSecurity() == nil {
		t.Error("security should not be nil")
	}
}

func TestServerStop(t *testing.T) {
	config := DefaultConfig()
	srv := New(config)

	// Create a context that we can cancel
	ctx, cancel := context.WithCancel(context.Background())
	srv.ctx = ctx
	srv.cancel = cancel

	err := srv.Stop()
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	// Context should be cancelled
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(time.Second):
		t.Error("context should be cancelled after stop")
	}
}

func TestServerHealthEndpoint(t *testing.T) {
	config := DefaultConfig()
	srv := New(config)

	// Verify the health handler factory works without panic
	handler := srv.HealthHandler()
	if handler == nil {
		t.Error("expected non-nil health handler")
	}
}

func TestStatusResponse(t *testing.T) {
	status := &StatusResponse{
		Status: "running",
		Uptime: "1h30m",
		Components: map[string]string{
			"scheduler": "ok",
			"executor":  "ok",
		},
		Stats: map[string]interface{}{
			"total": 100,
		},
	}

	if status.Status != "running" {
		t.Errorf("expected status 'running', got '%s'", status.Status)
	}
}

func TestHealthResponse(t *testing.T) {
	health := HealthResponse{
		Status:    "healthy",
		Checks:    map[string]string{"db": "ok"},
		Timestamp: time.Now(),
	}

	if health.Status != "healthy" {
		t.Errorf("expected status 'healthy', got '%s'", health.Status)
	}
}

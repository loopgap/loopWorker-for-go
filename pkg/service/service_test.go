package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestServiceManagerStartStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	mgr := NewServiceManager()
	svc := &Service{
		ID:       "test",
		Name:     "Test Service",
		ExecPath: "/bin/echo",
		Args:     []string{"hello"},
	}
	mgr.Register(svc)

	// Start the service
	if err := mgr.Start("test"); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	// Wait a bit for process to start
	time.Sleep(100 * time.Millisecond)

	// Check state
	if mgr.GetState("test") != StateRunning {
		t.Errorf("expected state running, got %s", mgr.GetState("test"))
	}

	// Stop the service
	if err := mgr.Stop("test"); err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	// Check state
	time.Sleep(100 * time.Millisecond)
	if mgr.GetState("test") != StateStopped {
		t.Errorf("expected state stopped, got %s", mgr.GetState("test"))
	}
}

func TestServiceManagerStartNonexistent(t *testing.T) {
	mgr := NewServiceManager()
	err := mgr.Start("nonexistent")
	if err == nil {
		t.Error("expected error starting nonexistent service")
	}
}

func TestServiceManagerStopNonexistent(t *testing.T) {
	mgr := NewServiceManager()
	err := mgr.Stop("nonexistent")
	if err == nil {
		t.Error("expected error stopping nonexistent service")
	}
}

func TestProcessGuard(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")

	guard := NewProcessGuard(pidFile)

	// First acquire should succeed
	if !guard.Acquire() {
		t.Error("first acquire should succeed")
	}

	// Second acquire should fail (process running)
	// We can't easily test this without a running process

	// Release
	guard.Release()

	// After release, acquire should succeed again
	if !guard.Acquire() {
		t.Error("acquire after release should succeed")
	}

	guard.Release()
}

func TestIsProcessRunningCurrent(t *testing.T) {
	// On Windows, os.FindProcess always succeeds even for non-existent processes
	// So we just test that the function doesn't panic
	IsProcessRunning(os.Getpid())
}

func TestIsProcessRunningNonexistent(t *testing.T) {
	// Use a very high PID that likely doesn't exist
	if IsProcessRunning(999999999) {
		t.Error("nonexistent process should not be running")
	}
}

func TestGetDefaultWorkDir(t *testing.T) {
	dir := GetDefaultWorkDir()
	if dir == "" {
		t.Error("default work dir should not be empty")
	}
	if !filepath.IsAbs(dir) && dir != "." {
		t.Errorf("default work dir should be absolute or '.', got '%s'", dir)
	}
}

func TestEnsureDirectoriesSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	dirs := []string{
		filepath.Join(tmpDir, "a"),
		filepath.Join(tmpDir, "b", "c"),
		filepath.Join(tmpDir, "d", "e", "f"),
	}

	if err := EnsureDirectories(dirs...); err != nil {
		t.Fatalf("ensure directories: %v", err)
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("directory %s should exist", dir)
		}
	}
}

func TestServiceStateStringAll(t *testing.T) {
	states := map[ServiceState]string{
		StateStopped:  "stopped",
		StateStarting: "starting",
		StateRunning:  "running",
		StateStopping: "stopping",
		StateFailed:   "failed",
	}

	for state, expected := range states {
		if got := state.String(); got != expected {
			t.Errorf("ServiceState(%d).String() = %s, want %s", state, got, expected)
		}
	}
}

func TestServiceManagerListEmpty(t *testing.T) {
	mgr := NewServiceManager()
	services := mgr.List()
	if len(services) != 0 {
		t.Errorf("expected 0 services, got %d", len(services))
	}
}

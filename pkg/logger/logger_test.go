package logger

import (
	"testing"

	"go.uber.org/zap"
)

func TestInit(t *testing.T) {
	// Test valid levels
	for _, level := range []string{"debug", "info", "warn", "error"} {
		if err := Init(level, false); err != nil {
			t.Errorf("Init(%s) failed: %v", level, err)
		}
	}

	// Test JSON format
	if err := Init("info", true); err != nil {
		t.Errorf("Init with JSON format failed: %v", err)
	}

	// Test default level
	if err := Init("invalid", false); err != nil {
		t.Errorf("Init with invalid level should not fail: %v", err)
	}
}

func TestGet(t *testing.T) {
	// Reset global state
	globalLogger = nil

	l := Get()
	if l == nil {
		t.Fatal("Get() should not return nil")
	}

	// Should return same instance
	l2 := Get()
	if l != l2 {
		t.Error("Get() should return same instance")
	}
}

func TestSet(t *testing.T) {
	custom := zap.NewNop()
	Set(custom)

	l := Get()
	if l != custom {
		t.Error("Set should replace global logger")
	}
}

func TestWith(t *testing.T) {
	l := With(zap.String("key", "value"))
	if l == nil {
		t.Fatal("With() should not return nil")
	}
}

func TestNamed(t *testing.T) {
	l := Named("test")
	if l == nil {
		t.Fatal("Named() should not return nil")
	}
}

func TestLoggingFunctions(t *testing.T) {
	// These should not panic
	Debug("debug message")
	Info("info message")
	Warn("warn message")
	Error("error message")
}

func TestSync(t *testing.T) {
	// Should not panic
	Sync()
}

func TestConcurrent(t *testing.T) {
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			Info("concurrent message")
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

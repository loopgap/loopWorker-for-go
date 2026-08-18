package errors

import (
	"testing"
)

func TestSentinelErrors(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrTaskNotFound", ErrTaskNotFound},
		{"ErrTaskAlreadyExists", ErrTaskAlreadyExists},
		{"ErrTaskTimeout", ErrTaskTimeout},
		{"ErrWorkerNotFound", ErrWorkerNotFound},
		{"ErrPluginNotFound", ErrPluginNotFound},
		{"ErrPluginTimeout", ErrPluginTimeout},
		{"ErrWorkflowNotFound", ErrWorkflowNotFound},
		{"ErrWorkflowCycle", ErrWorkflowCycle},
		{"ErrUnauthorized", ErrUnauthorized},
		{"ErrForbidden", ErrForbidden},
		{"ErrTokenExpired", ErrTokenExpired},
		{"ErrCircuitOpen", ErrCircuitOpen},
		{"ErrSandboxTimeout", ErrSandboxTimeout},
		{"ErrQueueFull", ErrQueueFull},
		{"ErrRateLimited", ErrRateLimited},
	}

	for _, s := range sentinels {
		if s.err == nil {
			t.Errorf("%s should not be nil", s.name)
		}
		if s.err.Error() == "" {
			t.Errorf("%s should have non-empty error message", s.name)
		}
	}
}

func TestWrap(t *testing.T) {
	base := New("base error")
	wrapped := Wrap(base, "context")

	if wrapped == nil {
		t.Fatal("Wrap should not return nil")
	}
	if !Is(wrapped, base) {
		t.Error("wrapped error should contain base error")
	}

	// Wrap nil should return nil
	if Wrap(nil, "context") != nil {
		t.Error("Wrap(nil) should return nil")
	}
}

func TestWrapf(t *testing.T) {
	base := New("base error")
	wrapped := Wrapf(base, "context %d", 42)

	if wrapped == nil {
		t.Fatal("Wrapf should not return nil")
	}
	if !Is(wrapped, base) {
		t.Error("wrapped error should contain base error")
	}

	// Wrapf nil should return nil
	if Wrapf(nil, "context %d", 42) != nil {
		t.Error("Wrapf(nil) should return nil")
	}
}

func TestIs(t *testing.T) {
	base := New("base")
	wrapped := Wrap(base, "context")

	if !Is(wrapped, base) {
		t.Error("Is should return true for wrapped error")
	}
	if Is(New("other"), base) {
		t.Error("Is should return false for different error")
	}
}

func TestNew(t *testing.T) {
	err := New("test error")
	if err == nil {
		t.Fatal("New should not return nil")
	}
	if err.Error() != "test error" {
		t.Errorf("expected 'test error', got '%s'", err.Error())
	}
}

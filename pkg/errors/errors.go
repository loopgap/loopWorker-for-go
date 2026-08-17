package errors

import (
	"errors"
	"fmt"
)

// Sentinel errors for the application.
var (
	// Task errors
	ErrTaskNotFound      = errors.New("task not found")
	ErrTaskAlreadyExists = errors.New("task already exists")
	ErrTaskInvalid       = errors.New("invalid task")
	ErrTaskTimeout       = errors.New("task execution timeout")
	ErrTaskCancelled     = errors.New("task cancelled")
	ErrTaskFailed        = errors.New("task failed")

	// Worker errors
	ErrWorkerNotFound  = errors.New("worker not found")
	ErrWorkerBusy      = errors.New("worker is busy")
	ErrWorkerStopped   = errors.New("worker is stopped")
	ErrWorkerDuplicate = errors.New("worker already exists")

	// Plugin errors
	ErrPluginNotFound  = errors.New("plugin not found")
	ErrPluginLoaded    = errors.New("plugin already loaded")
	ErrPluginFailed    = errors.New("plugin execution failed")
	ErrPluginTimeout   = errors.New("plugin execution timeout")
	ErrPluginInvalid   = errors.New("invalid plugin")

	// Workflow errors
	ErrWorkflowNotFound    = errors.New("workflow not found")
	ErrWorkflowInvalid     = errors.New("invalid workflow")
	ErrWorkflowFailed      = errors.New("workflow execution failed")
	ErrWorkflowCycle       = errors.New("workflow has circular dependency")
	ErrWorkflowStepFailed  = errors.New("workflow step failed")

	// Scheduler errors
	ErrQueueFull      = errors.New("task queue is full")
	ErrQueueEmpty     = errors.New("task queue is empty")
	ErrSchedulerDown  = errors.New("scheduler is not running")

	// Security errors
	ErrUnauthorized   = errors.New("unauthorized")
	ErrForbidden      = errors.New("forbidden")
	ErrTokenExpired   = errors.New("token expired")
	ErrTokenInvalid   = errors.New("invalid token")
	ErrUserExists     = errors.New("user already exists")
	ErrUserNotFound   = errors.New("user not found")
	ErrInvalidPassword = errors.New("invalid password")
	ErrRateLimited    = errors.New("rate limited")
	ErrAccountLocked  = errors.New("account locked")

	// System errors
	ErrNotRunning     = errors.New("system is not running")
	ErrAlreadyRunning = errors.New("system is already running")
	ErrConfigInvalid  = errors.New("invalid configuration")
	ErrDatabaseError  = errors.New("database error")
	ErrNetworkError   = errors.New("network error")

	// Circuit breaker errors
	ErrCircuitOpen    = errors.New("circuit breaker is open")
	ErrCircuitHalfOpen = errors.New("circuit breaker is half-open")

	// Sandbox errors
	ErrSandboxTimeout   = errors.New("sandbox execution timeout")
	ErrSandboxOversized = errors.New("output exceeds size limit")
	ErrSandboxPanic     = errors.New("plugin panicked")
)

// Wrap wraps an error with additional context.
func Wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// Wrapf wraps an error with formatted context.
func Wrapf(err error, format string, args ...interface{}) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}

// Is checks if err matches target.
func Is(err, target error) bool {
	return errors.Is(err, target)
}

// As finds the first error in err's chain that matches target.
func As(err error, target interface{}) bool {
	return errors.As(err, target)
}

// New creates a new error with the given message.
func New(msg string) error {
	return errors.New(msg)
}

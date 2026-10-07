package utils

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/logger"
)

// Logger is an interface that allows passing standard or custom loggers
type Logger interface {
	Printf(format string, v ...interface{})
}

// DefaultLogger is a fallback logger that prints to standard out
type DefaultLogger struct{}

func (l *DefaultLogger) Printf(format string, v ...interface{}) {
	logger.Error("panic recovered", zap.String("component", "SafeGo"), zap.Any("panic", v))
}

// globalLogger 使用atomic.Value实现并发安全的全局logger
var globalLogger atomic.Value

func init() {
	// 初始化默认logger（使用指针类型）
	var defaultLogger Logger = &DefaultLogger{}
	globalLogger.Store(&defaultLogger)
}

// SetLogger allows injecting a global logger for all SafeGo panics (并发安全)
func SetLogger(l Logger) {
	if l != nil {
		globalLogger.Store(&l)
	}
}

// getLogger 获取当前logger（并发安全）
func getLogger() Logger {
	if l, ok := globalLogger.Load().(*Logger); ok && l != nil {
		return *l
	}
	var fallback Logger = &DefaultLogger{}
	return fallback
}

// PanicError reports a panic that was recovered by a guarded goroutine.
// It is returned as an error (never re-raised) so callers can classify a crash
// separately from a timeout or an ordinary failure.
type PanicError struct {
	Value any    // the value passed to panic
	Stack string // stack trace captured at recovery
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("panic recovered: %v\nstack trace: %s", e.Value, e.Stack)
}

// IsPanic reports whether err, or any error wrapped by it, is a recovered panic.
func IsPanic(err error) bool {
	var pe *PanicError
	return errors.As(err, &pe)
}

// Result carries the outcome of a guarded goroutine: a value, or an error that
// may be a *PanicError.
type Result[T any] struct {
	Value T
	Err   error
}

// GoSafeE runs fn in a new goroutine and delivers exactly one Result on the
// returned channel. Unlike GoSafe, a panic is not swallowed: it is delivered as
// a *PanicError so the caller can return immediately and classify the failure.
// The channel is buffered, so the guarded goroutine never blocks even if the
// caller abandons it after a timeout.
func GoSafeE[T any](ctx context.Context, fn func(context.Context) (T, error)) <-chan Result[T] {
	ch := make(chan Result[T], 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := &PanicError{Value: r, Stack: string(debug.Stack())}
				getLogger().Printf("%v", err)
				ch <- Result[T]{Err: err}
			}
		}()

		value, err := fn(ctx)
		ch <- Result[T]{Value: value, Err: err}
	}()

	return ch
}

// GoSafe runs the provided function in a new goroutine and recovers from panics.
// This is the fundamental anti-crash primitive for the entire project.
// Panics are logged but not reported to any caller; use GoSafeE when the caller
// must observe the panic.
func GoSafe(ctx context.Context, fn func(context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := &PanicError{Value: r, Stack: string(debug.Stack())}
				getLogger().Printf("%v", err)
				// In a full integration, we would also emit an event to the EventBus or SelfHealer here
			}
		}()
		fn(ctx)
	}()
}

// GoSafeWithTimeout runs the provided function in a new goroutine with timeout protection.
// If the function execution exceeds the specified timeout, it will be cancelled via context.
// Returns a channel that receives the error (nil on success, error on panic/timeout).
func GoSafeWithTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context)) <-chan error {
	errCh := make(chan error, 1)

	// 创建超时context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)

	// 在独立goroutine中执行函数，带panic恢复
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := &PanicError{Value: r, Stack: string(debug.Stack())}
				getLogger().Printf("%v", err)
				cancel()
				errCh <- err
			}
		}()

		fn(timeoutCtx)
		close(done)
	}()

	// 监控超时
	go func() {
		select {
		case <-done:
			cancel()
			errCh <- nil
		case <-timeoutCtx.Done():
			if timeoutCtx.Err() == context.DeadlineExceeded {
				errCh <- fmt.Errorf("%w: after %v", lwerrors.ErrTaskTimeout, timeout)
			} else {
				errCh <- timeoutCtx.Err()
			}
		}
	}()

	return errCh
}

// GoSafeWithResult runs a function that returns a result in a goroutine with panic recovery.
// Returns channels for result and error.
func GoSafeWithResult[T any](ctx context.Context, fn func(context.Context) (T, error)) (<-chan T, <-chan error) {
	resultCh := make(chan T, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(resultCh)
		defer close(errCh)

		defer func() {
			if r := recover(); r != nil {
				err := &PanicError{Value: r, Stack: string(debug.Stack())}
				getLogger().Printf("%v", err)
				errCh <- err
			}
		}()

		result, err := fn(ctx)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	return resultCh, errCh
}

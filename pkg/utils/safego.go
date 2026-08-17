package utils

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Logger is an interface that allows passing standard or custom loggers
type Logger interface {
	Printf(format string, v ...interface{})
}

// DefaultLogger is a fallback logger that prints to standard out
type DefaultLogger struct{}

func (l *DefaultLogger) Printf(format string, v ...interface{}) {
	fmt.Printf("[SafeGo] "+format+"\n", v...)
}

// globalLogger 使用atomic.Value实现并发安全的全局logger
var globalLogger atomic.Value

// loggerOnce 确保默认logger只初始化一次
var loggerOnce sync.Once

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
	return *globalLogger.Load().(*Logger)
}

// GoSafe runs the provided function in a new goroutine and recovers from panics.
// This is the fundamental anti-crash primitive for the entire project.
func GoSafe(ctx context.Context, fn func(context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("panic recovered: %v\nstack trace: %s", r, string(debug.Stack()))
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
				stackTrace := string(debug.Stack())
				err := fmt.Errorf("panic recovered: %v\nstack trace: %s", r, stackTrace)
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
				errCh <- fmt.Errorf("goroutine execution timed out after %v", timeout)
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
				stackTrace := string(debug.Stack())
				err := fmt.Errorf("panic recovered: %v\nstack trace: %s", r, stackTrace)
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

package utils

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lwerrors "loopworker/pkg/errors"
)

// mockLogger 用于测试的并发安全logger
type mockLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *mockLogger) Printf(format string, v ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, format)
}

func (l *mockLogger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func TestGoSafe_PanicRecovery(t *testing.T) {
	logger := &mockLogger{}
	SetLogger(logger)

	done := make(chan struct{})
	GoSafe(context.Background(), func(ctx context.Context) {
		defer close(done)
		panic("test panic")
	})

	<-done
	time.Sleep(50 * time.Millisecond) // 等待goroutine完成

	if logger.Len() != 1 {
		t.Errorf("expected 1 log entry, got %d", logger.Len())
	}
}

func TestGoSafe_NormalExecution(t *testing.T) {
	done := make(chan struct{})
	GoSafe(context.Background(), func(ctx context.Context) {
		defer close(done)
		// 正常执行，无panic
	})

	select {
	case <-done:
		// 正常完成
	case <-time.After(time.Second):
		t.Fatal("goroutine did not complete in time")
	}
}

func TestGoSafeWithTimeout_Success(t *testing.T) {
	errCh := GoSafeWithTimeout(context.Background(), time.Second, func(ctx context.Context) {
		// 快速完成
	})

	err := <-errCh
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestGoSafeWithTimeout_Timeout(t *testing.T) {
	start := time.Now()
	errCh := GoSafeWithTimeout(context.Background(), 100*time.Millisecond, func(ctx context.Context) {
		// 模拟长时间执行
		time.Sleep(time.Second)
	})

	err := <-errCh
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestGoSafeWithTimeout_Panic(t *testing.T) {
	logger := &mockLogger{}
	SetLogger(logger)

	errCh := GoSafeWithTimeout(context.Background(), time.Second, func(ctx context.Context) {
		panic("test panic in timeout")
	})

	err := <-errCh
	if err == nil {
		t.Fatal("expected panic error, got nil")
	}

	if logger.Len() != 1 {
		t.Errorf("expected 1 log entry, got %d", logger.Len())
	}
}

func TestSetLogger_ConcurrentSafety(t *testing.T) {
	var wg sync.WaitGroup
	logger1 := &mockLogger{}
	logger2 := &mockLogger{}

	// 并发设置logger
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				SetLogger(logger1)
			} else {
				SetLogger(logger2)
			}
		}(i)
	}

	wg.Wait()

	// 验证logger仍然可用
	currentLogger := getLogger()
	if currentLogger == nil {
		t.Fatal("logger should not be nil after concurrent access")
	}
}

func TestGoSafe_ConcurrentExecution(t *testing.T) {
	var counter atomic.Int64
	var wg sync.WaitGroup

	// 并发执行多个goroutine
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		GoSafe(context.Background(), func(ctx context.Context) {
			defer wg.Done()
			counter.Add(1)
		})
	}

	wg.Wait()

	if counter.Load() != 1000 {
		t.Errorf("expected 1000 executions, got %d", counter.Load())
	}
}

func TestGoSafeWithResult_Success(t *testing.T) {
	expected := "test result"
	resultCh, errCh := GoSafeWithResult(context.Background(), func(ctx context.Context) (string, error) {
		return expected, nil
	})

	// 等待结果或错误
	timeout := time.After(time.Second)
	for {
		select {
		case result := <-resultCh:
			if result != expected {
				t.Errorf("expected %q, got %q", expected, result)
			}
			return
		case err := <-errCh:
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// err为nil时继续等待result
		case <-timeout:
			t.Fatal("timeout waiting for result")
		}
	}
}

func TestGoSafeWithResult_Error(t *testing.T) {
	expectedErr := "test error"
	_, errCh := GoSafeWithResult(context.Background(), func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("%s", expectedErr)
	})

	select {
	case err := <-errCh:
		if err == nil || err.Error() != expectedErr {
			t.Errorf("expected error %q, got %v", expectedErr, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for error")
	}
}

func TestGoSafeE_DeliversPanic(t *testing.T) {
	res := <-GoSafeE(context.Background(), func(ctx context.Context) (string, error) {
		panic("boom")
	})

	var pe *PanicError
	if !errors.As(res.Err, &pe) {
		t.Fatalf("expected *PanicError, got %#v", res.Err)
	}
	if pe.Value != "boom" {
		t.Errorf("expected panic value 'boom', got %v", pe.Value)
	}
	if !strings.Contains(pe.Error(), "boom") || !strings.Contains(pe.Stack, "safego_test.go") {
		t.Errorf("expected panic value and stack, got %q / %q", pe.Error(), pe.Stack)
	}
	if res.Value != "" {
		t.Errorf("expected zero value on panic, got %q", res.Value)
	}
	if !IsPanic(fmt.Errorf("wrapped: %w", res.Err)) {
		t.Error("IsPanic should see a wrapped *PanicError")
	}
	if IsPanic(errors.New("plain")) {
		t.Error("IsPanic must not match an ordinary error")
	}
}

func TestGoSafeE_DeliversValueAndError(t *testing.T) {
	res := <-GoSafeE(context.Background(), func(ctx context.Context) (int, error) {
		return 42, nil
	})
	if res.Err != nil || res.Value != 42 {
		t.Errorf("expected (42, nil), got (%d, %v)", res.Value, res.Err)
	}

	sentinel := errors.New("nope")
	res = <-GoSafeE(context.Background(), func(ctx context.Context) (int, error) {
		return 7, sentinel
	})
	if !errors.Is(res.Err, sentinel) || res.Value != 7 {
		t.Errorf("expected (7, sentinel), got (%d, %v)", res.Value, res.Err)
	}
}

// A panic must not be reported as a timeout by the caller of GoSafeE.
func TestGoSafeE_PanicIsNotATimeout(t *testing.T) {
	start := time.Now()
	res := <-GoSafeE(context.Background(), func(ctx context.Context) (int, error) {
		panic("immediate crash")
	})
	if IsPanic(res.Err) == false {
		t.Fatalf("expected a panic classification, got %v", res.Err)
	}
	if errors.Is(res.Err, lwerrors.ErrTaskTimeout) {
		t.Fatal("panic must not be reported as a timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("panic took %v to report; must return immediately", elapsed)
	}
}

func TestGoSafeWithTimeout_PanicIsTyped(t *testing.T) {
	err := <-GoSafeWithTimeout(context.Background(), time.Second, func(ctx context.Context) {
		panic("typed please")
	})
	if !IsPanic(err) {
		t.Fatalf("expected *PanicError from GoSafeWithTimeout, got %#v", err)
	}
}

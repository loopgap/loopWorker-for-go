package selfheal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewSelfHealer(t *testing.T) {
	config := DefaultConfig()
	sh := NewSelfHealer(config)
	if sh == nil {
		t.Fatal("SelfHealer should not be nil")
	}
}

// fastConfig returns a SelfHealConfig suitable for unit tests — minimal delays.
func fastConfig() SelfHealConfig {
	return SelfHealConfig{
		MaxRetries:        3,
		RetryDelay:        time.Millisecond,
		MaxRetryDelay:     10 * time.Millisecond,
		CircuitThreshold:  5,
		CircuitTimeout:    10 * time.Millisecond,
		HealthInterval:    time.Second,
		IncidentRetention: time.Hour,
		HalfOpenProbes:    1,
	}
}

func TestExecuteWithRecoverySuccess(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	ctx := context.Background()

	calls := int32(0)
	err := sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestExecuteWithRecoveryRetrySuccess(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	ctx := context.Background()

	calls := int32(0)
	err := sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return errors.New("temporary error")
		}
		return nil
	})

	if err != nil {
		t.Errorf("expected no error after retries, got %v", err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestExecuteWithRecoveryExhausted(t *testing.T) {
	config := DefaultConfig()
	config.MaxRetries = 2
	sh := NewSelfHealer(config)
	ctx := context.Background()

	calls := int32(0)
	err := sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("persistent error")
	})

	if err == nil {
		t.Error("expected error after retries exhausted")
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 retries), got %d", calls)
	}
}

func TestCircuitBreakerOpen(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("test")

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}

	if cb.GetState() != CircuitOpen {
		t.Error("circuit breaker should be open after 5 failures")
	}

	if cb.AllowRequest() {
		t.Error("circuit breaker should not allow requests when open")
	}
}

func TestCircuitBreakerRecovery(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("test")
	cb.timeout = 10 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}

	time.Sleep(15 * time.Millisecond)

	if !cb.AllowRequest() {
		t.Error("circuit breaker should allow request after timeout")
	}

	if cb.GetState() != CircuitHalfOpen {
		t.Error("circuit breaker should be half-open")
	}
}

func TestCircuitBreakerReset(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("test")

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}

	cb.Reset()

	if cb.GetState() != CircuitClosed {
		t.Error("circuit breaker should be closed after reset")
	}
}

func TestHealthCheck(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	healthy := true

	sh.RegisterHealthCheck(&HealthCheck{
		Name: "test",
		Check: func(ctx context.Context) error {
			if healthy {
				return nil
			}
			return errors.New("unhealthy")
		},
		Interval: time.Second,
		Timeout:  time.Second,
	})

	status := sh.GetHealthStatus()
	if status != HealthHealthy {
		t.Errorf("expected healthy status, got %s", status)
	}
}

func TestIncidentRecording(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	ctx := context.Background()

	_ = sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error {
		return errors.New("test error")
	})

	incidents := sh.GetIncidents()
	if len(incidents) == 0 {
		t.Error("expected incidents to be recorded")
	}
}

func TestRecoveryLog(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	ctx := context.Background()

	_ = sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error {
		return nil
	})

	log := sh.GetRecoveryLog()
	if len(log) == 0 {
		t.Error("expected recovery log to have entries")
	}
}

func TestHealthStatusDegraded(t *testing.T) {
	sh := NewSelfHealer(fastConfig())

	for i := 0; i < 5; i++ {
		sh.recordIncident("test", SeverityMedium, errors.New("error"))
	}

	status := sh.GetHealthStatus()
	if status != HealthUnhealthy {
		t.Errorf("expected unhealthy status, got %s", status)
	}
}

func TestSeverityString(t *testing.T) {
	tests := []struct {
		s    Severity
		want string
	}{
		{SeverityLow, "low"},
		{SeverityMedium, "medium"},
		{SeverityHigh, "high"},
		{SeverityCritical, "critical"},
	}

	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Severity.String() = %s, want %s", got, tt.want)
		}
	}
}

func TestRecoveryActionString(t *testing.T) {
	tests := []struct {
		a    RecoveryAction
		want string
	}{
		{ActionRetry, "retry"},
		{ActionRestart, "restart"},
		{ActionFailover, "failover"},
		{ActionCircuitBreak, "circuit_break"},
		{ActionDegradate, "degradate"},
		{ActionPanic, "panic"},
	}

	for _, tt := range tests {
		if got := tt.a.String(); got != tt.want {
			t.Errorf("RecoveryAction.String() = %s, want %s", got, tt.want)
		}
	}
}

func TestCircuitBreakerClosedToOpen(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("svc")
	if cb.GetState() != CircuitClosed {
		t.Error("should start closed")
	}
	for i := 0; i < 4; i++ {
		cb.RecordFailure()
	}
	if cb.GetState() != CircuitClosed {
		t.Error("should still be closed with 4 failures")
	}
	cb.RecordFailure()
	if cb.GetState() != CircuitOpen {
		t.Error("should be open after 5 failures")
	}
}

func TestCircuitBreakerHalfOpenToClosed(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("svc")
	cb.timeout = 10 * time.Millisecond
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if cb.GetState() != CircuitOpen {
		t.Error("should be open")
	}
	time.Sleep(15 * time.Millisecond)
	cb.AllowRequest()
	if cb.GetState() != CircuitHalfOpen {
		t.Error("should be half-open")
	}
	for i := 0; i < 3; i++ {
		cb.RecordSuccess()
	}
	if cb.GetState() != CircuitClosed {
		t.Error("should be closed after 3 successes")
	}
}

func TestCircuitBreakerHalfOpenToOpen(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("svc")
	cb.timeout = 10 * time.Millisecond
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	time.Sleep(15 * time.Millisecond)
	cb.AllowRequest()
	if cb.GetState() != CircuitHalfOpen {
		t.Error("should be half-open")
	}
	cb.RecordFailure()
	if cb.GetState() != CircuitOpen {
		t.Error("should be open again after failure in half-open")
	}
}

func TestExponentialBackoff(t *testing.T) {
	// Use default delay values for backoff calculation tests
	sh := NewSelfHealer(DefaultConfig())
	tests := []struct {
		attempt int
		wantMin time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
	}
	for _, tt := range tests {
		got := sh.calculateBackoff(tt.attempt)
		if got < tt.wantMin {
			t.Errorf("attempt %d: expected >= %v, got %v", tt.attempt, tt.wantMin, got)
		}
	}
}

func TestBackoffMaxDelay(t *testing.T) {
	config := DefaultConfig()
	config.RetryDelay = time.Second
	config.MaxRetryDelay = 10 * time.Second
	sh := NewSelfHealer(config)
	delay := sh.calculateBackoff(100)
	if delay > config.MaxRetryDelay {
		t.Errorf("delay %v exceeds max %v", delay, config.MaxRetryDelay)
	}
}

func TestExecuteWithRecoveryContextCancelled(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error { return errors.New("should not matter") })
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestHealthStatusCritical(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	sh.recordIncident("test", SeverityCritical, errors.New("critical"))
	status := sh.GetHealthStatus()
	if status != HealthCritical {
		t.Errorf("expected critical, got %s", status)
	}
}

func TestHealthStatusDegradedFewIncidents(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	sh.recordIncident("test", SeverityLow, errors.New("low"))
	sh.recordIncident("test", SeverityMedium, errors.New("med"))
	status := sh.GetHealthStatus()
	if status != HealthDegraded {
		t.Errorf("expected degraded, got %s", status)
	}
}

func TestHealthStatusString(t *testing.T) {
	tests := []struct {
		s    HealthStatus
		want string
	}{
		{HealthHealthy, "healthy"}, {HealthDegraded, "degraded"}, {HealthUnhealthy, "unhealthy"}, {HealthCritical, "critical"}, {HealthStatus(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("HealthStatus(%d).String() = %s, want %s", tt.s, got, tt.want)
		}
	}
}

func TestExecuteWithRecoveryCircuitBreakerOpen(t *testing.T) {
	config := fastConfig()
	config.CircuitThreshold = 2
	sh := NewSelfHealer(config)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = sh.ExecuteWithRecovery(ctx, "svc", func(ctx context.Context) error { return errors.New("fail") })
	}
	err := sh.ExecuteWithRecovery(ctx, "svc", func(ctx context.Context) error { return nil })
	if err == nil {
		t.Error("expected circuit breaker open error")
	}
}

// 并发压力测试

func TestConcurrentExecuteWithRecovery(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	ctx := context.Background()

	var wg sync.WaitGroup
	var successCount, failCount atomic.Int32
	n := 100

	// 并发执行带恢复的函数
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			err := sh.ExecuteWithRecovery(ctx, fmt.Sprintf("svc-%d", idx%10), func(ctx context.Context) error {
				if idx%3 == 0 {
					return errors.New("simulated error")
				}
				return nil
			})
			if err != nil {
				failCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	total := successCount.Load() + failCount.Load()
	if total != int32(n) {
		t.Errorf("expected total %d, got %d", n, total)
	}
	t.Logf("Success: %d, Failed: %d", successCount.Load(), failCount.Load())
}

func TestConcurrentCircuitBreakerOperations(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("concurrent-test")

	var wg sync.WaitGroup
	n := 100

	// 并发记录成功和失败
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				cb.RecordSuccess()
			} else {
				cb.RecordFailure()
			}
		}(i)
	}
	wg.Wait()

	// 验证熔断器状态
	state := cb.GetState()
	if state != CircuitClosed && state != CircuitOpen && state != CircuitHalfOpen {
		t.Errorf("unexpected circuit breaker state: %d", state)
	}
}

func TestConcurrentGetCircuitBreaker(t *testing.T) {
	sh := NewSelfHealer(fastConfig())

	var wg sync.WaitGroup
	breakers := make([]*CircuitBreaker, 100)

	// 并发获取熔断器
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func(idx int) {
			defer wg.Done()
			breakers[idx] = sh.GetCircuitBreaker("shared-breaker")
		}(i)
	}
	wg.Wait()

	// 验证所有获取的熔断器都是同一个实例
	for i := 1; i < 100; i++ {
		if breakers[i] != breakers[0] {
			t.Errorf("breaker %d is not the same instance as breaker 0", i)
			break
		}
	}
}

func TestConcurrentRecordIncident(t *testing.T) {
	sh := NewSelfHealer(fastConfig())

	var wg sync.WaitGroup
	n := 100

	// 并发记录事件
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			severity := Severity(idx % 4)
			sh.recordIncident(fmt.Sprintf("component-%d", idx%10), severity, errors.New("error"))
		}(i)
	}
	wg.Wait()

	// 验证事件被记录
	incidents := sh.GetIncidents()
	if len(incidents) != n {
		t.Errorf("expected %d incidents, got %d", n, len(incidents))
	}
}

func TestConcurrentHealthStatus(t *testing.T) {
	sh := NewSelfHealer(fastConfig())

	var wg sync.WaitGroup
	n := 100

	// 并发记录事件和读取健康状态
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				sh.recordIncident("test", SeverityMedium, errors.New("error"))
			}
			_ = sh.GetHealthStatus()
		}(i)
	}
	wg.Wait()

	// 最终健康状态应该是degraded或unhealthy
	status := sh.GetHealthStatus()
	if status == HealthHealthy {
		t.Error("expected health status to be degraded or unhealthy after incidents")
	}
}

func TestConcurrentRecoveryLog(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	ctx := context.Background()

	var wg sync.WaitGroup
	n := 50

	// 并发执行带恢复的函数并读取恢复日志
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			_ = sh.ExecuteWithRecovery(ctx, "test", func(ctx context.Context) error {
				if idx%2 == 0 {
					return errors.New("error")
				}
				return nil
			})
			_ = sh.GetRecoveryLog()
		}(i)
	}
	wg.Wait()

	// 验证恢复日志
	log := sh.GetRecoveryLog()
	if len(log) == 0 {
		t.Error("expected recovery log entries")
	}
}

func TestConcurrentAllowRequest(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	cb := sh.GetCircuitBreaker("test")

	var wg sync.WaitGroup
	n := 100
	allowed := make(chan bool, n)

	// 并发检查是否允许请求
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			allowed <- cb.AllowRequest()
		}()
	}
	wg.Wait()
	close(allowed)

	// 统计允许的请求数
	allowedCount := 0
	for a := range allowed {
		if a {
			allowedCount++
		}
	}

	// 初始状态应该是closed，所有请求都应该被允许
	if allowedCount != n {
		t.Errorf("expected all %d requests allowed, got %d", n, allowedCount)
	}
}

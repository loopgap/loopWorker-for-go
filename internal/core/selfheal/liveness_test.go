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

// probeConfig widens the half-open window so a test can observe the transition
// to half-open deterministically.
func probeConfig() SelfHealConfig {
	c := fastConfig()
	c.CircuitTimeout = 20 * time.Millisecond
	return c
}

// TestHalfOpenLimitsProbes is the F6 regression guard for the unlimited
// half-open bug: once the cooldown expires, a bounded number of probe requests
// may through, and every further request is rejected until one of them reports
// a verdict. Without the cap, a recovering-but-still-broken dependency receives
// the entire worker fleet at once.
func TestHalfOpenLimitsProbes(t *testing.T) {
	sh := NewSelfHealer(probeConfig())
	cb := sh.GetCircuitBreaker("svc")

	for i := 0; i < cb.failureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.GetState() != CircuitOpen {
		t.Fatalf("state = %v, want open", cb.GetState())
	}
	if cb.AllowRequest() {
		t.Fatal("open circuit must reject requests before the cooldown")
	}

	time.Sleep(cb.timeout + 5*time.Millisecond)

	allowed := 0
	for i := 0; i < cb.failureThreshold*4; i++ {
		if cb.AllowRequest() {
			allowed++
		}
	}
	if allowed == 0 {
		t.Fatal("half-open circuit admitted no probe at all")
	}
	if allowed > sh.config.HalfOpenProbes {
		t.Errorf("half-open admitted %d probes, want at most %d", allowed, sh.config.HalfOpenProbes)
	}
}

// TestHalfOpenProbeFailureReopensCircuit proves the probe slot is not a one-shot
// free-for-all: a failed probe must immediately re-open the circuit so the next
// caller is rejected again.
func TestHalfOpenProbeFailureReopensCircuit(t *testing.T) {
	sh := NewSelfHealer(probeConfig())
	cb := sh.GetCircuitBreaker("svc")

	for i := 0; i < cb.failureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(cb.timeout + 5*time.Millisecond)

	if !cb.AllowRequest() {
		t.Fatal("expected one probe to be admitted after the cooldown")
	}
	cb.RecordFailure()

	if cb.GetState() != CircuitOpen {
		t.Errorf("state = %v, want open after a failed probe", cb.GetState())
	}
	if cb.AllowRequest() {
		t.Error("reopened circuit must reject the next request")
	}
}

// TestHalfOpenSuccessClosesCircuit proves the happy path: enough probe
// successes close the circuit and normal traffic resumes.
func TestHalfOpenSuccessClosesCircuit(t *testing.T) {
	cfg := probeConfig()
	// Enough probe slots for the success threshold, so the closing path is
	// reachable without a rejected probe short-circuiting the loop.
	cfg.HalfOpenProbes = 3
	sh := NewSelfHealer(cfg)
	cb := sh.GetCircuitBreaker("svc")

	for i := 0; i < cb.failureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(cb.timeout + 5*time.Millisecond)

	if !cb.AllowRequest() {
		t.Fatal("expected a probe to be admitted")
	}
	for i := 0; i < cb.successThreshold; i++ {
		if !cb.AllowRequest() {
			t.Fatalf("probe %d was rejected while the circuit was still half-open", i)
		}
		cb.RecordSuccess()
	}

	if cb.GetState() != CircuitClosed {
		t.Errorf("state = %v, want closed after %d successes", cb.GetState(), cb.successThreshold)
	}
	if !cb.AllowRequest() {
		t.Error("closed circuit must admit requests")
	}
}

// TestExecuteWithRecoveryDoesNotMultiplyRetries pins the attempt count to the
// configured MaxRetries. ExecuteWithRecovery is called once per dispatch and the
// scheduler re-queues a failed task, so an inner loop that retries on top of the
// scheduler's own retry budget multiplies the two into dozens of executions of
// the same doomed task.
func TestExecuteWithRecoveryDoesNotMultiplyRetries(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxRetries = 3
	cfg.CircuitThreshold = 1000 // keep the breaker out of this assertion
	sh := NewSelfHealer(cfg)

	var calls int32
	err := sh.ExecuteWithRecovery(context.Background(), "svc", func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("transient")
	})
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if got := atomic.LoadInt32(&calls); got != int32(cfg.MaxRetries+1) {
		t.Errorf("fn called %d times, want %d (MaxRetries+1)", got, cfg.MaxRetries+1)
	}
}

// TestExecuteWithRecoveryFailsFastOnPermanentError is the error-classification
// half of F6. A permanently broken input must not burn the whole retry budget
// and its backoff sleeps: retrying it can never succeed.
func TestExecuteWithRecoveryFailsFastOnPermanentError(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxRetries = 3
	cfg.CircuitThreshold = 1000
	sh := NewSelfHealer(cfg)

	var calls int32
	cause := fmt.Errorf("%w: bad task input", ErrPermanentFailure)
	err := sh.ExecuteWithRecovery(context.Background(), "svc", func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return cause
	})
	if err == nil {
		t.Fatal("expected the permanent error to surface")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn called %d times for a permanent error, want 1", got)
	}
	if !errors.Is(err, ErrPermanentFailure) {
		t.Errorf("error %v does not wrap ErrPermanentFailure", err)
	}
}

// TestExecuteWithRecoveryRetriesTransientError is the other half: an error that
// is not classified as permanent must still get the full retry budget.
func TestExecuteWithRecoveryRetriesTransientError(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxRetries = 2
	cfg.CircuitThreshold = 1000
	sh := NewSelfHealer(cfg)

	var calls int32
	err := sh.ExecuteWithRecovery(context.Background(), "svc", func(context.Context) error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return errors.New("temporary network blip")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected recovery on the third attempt, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("fn called %d times, want 3", got)
	}
}

// TestExecuteWithRecoveryStopsOnCancelledContext proves the retry loop honours
// cancellation instead of sleeping out the remaining backoff.
func TestExecuteWithRecoveryStopsOnCancelledContext(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxRetries = 5
	cfg.RetryDelay = time.Hour
	cfg.MaxRetryDelay = time.Hour
	cfg.CircuitThreshold = 1000
	sh := NewSelfHealer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	done := make(chan error, 1)
	go func() {
		done <- sh.ExecuteWithRecovery(ctx, "svc", func(context.Context) error {
			atomic.AddInt32(&calls, 1)
			cancel()
			return errors.New("transient")
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExecuteWithRecovery ignored context cancellation and slept out a 1h backoff")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn called %d times after cancellation, want 1", got)
	}
}

// TestHealthChecksAreExecuted is the F6 "dead code" guard: a registered health
// check must actually run and report its status, otherwise registering one is a
// no-op that gives operators false confidence.
func TestHealthChecksAreExecuted(t *testing.T) {
	cfg := fastConfig()
	cfg.HealthInterval = 5 * time.Millisecond
	sh := NewSelfHealer(cfg)

	var runs int32
	sh.RegisterHealthCheck(&HealthCheck{
		Name:     "db",
		Check:    func(context.Context) error { atomic.AddInt32(&runs, 1); return nil },
		Interval: 5 * time.Millisecond,
		Timeout:  50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sh.StartHealthChecks(ctx)
	defer stop()

	waitFor(t, time.Second, func() bool { return atomic.LoadInt32(&runs) > 0 })

	status, ok := sh.HealthStatus("db")
	if !ok {
		t.Fatal("health status for registered check db is missing")
	}
	if status.LastRun.IsZero() {
		t.Error("healthy report has no LastRun timestamp")
	}
	if status.Status != HealthHealthy {
		t.Errorf("status = %v, want healthy for a check that always passes", status.Status)
	}
}

// TestHealthCheckFailureIsRecorded proves a failing check surfaces as unhealthy
// rather than being swallowed by its own goroutine.
func TestHealthCheckFailureIsRecorded(t *testing.T) {
	cfg := fastConfig()
	cfg.HealthInterval = 5 * time.Millisecond
	sh := NewSelfHealer(cfg)

	sh.RegisterHealthCheck(&HealthCheck{
		Name:     "db",
		Check:    func(context.Context) error { return errors.New("connection refused") },
		Interval: 5 * time.Millisecond,
		Timeout:  50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sh.StartHealthChecks(ctx)
	defer stop()

	waitFor(t, 2*time.Second, func() bool {
		status, ok := sh.HealthStatus("db")
		return ok && !status.LastRun.IsZero() && status.Status != HealthHealthy
	})

	status, _ := sh.HealthStatus("db")
	if status.Error == nil || !errors.Is(status.Error, errCheckFailed) {
		t.Errorf("health error = %v, want it to wrap errCheckFailed", status.Error)
	}
}

// TestHealthCheckTimeoutIsReported proves a hung check does not wedge the
// monitor: it is aborted at its own timeout and reported unhealthy.
func TestHealthCheckTimeoutIsReported(t *testing.T) {
	cfg := fastConfig()
	cfg.HealthInterval = 5 * time.Millisecond
	sh := NewSelfHealer(cfg)

	sh.RegisterHealthCheck(&HealthCheck{
		Name:     "hang",
		Check:    func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		Interval: 5 * time.Millisecond,
		Timeout:  20 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sh.StartHealthChecks(ctx)
	defer stop()

	waitFor(t, 2*time.Second, func() bool {
		status, ok := sh.HealthStatus("hang")
		return ok && !status.LastRun.IsZero() && status.Status != HealthHealthy
	})

	status, _ := sh.HealthStatus("hang")
	if status.Error == nil || !errors.Is(status.Error, errCheckTimeout) {
		t.Errorf("health error = %v, want it to wrap errCheckTimeout", status.Error)
	}
}

// TestHealthChecksRunConcurrently proves a slow check cannot serialize the
// others, which is the whole point of registering several.
func TestHealthChecksRunConcurrently(t *testing.T) {
	cfg := fastConfig()
	cfg.HealthInterval = 5 * time.Millisecond
	sh := NewSelfHealer(cfg)

	const checks = 4
	// A registered check is re-run every Interval, so counting invocations
	// would drive the WaitGroup negative and panic. Count each check once, on
	// its first sighting.
	var seen sync.Map // health check name -> struct{}
	var ran sync.WaitGroup
	ran.Add(checks)
	for i := 0; i < checks; i++ {
		name := string(rune('a' + i))
		sh.RegisterHealthCheck(&HealthCheck{
			Name: name,
			Check: func(context.Context) error {
				if _, loaded := seen.LoadOrStore(name, struct{}{}); !loaded {
					ran.Done()
				}
				return nil
			},
			Interval: 5 * time.Millisecond,
			Timeout:  time.Second,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sh.StartHealthChecks(ctx)
	defer stop()

	done := make(chan struct{})
	go func() { ran.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("health checks appear to run serially: a slow check blocks the rest")
	}
}

// TestDefaultConfigRetriesAreBounded documents the shipped budget so a future
// config change cannot quietly reintroduce a large multiplier.
func TestDefaultConfigRetriesAreBounded(t *testing.T) {
	c := DefaultConfig()
	if c.MaxRetries < 0 || c.MaxRetries > 5 {
		t.Errorf("MaxRetries = %d, want 0..5 so total attempts stay small", c.MaxRetries)
	}
	if c.HalfOpenProbes <= 0 {
		t.Errorf("HalfOpenProbes = %d, want a positive cap", c.HalfOpenProbes)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

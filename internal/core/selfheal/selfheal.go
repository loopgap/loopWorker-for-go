// Package selfheal implements resilience patterns: Circuit Breaker, Exponential Backoff, and Health Checks.
//
// Architecture Pattern: Circuit Breaker (Michael Nygard, "Release It!")
// ====================================================================
// The circuit breaker prevents cascading failures by monitoring error rates:
//
//	CLOSED ──(failures ≥ threshold)──► OPEN ──(timeout expires)──► HALF-OPEN
//	  ▲                                    │                          │
//	  │                                    │                          │
//	  └──(successes ≥ threshold)───────────┘────(failure)─────────────┘
//
// States:
//   - CLOSED: Normal operation. Failures are counted.
//   - OPEN: All requests are rejected immediately. Prevents hammering a failing service.
//   - HALF-OPEN: A probe request is allowed through. If it succeeds, circuit closes.
//
// Teaching Note: Why use sync.RWMutex for CircuitBreaker?
// ========================================================
// AllowRequest() is called on EVERY request (read-heavy), while RecordFailure/Success
// are called only on completion. Using RLock for AllowRequest and Lock for Record*
// maximizes throughput under concurrent load.
//
// Teaching Note: Exponential Backoff with Jitter
// ==============================================
// The calculateBackoff function implements exponential backoff: delay doubles each retry.
// In production, you should also add jitter (random delay) to prevent thundering herd:
//
//	delay = base * 2^attempt + random(0, base)
//
// This prevents all retrying clients from hitting the server at the same instant.
package selfheal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Severity int

const (
	SeverityLow Severity = iota
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

type RecoveryAction int

const (
	ActionRetry RecoveryAction = iota
	ActionRestart
	ActionFailover
	ActionCircuitBreak
	ActionDegradate
	ActionPanic
)

type HealthStatus int

const (
	// HealthUnknown is the zero value: a check that is registered but has not
	// reported yet. It is deliberately distinct from HealthHealthy so an
	// unprobed check is not reported as working.
	HealthUnknown HealthStatus = iota
	HealthHealthy
	HealthDegraded
	HealthUnhealthy
	HealthCritical
)

func (h HealthStatus) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthDegraded:
		return "degraded"
	case HealthUnhealthy:
		return "unhealthy"
	case HealthCritical:
		return "critical"
	case HealthUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

type Incident struct {
	ID        string
	Component string
	Severity  Severity
	Error     error
	Timestamp time.Time
	Resolved  bool
	Actions   []RecoveryAction
}

// errCheckFailed marks a health check that reported a failure of its own, as
// opposed to one that merely timed out.
var errCheckFailed = errors.New("health check reported failure")

// errCheckTimeout marks a health check that did not answer within its Timeout.
var errCheckTimeout = errors.New("health check timed out")

type HealthCheck struct {
	Name     string
	Check    func(ctx context.Context) error
	Interval time.Duration
	Timeout  time.Duration
}

// HealthReport is the latest observed result of one registered health check.
type HealthReport struct {
	Name     string
	Status   HealthStatus
	Error    error
	LastRun  time.Time
	Duration time.Duration
}

type CircuitBreaker struct {
	name             string
	failureCount     int
	failureThreshold int
	successCount     int
	successThreshold int
	state            CircuitState
	lastFailure      time.Time
	timeout          time.Duration
	// halfOpenProbes counts probe slots handed out while half-open, so a
	// recovering-but-still-broken dependency is not hit by the whole fleet.
	halfOpenProbes int
	halfOpenLimit  int
	mu             sync.RWMutex
}

type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

type SelfHealer struct {
	healthChecks    map[string]*HealthCheck
	health          map[string]HealthReport
	circuitBreakers map[string]*CircuitBreaker
	incidents       []*Incident
	recoveryLog     []RecoveryEvent
	mu              sync.RWMutex
	config          SelfHealConfig
	// checksWG tracks running health checks so a monitor can be stopped and
	// drained without racing a check that is mid-flight.
	checksWG sync.WaitGroup
}

type SelfHealConfig struct {
	MaxRetries        int
	RetryDelay        time.Duration
	MaxRetryDelay     time.Duration
	CircuitThreshold  int
	CircuitTimeout    time.Duration
	HealthInterval    time.Duration
	IncidentRetention time.Duration
	// HalfOpenProbes caps how many requests may probe a half-open circuit at
	// once. Without it, AllowRequest admits everything the moment the cooldown
	// expires and a still-broken dependency takes the whole fleet again.
	HalfOpenProbes int
}

type RecoveryEvent struct {
	Timestamp time.Time
	Component string
	Action    RecoveryAction
	Success   bool
	Error     error
	Duration  time.Duration
}

func DefaultConfig() SelfHealConfig {
	return SelfHealConfig{
		MaxRetries:        3,
		RetryDelay:        time.Second,
		MaxRetryDelay:     time.Minute,
		CircuitThreshold:  5,
		CircuitTimeout:    30 * time.Second,
		HealthInterval:    10 * time.Second,
		IncidentRetention: 24 * time.Hour,
		HalfOpenProbes:    1,
	}
}

func NewSelfHealer(config SelfHealConfig) *SelfHealer {
	if config.HalfOpenProbes <= 0 {
		config.HalfOpenProbes = 1
	}
	return &SelfHealer{
		healthChecks:    make(map[string]*HealthCheck),
		health:          make(map[string]HealthReport),
		circuitBreakers: make(map[string]*CircuitBreaker),
		incidents:       make([]*Incident, 0),
		recoveryLog:     make([]RecoveryEvent, 0),
		config:          config,
	}
}

func (sh *SelfHealer) RegisterHealthCheck(check *HealthCheck) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.healthChecks[check.Name] = check
	// Seed a report so a check that has not run yet is reported unknown rather
	// than being indistinguishable from a healthy one.
	if _, seen := sh.health[check.Name]; !seen {
		sh.health[check.Name] = HealthReport{Name: check.Name, Status: HealthUnknown}
	}
}

// StartHealthChecks runs every registered check on its own interval until the
// returned stop function is called or ctx is done. Registering a check without
// starting the monitor is a no-op, so this is what makes the registry real.
// The returned func stops the monitors and waits for in-flight checks.
func (sh *SelfHealer) StartHealthChecks(ctx context.Context) (stop func()) {
	sh.mu.RLock()
	checks := make([]*HealthCheck, 0, len(sh.healthChecks))
	for _, c := range sh.healthChecks {
		checks = append(checks, c)
	}
	sh.mu.RUnlock()

	done := make(chan struct{})
	for _, check := range checks {
		sh.checksWG.Add(1)
		go func(c *HealthCheck) {
			defer sh.checksWG.Done()
			sh.runHealthCheckLoop(ctx, c, done)
		}(check)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			sh.checksWG.Wait()
		})
	}
}

func (sh *SelfHealer) runHealthCheckLoop(ctx context.Context, check *HealthCheck, done <-chan struct{}) {
	interval := check.Interval
	if interval <= 0 {
		interval = sh.config.HealthInterval
	}
	if interval <= 0 {
		interval = time.Second
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-timer.C:
		}
		sh.RunHealthCheck(ctx, check)
		timer.Reset(interval)
	}
}

// RunHealthCheck executes one check under its own timeout and records the
// result. It is exported so an operator (or a test) can force an out-of-band
// probe without waiting for the interval.
func (sh *SelfHealer) RunHealthCheck(ctx context.Context, check *HealthCheck) HealthReport {
	timeout := check.Timeout
	if timeout <= 0 {
		timeout = sh.config.HealthInterval
	}
	if timeout <= 0 {
		timeout = time.Second
	}

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	err := check.Check(checkCtx)
	elapsed := time.Since(start)

	// A check that ignored its context and only returned because we cancelled
	// it is reported as a timeout, not as its own verdict.
	if err != nil && checkCtx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%w after %s: %w", errCheckTimeout, timeout, err)
	} else if err != nil {
		err = fmt.Errorf("%w: %w", errCheckFailed, err)
	}

	report := HealthReport{
		Name:     check.Name,
		Status:   HealthHealthy,
		LastRun:  start,
		Duration: elapsed,
	}
	if err != nil {
		report.Status = HealthUnhealthy
		report.Error = err
		sh.recordIncident(check.Name, SeverityHigh, err)
	}

	sh.mu.Lock()
	sh.health[check.Name] = report
	sh.mu.Unlock()

	return report
}

// HealthStatus returns the latest report for one registered check.
func (sh *SelfHealer) HealthStatus(name string) (HealthReport, bool) {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	report, ok := sh.health[name]
	return report, ok
}

// HealthReports returns a snapshot of every check the monitor knows about.
//
// Every registered check appears, including ones that have not run yet: the
// monitor ticks on an interval, so immediately after startup the results map is
// still empty and a snapshot taken from it alone would report "nothing is being
// monitored" while three checks are registered and waiting for their first tick.
// A registered check with no result yet reads HealthUnknown, which is what it is.
func (sh *SelfHealer) HealthReports() map[string]HealthReport {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	out := make(map[string]HealthReport, len(sh.healthChecks))
	for _, check := range sh.healthChecks {
		out[check.Name] = HealthReport{Name: check.Name, Status: HealthUnknown}
	}
	for name, report := range sh.health {
		out[name] = report
	}
	return out
}

// IsRetryable classifies an error for the retry loop. A permanent failure (bad
// input, missing dependency, cancelled context) will not be fixed by trying
// again, so it must fail on the first attempt instead of burning the budget and
// its backoff sleeps. Anything else is treated as transient.
func IsRetryable(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errCheckFailed) || errors.Is(err, errCheckTimeout) {
		return false
	}
	if errors.Is(err, ErrPermanentFailure) {
		return false
	}
	return true
}

// ErrPermanentFailure lets a caller mark its own error as not worth retrying.
var ErrPermanentFailure = errors.New("permanent failure")

func (sh *SelfHealer) GetCircuitBreaker(name string) *CircuitBreaker {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if cb, exists := sh.circuitBreakers[name]; exists {
		return cb
	}

	cb := &CircuitBreaker{
		name:             name,
		failureThreshold: sh.config.CircuitThreshold,
		successThreshold: 3,
		state:            CircuitClosed,
		timeout:          sh.config.CircuitTimeout,
		halfOpenLimit:    sh.config.HalfOpenProbes,
	}
	sh.circuitBreakers[name] = cb
	return cb
}

func (sh *SelfHealer) ExecuteWithRecovery(ctx context.Context, component string, fn func(ctx context.Context) error) error {
	cb := sh.GetCircuitBreaker(component)

	if !cb.AllowRequest() {
		return fmt.Errorf("circuit breaker open for component: %s", component)
	}

	var lastErr error
	for attempt := 0; attempt <= sh.config.MaxRetries; attempt++ {
		start := time.Now()

		err := fn(ctx)
		if err == nil {
			cb.RecordSuccess()
			sh.recordRecovery(component, ActionRetry, true, nil, time.Since(start))
			return nil
		}

		lastErr = err

		// A permanent failure is the caller's verdict that retrying is
		// pointless: fail now instead of spending the budget and the backoff
		// sleeps on something that cannot change.
		if !IsRetryable(err) {
			sh.recordIncident(component, SeverityMedium, err)
			sh.recordRecovery(component, ActionRetry, false, err, time.Since(start))
			return fmt.Errorf("permanent failure for %s: %w", component, err)
		}

		cb.RecordFailure()
		sh.recordIncident(component, SeverityMedium, err)
		sh.recordRecovery(component, ActionRetry, false, err, time.Since(start))

		if attempt < sh.config.MaxRetries {
			delay := sh.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}

	return fmt.Errorf("all %d retries exhausted for %s: %w", sh.config.MaxRetries, component, lastErr)
}

func (sh *SelfHealer) GetHealthStatus() HealthStatus {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	if len(sh.incidents) == 0 {
		return HealthHealthy
	}

	recentIncidents := sh.getRecentIncidents(5 * time.Minute)
	if len(recentIncidents) == 0 {
		return HealthHealthy
	}

	criticalCount := 0
	for _, incident := range recentIncidents {
		if incident.Severity >= SeverityCritical {
			criticalCount++
		}
	}

	if criticalCount > 0 {
		return HealthCritical
	}

	if len(recentIncidents) > 3 {
		return HealthUnhealthy
	}

	return HealthDegraded
}

func (sh *SelfHealer) GetIncidents() []*Incident {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	result := make([]*Incident, len(sh.incidents))
	copy(result, sh.incidents)
	return result
}

func (sh *SelfHealer) GetRecoveryLog() []RecoveryEvent {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	result := make([]RecoveryEvent, len(sh.recoveryLog))
	copy(result, sh.recoveryLog)
	return result
}

func (sh *SelfHealer) recordIncident(component string, severity Severity, err error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	incident := &Incident{
		ID:        fmt.Sprintf("inc-%d", time.Now().UnixNano()),
		Component: component,
		Severity:  severity,
		Error:     err,
		Timestamp: time.Now(),
	}

	sh.incidents = append(sh.incidents, incident)
	sh.cleanupIncidents()
}

func (sh *SelfHealer) recordRecovery(component string, action RecoveryAction, success bool, err error, duration time.Duration) {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	event := RecoveryEvent{
		Timestamp: time.Now(),
		Component: component,
		Action:    action,
		Success:   success,
		Error:     err,
		Duration:  duration,
	}

	sh.recoveryLog = append(sh.recoveryLog, event)
}

func (sh *SelfHealer) getRecentIncidents(duration time.Duration) []*Incident {
	cutoff := time.Now().Add(-duration)
	var result []*Incident
	for _, incident := range sh.incidents {
		if incident.Timestamp.After(cutoff) {
			result = append(result, incident)
		}
	}
	return result
}

func (sh *SelfHealer) cleanupIncidents() {
	cutoff := time.Now().Add(-sh.config.IncidentRetention)
	valid := make([]*Incident, 0)
	for _, incident := range sh.incidents {
		if incident.Timestamp.After(cutoff) {
			valid = append(valid, incident)
		}
	}
	sh.incidents = valid
}

func (sh *SelfHealer) calculateBackoff(attempt int) time.Duration {
	delay := sh.config.RetryDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay > sh.config.MaxRetryDelay {
			delay = sh.config.MaxRetryDelay
			break
		}
	}
	return delay
}

func (cb *CircuitBreaker) AllowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(cb.lastFailure) > cb.timeout {
			cb.state = CircuitHalfOpen
			cb.halfOpenProbes = 1
			cb.failureCount = 0
			return true
		}
		return false
	case CircuitHalfOpen:
		// Hand out at most halfOpenLimit probe slots. Letting every caller
		// through re-creates the outage the breaker was opened to prevent.
		if cb.halfOpenProbes >= cb.halfOpenLimit {
			return false
		}
		cb.halfOpenProbes++
		return true
	default:
		return false
	}
}

// RecordSuccess reports a completed call. While half-open it releases the probe
// slot and closes the circuit once enough probes have succeeded.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.successCount++
	cb.failureCount = 0

	if cb.state == CircuitHalfOpen {
		cb.halfOpenProbes--
		if cb.successCount >= cb.successThreshold {
			cb.state = CircuitClosed
			cb.successCount = 0
			cb.halfOpenProbes = 0
		}
	}
}

// RecordFailure reports a failed call. A failure while half-open reopens the
// circuit immediately: the dependency is still broken, so further probes are
// wasted work.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailure = time.Now()
	cb.successCount = 0

	if cb.state == CircuitHalfOpen || cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitOpen
		cb.halfOpenProbes = 0
	}
}

func (cb *CircuitBreaker) GetState() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = CircuitClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenProbes = 0
}

func (ra RecoveryAction) String() string {
	switch ra {
	case ActionRetry:
		return "retry"
	case ActionRestart:
		return "restart"
	case ActionFailover:
		return "failover"
	case ActionCircuitBreak:
		return "circuit_break"
	case ActionDegradate:
		return "degradate"
	case ActionPanic:
		return "panic"
	default:
		return "unknown"
	}
}

func (s Severity) String() string {
	switch s {
	case SeverityLow:
		return "low"
	case SeverityMedium:
		return "medium"
	case SeverityHigh:
		return "high"
	case SeverityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

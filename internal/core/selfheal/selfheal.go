package selfheal

import (
	"context"
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
	HealthHealthy HealthStatus = iota
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

type HealthCheck struct {
	Name     string
	Check    func(ctx context.Context) error
	Interval time.Duration
	Timeout  time.Duration
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
	mu               sync.RWMutex
}

type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

type SelfHealer struct {
	healthChecks    map[string]*HealthCheck
	circuitBreakers map[string]*CircuitBreaker
	incidents       []*Incident
	recoveryLog     []RecoveryEvent
	mu              sync.RWMutex
	config          SelfHealConfig
}

type SelfHealConfig struct {
	MaxRetries        int
	RetryDelay        time.Duration
	MaxRetryDelay     time.Duration
	CircuitThreshold  int
	CircuitTimeout    time.Duration
	HealthInterval    time.Duration
	IncidentRetention time.Duration
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
	}
}

func NewSelfHealer(config SelfHealConfig) *SelfHealer {
	return &SelfHealer{
		healthChecks:    make(map[string]*HealthCheck),
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
}

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
			return true
		}
		return false
	case CircuitHalfOpen:
		return true
	default:
		return false
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.successCount++
	cb.failureCount = 0

	if cb.state == CircuitHalfOpen && cb.successCount >= cb.successThreshold {
		cb.state = CircuitClosed
		cb.successCount = 0
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailure = time.Now()
	cb.successCount = 0

	if cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitOpen
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

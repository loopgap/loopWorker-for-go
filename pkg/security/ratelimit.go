package security

import (
	"sync"
	"time"
)

type RateLimiter struct {
	visitors map[string]*Visitor
	mu       sync.RWMutex
	rate     int
	window   time.Duration
}

type Visitor struct {
	count    int
	lastSeen time.Time
}

func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*Visitor),
		rate:     rate,
		window:   window,
	}

	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	visitor, exists := rl.visitors[ip]
	if !exists {
		rl.visitors[ip] = &Visitor{count: 1, lastSeen: time.Now()}
		return true
	}

	if time.Since(visitor.lastSeen) > rl.window {
		visitor.count = 1
		visitor.lastSeen = time.Now()
		return true
	}

	if visitor.count >= rl.rate {
		return false
	}

	visitor.count++
	visitor.lastSeen = time.Now()
	return true
}

func (rl *RateLimiter) GetCount(ip string) int {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	visitor, exists := rl.visitors[ip]
	if !exists {
		return 0
	}
	return visitor.count
}

func (rl *RateLimiter) Reset(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.visitors, ip)
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		for ip, visitor := range rl.visitors {
			if time.Since(visitor.lastSeen) > rl.window*2 {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

type InputValidator struct {
	rules map[string]ValidationRule
}

type ValidationRule struct {
	MinLength  int
	MaxLength  int
	Pattern    string
	Required   bool
	CustomFunc func(string) bool
}

func NewInputValidator() *InputValidator {
	return &InputValidator{
		rules: make(map[string]ValidationRule),
	}
}

func (v *InputValidator) AddRule(name string, rule ValidationRule) {
	v.rules[name] = rule
}

func (v *InputValidator) Validate(name, value string) error {
	rule, exists := v.rules[name]
	if !exists {
		return nil
	}

	if rule.Required && value == "" {
		return &ValidationError{Field: name, Message: "required"}
	}

	if len(value) < rule.MinLength {
		return &ValidationError{Field: name, Message: "too short"}
	}

	if rule.MaxLength > 0 && len(value) > rule.MaxLength {
		return &ValidationError{Field: name, Message: "too long"}
	}

	if rule.CustomFunc != nil && !rule.CustomFunc(value) {
		return &ValidationError{Field: name, Message: "invalid format"}
	}

	return nil
}

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

type SecurityConfig struct {
	MaxLoginAttempts  int
	LockoutDuration   time.Duration
	TokenExpiry       time.Duration
	PasswordMinLength int
	RequireHTTPS      bool
	AllowedOrigins    []string
}

func DefaultSecurityConfig() SecurityConfig {
	return SecurityConfig{
		MaxLoginAttempts:  5,
		LockoutDuration:   15 * time.Minute,
		TokenExpiry:       24 * time.Hour,
		PasswordMinLength: 8,
		RequireHTTPS:      true,
		AllowedOrigins:    []string{"*"},
	}
}

type AccountLocker struct {
	attempts map[string]int
	lockouts map[string]time.Time
	config   SecurityConfig
	mu       sync.RWMutex
}

func NewAccountLocker(config SecurityConfig) *AccountLocker {
	return &AccountLocker{
		attempts: make(map[string]int),
		lockouts: make(map[string]time.Time),
		config:   config,
	}
}

func (al *AccountLocker) RecordAttempt(username string, success bool) {
	al.mu.Lock()
	defer al.mu.Unlock()

	if success {
		delete(al.attempts, username)
		delete(al.lockouts, username)
		return
	}

	al.attempts[username]++
	if al.attempts[username] >= al.config.MaxLoginAttempts {
		al.lockouts[username] = time.Now()
	}
}

func (al *AccountLocker) IsLocked(username string) bool {
	al.mu.Lock()
	defer al.mu.Unlock()

	lockout, exists := al.lockouts[username]
	if !exists {
		return false
	}

	if time.Since(lockout) > al.config.LockoutDuration {
		delete(al.lockouts, username)
		delete(al.attempts, username)
		return false
	}

	return true
}

func (al *AccountLocker) GetAttempts(username string) int {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.attempts[username]
}

func (al *AccountLocker) Reset(username string) {
	al.mu.Lock()
	defer al.mu.Unlock()
	delete(al.attempts, username)
	delete(al.lockouts, username)
}

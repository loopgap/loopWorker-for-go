package security

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)

	if !rl.Allow("192.168.1.1") {
		t.Error("first request should be allowed")
	}

	for i := 0; i < 4; i++ {
		if !rl.Allow("192.168.1.1") {
			t.Errorf("request %d should be allowed", i+2)
		}
	}

	if rl.Allow("192.168.1.1") {
		t.Error("request should be rate limited")
	}
}

func TestRateLimiterDifferentIPs(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)

	if !rl.Allow("192.168.1.1") {
		t.Error("first IP should be allowed")
	}

	if !rl.Allow("192.168.1.2") {
		t.Error("second IP should be allowed")
	}

	if !rl.Allow("192.168.1.1") {
		t.Error("first IP second request should be allowed")
	}

	if rl.Allow("192.168.1.1") {
		t.Error("first IP should be rate limited")
	}

	if !rl.Allow("192.168.1.2") {
		t.Error("second IP second request should be allowed")
	}
}

func TestRateLimiterReset(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)

	rl.Allow("192.168.1.1")
	rl.Allow("192.168.1.1")

	rl.Reset("192.168.1.1")

	if !rl.Allow("192.168.1.1") {
		t.Error("should be allowed after reset")
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	rl := NewRateLimiter(100, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow("192.168.1.1")
		}()
	}
	wg.Wait()

	count := rl.GetCount("192.168.1.1")
	if count != 50 {
		t.Errorf("expected 50 requests, got %d", count)
	}
}

func TestInputValidator(t *testing.T) {
	v := NewInputValidator()

	v.AddRule("username", ValidationRule{
		MinLength: 3,
		MaxLength: 20,
		Required:  true,
	})

	if err := v.Validate("username", "admin"); err != nil {
		t.Errorf("valid username should pass: %v", err)
	}

	if err := v.Validate("username", "ab"); err == nil {
		t.Error("short username should fail")
	}

	if err := v.Validate("username", ""); err == nil {
		t.Error("empty username should fail")
	}
}

func TestInputValidatorCustomRule(t *testing.T) {
	v := NewInputValidator()

	v.AddRule("email", ValidationRule{
		CustomFunc: func(s string) bool {
			return len(s) > 0 && contains(s, "@")
		},
	})

	if err := v.Validate("email", "test@example.com"); err != nil {
		t.Errorf("valid email should pass: %v", err)
	}

	if err := v.Validate("email", "invalid"); err == nil {
		t.Error("invalid email should fail")
	}
}

func TestAccountLocker(t *testing.T) {
	config := DefaultSecurityConfig()
	al := NewAccountLocker(config)

	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)

	if !al.IsLocked("user1") {
		t.Error("user should be locked after 5 failed attempts")
	}
}

func TestAccountLockerSuccess(t *testing.T) {
	config := DefaultSecurityConfig()
	al := NewAccountLocker(config)

	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", true)

	if al.IsLocked("user1") {
		t.Error("user should not be locked after successful attempt")
	}

	if al.GetAttempts("user1") != 0 {
		t.Error("attempts should be reset after success")
	}
}

func TestAccountLockerReset(t *testing.T) {
	config := DefaultSecurityConfig()
	al := NewAccountLocker(config)

	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.Reset("user1")

	if al.IsLocked("user1") {
		t.Error("user should not be locked after reset")
	}
}

func TestAccountLockerLockoutExpiry(t *testing.T) {
	config := SecurityConfig{
		MaxLoginAttempts: 3,
		LockoutDuration:  100 * time.Millisecond,
	}
	al := NewAccountLocker(config)

	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)

	if !al.IsLocked("user1") {
		t.Error("user should be locked")
	}

	time.Sleep(150 * time.Millisecond)

	if al.IsLocked("user1") {
		t.Error("user should be unlocked after lockout duration")
	}
}

func TestSecurityConfigDefaults(t *testing.T) {
	config := DefaultSecurityConfig()

	if config.MaxLoginAttempts != 5 {
		t.Errorf("expected 5 max attempts, got %d", config.MaxLoginAttempts)
	}

	if config.PasswordMinLength != 8 {
		t.Errorf("expected 8 min password length, got %d", config.PasswordMinLength)
	}

	if !config.RequireHTTPS {
		t.Error("HTTPS should be required by default")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

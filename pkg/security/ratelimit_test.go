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

// ---- Strict coverage tests ----

// TestValidationError 验证 ValidationError.Error() 方法。
func TestValidationError(t *testing.T) {
	ve := &ValidationError{Field: "username", Message: "too short"}
	if ve.Error() != "username: too short" {
		t.Errorf("expected 'username: too short', got '%s'", ve.Error())
	}
}

// TestGetCountUnknownIP 验证 GetCount 返回正确计数。
func TestGetCountUnknownIP(t *testing.T) {
	rl := NewRateLimiter(10, time.Minute)

	if rl.GetCount("1.2.3.4") != 0 {
		t.Error("expected 0 for unknown IP")
	}

	rl.Allow("1.2.3.4")
	if rl.GetCount("1.2.3.4") != 1 {
		t.Errorf("expected 1, got %d", rl.GetCount("1.2.3.4"))
	}
}

// TestResetIP 验证 Reset 清除计数。
func TestResetIP(t *testing.T) {
	rl := NewRateLimiter(10, time.Minute)
	rl.Allow("1.2.3.4")
	rl.Allow("1.2.3.4")

	rl.Reset("1.2.3.4")
	if rl.GetCount("1.2.3.4") != 0 {
		t.Errorf("expected 0 after reset, got %d", rl.GetCount("1.2.3.4"))
	}
}

// TestWindowExpiryReset 验证时间窗口过期后重置计数。
func TestWindowExpiryReset(t *testing.T) {
	rl := NewRateLimiter(5, 50*time.Millisecond)

	rl.Allow("1.2.3.4")
	if rl.GetCount("1.2.3.4") != 1 {
		t.Error("expected count 1")
	}

	time.Sleep(100 * time.Millisecond)

	if !rl.Allow("1.2.3.4") {
		t.Error("should allow after window reset")
	}
	if rl.GetCount("1.2.3.4") != 1 {
		t.Errorf("expected 1 after window reset, got %d", rl.GetCount("1.2.3.4"))
	}
}

// TestInputValidatorRequired 验证必填字段验证。
func TestInputValidatorRequired(t *testing.T) {
	v := NewInputValidator()
	v.AddRule("name", ValidationRule{Required: true})

	if err := v.Validate("name", ""); err == nil {
		t.Error("expected error for empty required field")
	}
	if err := v.Validate("name", "valid"); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestInputValidatorMinLength 验证最小长度验证。
func TestInputValidatorMinLength(t *testing.T) {
	v := NewInputValidator()
	v.AddRule("pass", ValidationRule{MinLength: 8})

	if err := v.Validate("pass", "short"); err == nil {
		t.Error("expected error for too short value")
	}
	if err := v.Validate("pass", "longpassword"); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestInputValidatorMaxLength 验证最大长度验证。
func TestInputValidatorMaxLength(t *testing.T) {
	v := NewInputValidator()
	v.AddRule("name", ValidationRule{MaxLength: 5})

	if err := v.Validate("name", "toolongname"); err == nil {
		t.Error("expected error for too long value")
	}
}

// TestInputValidatorCustomFunc 验证自定义验证函数。
func TestInputValidatorCustomFunc(t *testing.T) {
	v := NewInputValidator()
	v.AddRule("email", ValidationRule{
		CustomFunc: func(s string) bool {
			return len(s) > 0 && s[0] != '@'
		},
	})

	if err := v.Validate("email", "@invalid"); err == nil {
		t.Error("expected error for invalid email")
	}
	if err := v.Validate("email", "valid@email.com"); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestInputValidatorNoRule 验证无规则时返回 nil。
func TestInputValidatorNoRule(t *testing.T) {
	v := NewInputValidator()
	if err := v.Validate("unknown", "value"); err != nil {
		t.Errorf("expected nil for unknown field, got %v", err)
	}
}

// TestAccountLockerBasic 验证账户锁定机制。
func TestAccountLockerBasic(t *testing.T) {
	config := DefaultSecurityConfig()
	config.MaxLoginAttempts = 3
	config.LockoutDuration = 100 * time.Millisecond

	al := NewAccountLocker(config)

	for i := 0; i < 3; i++ {
		al.RecordAttempt("user1", false)
	}

	if !al.IsLocked("user1") {
		t.Error("expected user1 to be locked after 3 failures")
	}
	if al.GetAttempts("user1") != 3 {
		t.Errorf("expected 3 attempts, got %d", al.GetAttempts("user1"))
	}

	time.Sleep(150 * time.Millisecond)
	if al.IsLocked("user1") {
		t.Error("expected user1 to be unlocked after lockout duration")
	}
}

// TestAccountLockerSuccessResetsAttempts 验证成功登录重置计数。
func TestAccountLockerSuccessResetsAttempts(t *testing.T) {
	al := NewAccountLocker(DefaultSecurityConfig())

	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", true)

	if al.GetAttempts("user1") != 0 {
		t.Errorf("expected 0 after success, got %d", al.GetAttempts("user1"))
	}
	if al.IsLocked("user1") {
		t.Error("should not be locked after success")
	}
}

// TestAccountLockerManualReset 验证手动重置。
func TestAccountLockerManualReset(t *testing.T) {
	al := NewAccountLocker(DefaultSecurityConfig())

	al.RecordAttempt("user1", false)
	al.RecordAttempt("user1", false)
	al.Reset("user1")

	if al.GetAttempts("user1") != 0 {
		t.Errorf("expected 0 after reset, got %d", al.GetAttempts("user1"))
	}
}

// TestHashPasswordAndCheck 验证密码哈希和校验。
func TestHashPasswordAndCheck(t *testing.T) {
	hash := HashPassword("mypassword")
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if hash == "mypassword" {
		t.Error("hash should not equal plaintext")
	}

	if !CheckPassword("mypassword", hash) {
		t.Error("expected password to match")
	}
	if CheckPassword("wrongpassword", hash) {
		t.Error("wrong password should not match")
	}
}

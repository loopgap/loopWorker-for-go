package security

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCreateUser(t *testing.T) {
	sm := NewSecurityManager()

	user, err := sm.CreateUser("testuser", "password123", "viewer")
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	if user.Username != "testuser" {
		t.Errorf("expected username testuser, got %s", user.Username)
	}

	if user.Role.Name != "viewer" {
		t.Errorf("expected role viewer, got %s", user.Role.Name)
	}
}

func TestCreateDuplicateUser(t *testing.T) {
	sm := NewSecurityManager()

	_, err := sm.CreateUser("testuser", "password123", "viewer")
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	_, err = sm.CreateUser("testuser", "password456", "admin")
	if err == nil {
		t.Error("expected error for duplicate user")
	}
}

func TestAuthenticate(t *testing.T) {
	sm := NewSecurityManager()

	_, err := sm.CreateUser("testuser", "password123", "viewer")
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	token, err := sm.Authenticate("testuser", "password123")
	if err != nil {
		t.Fatalf("authenticate failed: %v", err)
	}

	if token.Value == "" {
		t.Error("token should not be empty")
	}
}

func TestAuthenticateInvalidUser(t *testing.T) {
	sm := NewSecurityManager()

	_, err := sm.Authenticate("nonexistent", "password")
	if err == nil {
		t.Error("expected error for invalid user")
	}
}

func TestValidateToken(t *testing.T) {
	sm := NewSecurityManager()

	_, _ = sm.CreateUser("testuser", "password123", "viewer")
	token, _ := sm.Authenticate("testuser", "password123")

	user, err := sm.ValidateToken(token.Value)
	if err != nil {
		t.Fatalf("validate token failed: %v", err)
	}

	if user.Username != "testuser" {
		t.Errorf("expected username testuser, got %s", user.Username)
	}
}

func TestValidateInvalidToken(t *testing.T) {
	sm := NewSecurityManager()

	_, err := sm.ValidateToken("invalid-token")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestAuthorize(t *testing.T) {
	sm := NewSecurityManager()

	viewer, _ := sm.CreateUser("viewer", "pass", "viewer")
	admin, _ := sm.CreateUser("admin", "pass", "admin")

	if !sm.Authorize(admin, PermRead) {
		t.Error("admin should have read permission")
	}

	if !sm.Authorize(admin, PermAdmin) {
		t.Error("admin should have admin permission")
	}

	if !sm.Authorize(viewer, PermRead) {
		t.Error("viewer should have read permission")
	}

	if sm.Authorize(viewer, PermAdmin) {
		t.Error("viewer should not have admin permission")
	}
}

func TestRevokeToken(t *testing.T) {
	sm := NewSecurityManager()

	_, _ = sm.CreateUser("testuser", "pass", "viewer")
	token, _ := sm.Authenticate("testuser", "pass")

	sm.RevokeToken(token.Value)

	_, err := sm.ValidateToken(token.Value)
	if err == nil {
		t.Error("expected error after revoke")
	}
}

func TestAuditLog(t *testing.T) {
	sm := NewSecurityManager()

	_, _ = sm.CreateUser("testuser", "pass", "viewer")
	_, _ = sm.Authenticate("testuser", "pass")

	log := sm.GetAuditLog()
	if len(log) == 0 {
		t.Error("audit log should have entries")
	}
}

func TestPasswordHash(t *testing.T) {
	hash1 := HashPassword("password")
	hash2 := HashPassword("password")

	if hash1 != hash2 {
		t.Error("same password should produce same hash")
	}

	if hash1 == HashPassword("different") {
		t.Error("different passwords should produce different hashes")
	}
}

func TestAuthenticateInvalidPassword(t *testing.T) {
	sm := NewSecurityManager()
	_, _ = sm.CreateUser("user", "correct", "viewer")
	_, err := sm.Authenticate("user", "wrong")
	if err == nil {
		t.Error("expected error for wrong password")
	}
}

func TestTokenExpiry(t *testing.T) {
	sm := NewSecurityManager()
	sm.tokenTTL = 50 * time.Millisecond
	_, _ = sm.CreateUser("user", "pass", "viewer")
	token, _ := sm.Authenticate("user", "pass")
	_, err := sm.ValidateToken(token.Value)
	if err != nil {
		t.Fatalf("token should be valid: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	_, err = sm.ValidateToken(token.Value)
	if err == nil {
		t.Error("token should be expired")
	}
}

func TestAuthorizeOperator(t *testing.T) {
	sm := NewSecurityManager()
	op, _ := sm.CreateUser("op", "pass", "operator")
	if !sm.Authorize(op, PermRead) {
		t.Error("operator should have read")
	}
	if !sm.Authorize(op, PermWrite) {
		t.Error("operator should have write")
	}
	if !sm.Authorize(op, PermExecute) {
		t.Error("operator should have execute")
	}
	if sm.Authorize(op, PermAdmin) {
		t.Error("operator should not have admin")
	}
}

func TestCreateUserInvalidRole(t *testing.T) {
	sm := NewSecurityManager()
	_, err := sm.CreateUser("user", "pass", "nonexistent")
	if err == nil {
		t.Error("expected error for invalid role")
	}
}

func TestMultipleAuthenticateAudit(t *testing.T) {
	sm := NewSecurityManager()
	_, _ = sm.CreateUser("user", "pass", "viewer")
	_, _ = sm.Authenticate("user", "pass")
	_, _ = sm.Authenticate("user", "wrong")
	_, _ = sm.Authenticate("nonexistent", "pass")
	log := sm.GetAuditLog()
	successes := 0
	failures := 0
	for _, entry := range log {
		if entry.Success {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 {
		t.Errorf("expected 1 success, got %d", successes)
	}
	if failures != 2 {
		t.Errorf("expected 2 failures, got %d", failures)
	}
}

func TestAuditLogCopy(t *testing.T) {
	sm := NewSecurityManager()
	_, _ = sm.CreateUser("user", "pass", "viewer")
	_, _ = sm.Authenticate("user", "pass")
	log := sm.GetAuditLog()
	log = append(log, AuditEntry{})
	if len(sm.GetAuditLog()) == len(log) {
		t.Error("modifying returned slice should not affect internal")
	}
}

func TestPasswordHashEmpty(t *testing.T) {
	h := HashPassword("")
	if h == "" {
		t.Error("hash of empty string should not be empty")
	}
	h2 := HashPassword("test")
	if h == h2 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestRevokeNonexistentToken(t *testing.T) {
	sm := NewSecurityManager()
	sm.RevokeToken("nonexistent") // should not panic
}

func TestDefaultRoles(t *testing.T) {
	sm := NewSecurityManager()
	admin, _ := sm.CreateUser("a", "p", "admin")
	op, _ := sm.CreateUser("o", "p", "operator")
	viewer, _ := sm.CreateUser("v", "p", "viewer")
	if !sm.Authorize(admin, PermAdmin) {
		t.Error("admin should have admin")
	}
	if !sm.Authorize(op, PermWrite) {
		t.Error("operator should have write")
	}
	if sm.Authorize(viewer, PermWrite) {
		t.Error("viewer should not have write")
	}
}

// 并发压力测试

func TestConcurrentCreateUser(t *testing.T) {
	sm := NewSecurityManager()
	var wg sync.WaitGroup
	n := 100

	// 并发创建用户
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			username := fmt.Sprintf("user-%d", idx)
			_, err := sm.CreateUser(username, "password", "viewer")
			if err != nil {
				t.Errorf("create user %s: %v", username, err)
			}
		}(i)
	}
	wg.Wait()

	// 验证用户数量
	if len(sm.users) != n {
		t.Errorf("expected %d users, got %d", n, len(sm.users))
	}
}

func TestConcurrentAuthenticate(t *testing.T) {
	sm := NewSecurityManager()

	// 预先创建用户
	for i := 0; i < 10; i++ {
		username := fmt.Sprintf("user-%d", i)
		_, _ = sm.CreateUser(username, "password", "viewer")
	}

	var wg sync.WaitGroup
	n := 100

	// 并发认证
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			username := fmt.Sprintf("user-%d", idx%10)
			_, err := sm.Authenticate(username, "password")
			if err != nil {
				t.Errorf("authenticate %s: %v", username, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestConcurrentValidateToken(t *testing.T) {
	sm := NewSecurityManager()

	// 预先创建用户并获取token
	_, _ = sm.CreateUser("user", "password", "viewer")
	token, _ := sm.Authenticate("user", "password")

	var wg sync.WaitGroup
	n := 100

	// 并发验证token
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := sm.ValidateToken(token.Value)
			if err != nil {
				t.Errorf("validate token: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentAuthorize(t *testing.T) {
	sm := NewSecurityManager()

	// 预先创建用户
	users := make([]*User, 10)
	for i := 0; i < 10; i++ {
		username := fmt.Sprintf("user-%d", i)
		user, _ := sm.CreateUser(username, "password", "viewer")
		users[i] = user
	}

	var wg sync.WaitGroup
	n := 100

	// 并发授权检查
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			user := users[idx%10]
			_ = sm.Authorize(user, PermRead)
		}(i)
	}
	wg.Wait()
}

func TestConcurrentRevokeToken(t *testing.T) {
	sm := NewSecurityManager()

	// 预先创建用户并获取token
	_, _ = sm.CreateUser("user", "password", "viewer")
	token, _ := sm.Authenticate("user", "password")

	var wg sync.WaitGroup
	n := 100

	// 并发撤销token
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			sm.RevokeToken(token.Value)
		}()
	}
	wg.Wait()
}

func TestConcurrentMixedOperations(t *testing.T) {
	sm := NewSecurityManager()
	var wg sync.WaitGroup
	n := 100

	// 并发混合操作
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			username := fmt.Sprintf("user-%d", idx%10)
			// 创建用户
			_, _ = sm.CreateUser(username, "password", "viewer")
			// 认证
			token, _ := sm.Authenticate(username, "password")
			if token != nil {
				// 验证token
				_, _ = sm.ValidateToken(token.Value)
				// 撤销token
				sm.RevokeToken(token.Value)
			}
		}(i)
	}
	wg.Wait()
}

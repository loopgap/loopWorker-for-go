package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"go.uber.org/zap"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/logger"
)

type Permission string

// maxAuditEntries bounds the in-memory audit trail (drop-oldest) so a noisy
// client cannot grow the heap without limit.
const maxAuditEntries = 10000

const (
	PermRead    Permission = "read"
	PermWrite   Permission = "write"
	PermExecute Permission = "execute"
	PermAdmin   Permission = "admin"
)

type Role struct {
	Name        string
	Permissions []Permission
}

type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         *Role
	CreatedAt    time.Time
	LastLogin    *time.Time
}

type Token struct {
	Value     string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

type AuditEntry struct {
	Timestamp time.Time
	UserID    string
	Action    string
	Resource  string
	Success   bool
	IP        string
}

type SecurityManager struct {
	users    map[string]*User
	tokens   map[string]*Token
	roles    map[string]*Role
	auditLog []AuditEntry
	mu       sync.RWMutex
	tokenTTL time.Duration
}

func NewSecurityManager() *SecurityManager {
	sm := &SecurityManager{
		users:    make(map[string]*User),
		tokens:   make(map[string]*Token),
		roles:    make(map[string]*Role),
		auditLog: make([]AuditEntry, 0),
		tokenTTL: 24 * time.Hour,
	}

	sm.initDefaultRoles()
	return sm
}

func (sm *SecurityManager) initDefaultRoles() {
	sm.roles["admin"] = &Role{
		Name:        "admin",
		Permissions: []Permission{PermRead, PermWrite, PermExecute, PermAdmin},
	}
	sm.roles["operator"] = &Role{
		Name:        "operator",
		Permissions: []Permission{PermRead, PermWrite, PermExecute},
	}
	sm.roles["viewer"] = &Role{
		Name:        "viewer",
		Permissions: []Permission{PermRead},
	}
}

func (sm *SecurityManager) CreateUser(username, password string, roleName string) (*User, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	role, ok := sm.roles[roleName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", lwerrors.ErrForbidden, roleName)
	}

	for _, u := range sm.users {
		if u.Username == username {
			return nil, fmt.Errorf("%w: %s", lwerrors.ErrUserExists, username)
		}
	}

	user := &User{
		ID:           generateID(),
		Username:     username,
		PasswordHash: HashPassword(password),
		Role:         role,
		CreatedAt:    time.Now(),
	}

	sm.users[user.ID] = user
	return user, nil
}

func (sm *SecurityManager) Authenticate(username, password string) (*Token, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var user *User
	for _, u := range sm.users {
		if u.Username == username {
			user = u
			break
		}
	}

	if user == nil {
		sm.recordAudit("", "authenticate", "auth", false, "")
		return nil, lwerrors.ErrUserNotFound
	}

	if !CheckPassword(password, user.PasswordHash) {
		sm.recordAudit(user.ID, "authenticate", "auth", false, "")
		return nil, lwerrors.ErrInvalidPassword
	}

	now := time.Now()
	user.LastLogin = &now

	token := &Token{
		Value:     generateID(),
		UserID:    user.ID,
		ExpiresAt: now.Add(sm.tokenTTL),
		CreatedAt: now,
	}

	sm.pruneExpiredLocked()
	sm.tokens[HashToken(token.Value)] = token
	sm.recordAudit(user.ID, "authenticate", "auth", true, "")
	return token, nil
}

func (sm *SecurityManager) ValidateToken(tokenValue string) (*User, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	token, ok := sm.tokens[HashToken(tokenValue)]
	if !ok {
		return nil, lwerrors.ErrTokenInvalid
	}

	if time.Now().After(token.ExpiresAt) {
		return nil, lwerrors.ErrTokenExpired
	}

	user, ok := sm.users[token.UserID]
	if !ok {
		return nil, lwerrors.ErrUserNotFound
	}

	return user, nil
}

func (sm *SecurityManager) Authorize(user *User, permission Permission) bool {
	for _, p := range user.Role.Permissions {
		if p == permission || p == PermAdmin {
			return true
		}
	}
	return false
}

func (sm *SecurityManager) RevokeToken(tokenValue string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.tokens, HashToken(tokenValue))
}

func (sm *SecurityManager) GetAuditLog() []AuditEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]AuditEntry, len(sm.auditLog))
	copy(result, sm.auditLog)
	return result
}

func (sm *SecurityManager) recordAudit(userID, action, resource string, success bool, ip string) {
	entry := AuditEntry{
		Timestamp: time.Now(),
		UserID:    userID,
		Action:    action,
		Resource:  resource,
		Success:   success,
		IP:        ip,
	}
	sm.auditLog = append(sm.auditLog, entry)
	if len(sm.auditLog) > maxAuditEntries {
		sm.auditLog = append(sm.auditLog[:0], sm.auditLog[len(sm.auditLog)-maxAuditEntries:]...)
	}
}

func (sm *SecurityManager) pruneExpiredLocked() {
	now := time.Now()
	for key, token := range sm.tokens {
		if now.After(token.ExpiresAt) {
			delete(sm.tokens, key)
		}
	}
}

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func HashPassword(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		// 如果bcrypt失败，回退到SHA-256（不推荐，但保证功能正常）
		logger.Warn("bcrypt failed, falling back to SHA-256", zap.Error(err))
		h := sha256.Sum256([]byte(password))
		return hex.EncodeToString(h[:])
	}
	return string(hash)
}

func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

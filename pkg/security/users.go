package security

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	lwerrors "loopworker/pkg/errors"
)

// AuthenticateUser verifies a username/password pair and reports the caller's
// stable subject id and role. It satisfies the UserVerifier interface consumed
// by Authenticator, which trades the result for a signed bearer token.
func (sm *SecurityManager) AuthenticateUser(username, password string) (string, string, error) {
	token, err := sm.Authenticate(username, password)
	if err != nil {
		return "", "", err
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	user, ok := sm.users[token.UserID]
	if !ok || user.Role == nil {
		return "", "", lwerrors.ErrUserNotFound
	}
	return user.ID, user.Role.Name, nil
}

// CreateUserWithRole creates a user, enforcing the configured password policy.
func (sm *SecurityManager) CreateUserWithRole(username, password, roleName string, minPasswordLength int) (*User, error) {
	if minPasswordLength > 0 && len(password) < minPasswordLength {
		return nil, fmt.Errorf("%w: password must be at least %d characters", lwerrors.ErrConfigInvalid, minPasswordLength)
	}
	return sm.CreateUser(username, password, roleName)
}

// HashToken is the SHA-256 digest used as the map key for session tokens, so a
// process dump or future on-disk store never holds a usable credential.
func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// TokenRecord is a redacted view of an issued session token.
type TokenRecord struct {
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Tokens lists issued tokens without exposing their values.
func (sm *SecurityManager) Tokens() []TokenRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	out := make([]TokenRecord, 0, len(sm.tokens))
	for _, t := range sm.tokens {
		out = append(out, TokenRecord{UserID: t.UserID, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt})
	}
	return out
}

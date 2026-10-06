package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	lwerrors "loopworker/pkg/errors"
)

// Credential formats. A plaintext API key is only ever held by the caller: the
// store keeps its SHA-256 digest, so a leaked database or config file is useless.
const (
	APIKeyPrefix      = "lwk_"
	BearerTokenPrefix = "lwt_"

	ViaAPIKey = "api_key"
	ViaBearer = "bearer"

	defaultTokenTTL     = time.Hour
	defaultAPIKeyHeader = "X-API-Key"
	// BootstrapKeyName is the display name of the ephemeral, single-process key.
	BootstrapKeyName = "ephemeral-bootstrap"
)

// Roles recognised by the API. Values match SecurityManager's built-in roles.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// Principal is the authenticated caller attached to a request context.
type Principal struct {
	Subject   string    `json:"subject"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Via       string    `json:"via"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Permissions returns the permission set granted to the principal's role.
func (p *Principal) Permissions() []Permission {
	return PermissionsForRole(p.Role)
}

// HasPermission reports whether the principal's role grants perm.
func (p *Principal) HasPermission(perm Permission) bool {
	return roleGrants(p.Role, perm)
}

func roleGrants(role string, perm Permission) bool {
	for _, granted := range PermissionsForRole(role) {
		if granted == perm || granted == PermAdmin {
			return true
		}
	}
	return false
}

// PermissionsForRole is the single source of truth for RBAC. Unknown roles get
// no permissions (fail closed).
func PermissionsForRole(role string) []Permission {
	switch role {
	case RoleAdmin:
		return []Permission{PermRead, PermWrite, PermExecute, PermAdmin}
	case RoleOperator:
		return []Permission{PermRead, PermWrite, PermExecute}
	case RoleViewer:
		return []Permission{PermRead}
	default:
		return nil
	}
}

// APIKeyRecord is a stored (hashed) API key.
type APIKeyRecord struct {
	ID        string
	Name      string
	Role      string
	KeyHash   string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Expired reports whether the key is past its expiry. Zero expiry never expires.
func (k *APIKeyRecord) Expired(now time.Time) bool {
	return !k.ExpiresAt.IsZero() && now.After(k.ExpiresAt)
}

// UserVerifier lets a password-backed user store (e.g. SecurityManager) trade
// credentials for a bearer token.
type UserVerifier interface {
	AuthenticateUser(username, password string) (subject, role string, err error)
}

// AuthConfig configures an Authenticator. Keys carry either a plaintext key
// (accepted from env only) or a hex SHA-256 digest (preferred in config files).
type AuthConfig struct {
	Keys          []APIKeyRecord
	SigningSecret []byte
	TokenTTL      time.Duration
	APIKeyHeader  string
	Users         UserVerifier
	// AllowBootstrapKey (default true) mints a random admin key when no other
	// credential is configured, so a fresh install is usable without docs while
	// still requiring a credential on every request. The plaintext is returned
	// once and kept in memory only.
	AllowBootstrapKey bool
	// Now is injectable for tests.
	Now func() time.Time
}

// DefaultAuthConfig requires credentials and enables the bootstrap key.
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		TokenTTL:          defaultTokenTTL,
		APIKeyHeader:      defaultAPIKeyHeader,
		AllowBootstrapKey: true,
		Now:               time.Now,
	}
}

// AuthFailureWriter renders authentication/authorization failures. pkg/api
// injects its JSON error envelope so every rejection looks the same.
type AuthFailureWriter func(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any)

// Authenticator validates API keys and signed bearer tokens and enforces RBAC.
type Authenticator struct {
	mu       sync.RWMutex
	byHash   map[string]*APIKeyRecord
	byID     map[string]*APIKeyRecord
	secret   []byte
	ttl      time.Duration
	header   string
	users    UserVerifier
	revoked  map[string]time.Time
	boot     *APIKeyRecord
	bootText string
	now      func() time.Time

	failure AuthFailureWriter
}

// NewAuthenticator builds an Authenticator. It fails closed: with no keys and
// bootstrap disabled it returns an error rather than an open API.
func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ttl := cfg.TokenTTL
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	header := cfg.APIKeyHeader
	if header == "" {
		header = defaultAPIKeyHeader
	}

	a := &Authenticator{
		byHash:  make(map[string]*APIKeyRecord),
		byID:    make(map[string]*APIKeyRecord),
		ttl:     ttl,
		header:  header,
		users:   cfg.Users,
		revoked: make(map[string]time.Time),
		now:     now,
	}

	secret := cfg.SigningSecret
	if len(secret) == 0 {
		if env := os.Getenv("LOOPWORKER_AUTH_SIGNING_SECRET"); env != "" {
			secret = []byte(env)
		} else {
			secret = make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return nil, fmt.Errorf("generate signing secret: %w", err)
			}
		}
	}
	a.secret = append([]byte(nil), secret...)

	for i := range cfg.Keys {
		key := cfg.Keys[i]
		if err := a.addKey(&key); err != nil {
			return nil, fmt.Errorf("api key %q: %w", key.Name, err)
		}
	}

	if len(a.byHash) == 0 && cfg.AllowBootstrapKey {
		plaintext, rec, err := GenerateAPIKey(BootstrapKeyName, RoleAdmin, time.Time{})
		if err != nil {
			return nil, fmt.Errorf("bootstrap api key: %w", err)
		}
		rec.ID = "bootstrap"
		if err := a.addKey(rec); err != nil {
			return nil, fmt.Errorf("bootstrap api key: %w", err)
		}
		a.boot = rec
		a.bootText = plaintext
	}

	if len(a.byHash) == 0 {
		return nil, fmt.Errorf("%w: no API keys configured and bootstrap key disabled", lwerrors.ErrConfigInvalid)
	}
	return a, nil
}

func (a *Authenticator) addKey(rec *APIKeyRecord) error {
	if rec.Name == "" || !validRole(rec.Role) {
		return lwerrors.ErrConfigInvalid
	}
	if rec.KeyHash == "" {
		return fmt.Errorf("missing key material: set key_hash (hex SHA-256 of a %s... key) or key", APIKeyPrefix)
	}
	if rec.ID == "" {
		rec.ID = "key-" + rec.KeyHash[:12]
	}
	if _, dup := a.byID[rec.ID]; dup {
		return fmt.Errorf("duplicate key id %q", rec.ID)
	}
	a.byHash[strings.ToLower(rec.KeyHash)] = rec
	a.byID[rec.ID] = rec
	return nil
}

func validRole(role string) bool {
	switch role {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	default:
		return false
	}
}

// ValidRole reports whether role is one of the built-in roles.
func ValidRole(role string) bool { return validRole(role) }

// Roles lists the built-in roles.
func Roles() []string { return []string{RoleAdmin, RoleOperator, RoleViewer} }

// SetFailureWriter installs the renderer used for 401/403 responses.
func (a *Authenticator) SetFailureWriter(w AuthFailureWriter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failure = w
}

// TokenTTL is the default lifetime of issued bearer tokens.
func (a *Authenticator) TokenTTL() time.Duration { return a.ttl }

// APIKeyHeader is the header name accepted for API keys.
func (a *Authenticator) APIKeyHeader() string { return a.header }

// HasCredentials reports whether any non-bootstrap credential is configured.
func (a *Authenticator) HasCredentials() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.byHash) > 0 && !(len(a.byHash) == 1 && a.boot != nil)
}

// BootstrapKey returns the ephemeral admin key (plaintext, id) minted when no
// credentials were configured. Empty string when the operator supplied keys.
func (a *Authenticator) BootstrapKey() (plaintext, keyID string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.boot == nil {
		return "", ""
	}
	return a.bootText, a.boot.ID
}

// AddKey registers a new API key and returns its plaintext once.
func (a *Authenticator) AddKey(name, role string, expiresAt time.Time) (plaintext string, rec *APIKeyRecord, err error) {
	plaintext, rec, err = GenerateAPIKey(name, role, expiresAt)
	if err != nil {
		return "", nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.addKey(rec); err != nil {
		return "", nil, err
	}
	return plaintext, rec, nil
}

// RevokeKey removes a key by id and invalidates its bearer tokens.
func (a *Authenticator) RevokeKey(keyID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.byID[keyID]
	if !ok {
		return lwerrors.ErrTokenInvalid
	}
	delete(a.byID, keyID)
	delete(a.byHash, rec.KeyHash)
	if a.boot != nil && a.boot.ID == keyID {
		a.boot = nil
		a.bootText = ""
	}
	return nil
}

// Keys lists stored keys without any key material.
func (a *Authenticator) Keys() []APIKeyRecord {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]APIKeyRecord, 0, len(a.byID))
	for _, rec := range a.byID {
		c := *rec
		c.KeyHash = ""
		out = append(out, c)
	}
	return out
}

// GenerateAPIKey returns a fresh random key plus its stored record.
func GenerateAPIKey(name, role string, expiresAt time.Time) (plaintext string, rec *APIKeyRecord, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate api key: %w", err)
	}
	plaintext = APIKeyPrefix + base64.RawURLEncoding.EncodeToString(buf)
	idBuf := make([]byte, 8)
	if _, err := rand.Read(idBuf); err != nil {
		return "", nil, fmt.Errorf("generate key id: %w", err)
	}
	rec = &APIKeyRecord{
		ID:        "key-" + hex.EncodeToString(idBuf),
		Name:      name,
		Role:      role,
		KeyHash:   HashAPIKey(plaintext),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
	}
	return plaintext, rec, nil
}

// HashAPIKey is the hex SHA-256 digest used for storage and lookup.
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// AuthenticateAPIKey resolves a plaintext API key to a principal.
func (a *Authenticator) AuthenticateAPIKey(plaintext string) (*Principal, error) {
	if plaintext == "" {
		return nil, lwerrors.ErrTokenInvalid
	}
	sum := sha256.Sum256([]byte(plaintext))
	hash := strings.ToLower(hex.EncodeToString(sum[:]))

	a.mu.RLock()
	rec := a.byHash[hash]
	now := a.now()
	a.mu.RUnlock()

	if rec == nil {
		return nil, lwerrors.ErrTokenInvalid
	}
	if rec.Expired(now) {
		return nil, lwerrors.ErrTokenExpired
	}
	return &Principal{
		Subject:   rec.ID,
		Name:      rec.Name,
		Role:      rec.Role,
		Via:       ViaAPIKey,
		ExpiresAt: rec.ExpiresAt,
	}, nil
}

type tokenClaims struct {
	Subject string `json:"sub"`
	Role    string `json:"role"`
	Issued  int64  `json:"iat"`
	Expires int64  `json:"exp"`
	ID      string `json:"jti"`
	Via     string `json:"via"`
}

// IssueToken mints a signed bearer token for a key id and role.
func (a *Authenticator) IssueToken(subject, role string, ttl time.Duration) (string, time.Time, error) {
	return a.issueToken(subject, role, ttl, ViaAPIKey)
}

func (a *Authenticator) issueToken(subject, role string, ttl time.Duration, via string) (string, time.Time, error) {
	if !validRole(role) {
		return "", time.Time{}, fmt.Errorf("%w: unknown role %q", lwerrors.ErrConfigInvalid, role)
	}
	if ttl <= 0 {
		ttl = a.ttl
	}
	now := a.now().UTC()
	exp := now.Add(ttl)

	idBuf := make([]byte, 12)
	if _, err := rand.Read(idBuf); err != nil {
		return "", time.Time{}, fmt.Errorf("generate token id: %w", err)
	}
	claims := tokenClaims{
		Subject: subject,
		Role:    role,
		Issued:  now.Unix(),
		Expires: exp.Unix(),
		ID:      hex.EncodeToString(idBuf),
		Via:     via,
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode token: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := a.sign(encoded)
	return BearerTokenPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(mac), exp, nil
}

func (a *Authenticator) sign(payload string) []byte {
	a.mu.RLock()
	secret := a.secret
	a.mu.RUnlock()
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// AuthenticateBearer verifies a signed bearer token.
func (a *Authenticator) AuthenticateBearer(raw string) (*Principal, error) {
	if !strings.HasPrefix(raw, BearerTokenPrefix) {
		return nil, lwerrors.ErrTokenInvalid
	}
	body := strings.TrimPrefix(raw, BearerTokenPrefix)
	dot := strings.LastIndex(body, ".")
	if dot <= 0 || dot == len(body)-1 {
		return nil, lwerrors.ErrTokenInvalid
	}
	encoded, sig := body[:dot], body[dot+1:]

	wantSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, lwerrors.ErrTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) > 4096 {
		return nil, lwerrors.ErrTokenInvalid
	}
	if !hmac.Equal(a.sign(encoded), wantSig) {
		return nil, lwerrors.ErrTokenInvalid
	}

	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, lwerrors.ErrTokenInvalid
	}
	now := a.now().UTC()
	if claims.Expires == 0 || now.Unix() >= claims.Expires {
		return nil, lwerrors.ErrTokenExpired
	}
	if !validRole(claims.Role) {
		return nil, lwerrors.ErrTokenInvalid
	}

	a.mu.RLock()
	_, isRevoked := a.revoked[claims.ID]
	sourceValid := true
	if claims.Via == ViaAPIKey {
		rec, known := a.byID[claims.Subject]
		sourceValid = known && !rec.Expired(now)
	}
	a.mu.RUnlock()

	if isRevoked {
		return nil, lwerrors.ErrTokenInvalid
	}
	if !sourceValid {
		return nil, lwerrors.ErrTokenExpired
	}
	return &Principal{
		Subject:   claims.Subject,
		Role:      claims.Role,
		Via:       ViaBearer,
		ExpiresAt: time.Unix(claims.Expires, 0).UTC(),
	}, nil
}

// RevokeToken invalidates a bearer token by its jti until it expires naturally.
func (a *Authenticator) RevokeToken(raw string) error {
	claims, err := a.claimsOf(raw)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked[claims.ID] = time.Unix(claims.Expires, 0)
	a.pruneRevokedLocked()
	return nil
}

func (a *Authenticator) claimsOf(raw string) (*tokenClaims, error) {
	body := strings.TrimPrefix(raw, BearerTokenPrefix)
	dot := strings.LastIndex(body, ".")
	if dot <= 0 {
		return nil, lwerrors.ErrTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil {
		return nil, lwerrors.ErrTokenInvalid
	}
	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, lwerrors.ErrTokenInvalid
	}
	return &claims, nil
}

func (a *Authenticator) pruneRevokedLocked() {
	now := a.now()
	for jti, exp := range a.revoked {
		if now.After(exp) {
			delete(a.revoked, jti)
		}
	}
}

// Login trades username/password credentials (when a UserVerifier is configured)
// for a bearer token.
func (a *Authenticator) Login(username, password string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	a.mu.RLock()
	users := a.users
	a.mu.RUnlock()
	if users == nil {
		return "", time.Time{}, fmt.Errorf("%w: password login is not configured; use an API key from LOOPWORKER_API_KEYS", lwerrors.ErrForbidden)
	}
	subject, role, err := users.AuthenticateUser(username, password)
	if err != nil {
		return "", time.Time{}, err
	}
	return a.issueToken(subject, role, ttl, "user")
}

// AuthenticateRequest resolves the caller from Authorization / API-key headers.
// It returns lwerrors.ErrUnauthorized when no credential is present and
// ErrTokenInvalid/ErrTokenExpired when a credential is present but unusable.
func (a *Authenticator) AuthenticateRequest(r *http.Request) (*Principal, error) {
	if key := r.Header.Get(a.header); key != "" {
		return a.AuthenticateAPIKey(key)
	}
	if raw := r.Header.Get("Authorization"); raw != "" {
		scheme, value, found := strings.Cut(raw, " ")
		if !found || !strings.EqualFold(scheme, "bearer") {
			return nil, lwerrors.ErrTokenInvalid
		}
		if strings.HasPrefix(value, APIKeyPrefix) {
			return a.AuthenticateAPIKey(value)
		}
		return a.AuthenticateBearer(value)
	}
	return nil, lwerrors.ErrUnauthorized
}

// Authorize checks a permission and reports the missing one.
func (a *Authenticator) Authorize(p *Principal, perm Permission) (bool, Permission) {
	if p == nil || !p.HasPermission(perm) {
		return false, perm
	}
	return true, perm
}

// ConstantTimeEqual is exported for callers that compare secrets.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

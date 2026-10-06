package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"loopworker/pkg/security"
)

// createToken trades a credential for a short-lived bearer token. Accepting the
// API key in the body keeps the key out of access logs when callers use Basic
// style flows, but the header path works too.
func (s *APIServer) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIKey   string `json:"api_key"`
		Username string `json:"username"`
		Password string `json:"password"`
		TTL      string `json:"ttl"`
		Role     string `json:"role"`
	}
	fields := []string{"api_key", "username", "password", "ttl", "role"}
	if err := decodeJSON(r, &body, fields, s.cfg.MaxBodyBytes); err != nil {
		sendError(w, r, err)
		return
	}

	ttl := s.auth.TokenTTL()
	if raw := strings.TrimSpace(body.TTL); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			sendError(w, r, &FieldError{Field: "ttl",
				Reason: "\"" + raw + "\" is not a valid duration",
				Fix:    "use a Go duration such as 15m, 1h or 24h (max " + s.auth.TokenTTL().String() + ")",
				Status: http.StatusBadRequest, Code: CodeInvalidRequest})
			return
		}
		if parsed > s.auth.TokenTTL() {
			parsed = s.auth.TokenTTL()
		}
		ttl = parsed
	}

	principal, _ := security.PrincipalFromContext(r.Context())
	callerKey := body.APIKey
	if callerKey == "" && principal != nil && principal.Via == security.ViaBearer {
		// Re-authentication is required: a short-lived bearer token may not
		// mint another one, or a stolen token would be renewable forever.
		sendError(w, r, &FieldError{Field: "api_key",
			Reason: "bearer tokens cannot mint new tokens",
			Fix:    "POST /api/v1/auth/token with {\"api_key\":\"lwk_...\"}; the " + s.auth.APIKeyHeader() + " header authenticates the request but its plaintext is never recoverable here",
			Status: http.StatusForbidden, Code: CodeForbidden})
		return
	}

	subject, role := "", ""
	if callerKey != "" {
		resolved, err := s.auth.AuthenticateAPIKey(callerKey)
		if err != nil {
			sendError(w, r, err)
			return
		}
		subject, role = resolved.Subject, resolved.Role
	} else if body.Username != "" || body.Password != "" {
		token, expiresAt, err := s.auth.Login(body.Username, body.Password, ttl)
		if err != nil {
			sendError(w, r, loginError(err))
			return
		}
		sendSuccess(w, r, map[string]any{
			"token_type":   "Bearer",
			"access_token": token,
			"expires_at":   expiresAt.UTC().Format(time.RFC3339Nano),
			"expires_in":   int(time.Until(expiresAt).Seconds()),
		}, http.StatusOK)
		return
	} else {
		sendError(w, r, &FieldError{Field: "api_key",
			Reason: "no credentials were supplied in the request body or headers",
			Fix:    "send {\"api_key\":\"lwk_...\"}; development keys are printed once at server start",
			Status: http.StatusBadRequest, Code: CodeInvalidRequest})
		return
	}

	token, expiresAt, err := s.auth.IssueToken(subject, role, ttl)
	if err != nil {
		sendError(w, r, err)
		return
	}
	sendSuccess(w, r, map[string]any{
		"token_type":   "Bearer",
		"access_token": token,
		"role":         role,
		"subject":      subject,
		"expires_at":   expiresAt.UTC().Format(time.RFC3339Nano),
		"expires_in":   int(time.Until(expiresAt).Seconds()),
	}, http.StatusOK)
}

// loginError keeps password probing uninformative but still actionable.
func loginError(err error) error {
	return &FieldError{Field: "username",
		Reason: "the username or password is incorrect, or password login is disabled in this build",
		Fix:    "use an API key with POST /api/v1/auth/token, or ask an administrator to enable the user store",
		Status: http.StatusUnauthorized,
		Code:   CodeUnauthorized,
		Cause:  err}
}

// whoami echoes the resolved caller. It is the fastest way for a customer to
// prove a credential works and see the role it maps to.
func (s *APIServer) whoami(w http.ResponseWriter, r *http.Request) {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok || principal == nil {
		sendError(w, r, &FieldError{Reason: "no authenticated caller", Fix: authHint,
			Status: http.StatusUnauthorized, Code: CodeUnauthorized})
		return
	}
	perms := make([]string, 0, len(principal.Permissions()))
	for _, perm := range principal.Permissions() {
		perms = append(perms, string(perm))
	}
	sendSuccess(w, r, map[string]any{
		"subject":     principal.Subject,
		"name":        principal.Name,
		"role":        principal.Role,
		"via":         principal.Via,
		"permissions": perms,
		"expires_at":  formatTimePtr(expiryOf(principal)),
		"request_id":  RequestIDFromContext(r.Context()),
	}, http.StatusOK)
}

func expiryOf(p *security.Principal) *time.Time {
	if p.ExpiresAt.IsZero() {
		return nil
	}
	at := p.ExpiresAt
	return &at
}

// createAPIKey lets an operator issue a credential through the API instead of
// editing config files. The plaintext is returned exactly once.
func (s *APIServer) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Role string `json:"role"`
		TTL  string `json:"ttl"`
	}
	if err := decodeJSON(r, &body, []string{"name", "role", "ttl"}, s.cfg.MaxBodyBytes); err != nil {
		sendError(w, r, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 64 {
		sendError(w, r, &FieldError{Field: "name",
			Reason: "name must be 1-64 characters",
			Fix:    "use a short label such as \"ci-deployments\"",
			Status: http.StatusBadRequest, Code: CodeInvalidRequest})
		return
	}
	role := strings.TrimSpace(body.Role)
	if role == "" {
		role = security.RoleOperator
	}
	if !security.ValidRole(role) {
		sendError(w, r, &FieldError{Field: "role",
			Reason: "\"" + role + "\" is not a role",
			Fix:    "use \"viewer\", \"operator\" or \"admin\"",
			Status: http.StatusBadRequest, Code: CodeInvalidRequest,
			Details: map[string]any{"allowed": []string{security.RoleViewer, security.RoleOperator, security.RoleAdmin}}})
		return
	}
	var expires time.Time
	if raw := strings.TrimSpace(body.TTL); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			sendError(w, r, &FieldError{Field: "ttl", Reason: "\"" + raw + "\" is not a valid duration",
				Fix: "use a Go duration such as 720h", Status: http.StatusBadRequest, Code: CodeInvalidRequest})
			return
		}
		expires = time.Now().UTC().Add(d)
	}

	plaintext, record, err := s.auth.AddKey(name, role, expires)
	if err != nil {
		sendError(w, r, err)
		return
	}
	sendSuccess(w, r, map[string]any{
		"id":         record.ID,
		"name":       record.Name,
		"role":       record.Role,
		"api_key":    plaintext,
		"expires_at": formatTimePtr(expiryPtr(record.ExpiresAt)),
		"warning":    "this is the only time the key is shown; store it in your secret manager",
	}, http.StatusCreated)
}

func expiryPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// listAPIKeys lists stored keys with the secret material removed.
func (s *APIServer) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	records := s.auth.Keys()
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"id":         record.ID,
			"name":       record.Name,
			"role":       record.Role,
			"created_at": formatTime(record.CreatedAt),
			"expires_at": formatTimePtr(expiryPtr(record.ExpiresAt)),
		})
	}
	sendSuccess(w, r, map[string]any{"keys": out, "total": len(out)}, http.StatusOK)
}

// revokeAPIKey deletes a key; its bearer tokens stop validating immediately.
func (s *APIServer) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "keyID")
	if id == "" {
		sendError(w, r, &FieldError{Field: "keyID", Reason: "key id is missing",
			Fix: "copy it from GET /api/v1/auth/keys", Status: http.StatusBadRequest, Code: CodeInvalidRequest})
		return
	}
	if id == "bootstrap" {
		sendError(w, r, &FieldError{Field: "keyID",
			Reason: "the ephemeral bootstrap key cannot be revoked through the API",
			Fix:    "configure LOOPWORKER_API_KEYS and restart; the bootstrap key disappears with the process",
			Status: http.StatusConflict, Code: CodeTaskStateConflict})
		return
	}
	if err := s.auth.RevokeKey(id); err != nil {
		sendError(w, r, &FieldError{Field: "keyID",
			Reason: "no API key with id \"" + id + "\" exists",
			Fix:    "list keys with GET /api/v1/auth/keys",
			Status: http.StatusNotFound, Code: CodeNotFound, Cause: err})
		return
	}
	sendSuccess(w, r, map[string]any{"id": id, "revoked": true}, http.StatusOK)
}

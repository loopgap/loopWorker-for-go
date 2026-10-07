package security

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	lwerrors "loopworker/pkg/errors"
)

type principalContextKey struct{}

// ContextWithPrincipal attaches an authenticated principal.
func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext returns the authenticated principal, if any.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(*Principal)
	return p, ok && p != nil
}

// WriteAuthFailure is the fallback renderer used when no AuthFailureWriter has
// been installed. It emits the same field names as the API envelope.
func WriteAuthFailure(w http.ResponseWriter, _ *http.Request, status int, code, message string, details map[string]any) {
	body := map[string]any{
		"success": false,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	if len(details) > 0 {
		if errBody, ok := body["error"].(map[string]any); ok {
			errBody["details"] = details
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// RequireAuth rejects requests without a valid credential (401) and stores the
// principal in the request context.
func (a *Authenticator) RequireAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			principal, err := a.AuthenticateRequest(r)
			if err != nil {
				status, code, msg := a.authFailure(err)
				a.render(w, r, status, code, msg, nil)
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}
}

// RequirePermission enforces RBAC for the routes it wraps. It must run after
// RequireAuth; a missing principal is treated as 401, an insufficient role as 403.
func (a *Authenticator) RequirePermission(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				a.render(w, r, http.StatusUnauthorized, "UNAUTHORIZED",
					"No authenticated caller on this request. Fix: send a credential on this route; see GET /api/v1/openapi.json securitySchemes.",
					map[string]any{"required_permission": string(perm)})
				return
			}
			if granted, missing := a.Authorize(principal, perm); !granted {
				a.render(w, r, http.StatusForbidden, "FORBIDDEN",
					"Role \""+principal.Role+"\" cannot perform this operation; it requires permission \""+
						string(missing)+"\". Fix: ask an administrator for a key with role \"operator\" (write/execute) or \"admin\", or use GET endpoints instead of mutating ones.",
					map[string]any{
						"role":                 principal.Role,
						"required_permission":  string(missing),
						"permissions_for_role": names(a.permissionsFor(principal)),
					})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *Authenticator) permissionsFor(p *Principal) []Permission { return p.Permissions() }

func names(perms []Permission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, string(p))
	}
	return out
}

func (a *Authenticator) render(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	a.WriteFailure(w, r, status, code, message, details)
}

// WriteFailure emits an authentication/authorization problem through the
// installed AuthFailureWriter, so middleware and handlers share one envelope.
func (a *Authenticator) WriteFailure(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	a.mu.RLock()
	writer := a.failure
	a.mu.RUnlock()
	if writer == nil {
		writer = WriteAuthFailure
	}
	writer(w, r, status, code, message, details)
}

func (a *Authenticator) authFailure(err error) (int, string, string) {
	switch {
	case lwerrors.Is(err, lwerrors.ErrUnauthorized):
		return http.StatusUnauthorized, "UNAUTHORIZED",
			"No credentials supplied. Fix: send a header \"Authorization: Bearer <token>\" or \"" + a.header + ": lwk_...\"; start the server without configured keys to print a one-time development key, or set LOOPWORKER_API_KEYS for a permanent one."
	case lwerrors.Is(err, lwerrors.ErrTokenExpired):
		return http.StatusUnauthorized, "TOKEN_EXPIRED",
			"The credential exists but has expired. Fix: request a new token from POST /api/v1/auth/token, or reload an unexpired API key."
	case lwerrors.Is(err, lwerrors.ErrTokenInvalid):
		return http.StatusUnauthorized, "TOKEN_INVALID",
			"The credential is malformed, unknown, or revoked. Fix: copy the key without truncation and check that it was issued by this server instance (bootstrap keys die with the process); the reason is intentionally not itemised so guessing is not cheaper."
	case lwerrors.Is(err, lwerrors.ErrForbidden):
		return http.StatusForbidden, "FORBIDDEN",
			"This credential is not allowed to sign in. Fix: use an API key with a role that is permitted for POST /api/v1/auth/token."
	default:
		return http.StatusUnauthorized, "UNAUTHORIZED", "Authentication failed."
	}
}

// ClientIP resolves the address used as a rate-limit bucket key. Proxy headers
// are only honoured when trustProxy is enabled, and then only the rightmost hop
// (appended by the trusted proxy) is used, so clients cannot forge the chain.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			hops := strings.Split(xff, ",")
			if last := strings.TrimSpace(hops[len(hops)-1]); last != "" {
				return hostOnly(last)
			}
		} else if real := r.Header.Get("X-Real-IP"); real != "" {
			return hostOnly(real)
		}
	}
	return hostOnly(r.RemoteAddr)
}

func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "unknown"
	}
	if host, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return host
	}
	// X-Forwarded-For entries are not always bracketed for IPv6.
	if strings.Count(addr, ":") > 1 {
		return addr
	}
	return addr
}

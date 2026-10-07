package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"loopworker/pkg/security"
)

// middlewareConfig bundles the collaborators the middlewares need.
type middlewareBundle struct {
	auth    *security.Authenticator
	limits  *security.TieredRateLimiter
	cfg     Config
	streams *security.ConcurrencyLimiter
}

// requestIDKey is the context key holding the per-request identifier.
type requestIDKey struct{}

// RequestIDFromContext returns the id echoed in responses and logs.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// requestIDMiddleware accepts a caller-supplied X-Request-ID (bounded, sanitized)
// or mints one, and echoes it back on the response.
func (b *middlewareBundle) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" || len(id) > 64 || !isRequestIDSafe(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func isRequestIDSafe(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// rateLimitIP is the outer guard for everything reached before authentication
// (health probes, the OpenAPI document, static assets). Requests that already
// carry a principal are left to principalLimit, which runs after authentication:
// charging them here would silently bill every credentialed caller the tiny
// anonymous budget.
func (b *middlewareBundle) rateLimitIP(next http.Handler) http.Handler {
	return b.limit(next, func(r *http.Request) (string, bool, bool) {
		if _, ok := security.PrincipalFromContext(r.Context()); ok {
			return "", false, false
		}
		return "ip:" + security.ClientIP(r, b.cfg.TrustProxy), false, true
	})
}

// principalLimit runs inside the authenticated group, so the caller is known and
// each credential gets its own budget instead of sharing one per IP.
func (b *middlewareBundle) principalLimit(next http.Handler) http.Handler {
	return b.limit(next, func(r *http.Request) (string, bool, bool) {
		p, ok := security.PrincipalFromContext(r.Context())
		if !ok || p == nil {
			return "", false, false
		}
		return "principal:" + p.Subject + ":" + p.Role, true, true
	})
}

// authMiddleware authenticates every route under the mounted group.
//
// In local trust mode there is no credential to check: the host bound the
// listener to loopback and the operator turned authentication off, so every
// request is the operator. See local_trust.go for why that is bounded.
func (b *middlewareBundle) authMiddleware(next http.Handler) http.Handler {
	if b.cfg.LocalTrust {
		return authenticateLocalTrust(next)
	}
	return b.auth.RequireAuth()(next)
}

// permissionMiddleware requires a permission for the wrapped routes.
//
// Local trust mode short-circuits because the synthetic principal is already an
// administrator; RequirePermission would pass anyway, and saying so explicitly
// keeps the intent readable.
func (b *middlewareBundle) require(perm security.Permission) func(http.Handler) http.Handler {
	if b.cfg.LocalTrust {
		return func(next http.Handler) http.Handler { return next }
	}
	return b.auth.RequirePermission(perm)
}

// timeoutMiddleware applies a deadline to ordinary requests and skips it for
// long-lived streams.
func (b *middlewareBundle) timeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStreamingPath(r.URL.Path) || b.cfg.RequestTimeout <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), b.cfg.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil {
			// Headers are already flushed for most handlers; logging is the
			// only safe action left here.
			logRequestError(r, classification{status: http.StatusGatewayTimeout, code: CodeTimeout}, ctx.Err())
		}
	})
}

func isStreamingPath(path string) bool {
	return strings.HasSuffix(path, "/events/live")
}

// bodyLimit caps the request body and reports the limit through the envelope.
func (b *middlewareBundle) bodyLimit(next http.Handler) http.Handler {
	limit := b.cfg.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			sendError(w, r, &FieldError{
				Field:   "body",
				Reason:  "request body is " + itoa64(r.ContentLength) + " bytes, over the " + itoa64(limit) + " byte limit",
				Fix:     "shrink the payload, or stream large inputs by reference (store the blob and pass a URI in \"config\")",
				Status:  http.StatusRequestEntityTooLarge,
				Code:    CodeRequestTooLarge,
				Details: map[string]any{"limit_bytes": limit},
			})
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets the baseline hardening headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// cors implements an explicit origin allow-list. It never pairs a wildcard with
// credentialed requests.
func (b *middlewareBundle) cors(next http.Handler) http.Handler {
	allowed := b.cfg.AllowedOrigins
	if len(allowed) == 0 {
		allowed = DefaultAllowedOrigins()
	}
	wildcard := b.cfg.AllowWildcard
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		h := w.Header()
		h.Add("Vary", "Origin")

		if origin != "" {
			match := ""
			if wildcard {
				match = "*"
			} else {
				for _, o := range allowed {
					if subtle.ConstantTimeCompare([]byte(strings.ToLower(o)), []byte(strings.ToLower(origin))) == 1 {
						match = o
						break
					}
				}
			}
			if match != "" {
				h.Set("Access-Control-Allow-Origin", match)
				h.Set("Vary", "Access-Control-Allow-Origin")
				if match != "*" {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
			}
		}

		// The authenticator is nil when the credential configuration was
		// unusable; every route then answers 503, so the header name must not
		// come from a nil receiver.
		keyHeader := defaultAPIKeyHeader
		if b.auth != nil {
			keyHeader = b.auth.APIKeyHeader()
		}
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", strings.Join([]string{
			"Accept", "Content-Type", "Authorization", keyHeader, "X-Request-ID", "Last-Event-ID",
		}, ", "))
		h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining")
		h.Set("Access-Control-Max-Age", "600")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limit is the shared body of the two rate-limit tiers. resolve returns the
// bucket key, whether it is the authenticated tier, and whether this middleware
// should charge the request at all.
func (b *middlewareBundle) limit(next http.Handler, resolve func(*http.Request) (string, bool, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.auth == nil || b.limits == nil {
			next.ServeHTTP(w, r)
			return
		}
		key, authenticated, charged := resolve(r)
		if !charged || b.limits.AllowCost(key, authenticated, costForPath(r)) {
			next.ServeHTTP(w, r)
			return
		}

		retryAfter := b.limits.RetryAfter(key, authenticated)
		seconds := int(retryAfter.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", itoa(seconds))
		limit := b.cfg.AnonRate
		if authenticated {
			limit = b.cfg.Authenticated
		}
		writeJSON(w, r, http.StatusTooManyRequests, Envelope{Success: false, Error: &ErrorBody{
			Code: CodeRateLimited,
			Message: "This caller exceeded " + itoa(limit) + " requests per " + b.cfg.AnonWindow.String() +
				". Fix: back off for the number of seconds in the Retry-After header, batch requests, or use a credentialed key for the larger budget.",
			Details: map[string]any{
				"limit":         limit,
				"window":        b.cfg.AnonWindow.String(),
				"retry_after_s": seconds,
				"authenticated": authenticated,
			},
		}})
	})
}

// costForPath charges streams and graph builds more than cheap reads.
func costForPath(r *http.Request) int {
	switch {
	case isStreamingPath(r.URL.Path):
		return 5
	case r.Method == http.MethodPost || r.Method == http.MethodDelete || r.Method == http.MethodPut:
		return 2
	default:
		return 1
	}
}

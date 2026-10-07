package api

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
	"strings"

	"go.uber.org/zap"

	"loopworker/pkg/logger"
	"loopworker/pkg/security"
)

// middleware_Recoverer converts a handler panic into a 500 envelope instead of a
// bare stack dump, and records the stack under the request id.
func middleware_Recoverer() func(http.Handler) http.Handler {
	//nolint:contextcheck // the panic handler reads the request context it already holds; it takes no derived context
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Get().Error("handler panic",
						zap.String("request_id", RequestIDFromContext(r.Context())),
						zap.String("path", r.URL.Path),
						zap.Any("panic", recovered),
						zap.ByteString("stack", debug.Stack()))
					if !responseStarted(w) {
						writeJSON(w, r, http.StatusInternalServerError, Envelope{Success: false, Error: &ErrorBody{
							Code:    CodeInternalError,
							Message: "The handler panicked. Fix: retry once; if it persists, send this request_id to support - the server log holds the stack trace and the failing endpoint.",
						}})
					}
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// responseStarted reports whether headers were already flushed (a partially
// written stream cannot be replaced by an error body).
func responseStarted(w http.ResponseWriter) bool {
	if tracker, ok := w.(interface{ Written() bool }); ok {
		return tracker.Written()
	}
	return false
}

// stdlibErrorLog routes net/http's internal errors into zap.
func stdlibErrorLog() *log.Logger {
	return log.New(&zapLogWriter{}, "", 0)
}

// zapLogWriter adapts io.Writer to a zap field string.
type zapLogWriter struct{}

func (zapLogWriter) Write(p []byte) (int, error) {
	message := strings.TrimRight(string(p), "\n")
	if message != "" {
		logger.Get().Warn("http: " + message)
	}
	return len(p), nil
}

// jsonBytes encodes a value or returns a JSON error stub.
func jsonBytes(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"success":false,"error":{"code":"INTERNAL_ERROR","message":"response encoding failed"}}`)
	}
	return encoded
}

// logBootstrapCredential announces the ephemeral development key exactly once.
// It is the documented "get a working credential in under a minute" path, and it
// disappears with the process, so it is not a standing backdoor.
func logBootstrapCredential(auth *security.Authenticator) {
	plaintext, keyID := auth.BootstrapKey()
	if plaintext == "" {
		return
	}
	logger.Get().Warn("no API credentials configured: issued an ephemeral admin API key for this process only",
		zap.String("key_id", keyID),
		zap.String("api_key", plaintext),
		zap.String("usage", "curl -H \"X-API-Key: "+plaintext+"\" http://127.0.0.1:19527/api/v1/auth/whoami"),
		zap.String("permanent_keys", "set LOOPWORKER_API_KEYS=id:role:hex-sha256 or POST /api/v1/auth/keys with this key"),
	)
}

// IsLoopbackAddr reports whether addr names the loopback interface and nothing
// else.
//
// An empty host is deliberately NOT loopback. Both ":19527" and "0.0.0.0:19527"
// bind every interface, so a server that classified either as private would be
// reachable from the network while reasoning that it was not. Callers that want
// "no address given" to mean "skip the check" must test for that themselves,
// which ValidateBindAddress does.
func IsLoopbackAddr(addr string) bool {
	addr = strings.TrimSpace(addr)
	// Match the bare forms first. A bare IPv6 literal has no port, so splitting
	// on the last colon would cut "::1" down to ":" and lose the address.
	switch addr {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	host := addr
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		host = addr[:idx]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	// The whole 127.0.0.0/8 block is loopback, not just 127.0.0.1.
	return strings.HasPrefix(host, "127.")
}

// ValidateBindAddress refuses a writable API bound to a public interface
// without configured credentials. The integration pass must call it with the
// listen address before serving.
func ValidateBindAddress(addr string, auth *security.Authenticator) error {
	if addr == "" {
		return nil
	}
	if IsLoopbackAddr(addr) {
		return nil
	}
	if auth != nil && !auth.HasCredentials() {
		return &FieldError{
			Field:  "bind_address",
			Reason: "the API would listen on " + addr + " using only an ephemeral bootstrap key",
			Fix:    "set LOOPWORKER_API_KEYS=id:role:hex-sha256 (or bind to 127.0.0.1) before exposing the service",
			Status: http.StatusServiceUnavailable,
			Code:   CodeServiceUnavailable,
		}
	}
	return nil
}

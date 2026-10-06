package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"loopworker/pkg/logger"
)

var requestIDCounter int64

// newRequestID mints a monotonic, collision-resistant id: <unix-nano>-<seq>-<rand>.
func newRequestID() string {
	seq := atomic.AddInt64(&requestIDCounter, 1)
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		buf = []byte{byte(time.Now().UnixNano()), 0, 0, 0}
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + strconv.FormatInt(seq, 10) + "-" + hex.EncodeToString(buf)
}

func itoa(n int) string { return strconv.Itoa(n) }

// nowRFC3339 stamps responses with a UTC RFC3339Nano clock.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// logAsyncError records background failures that never reach a response body.
func logAsyncError(message, subject string, cause error) {
	logger.Get().Warn(message, zap.String("subject", subject), zap.Error(cause))
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// sortStrings orders a slice in place (used for deterministic error details).
func sortStrings(items []string) { sort.Strings(items) }

// unwrapFlusher finds the http.Flusher behind any number of ResponseWriter
// wrappers. Returning nil means something in the chain swallowed Flush(), which
// used to break every SSE connection.
func unwrapFlusher(w http.ResponseWriter) (http.Flusher, bool) {
	for current := w; current != nil; {
		if flusher, ok := current.(http.Flusher); ok {
			return flusher, true
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil, false
		}
		current = unwrapper.Unwrap()
	}
	return nil, false
}

// logRequestError records the internal cause of a 5xx so the customer-facing
// message can stay generic while support can still trace it by request id.
func logRequestError(r *http.Request, entry classification, cause error) {
	logger.Get().Warn("request failed",
		zap.String("request_id", RequestIDFromContext(r.Context())),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.Int("status", entry.status),
		zap.String("code", string(entry.code)),
		zap.Error(cause),
	)
}

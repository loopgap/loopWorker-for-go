package api

import (
	"bufio"
	"io"
	"net"
	"net/http"

	"go.uber.org/zap"

	"loopworker/pkg/logger"
)

// statusWriter records the response code for access logging while forwarding every
// optional interface of the underlying ResponseWriter. Not propagating
// http.Flusher is what made SSE answer "Streaming unsupported".
type statusWriter struct {
	http.ResponseWriter
	status    int
	bytes     int
	started   bool
	streaming bool
}

func newStatusWriter(w http.ResponseWriter) *statusWriter {
	return &statusWriter{ResponseWriter: w, status: http.StatusOK}
}

// Written reports whether the response has begun.
func (sw *statusWriter) Written() bool { return sw.started }

// WriteHeader captures the status once and forwards it.
func (sw *statusWriter) WriteHeader(status int) {
	if sw.started {
		return
	}
	sw.status = status
	sw.started = true
	sw.ResponseWriter.WriteHeader(status)
}

// Write counts bytes and marks the stream as begun.
func (sw *statusWriter) Write(body []byte) (int, error) {
	if !sw.started {
		sw.started = true
	}
	n, err := sw.ResponseWriter.Write(body)
	sw.bytes += n
	return n, err
}

// Flush forwards to the wrapped writer when it supports streaming.
func (sw *statusWriter) Flush() {
	if flusher, ok := sw.ResponseWriter.(http.Flusher); ok {
		sw.streaming = true
		flusher.Flush()
		return
	}
	logger.Get().Warn("response writer in chain does not implement http.Flusher")
}

// Unwrap exposes the wrapped writer so http.ResponseController can reach it.
func (sw *statusWriter) Unwrap() http.ResponseWriter { return sw.ResponseWriter }

// Hijack forwards to the wrapped writer (WebSocket/upgrade support).
func (sw *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := sw.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, net.ErrClosed
}

// ReadFrom forwards sendfile support for large static writes.
func (sw *statusWriter) ReadFrom(reader io.Reader) (int64, error) {
	if rf, ok := sw.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(reader)
	}
	return io.Copy(struct{ io.Writer }{sw}, reader)
}

// accessLog emits one structured line per request.
func (b *middlewareBundle) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writer := newStatusWriter(w)
		defer func() {
			if isStreamingPath(r.URL.Path) {
				return
			}
			logger.Get().Info("request",
				zap.String("request_id", RequestIDFromContext(r.Context())),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", writer.status),
				zap.Int("bytes", writer.bytes),
				zap.String("caller", principalSubject(r)),
			)
		}()
		next.ServeHTTP(writer, r)
	})
}

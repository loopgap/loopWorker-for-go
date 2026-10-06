package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/security"
)

// AdminAddr returns the loopback address the metrics listener should bind to.
func (s *APIServer) AdminAddr() string {
	bind := s.cfg.AdminBind
	if bind == "" {
		bind = DefaultAdminBind
	}
	port := s.cfg.AdminPort
	if port < 0 {
		// Only a negative port falls back to the default. Zero is the net
		// package's "let the OS pick a free port" and callers rely on it: a
		// fixed default plus concurrent test binaries means two processes fight
		// over 19528 and the loser reports a bind error that has nothing to do
		// with the code under test. DefaultConfig already fills in 19528.
		port = DefaultAdminPort
	}
	return fmt.Sprintf("%s:%d", bind, port)
}

// AdminHandler builds the authenticated admin surface. Runtime metrics never
// live on the public port: they name goroutines, memory and task volumes.
func (s *APIServer) AdminHandler() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware_Recoverer())
	router.Use(bundleForAdmin(s).requestID)

	router.Get("/healthz", s.livenessProbe)

	router.Group(func(authed chi.Router) {
		authed.Use(s.auth.RequireAuth())
		authed.Use(s.auth.RequirePermission(security.PermAdmin))

		// Method, not Handle: a scrape is a GET. Handle would register every
		// verb and silently widen the contract openapi.json declares.
		authed.Method(http.MethodGet, "/metrics", promhttp.Handler())
		authed.Get("/runtime/stats", s.adminStats)
		authed.Get("/logs", s.adminLogs)
		authed.Get("/events/stats", s.adminEventStats)
	})
	return router
}

func bundleForAdmin(s *APIServer) *middlewareBundle {
	return &middlewareBundle{auth: s.auth, limits: s.limits, cfg: s.cfg, streams: s.streams}
}

// adminStats answers GET /runtime/stats with scheduler counters for operators.
func (s *APIServer) adminStats(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"readiness":  s.readies(),
		"stats":      s.statsForResponse(),
		"queue_size": s.queueSize(),
		"streams":    s.streams.Total(),
		"config":     s.publicConfigSnapshot(),
		"sampled_at": nowRFC3339(),
	}
	sendSuccess(w, r, body, http.StatusOK)
}

// adminLogs answers GET /logs from the observer (replaces the old public route).
func (s *APIServer) adminLogs(w http.ResponseWriter, r *http.Request) {
	if s.deps.Observer == nil {
		sendError(w, r, missingDependency("observer", "the observer is not wired into this server"))
		return
	}
	sendSuccess(w, r, map[string]any{"logs": s.deps.Observer.GetLogs()}, http.StatusOK)
}

// adminEventStats reports bus counters so a customer can see whether their
// stream budget, not the server, is the bottleneck.
func (s *APIServer) adminEventStats(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"streams_total":          s.streams.Total(),
		"max_streams_per_caller": s.streams.MaxPerKey(),
		"max_streams_total":      s.streams.MaxTotal(),
	}
	if statter, ok := s.deps.Events.(interface{ GetStats() *event.BusStats }); ok {
		if stats := statter.GetStats(); stats != nil {
			body["bus"] = map[string]any{
				"subscribers":      stats.SubscriberCount,
				"events_published": stats.EventsPublished,
				"events_delivered": stats.EventsDelivered,
				"events_dropped":   stats.EventsDropped,
			}
		}
	}
	sendSuccess(w, r, body, http.StatusOK)
}

// publicConfigSnapshot describes limits a caller can be rejected by.
func (s *APIServer) publicConfigSnapshot() map[string]any {
	return map[string]any{
		"anon_rate":              s.cfg.AnonRate,
		"authenticated_rate":     s.cfg.Authenticated,
		"window":                 s.cfg.AnonWindow.String(),
		"max_body_bytes":         s.cfg.MaxBodyBytes,
		"max_input_bytes":        s.cfg.MaxInputBytes,
		"max_streams_per_caller": s.cfg.MaxStreamsPerCaller,
		"max_streams_total":      s.cfg.MaxStreamsTotal,
		"graph_max_nodes":        s.cfg.GraphMaxNodes,
		"max_limit":              s.cfg.MaxLimit,
		"trust_proxy":            s.cfg.TrustProxy,
		"request_timeout":        s.cfg.RequestTimeout.String(),
		"allowed_origins":        s.cfg.AllowedOrigins,
	}
}

// StartAdmin binds the admin listener. It returns the running server so the
// integration pass can shut it down with the rest of the process.
func (s *APIServer) StartAdmin(ctx context.Context) (*http.Server, error) {
	if !s.cfg.EnableAdmin {
		return nil, nil
	}
	if s.auth == nil {
		return nil, &FieldError{Reason: "the admin listener needs an authenticator",
			Fix:    "start the API server with a usable authentication configuration",
			Status: http.StatusServiceUnavailable, Code: CodeServiceUnavailable}
	}

	// Bind synchronously. The admin port has a fixed default, so "address
	// already in use" is the common failure and net.Listen reports it right
	// away. Serving from a background goroutine and hoping an error shows up
	// within a timeout turns that clear message into a listener that dies after
	// StartAdmin already reported success - and under load the goroutine may not
	// even have run yet.
	ln, err := net.Listen("tcp", s.AdminAddr())
	if err != nil {
		return nil, fmt.Errorf("admin listener cannot bind %s: %w", s.AdminAddr(), err)
	}

	server := NewHTTPServer(ln.Addr().String(), s.AdminHandler(), s.cfg)
	server.WriteTimeout = 30 * time.Second
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("admin listener stopped", zap.Error(err), zap.String("address", ln.Addr().String()))
		}
	}()
	_ = ctx // binding is synchronous; the caller's context cannot cancel it
	return server, nil
}

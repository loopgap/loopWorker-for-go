package api

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"loopworker/version"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	"loopworker/pkg/security"
	"loopworker/pkg/workflow"
)

//go:embed all:dist
var webCanvas embed.FS

//go:embed openapi.json
var openAPISpec []byte

// OpenAPISpecJSON is the machine-readable contract served at
// GET /api/v1/openapi.json. CI and docs tooling can write it out verbatim.
var OpenAPISpecJSON = append([]byte(nil), openAPISpec...)

// APIServer owns the HTTP surface: routing, authN/authZ, rate limiting, the
// error contract and the streaming endpoint.
type APIServer struct {
	// Router is the chi mux to mount on an http.Server.
	Router *chi.Mux

	cfg     Config
	deps    Dependencies
	lister  TaskLister
	auth    *security.Authenticator
	streams *security.ConcurrencyLimiter
	limits  *security.TieredRateLimiter
	version string

	// configError blocks all traffic when credentials could not be constructed,
	// so a broken configuration never degrades into an open API.
	configError error

	closeOnce sync.Once
}

// NewAPIServer wires the API server over the core components. It always returns
// a server; an unusable configuration produces a fail-closed server whose routes
// answer 503 with the repair hint (see APIServer.ConfigError).
func NewAPIServer(
	sched *scheduler.Scheduler,
	exec *executor.Executor,
	bus *event.EventBus,
	wfe *workflow.WorkflowEngine,
	obs *observer.Observer,
	opts ...Option,
) *APIServer {
	deps := Dependencies{}
	if sched != nil {
		deps.Tasks = sched
		deps.Lister = sched
	}
	if exec != nil {
		deps.Workers = exec
	}
	if bus != nil {
		deps.Events = bus
	}
	if wfe != nil {
		deps.Workflows = wfe
	}
	if obs != nil {
		deps.Observer = obs
	}
	return NewAPIServerWithDependencies(deps, opts...)
}

// NewAPIServerWithDependencies builds the HTTP surface over explicit ports.
func NewAPIServerWithDependencies(deps Dependencies, opts ...Option) *APIServer {
	cfg := newConfig(opts...)
	if deps.Lister == nil {
		if lister, ok := deps.Tasks.(TaskLister); ok {
			deps.Lister = lister
		}
	}

	server := &APIServer{
		cfg:     cfg,
		deps:    deps,
		lister:  deps.Lister,
		limits:  security.NewTieredRateLimiter(cfg.AnonRate, cfg.AnonWindow, cfg.Authenticated, cfg.AuthWindow),
		streams: security.NewConcurrencyLimiter(cfg.MaxStreamsPerCaller, cfg.MaxStreamsTotal),
		version: versionString(),
	}

	if auth, err := security.NewAuthenticator(cfg.Auth); err != nil {
		server.configError = err
	} else {
		auth.SetFailureWriter(failureWriter)
		server.auth = auth
		logBootstrapCredential(auth)
	}

	server.Router = chi.NewRouter()
	server.registerRoutes()
	return server
}

// ConfigError reports why the server refused to accept credentials, if any.
func (s *APIServer) ConfigError() error { return s.configError }

// Authenticator exposes the wired authenticator for the admin listener.
func (s *APIServer) Authenticator() *security.Authenticator { return s.auth }

// Close stops background goroutines (limiter cleanup).
func (s *APIServer) Close() {
	s.closeOnce.Do(func() {
		if s.limits != nil {
			s.limits.Stop()
		}
	})
}

// blockConfigError answers every request when credentials are unusable.
func (s *APIServer) blockConfigError() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.configError == nil {
				next.ServeHTTP(w, r)
				return
			}
			writeJSON(w, r, http.StatusServiceUnavailable, Envelope{Success: false, Error: &ErrorBody{
				Code: CodeServiceUnavailable,
				Message: "The server started with an unusable authentication configuration, so every endpoint is closed. Fix: " +
					s.configError.Error(),
			}})
		})
	}
}

// registerRoutes installs middleware and the versioned API surface.
func (s *APIServer) registerRoutes() {
	bundle := &middlewareBundle{auth: s.auth, limits: s.limits, cfg: s.cfg, streams: s.streams}

	// Public listener: no prometheus registry, no runtime stats.
	s.Router.Use(securityHeaders)
	s.Router.Use(bundle.cors)
	s.Router.Use(bundle.requestID)
	s.Router.Use(bundle.accessLog)
	s.Router.Use(s.blockConfigError())
	s.Router.Use(middleware_Recoverer())
	s.Router.Use(bundle.bodyLimit)
	s.Router.Use(bundle.timeout)
	s.Router.NotFound(s.notFoundHandler())
	s.Router.MethodNotAllowed(s.methodNotAllowedHandler())

	// Unauthenticated routes are limited per client IP; authenticated routes are
	// limited per credential inside the group below. Splitting the two is what
	// stops a NAT'd office from sharing one anonymous budget while every API key
	// is also billed for anonymous traffic.
	public := s.Router.With(bundle.rateLimitIP)
	public.Get("/healthz", s.livenessProbe)

	s.Router.Route("/api/v1", func(r chi.Router) {
		r.With(bundle.rateLimitIP).Get("/health", s.healthCheck)
		r.With(bundle.rateLimitIP).Get("/openapi.json", s.getOpenAPISpec)

		// Every endpoint below requires a credential; role checks happen per
		// route so a viewer cannot mutate state and an operator cannot manage
		// keys.
		r.With(bundle.authMiddleware, bundle.principalLimit).Group(func(authed chi.Router) {
			authed.Get("/auth/whoami", s.whoami)
			authed.Get("/workers", s.listWorkers)
			authed.Get("/tasks", s.listTasks)
			authed.Get("/tasks/{taskID}", s.getTask)
			authed.Get("/workflow/graph", s.getWorkflowGraph)
			authed.Get("/workflow/list", s.listWorkflows)
			authed.Get("/workflow/{workflowID}", s.getWorkflow)
			authed.Get("/events/live", s.streamEventsLive)

			authed.With(bundle.require(security.PermWrite)).Post("/tasks", s.createTask)
			authed.With(bundle.require(security.PermWrite)).Delete("/tasks/{taskID}", s.deleteTask)
			authed.With(bundle.require(security.PermWrite)).Post("/tasks/{taskID}/cancel", s.cancelTask)
			authed.With(bundle.require(security.PermWrite)).Post("/tasks/{taskID}/dependencies", s.addTaskDependency)
			authed.With(bundle.require(security.PermExecute)).Post("/workflow/execute", s.executeWorkflow)

			authed.With(bundle.require(security.PermAdmin)).Post("/auth/token", s.createToken)
			authed.With(bundle.require(security.PermAdmin)).Post("/auth/keys", s.createAPIKey)
			authed.With(bundle.require(security.PermAdmin)).Get("/auth/keys", s.listAPIKeys)
			authed.With(bundle.require(security.PermAdmin)).Delete("/auth/keys/{keyID}", s.revokeAPIKey)
		})
	})

	if s.cfg.ServeStatic {
		s.Router.With(bundle.rateLimitIP).Handle("/*", s.staticHandler())
	}
}

// livenessProbe is an unauthenticated, payload-free readiness signal for
// orchestrators; it never reports workload statistics.
func (s *APIServer) livenessProbe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	body := map[string]any{"status": "alive", "readiness": s.readies()}
	if s.configError != nil {
		status = http.StatusServiceUnavailable
		body["status"] = "unavailable"
	}
	w.WriteHeader(status)
	_, _ = w.Write(jsonBytes(body))
}

func (s *APIServer) notFoundHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusNotFound, Envelope{Success: false, Error: &ErrorBody{
			Code:    CodeNotFound,
			Message: "No route matches " + r.Method + " " + cleanPath(r.URL.Path) + ". Fix: GET /api/v1/openapi.json lists every endpoint this build ships.",
		}})
	}
}

func (s *APIServer) methodNotAllowedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusMethodNotAllowed, Envelope{Success: false, Error: &ErrorBody{
			Code:    CodeMethodNotAllowed,
			Message: r.Method + " is not allowed on " + cleanPath(r.URL.Path) + ". Fix: check the allowed verbs in GET /api/v1/openapi.json.",
		}})
	}
}

func cleanPath(path string) string {
	if len(path) > 200 {
		return path[:200] + "..."
	}
	return path
}

// canvasIndex is the SPA shell served for extensionless paths.
//
// It is read once at init rather than through http.FileServer because net/http's
// serveFile 301-redirects any path ending in /index.html to "./". For the request
// a browser sends first - GET / - that redirect resolves to / again, so the
// landing page spun forever. Serving the bytes directly is both the fix and the
// cheaper path; the shell is a compile-time constant, not a file we might change.
var canvasIndex = func() []byte {
	b, err := fs.ReadFile(webCanvas, "dist/index.html")
	if err != nil {
		// Cannot happen: dist/index.html is covered by the embed directive above,
		// and a build without it is a build that should not have succeeded.
		panic("loopworker: embedded canvas is missing dist/index.html: " + err.Error())
	}
	return b
}()

// staticHandler serves the embedded canvas without exposing API internals.
func (s *APIServer) staticHandler() http.HandlerFunc {
	fileServer := http.FileServer(http.FS(webCanvas))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/metrics") {
			s.notFoundHandler()(w, r)
			return
		}
		if filepath.Ext(r.URL.Path) == "" {
			// Every extensionless path is the SPA's job to route, so the shell is
			// the correct answer for all of them - including "/".
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			w.Write(canvasIndex)
			return
		}
		r.URL.Path = "/dist" + r.URL.Path
		fileServer.ServeHTTP(w, r)
	}
}

// getOpenAPISpec serves the embedded contract so a customer can codegen a client
// without asking support anything.
func (s *APIServer) getOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	sendRaw(w, http.StatusOK, "application/json; charset=utf-8", OpenAPISpecJSON)
}

// versionString reports the build version from the single source of truth.
// This used to be a second `var serverVersion = "dev"` that release tooling was
// supposed to fill in through SetVersion - and nothing ever called it, so a
// freshly built binary advertised itself as "dev" while `loopworker version`
// printed the real value. version.Version is already the ldflags target
// (see Makefile / .release/build.ps1), so read it directly.
func versionString() string { return version.Version }

// NewHTTPServer returns an http.Server with slowloris-safe timeouts. Streaming
// responses are exempt from the handler deadline, not from these socket
// timeouts, so keep WriteTimeout generous or serve events from a second server.
func NewHTTPServer(addr string, handler http.Handler, cfg Config) *http.Server {
	read := cfg.ReadTimeout
	if read <= 0 {
		read = 15 * time.Second
	}
	write := cfg.WriteTimeout
	if write <= 0 {
		write = 75 * time.Second
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = 60 * time.Second
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       read,
		WriteTimeout:      write,
		IdleTimeout:       idle,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          stdlibErrorLog(),
	}
}

var _ = context.Canceled
var _ = errors.Is

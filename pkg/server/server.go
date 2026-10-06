// Package server owns the LoopWorker process: it builds the engine components
// from a resolved configuration, runs the startup self-check, starts every
// component in a defined order, serves HTTP and shuts everything down in the
// reverse order within a bounded drain window.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"loopworker/internal/config"
	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/api"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/plugin"
	"loopworker/pkg/security"
	"loopworker/pkg/skill"
	"loopworker/pkg/utils"
	"loopworker/pkg/workflow"
)

// ErrClosed is returned by Start once the server has been stopped.
var ErrClosed = errors.New("server closed")

// Closer is implemented by components that own OS resources (database handles,
// open files). pkg/server calls Close on anything that supports it during Stop.
type Closer interface {
	Close() error
}

type state int

const (
	stateIdle state = iota
	stateRunning
	stateStopping
	stateStopped
)

func (s state) String() string {
	switch s {
	case stateRunning:
		return "running"
	case stateStopping:
		return "stopping"
	case stateStopped:
		return "stopped"
	default:
		return "idle"
	}
}

// Components holds every engine component built from one configuration.
type Components struct {
	Scheduler      *scheduler.Scheduler
	Dispatcher     *dispatcher.Dispatcher
	Executor       *executor.Executor
	Sandbox        *sandbox.Sandbox
	PluginMgr      *plugin.PluginManager
	Observer       *observer.Observer
	SelfHeal       *selfheal.SelfHealer
	APIServer      *api.APIServer
	EventBus       *event.EventBus
	EventStore     event.EventStore
	SkillRegistry  *skill.SkillRegistry
	WorkflowEngine *workflow.WorkflowEngine
	TaskBridge     *scheduler.SchedulerBridge

	eventStoreErr error
}

// BuildComponents creates the components described by cfg. Anything that cannot
// work is returned as an error: the server never starts in a knowingly broken
// configuration.
func BuildComponents(cfg *config.Config) (*Components, error) {
	if cfg == nil {
		return nil, fmt.Errorf("build components: nil configuration")
	}
	if err := EnsureDirs(cfg.WorkDir, cfg.Data.Dir, cfg.Plugins.Dir); err != nil {
		return nil, err
	}

	c := &Components{}

	store, err := event.NewLocalEventStore(cfg.Data.Dir)
	if err != nil {
		// Event persistence is the one capability that degrades instead of
		// aborting; it is reported in the health and doctor output.
		c.eventStoreErr = err
		logger.Warn("event store unavailable, events will not be persisted",
			zap.String("data_dir", cfg.Data.Dir), zap.Error(err))
	} else {
		c.EventStore = store
	}
	c.EventBus = event.NewEventBus(c.EventStore)

	c.Sandbox = sandbox.NewSandbox(sandbox.SandboxConfig{
		MaxMemoryMB:   cfg.Sandbox.MaxMemoryMB,
		MaxCPUSeconds: cfg.Sandbox.MaxCPUSeconds,
		MaxOutputMB:   cfg.Sandbox.MaxOutputMB,
		MaxConcurrent: cfg.Sandbox.MaxConcurrent,
		AllowedHosts:  cfg.Sandbox.AllowedHosts,
	})
	c.Sandbox.SetEventBus(c.EventBus)

	c.SkillRegistry = newSkillRegistry(cfg)

	pm, err := plugin.NewPluginManager(c.Sandbox, c.EventBus, cfg.Plugins.Dir, plugin.WithSkillRegistry(c.SkillRegistry))
	if err != nil {
		return nil, fmt.Errorf("create plugin manager for %s: %w", cfg.Plugins.Dir, err)
	}
	c.PluginMgr = pm

	c.WorkflowEngine = workflow.NewWorkflowEngine(workflow.WithEventBus(c.EventBus))

	sched, err := scheduler.NewSchedulerWithStorage(c.EventBus, scheduler.StorageConfig{
		Path: cfg.DBPath(),
	})
	if err != nil {
		return nil, fmt.Errorf("open task database %s: %w", cfg.DBPath(), err)
	}
	c.Scheduler = sched

	c.TaskBridge = scheduler.NewSchedulerBridge(c.Scheduler)
	c.WorkflowEngine.SetDispatcher(c.TaskBridge)
	// Register workflows here, not in boot(): the /workflow routes read the engine
	// the moment APIServer is constructed below, and the step resolvers need
	// SkillRegistry and TaskBridge to already exist. A rejected definition is a
	// warning, not a refusal - one bad file must not keep the server down.
	if workflowRun, err := RegisterWorkflows(cfg, c); err != nil {
		logger.Warn("some workflow definitions were rejected", zap.Error(err))
	} else if workflowRun != nil {
		logger.Info("workflows registered", zap.String("summary", workflowRun.Summary()))
	}
	c.Dispatcher = dispatcher.NewDispatcher(c.Scheduler, c.EventBus)
	c.Observer = observer.NewObserver(c.EventBus)

	if cfg.SelfHeal.Enabled {
		c.SelfHeal = selfheal.NewSelfHealer(selfheal.SelfHealConfig{
			MaxRetries:        cfg.SelfHeal.Retry.MaxAttempts,
			RetryDelay:        cfg.SelfHeal.Retry.Backoff,
			MaxRetryDelay:     maxDuration(cfg.SelfHeal.Retry.Backoff*4, time.Minute),
			CircuitThreshold:  cfg.SelfHeal.CircuitBreaker.Threshold,
			CircuitTimeout:    cfg.SelfHeal.CircuitBreaker.Timeout,
			HealthInterval:    10 * time.Second,
			IncidentRetention: 24 * time.Hour,
		})
	}

	c.Executor = executor.NewExecutor(c.Scheduler, c.Dispatcher, c.Sandbox, c.EventBus, c.SelfHeal,
		executor.WithTaskTimeout(cfg.Workers.TaskTimeout))
	if client := llmClientFor(cfg); client != nil {
		c.Executor.SetLLMClient(client)
	}
	c.Executor.SetSkillContext(skillContext(cfg, c.SkillRegistry, c.EventBus))

	authCfg, err := authConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	// WithEnvOverrides comes last so LOOPWORKER_API_* still wins over anything
	// decided here, which is the flag > env > file precedence the rest of the
	// config follows. The admin bind address is not configurable on purpose:
	// api.DefaultAdminBind is loopback, and letting it move would put /metrics
	// on a public interface.
	c.APIServer = api.NewAPIServer(c.Scheduler, c.Executor, c.EventBus, c.WorkflowEngine, c.Observer,
		api.WithAuth(authCfg),
		api.WithAdminListener(api.DefaultAdminBind, cfg.Server.AdminPort),
		api.WithEnvOverrides(),
	)

	return c, nil
}

// authConfigFor resolves the single set of credentials the HTTP surface accepts.
//
// Two sources exist and both are honoured, because an operator who configures
// both expects both to work:
//
//   - the environment: LOOPWORKER_API_KEYS (hex SHA-256 digests, preferred) and
//     LOOPWORKER_API_KEYS_PLAIN (lwk_ keys, hashed on sight and never retained);
//   - security.api_key from the config file.
//
// A malformed environment entry is fatal rather than ignored: a credential that
// silently fails to register is an API nobody can authenticate against.
func authConfigFor(cfg *config.Config) (security.AuthConfig, error) {
	authCfg, err := security.AuthConfigFromEnv()
	if err != nil {
		return authCfg, fmt.Errorf("read API credentials from the environment: %w\n"+
			"  fix: each %s entry is id:role:credential with role in {admin, operator, viewer};\n"+
			"       %s takes plaintext lwk_ keys, %s takes 64-char hex SHA-256 digests\n"+
			"  docs: README.md#configuration",
			err, security.EnvAPIKeys, security.EnvAPIKeysPlaintext, security.EnvAPIKeys)
	}

	if key := strings.TrimSpace(cfg.Security.APIKey); key != "" {
		authCfg.Keys = append(authCfg.Keys, security.APIKeyRecord{
			ID:      "config-file",
			Name:    "security.api_key",
			Role:    security.RoleAdmin,
			KeyHash: security.HashAPIKey(key),
		})
	} else if cfg.Security.AuthRequired {
		// config.Validate rejects this combination, so reaching it means the
		// caller assembled a Config by hand instead of going through config.Load.
		return authCfg, fmt.Errorf("security.auth_required is true but security.api_key is empty\n" +
			"  fix: set security.api_key to a key of at least 16 characters, or set\n" +
			"       security.auth_required=false to run on the ephemeral bootstrap key\n" +
			"  docs: README.md#configuration")
	}
	return authCfg, nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// EnsureDirs creates the directories a component needs, refusing empty paths so
// a misconfiguration cannot silently produce "mkdir :".
func EnsureDirs(dirs ...string) error {
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("refusing to create an empty directory path; check data.dir, plugins.dir and work_dir")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

// Server is a LoopWorker instance bound to one configuration.
type Server struct {
	cfg        *config.Config
	components *Components

	mu          sync.Mutex
	state       state
	startTime   time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	httpServer  *http.Server
	adminServer *http.Server
	listener    net.Listener
	diag        *Diagnostics
	pluginRun   *PluginRun
	workerIDs   []string
	stopHealth  func()
}

// New builds the components for cfg and returns a server ready to Start.
func New(cfg *config.Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("server.New: nil configuration; pass config.Load or config.Defaults")
	}
	components, err := BuildComponents(cfg)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, components: components, state: stateIdle}, nil
}

// Config returns the configuration the server was built from.
func (s *Server) Config() *config.Config { return s.cfg }

// State reports the lifecycle state.
func (s *Server) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.String()
}

func (s *Server) startedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startTime
}

func (s *Server) workerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workerIDs)
}

// Start runs the self-check, starts every component in order and serves HTTP.
// It blocks until the server is shut down or fails.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.state != stateIdle {
		current := s.state
		s.mu.Unlock()
		return fmt.Errorf("server cannot start from state %s", current)
	}
	s.state = stateRunning
	s.startTime = time.Now()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	ctx := s.ctx
	cfg := s.cfg
	s.mu.Unlock()

	ln, err := s.boot(ctx)
	if err != nil {
		if cleanupErr := s.Stop(); cleanupErr != nil {
			logger.Warn("cleanup after failed startup reported further problems", zap.Error(cleanupErr))
		}
		return err
	}

	httpServer := &http.Server{
		Handler:      s.handler(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		ErrorLog:     httpErrorLog(),
	}

	s.mu.Lock()
	s.httpServer = httpServer
	s.listener = ln
	addr := ln.Addr()
	s.mu.Unlock()

	logger.Info("LoopWorker listening",
		zap.String("address", addr.String()),
		zap.String("version", versionString()),
		zap.Int("workers", s.workerCount()))

	err = httpServer.Serve(ln)

	s.mu.Lock()
	drainedByStop := s.state == stateStopping || s.state == stateStopped
	s.mu.Unlock()
	if !drainedByStop {
		if cleanupErr := s.Stop(); cleanupErr != nil && err == nil {
			err = fmt.Errorf("listener ended and cleanup reported problems: %w", cleanupErr)
		}
	}
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// boot runs the self-check and starts every component in order, returning the
// listener the HTTP server should serve on. On any failure nothing is left
// half-started: the caller stops the server, which releases the listener.
func (s *Server) boot(ctx context.Context) (net.Listener, error) {
	cfg, components := s.cfg, s.components

	diag := Diagnose(cfg)
	s.mu.Lock()
	s.diag = diag
	s.mu.Unlock()
	diag.Log()
	if err := diag.FatalError(); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return nil, portError(cfg, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	// Fail closed before anything is served: a public bind address with no
	// credential at all is a configuration this server refuses to run, not one
	// it downgrades to a warning.
	if err := api.ValidateBindAddress(cfg.Addr(), components.APIServer.Authenticator()); err != nil {
		return nil, err
	}
	// The admin listener carries /metrics and friends; it is observability, not
	// the service. A port collision there must not stop a server whose API port
	// is free, so it degrades to a warning naming the remedy.
	if admin, err := components.APIServer.StartAdmin(ctx); err != nil {
		logger.Warn("admin listener unavailable; metrics and runtime stats are not exposed",
			zap.String("reason", err.Error()),
			zap.String("fix", "set LOOPWORKER_API_ADMIN_PORT to a free port, or stop the process holding it"))
	} else if admin != nil {
		s.mu.Lock()
		s.adminServer = admin
		s.mu.Unlock()
		logger.Info("admin listener bound", zap.String("address", admin.Addr),
			zap.String("endpoints", "/metrics, /runtime/stats, /logs, /events/stats"))
	}

	if err := components.Observer.Start(ctx); err != nil {
		return nil, fmt.Errorf("start observer: %w", err)
	}

	run, err := s.loadPlugins(ctx)
	s.mu.Lock()
	s.pluginRun = run
	s.mu.Unlock()
	if err != nil {
		// A plugin that will not load is an operator problem with one directory,
		// not a reason to refuse to serve. `loopworker doctor` names the
		// offending directory; the rest of the server stays up.
		logger.Warn("some plugins did not load", zap.Error(err))
	}

	if err := s.startWorkers(ctx, run); err != nil {
		return nil, err
	}

	components.Executor.StartWatchdog(ctx)
	utils.GoSafe(ctx, func(inner context.Context) {
		if err := components.Executor.Run(inner); err != nil {
			logger.Warn("executor pump stopped", zap.Error(err))
		}
	})

	// Without this the self-healer's health monitor never runs, which is the
	// same dead-code shape as the circuit breaker it is supposed to feed.
	if components.SelfHeal != nil {
		// Register before starting: StartHealthChecks snapshots the registry, so a
		// check added afterwards would never be run. That ordering is the whole
		// reason this monitor looked alive and was not.
		RegisterHealthChecks(components.SelfHeal, components, cfg)
		stop := components.SelfHeal.StartHealthChecks(ctx)
		s.mu.Lock()
		s.stopHealth = stop
		s.mu.Unlock()
	}

	return ln, nil
}

// loadPlugins discovers and loads the plugins configured for this server.
func (s *Server) loadPlugins(ctx context.Context) (*PluginRun, error) {
	return DiscoverAndLoad(ctx, s.components.PluginMgr, s.cfg)
}

func (s *Server) startWorkers(ctx context.Context, run *PluginRun) error {
	pluginID := run.primaryPlugin()
	started := make([]string, 0, s.cfg.Workers.Count)
	for i := 1; i <= s.cfg.Workers.Count; i++ {
		id := fmt.Sprintf("worker-%d", i)
		if err := s.components.Executor.StartWorker(ctx, id, pluginID); err != nil {
			for _, prev := range started {
				stopCtx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
				_ = s.components.Executor.StopWorker(stopCtx, prev)
				cancel()
			}
			return fmt.Errorf("start %s for plugin %q: %w (reduce workers.count if the host is low on memory)", id, pluginID, err)
		}
		started = append(started, id)
	}
	s.mu.Lock()
	s.workerIDs = started
	s.mu.Unlock()
	logger.Info("workers started", zap.Int("count", len(started)), zap.String("plugin", pluginID))
	return nil
}

// Stop cancels the component context, drains HTTP and workers within the
// configured timeout and releases every owned resource.
//
// It is safe (and required) to call after New succeeded but Start failed or was
// never called: New already opens the task database, the instance lock and the
// event store, so an early return would leak them for the life of the process.
// The steps below are each nil-safe and no-op when the step never ran.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.state == stateStopping || s.state == stateStopped {
		s.mu.Unlock()
		return nil
	}
	s.state = stateStopping
	httpServer, adminServer, listener, cancel, components, timeout := s.httpServer, s.adminServer, s.listener, s.cancel, s.components, s.cfg.Server.ShutdownTimeout
	stopHealth := s.stopHealth
	s.mu.Unlock()

	var problems []string

	if stopHealth != nil {
		stopHealth()
	}
	if cancel != nil {
		cancel()
	}

	if adminServer != nil {
		adminCtx, adminCancel := context.WithTimeout(context.Background(), timeout)
		if err := adminServer.Shutdown(adminCtx); err != nil {
			problems = append(problems, fmt.Sprintf("admin drain: %v", err))
			_ = adminServer.Close()
		}
		adminCancel()
	}

	if httpServer != nil {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), timeout)
		if err := httpServer.Shutdown(drainCtx); err != nil {
			problems = append(problems, fmt.Sprintf("http drain: %v", err))
			_ = httpServer.Close()
		}
		drainCancel()
	} else if listener != nil {
		_ = listener.Close()
	}

	if components != nil && components.Executor != nil {
		if err := drainWorkers(components.Executor, timeout); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if components != nil {
		if components.Scheduler != nil {
			if err := components.Scheduler.Close(); err != nil {
				problems = append(problems, fmt.Sprintf("close task database: %v", err))
			}
		}
		if closer, ok := components.EventStore.(Closer); ok {
			if err := closer.Close(); err != nil {
				problems = append(problems, fmt.Sprintf("close event store: %v", err))
			}
		}
		if components.EventBus != nil {
			components.EventBus.Close()
		}
		if components.Sandbox != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), timeout)
			if err := components.Sandbox.Close(closeCtx); err != nil {
				problems = append(problems, fmt.Sprintf("close wasm sandbox: %v", err))
			}
			closeCancel()
		}
		if components.APIServer != nil {
			// Releases the rate-limiter janitor goroutine; without it a restart
			// in the same process leaks one goroutine per server.
			components.APIServer.Close()
		}
	}

	s.mu.Lock()
	s.state = stateStopped
	s.mu.Unlock()

	if len(problems) > 0 {
		return fmt.Errorf("shutdown reported problems: %s", strings.Join(problems, "; "))
	}
	return nil
}

// drainWorkers lets the executor pump stop its own workers first (it owns the
// stop sequence) and only forces the issue when they are still registered after
// the grace period.
func drainWorkers(exec *executor.Executor, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for exec.WorkerCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if exec.WorkerCount() == 0 {
		return nil
	}

	remaining := time.Until(deadline)
	if remaining < time.Second {
		remaining = time.Second
	}
	done := make(chan error, 1)
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), remaining)
		defer cancel()
		done <- exec.StopAllWorkers(stopCtx)
	}()
	select {
	case err := <-done:
		if errors.Is(err, lwerrors.ErrWorkerNotFound) {
			return nil
		}
		return err
	case <-time.After(remaining + time.Second):
		return fmt.Errorf("worker drain timed out after %s with %d worker(s) still running; "+
			"in-flight tasks may be resumed as queued after a restart", remaining+time.Second, exec.WorkerCount())
	}
}

func portError(cfg *config.Config, err error) error {
	port := strings.TrimPrefix(cfg.Addr(), "0.0.0.0:")
	return fmt.Errorf("cannot listen on %s: %w\n"+
		"  cause: another process holds this port, or the address is not local to this host.\n"+
		"  fix:   run with --port <free port> (for example %d), or stop the process holding %s\n"+
		"         (Windows: netstat -ano | findstr %s, Linux/macOS: lsof -i :%s)\n"+
		"  docs:  README.md#quick-start",
		cfg.Addr(), err, cfg.Server.Port+1, port, port, port)
}

func (s *Server) handler() http.Handler {
	components := s.components
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.HealthHandler())
	mux.HandleFunc("/statusz", s.StatusHandler())
	mux.HandleFunc("/shutdown", s.HandleShutdown)
	// Authentication is pkg/api's job, in one place, with one credential set
	// (see authConfigFor). A second gate here used to read security.api_key
	// independently, which meant the file key and the environment keys were two
	// systems that could disagree about who is allowed in.
	mux.Handle("/", components.APIServer.Router)
	return mux
}

// GetComponents returns the server's components.
func (s *Server) GetComponents() *Components { return s.components }

// Scheduler exposes the task scheduler.
func (s *Server) Scheduler() *scheduler.Scheduler { return s.components.Scheduler }

// Executor exposes the task executor.
func (s *Server) Executor() *executor.Executor { return s.components.Executor }

// Observer exposes the metrics and log observer.
func (s *Server) Observer() *observer.Observer { return s.components.Observer }

// EventBus exposes the event bus.
func (s *Server) EventBus() *event.EventBus { return s.components.EventBus }

// PluginManager exposes the plugin manager.
func (s *Server) PluginManager() *plugin.PluginManager { return s.components.PluginMgr }

// WorkflowEngine exposes the DAG workflow engine.
func (s *Server) WorkflowEngine() *workflow.WorkflowEngine { return s.components.WorkflowEngine }

// SkillRegistry exposes the skill registry.
func (s *Server) SkillRegistry() *skill.SkillRegistry { return s.components.SkillRegistry }

type HealthResponse struct {
	Status    string            `json:"status"`
	State     string            `json:"state"`
	Checks    map[string]string `json:"checks"`
	Issues    []string          `json:"issues,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// HealthHandler reports the real state of each component instead of a fixed
// "ok": every check is derived from the component it names.
func (s *Server) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health := s.Health()
		w.Header().Set("Content-Type", "application/json")
		if health.Status != "healthy" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(health)
	}
}

// Health computes the health of the running server.
func (s *Server) Health() *HealthResponse {
	components := s.GetComponents()
	health := &HealthResponse{
		Status:    "healthy",
		State:     s.State(),
		Checks:    map[string]string{},
		Timestamp: time.Now(),
	}
	degrade := func(check, detail string) {
		health.Checks[check] = detail
		health.Status = "degraded"
		health.Issues = append(health.Issues, fmt.Sprintf("%s: %s", check, detail))
	}

	if components == nil {
		health.Checks["components"] = "not built"
		health.Status = "degraded"
		return health
	}

	stats := components.Scheduler.GetStats()
	queued, _ := stats["queued"].(int)
	failed, _ := stats["failed"].(int)
	deadLettered, _ := stats["dead_letter"].(int)
	// A task that exhausts its retries lands in dead_letter, not failed. Counting
	// only `failed` let every permanently stuck task report a green server.
	switch {
	case deadLettered > 0 || failed > 0:
		degrade("scheduler", fmt.Sprintf("%d failed task(s), %d dead-lettered (retries exhausted), %d queued", failed, deadLettered, queued))
	default:
		health.Checks["scheduler"] = fmt.Sprintf("ok (%d queued, persisted to %s)", queued, s.cfg.DBPath())
	}

	execStats := components.Executor.GetStats()
	health.Checks["executor"] = fmt.Sprintf("ok (%d/%d workers running, %d executed, %d failed)",
		execStats.ActiveWorkers, s.cfg.Workers.Count, execStats.TotalTasksRun, execStats.TotalTasksFailed)
	if execStats.TotalTasksFailed > 0 {
		health.Status = "degraded"
		health.Issues = append(health.Issues, fmt.Sprintf("executor: %d task(s) failed", execStats.TotalTasksFailed))
	}

	sbStats := components.Sandbox.GetStats()
	health.Checks["sandbox"] = fmt.Sprintf("ok (%d plugin(s), %d executions, limits %dMB/%ds)",
		sbStats.PluginsLoaded, sbStats.ExecutionsTotal, s.cfg.Sandbox.MaxMemoryMB, s.cfg.Sandbox.MaxCPUSeconds)
	if sbStats.PluginsLoaded == 0 {
		health.Checks["sandbox"] += " - no plugin loaded, wasm tasks will fail"
	}

	obsHealth := components.Observer.GetHealth()
	health.Checks["observer"] = fmt.Sprintf("ok (%v metrics, %v traces, %v logs)",
		obsHealth["metrics_count"], obsHealth["traces_count"], obsHealth["logs_count"])

	if components.eventStoreErr != nil {
		degrade("events", fmt.Sprintf("event store unavailable: %v", components.eventStoreErr))
	} else {
		busStats := components.EventBus.GetStats()
		health.Checks["events"] = fmt.Sprintf("ok (%d published, %d dropped, %d subscribers)",
			busStats.EventsPublished, busStats.EventsDropped, busStats.SubscriberCount)
		if busStats.EventsDropped > 0 {
			degrade("events", fmt.Sprintf("%d event(s) dropped", busStats.EventsDropped))
		}
	}

	if components.SelfHeal != nil {
		healStatus := components.SelfHeal.GetHealthStatus()
		health.Checks["selfheal"] = healStatus.String()
		if healStatus != selfheal.HealthHealthy {
			degrade("selfheal", healStatus.String())
		}
		// Name the individual checks. "selfheal: degraded" tells an operator that
		// something is wrong and nothing about what; the whole point of running a
		// health check is being able to act on it.
		for _, name := range sortedHealthReportNames(components.SelfHeal) {
			report, ok := components.SelfHeal.HealthStatus(name)
			if !ok {
				continue
			}
			entry := report.Status.String()
			if report.Error != nil {
				entry += ": " + report.Error.Error()
			}
			health.Checks["selfheal."+name] = entry
		}
	} else {
		health.Checks["selfheal"] = "disabled by configuration (selfheal.enabled=false)"
	}

	s.mu.Lock()
	run := s.pluginRun
	s.mu.Unlock()
	switch {
	case run == nil:
		// An absent key reads as "fine" to a health scraper. Say what is
		// actually known: the server is built but has not loaded plugins yet.
		health.Checks["plugins"] = fmt.Sprintf("not loaded yet (plugins.dir=%s); task execution starts once plugins are discovered", s.cfg.Plugins.Dir)
	case len(run.Failed) > 0:
		degrade("plugins", run.Summary())
	default:
		health.Checks["plugins"] = run.Summary()
	}

	s.mu.Lock()
	diag := s.diag
	s.mu.Unlock()
	if diag != nil {
		for _, c := range diag.Checks {
			if c.Status == StatusFail {
				degrade("startup:"+c.Name, c.Detail)
			}
		}
	}
	return health
}

type StatusResponse struct {
	Status     string                 `json:"status"`
	Version    string                 `json:"version"`
	Uptime     string                 `json:"uptime"`
	Addr       string                 `json:"addr"`
	Components map[string]string      `json:"components"`
	Stats      map[string]interface{} `json:"stats"`
}

// StatusHandler serves the computed status document.
func (s *Server) StatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.GetStatus())
	}
}

// GetStatus reports the real component state of the process.
func (s *Server) GetStatus() *StatusResponse {
	components := s.GetComponents()
	stats := map[string]interface{}{}
	componentsState := map[string]string{}
	if components != nil {
		stats = components.Scheduler.GetStats()
		execStats := components.Executor.GetStats()
		stats["workers_running"] = execStats.ActiveWorkers
		stats["workers_configured"] = s.cfg.Workers.Count
		stats["events"] = components.EventBus.GetStats()
		componentsState["scheduler"] = s.State()
		componentsState["executor"] = fmt.Sprintf("%d worker(s)", execStats.ActiveWorkers)
		componentsState["observer"] = "started"
		componentsState["sandbox"] = fmt.Sprintf("%d plugin(s)", components.Sandbox.GetStats().PluginsLoaded)
		componentsState["security"] = s.cfg.Security.Posture()
	}
	return &StatusResponse{
		Status:     s.State(),
		Version:    versionString(),
		Uptime:     time.Since(s.startedAt()).String(),
		Addr:       s.cfg.Addr(),
		Components: componentsState,
		Stats:      stats,
	}
}

// HandleShutdown stops the server when POSTed to; used by operators that cannot
// send a signal to the process.
func (s *Server) HandleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})

	utils.GoSafe(context.Background(), func(context.Context) {
		time.Sleep(100 * time.Millisecond)
		if err := s.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "Shutdown error: %v\n", err)
		}
	})
}

// WorkDir, DataDir and PluginsDir are convenience accessors for host commands.
func (s *Server) DataDir() string    { return s.cfg.Data.Dir }
func (s *Server) PluginsDir() string { return filepath.Clean(s.cfg.Plugins.Dir) }

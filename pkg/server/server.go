package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/api"
	"loopworker/pkg/dashboard"
	"loopworker/pkg/event"
	"loopworker/pkg/plugin"
	"loopworker/pkg/security"
	"loopworker/pkg/service"
	"loopworker/pkg/skill"
	"loopworker/pkg/utils"
	"loopworker/pkg/workflow"
)

type Server struct {
	config       *Config
	startTime    time.Time
	scheduler    *scheduler.Scheduler
	dispatcher   *dispatcher.Dispatcher
	executor     *executor.Executor
	sandbox      *sandbox.Sandbox
	pluginMgr    *plugin.PluginManager
	observer     *observer.Observer
	selfHeal     *selfheal.SelfHealer
	security     *security.SecurityManager
	apiServer    *api.APIServer
	dashboard    *dashboard.Dashboard
	eventBus     *event.EventBus
	serviceMgr   *service.ServiceManager
	httpServer   *http.Server
	skillRegistry *skill.SkillRegistry
	workflowEngine *workflow.WorkflowEngine
	taskBridge   *scheduler.SchedulerBridge
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
}

type Config struct {
	Port       int
	PluginsDir string
	DataDir    string
	WorkDir    string
	Language   string
	Theme      string
}

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	workDir := filepath.Join(home, ".loopworker")

	return &Config{
		Port:       19527,
		PluginsDir: filepath.Join(workDir, "plugins"),
		DataDir:    filepath.Join(workDir, "data"),
		WorkDir:    workDir,
		Language:   "en",
		Theme:      "glass",
	}
}

func New(config *Config) *Server {
	if config == nil {
		config = DefaultConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	_ = service.EnsureDirectories(config.DataDir)

	var eventStore event.EventStore = nil
	if config.DataDir != "" {
		store, storeErr := event.NewLocalEventStore(config.DataDir)
		if storeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to open event store at %s: %v (events not persisted)\n", config.DataDir, storeErr)
		} else {
			eventStore = store
		}
	}

	bus := event.NewEventBus(eventStore)

	sb := sandbox.NewSandbox(sandbox.SandboxConfig{
		MaxMemoryMB:   256,
		MaxCPUSeconds: 30,
		MaxOutputMB:   64,
		MaxConcurrent: 10,
	})
	sb.SetEventBus(bus)

	// Build SkillRegistry with built-in skill definitions
	skillRegistry := skill.NewSkillRegistry()
	skillRegistry.Register(skill.SkillDefinition{
		Name:        "llm.chat",
		Version:     "1.0.0",
		Description: "LLM structured chat completion",
		InputTypes:  []string{"text", "json"},
		OutputTypes: []string{"text", "json"},
	}, nil)
	skillRegistry.Register(skill.SkillDefinition{
		Name:        "research.anomaly",
		Version:     "1.0.0",
		Description: "Anomaly detection on numeric data",
		InputTypes:  []string{"[]float64"},
		OutputTypes: []string{"json"},
	}, nil)

	pm, _ := plugin.NewPluginManager(sb, bus, config.PluginsDir, plugin.WithSkillRegistry(skillRegistry))

	// Build WorkflowEngine with event bus and task bridge
	wfe := workflow.NewWorkflowEngine(
		workflow.WithEventBus(bus),
	)

	s := scheduler.NewScheduler(bus)
	bridge := scheduler.NewSchedulerBridge(s)
	wfe.SetDispatcher(bridge)
	d := dispatcher.NewDispatcher(s, bus)
	o := observer.NewObserver(bus)
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())
	e := executor.NewExecutor(s, d, sb, bus, sh)
	sm := security.NewSecurityManager()
	apiSrv := api.NewAPIServer(s, e, bus, wfe)
	dash := dashboard.NewDashboard(s, o, bus)
	mgr := service.NewServiceManager()

	// Build SkillContext and inject into executor
	// Config map is always non-nil so downstream code can safely store keys into it.
	skillConfig := map[string]interface{}{}
	skillCtx := skillRegistry.BuildContext(nil, bus, nil, skillConfig)
	e.SetSkillContext(skillCtx)

	return &Server{
		config:        config,
		startTime:     time.Now(),
		scheduler:     s,
		dispatcher:    d,
		executor:      e,
		sandbox:       sb,
		pluginMgr:     pm,
		observer:      o,
		selfHeal:      sh,
		security:      sm,
		apiServer:     apiSrv,
		dashboard:     dash,
		eventBus:      bus,
		serviceMgr:    mgr,
		httpServer:    nil,
		skillRegistry: skillRegistry,
		workflowEngine: wfe,
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (s *Server) Start() error {
	// Ensure directories exist
	if err := service.EnsureDirectories(
		s.config.WorkDir,
		s.config.PluginsDir,
		s.config.DataDir,
	); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}

	// Start observer
	_ = s.observer.Start(s.ctx)

	// Start worker and watchdog
	_ = s.executor.StartWorker(s.ctx, "worker-1", "default")
	s.executor.StartWatchdog(s.ctx)

	// Start dashboard
	s.dashboard.StartEventListening()
	s.dashboard.RegisterHandlers()

	addr := fmt.Sprintf(":%d", s.config.Port)
	fmt.Printf("LoopWorker starting on http://localhost%s\n", addr)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.apiServer.Router,
	}

	return s.httpServer.ListenAndServe()
}

func (s *Server) Stop() error {
	s.cancel()

	// Gracefully shutdown HTTP server
	if s.httpServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP shutdown error: %v\n", err)
		}
	}

	// Stop executor workers
	if err := s.executor.StopAllWorkers(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Worker shutdown error: %v\n", err)
	}

	s.eventBus.Close()
	s.serviceMgr.StopAll()
	return nil
}

func (s *Server) GetScheduler() *scheduler.Scheduler {
	return s.scheduler
}

func (s *Server) GetExecutor() *executor.Executor {
	return s.executor
}

func (s *Server) GetObserver() *observer.Observer {
	return s.observer
}

func (s *Server) GetSecurity() *security.SecurityManager {
	return s.security
}

// WorkflowEngine returns the server's workflow engine.
func (s *Server) WorkflowEngine() *workflow.WorkflowEngine {
	return s.workflowEngine
}

// GetWorkflowEngine returns the workflow engine (used by API layer via interface).
func (s *Server) GetWorkflowEngine() interface{} {
	return s.workflowEngine
}

// SkillRegistry returns the server's skill registry.
func (s *Server) SkillRegistry() *skill.SkillRegistry {
	return s.skillRegistry
}

type HealthResponse struct {
	Status    string            `json:"status"`
	Checks    map[string]string `json:"checks"`
	Timestamp time.Time         `json:"timestamp"`
}

// HealthHandler returns an http.HandlerFunc for the health endpoint.
// Use this instead of registering directly on DefaultServeMux to avoid
// duplicate registration panics in tests.
func (s *Server) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health := HealthResponse{
			Status:    "healthy",
			Checks:    make(map[string]string),
			Timestamp: time.Now(),
		}

		// Check scheduler
		stats := s.scheduler.GetStats()
		if failed, ok := stats["failed"].(int); ok && failed > 0 {
			health.Checks["scheduler"] = fmt.Sprintf("ok (%d failed tasks)", failed)
		} else {
			health.Checks["scheduler"] = "ok"
		}

		// Check executor
		execStats := s.executor.GetStats()
		if execStats.TotalTasksFailed > 0 {
			health.Checks["executor"] = fmt.Sprintf("ok (%d workers, %d failed)", execStats.ActiveWorkers, execStats.TotalTasksFailed)
		} else {
			health.Checks["executor"] = fmt.Sprintf("ok (%d workers)", execStats.ActiveWorkers)
		}

		// Check observer
		health.Checks["observer"] = "ok"

		// Check sandbox
		health.Checks["sandbox"] = "ok"

		// Check self-healer
		healStatus := s.selfHeal.GetHealthStatus()
		health.Checks["selfheal"] = healStatus.String()
		if healStatus != selfheal.HealthHealthy {
			health.Status = "degraded"
		}

		w.Header().Set("Content-Type", "application/json")
		if health.Status != "healthy" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(health)
	}
}

// SetupHealthEndpoint registers the health endpoint on http.DefaultServeMux.
// Deprecated: Use HealthHandler() with a custom mux instead.
func (s *Server) SetupHealthEndpoint() {
	http.HandleFunc("/health", s.HealthHandler())
}

type StatusResponse struct {
	Status     string                 `json:"status"`
	Uptime     string                 `json:"uptime"`
	Components map[string]string      `json:"components"`
	Stats      map[string]interface{} `json:"stats"`
}

func (s *Server) GetStatus() *StatusResponse {
	return &StatusResponse{
		Status: "running",
		Uptime: time.Since(s.startTime).String(),
		Components: map[string]string{
			"scheduler": "ok",
			"executor":  "ok",
			"observer":  "ok",
			"security":  "ok",
		},
		Stats: s.scheduler.GetStats(),
	}
}

func (s *Server) HandleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "shutting_down",
	})

	utils.GoSafe(context.Background(), func(ctx context.Context) {
		time.Sleep(100 * time.Millisecond)
		if err := s.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "Shutdown error: %v\n", err)
		}
	})
}

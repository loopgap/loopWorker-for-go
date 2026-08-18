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

	"go.uber.org/zap"
	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/api"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/plugin"
	"loopworker/pkg/security"
	"loopworker/pkg/skill"
	"loopworker/pkg/utils"
	"loopworker/pkg/workflow"
)

// ensureDirectories creates directories if they don't exist.
func ensureDirectories(dirs ...string) error {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

// ServiceManager manages the server lifecycle.
type ServiceManager struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func NewServiceManager() *ServiceManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &ServiceManager{ctx: ctx, cancel: cancel}
}

func (m *ServiceManager) StopAll() {
	m.cancel()
}

// Components holds all the core components of the LoopWorker server.
type Components struct {
	Scheduler      *scheduler.Scheduler
	Dispatcher     *dispatcher.Dispatcher
	Executor       *executor.Executor
	Sandbox        *sandbox.Sandbox
	PluginMgr      *plugin.PluginManager
	Observer       *observer.Observer
	SelfHeal       *selfheal.SelfHealer
	Security       *security.SecurityManager
	APIServer      *api.APIServer
	EventBus       *event.EventBus
	ServiceMgr     *ServiceManager
	SkillRegistry  *skill.SkillRegistry
	WorkflowEngine *workflow.WorkflowEngine
	TaskBridge     *scheduler.SchedulerBridge
}

// BuildComponents creates all the core components from the given configuration.
func BuildComponents(config *Config) (*Components, error) {
	if config == nil {
		config = DefaultConfig()
	}

	// Ensure data directory exists
	if err := ensureDirectories(config.DataDir); err != nil {
		return nil, fmt.Errorf("ensure data directory: %w", err)
	}

	// Create event store
	var eventStore event.EventStore = nil
	if config.DataDir != "" {
		store, storeErr := event.NewLocalEventStore(config.DataDir)
		if storeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to open event store at %s: %v (events not persisted)\n", config.DataDir, storeErr)
		} else {
			eventStore = store
		}
	}

	// Create event bus
	bus := event.NewEventBus(eventStore)

	// Create sandbox
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{
		MaxMemoryMB:   256,
		MaxCPUSeconds: 30,
		MaxOutputMB:   64,
		MaxConcurrent: 10,
	})
	sb.SetEventBus(bus)

	// Create skill registry with built-in skills
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

	// Create plugin manager
	pm, err := plugin.NewPluginManager(sb, bus, config.PluginsDir, plugin.WithSkillRegistry(skillRegistry))
	if err != nil {
		return nil, fmt.Errorf("create plugin manager: %w", err)
	}

	// Create workflow engine
	wfe := workflow.NewWorkflowEngine(
		workflow.WithEventBus(bus),
	)

	// Create scheduler and dispatcher
	s := scheduler.NewScheduler(bus)
	bridge := scheduler.NewSchedulerBridge(s)
	wfe.SetDispatcher(bridge)
	d := dispatcher.NewDispatcher(s, bus)

	// Create observer
	o := observer.NewObserver(bus)

	// Create self-healer
	sh := selfheal.NewSelfHealer(selfheal.DefaultConfig())

	// Create executor
	e := executor.NewExecutor(s, d, sb, bus, sh)

	// Create security manager
	sm := security.NewSecurityManager()

	// Create API server
	apiSrv := api.NewAPIServer(s, e, bus, wfe, o)

	// Create service manager
	mgr := NewServiceManager()

	// Build skill context and inject into executor
	skillConfig := map[string]interface{}{}
	skillCtx := skillRegistry.BuildContext(nil, bus, nil, skillConfig)
	e.SetSkillContext(skillCtx)

	return &Components{
		Scheduler:      s,
		Dispatcher:     d,
		Executor:       e,
		Sandbox:        sb,
		PluginMgr:      pm,
		Observer:       o,
		SelfHeal:       sh,
		Security:       sm,
		APIServer:      apiSrv,
		EventBus:       bus,
		ServiceMgr:     mgr,
		SkillRegistry:  skillRegistry,
		WorkflowEngine: wfe,
		TaskBridge:     bridge,
	}, nil
}

type Server struct {
	config     *Config
	startTime  time.Time
	components *Components
	httpServer *http.Server
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
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

	components, err := BuildComponents(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to build components: %v\n", err)
		// Continue with partial components
	}

	return &Server{
		config:     config,
		startTime:  time.Now(),
		components: components,
		httpServer: nil,
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (s *Server) Start() error {
	if s.components == nil {
		return fmt.Errorf("server components not initialized")
	}

	// Ensure directories exist
	if err := ensureDirectories(
		s.config.WorkDir,
		s.config.PluginsDir,
		s.config.DataDir,
	); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}

	// Start observer
	if err := s.components.Observer.Start(s.ctx); err != nil {
		return fmt.Errorf("start observer: %w", err)
	}

	// Start worker and watchdog
	if err := s.components.Executor.StartWorker(s.ctx, "worker-1", "default"); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	s.components.Executor.StartWatchdog(s.ctx)

	addr := fmt.Sprintf(":%d", s.config.Port)
	logger.Info("LoopWorker starting", zap.String("address", addr))

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.components.APIServer.Router,
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
	if s.components != nil && s.components.Executor != nil {
		if err := s.components.Executor.StopAllWorkers(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "Worker shutdown error: %v\n", err)
		}
	}

	// Close event bus
	if s.components != nil && s.components.EventBus != nil {
		s.components.EventBus.Close()
	}

	// Stop service manager
	if s.components != nil && s.components.ServiceMgr != nil {
		s.components.ServiceMgr.StopAll()
	}

	return nil
}

// GetComponents returns the server's components.
func (s *Server) GetComponents() *Components {
	return s.components
}

func (s *Server) GetScheduler() *scheduler.Scheduler {
	if s.components == nil {
		return nil
	}
	return s.components.Scheduler
}

func (s *Server) GetExecutor() *executor.Executor {
	if s.components == nil {
		return nil
	}
	return s.components.Executor
}

func (s *Server) GetObserver() *observer.Observer {
	if s.components == nil {
		return nil
	}
	return s.components.Observer
}

func (s *Server) GetSecurity() *security.SecurityManager {
	if s.components == nil {
		return nil
	}
	return s.components.Security
}

// WorkflowEngine returns the server's workflow engine.
func (s *Server) WorkflowEngine() *workflow.WorkflowEngine {
	if s.components == nil {
		return nil
	}
	return s.components.WorkflowEngine
}

// GetWorkflowEngine returns the workflow engine (used by API layer via interface).
func (s *Server) GetWorkflowEngine() interface{} {
	if s.components == nil {
		return nil
	}
	return s.components.WorkflowEngine
}

// SkillRegistry returns the server's skill registry.
func (s *Server) SkillRegistry() *skill.SkillRegistry {
	if s.components == nil {
		return nil
	}
	return s.components.SkillRegistry
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
		if s.components != nil && s.components.Scheduler != nil {
			stats := s.components.Scheduler.GetStats()
			if failed, ok := stats["failed"].(int); ok && failed > 0 {
				health.Checks["scheduler"] = fmt.Sprintf("ok (%d failed tasks)", failed)
			} else {
				health.Checks["scheduler"] = "ok"
			}
		} else {
			health.Checks["scheduler"] = "not available"
		}

		// Check executor
		if s.components != nil && s.components.Executor != nil {
			execStats := s.components.Executor.GetStats()
			if execStats.TotalTasksFailed > 0 {
				health.Checks["executor"] = fmt.Sprintf("ok (%d workers, %d failed)", execStats.ActiveWorkers, execStats.TotalTasksFailed)
			} else {
				health.Checks["executor"] = fmt.Sprintf("ok (%d workers)", execStats.ActiveWorkers)
			}
		} else {
			health.Checks["executor"] = "not available"
		}

		// Check observer
		health.Checks["observer"] = "ok"

		// Check sandbox
		health.Checks["sandbox"] = "ok"

		// Check self-healer
		if s.components != nil && s.components.SelfHeal != nil {
			healStatus := s.components.SelfHeal.GetHealthStatus()
			health.Checks["selfheal"] = healStatus.String()
			if healStatus != selfheal.HealthHealthy {
				health.Status = "degraded"
			}
		} else {
			health.Checks["selfheal"] = "not available"
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
	stats := map[string]interface{}{}
	if s.components != nil && s.components.Scheduler != nil {
		stats = s.components.Scheduler.GetStats()
	}

	return &StatusResponse{
		Status: "running",
		Uptime: time.Since(s.startTime).String(),
		Components: map[string]string{
			"scheduler": "ok",
			"executor":  "ok",
			"observer":  "ok",
			"security":  "ok",
		},
		Stats: stats,
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

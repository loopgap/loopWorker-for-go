package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/logger"
	"loopworker/pkg/security"
	"loopworker/pkg/utils"
	"loopworker/pkg/workflow"
	"go.uber.org/zap"
)

//go:embed all:dist
var webCanvas embed.FS

var requestIDCounter int64

type APIResponse struct {
	Success   bool        `json:"success"`
	Data      interface{} `json:"data,omitempty"`
	Error     string      `json:"error,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	RequestID string      `json:"request_id"`
}

type APIServer struct {
	Router      *chi.Mux
	scheduler   *scheduler.Scheduler
	wfe         *workflow.WorkflowEngine
	executor    *executor.Executor
	observer    *observer.Observer
	eventBus    *event.EventBus
	rateLimiter *security.RateLimiter
}

func NewAPIServer(sched *scheduler.Scheduler, exec *executor.Executor, bus *event.EventBus, wfe *workflow.WorkflowEngine, obs *observer.Observer) *APIServer {
	r := chi.NewRouter()

	// Security middlewares
	r.Use(corsMiddleware)
	r.Use(rateLimitMiddleware)
	r.Use(requestIDMiddleware)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(requestBodyLimitMiddleware)
	r.Use(requestLoggingMiddleware)

	api := &APIServer{
		Router:      r,
		scheduler:   sched,
		wfe:         wfe,
		executor:    exec,
		observer:    obs,
		eventBus:    bus,
		rateLimiter: security.NewRateLimiter(100, time.Minute),
	}

	api.registerRoutes()
	return api
}

// CORS middleware with configurable allowed origins
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowedOrigins := []string{"*"}

		for _, allowed := range allowedOrigins {
			if allowed == "*" || allowed == origin {
				w.Header().Set("Access-Control-Allow-Origin", allowed)
				break
			}
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware limits requests per IP using the security.RateLimiter.
// Returns 429 Too Many Requests when the client exceeds the rate limit.
func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ip = xff
		}
		if !DefaultRateLimiter.Allow(ip) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// DefaultRateLimiter is the global rate limiter shared by all API handlers.
// It allows 100 requests per minute per IP.
var DefaultRateLimiter = security.NewRateLimiter(100, time.Minute)

// requestIDMiddleware ensures every request has a unique identifier
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&requestIDCounter, 1))
		}
		ctx := context.WithValue(r.Context(), "request_id", requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestBodyLimitMiddleware limits request body size to 10MB
func requestBodyLimitMiddleware(next http.Handler) http.Handler {
	const maxBodySize = 10 << 20 // 10MB
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		next.ServeHTTP(w, r)
	})
}

// requestLoggingMiddleware logs all incoming requests
func requestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)

		logger.Info("request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", rw.statusCode),
			zap.Duration("duration", duration))
	})
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (s *APIServer) registerRoutes() {
	s.Router.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", s.healthCheck)
		r.Get("/workflow/graph", s.getWorkflowGraph)
		r.Get("/workflow/list", s.listWorkflows)

		r.Post("/workflow/execute", s.executeWorkflow)
		r.Get("/events/live", s.streamEventsLive)
		r.Get("/metrics", s.getMetrics)
		r.Get("/logs", s.getLogs)

		r.Route("/tasks", func(r chi.Router) {
			r.Get("/", s.listTasks)
			r.Post("/", s.createTask)
			r.Route("/{taskID}", func(r chi.Router) {
				r.Get("/", s.getTask)
				r.Delete("/", s.deleteTask)
				r.Post("/dependencies", s.addTaskDependency)
			})
		})

		r.Route("/workers", func(r chi.Router) {
			r.Get("/", s.listWorkers)
		})
	})

	// Prometheus metrics endpoint
	s.Router.Handle("/metrics", promhttp.Handler())

	// Embedded static canvas client
	fs := http.FileServer(http.FS(webCanvas))
	s.Router.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		ext := filepath.Ext(r.URL.Path)
		if ext == "" {
			r.URL.Path = "/dist/index.html"
		} else {
			r.URL.Path = "/dist" + r.URL.Path
		}

		fs.ServeHTTP(w, r)
	}))
}

// Helpers for responses
func sendSuccess(w http.ResponseWriter, r *http.Request, data interface{}, status int) {
	render.Status(r, status)
	render.JSON(w, r, APIResponse{
		Success:   true,
		Data:      data,
		Timestamp: time.Now(),
		RequestID: middleware.GetReqID(r.Context()),
	})
}

func sendError(w http.ResponseWriter, r *http.Request, err error, status int) {
	render.Status(r, status)
	render.JSON(w, r, APIResponse{
		Success:   false,
		Error:     err.Error(),
		Timestamp: time.Now(),
		RequestID: middleware.GetReqID(r.Context()),
	})
}

// Handlers
func (s *APIServer) healthCheck(w http.ResponseWriter, r *http.Request) {
	stats := s.scheduler.GetStats()
	sendSuccess(w, r, map[string]interface{}{
		"status": "healthy",
		"stats":  stats,
	}, http.StatusOK)
}

// getMetrics returns observer metrics (replaces retired dashboard /api/metrics).
func (s *APIServer) getMetrics(w http.ResponseWriter, r *http.Request) {
	if s.observer == nil {
		sendError(w, r, fmt.Errorf("observer not available"), http.StatusServiceUnavailable)
		return
	}
	metrics := s.observer.GetMetrics()
	sendSuccess(w, r, metrics, http.StatusOK)
}

// getLogs returns observer logs (replaces retired dashboard /api/logs).
func (s *APIServer) getLogs(w http.ResponseWriter, r *http.Request) {
	if s.observer == nil {
		sendError(w, r, fmt.Errorf("observer not available"), http.StatusServiceUnavailable)
		return
	}
	logs := s.observer.GetLogs()
	sendSuccess(w, r, logs, http.StatusOK)
}

type CreateTaskRequest struct {
	Type        string                 `json:"type"`
	Config      map[string]interface{} `json:"config"`
	Input       []byte                 `json:"input"`
	IsAgent     bool                   `json:"is_agent"`
	AgentConfig *scheduler.AgentConfig `json:"agent_config"`
}

func (req *CreateTaskRequest) Bind(r *http.Request) error {
	if req.Type == "" {
		return lwerrors.ErrTaskInvalid
	}
	if len(req.Type) > 255 {
		return fmt.Errorf("task type too long (max 255 characters)")
	}
	if len(req.Input) > 10*1024*1024 { // 10MB limit
		return fmt.Errorf("task input too large (max 10MB)")
	}
	return nil
}

func (s *APIServer) listTasks(w http.ResponseWriter, r *http.Request) {
	// Parse pagination parameters
	limit := 50 // default
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := fmt.Sscanf(l, "%d", &limit); err == nil && parsed == 1 {
			if limit < 1 {
				limit = 1
			}
			if limit > 1000 {
				limit = 1000
			}
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := fmt.Sscanf(o, "%d", &offset); err == nil && parsed == 1 {
			if offset < 0 {
				offset = 0
			}
		}
	}

	// Parse filter parameters
	filter := scheduler.TaskFilter{}
	if state := r.URL.Query().Get("state"); state != "" {
		filter.States = []scheduler.TaskState{scheduler.TaskState(state)}
	}
	if taskType := r.URL.Query().Get("type"); taskType != "" {
		filter.Types = []string{taskType}
	}

	tasks := s.scheduler.ListTasks(filter)

	// Apply pagination
	total := len(tasks)
	if offset >= total {
		tasks = []*scheduler.Task{}
	} else {
		end := offset + limit
		if end > total {
			end = total
		}
		tasks = tasks[offset:end]
	}

	// Return with pagination metadata
	sendSuccess(w, r, map[string]interface{}{
		"tasks":  tasks,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}, http.StatusOK)
}

func (s *APIServer) createTask(w http.ResponseWriter, r *http.Request) {
	data := &CreateTaskRequest{}
	if err := render.Bind(r, data); err != nil {
		sendError(w, r, err, http.StatusBadRequest)
		return
	}

	task, err := s.scheduler.CreateTask(r.Context(), data.Type, data.Config, data.Input)
	if err != nil {
		sendError(w, r, err, http.StatusInternalServerError)
		return
	}

	if data.IsAgent {
		task.IsAgent = true
		task.AgentConfig = data.AgentConfig
		s.scheduler.SaveTask(task)
	}

	if err := s.scheduler.QueueTask(r.Context(), task.ID); err != nil {
		sendError(w, r, err, http.StatusInternalServerError)
		return
	}

	sendSuccess(w, r, task, http.StatusCreated)
}

func (s *APIServer) getTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskID")
	task, exists := s.scheduler.GetTask(id)
	if !exists {
		sendError(w, r, lwerrors.ErrTaskNotFound, http.StatusNotFound)
		return
	}
	sendSuccess(w, r, task, http.StatusOK)
}

func (s *APIServer) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskID")
	if err := s.scheduler.CancelTask(r.Context(), id); err != nil {
		sendError(w, r, err, http.StatusInternalServerError)
		return
	}
	sendSuccess(w, r, map[string]string{"id": id, "status": "cancelled"}, http.StatusOK)
}

func (s *APIServer) listWorkers(w http.ResponseWriter, r *http.Request) {
	workers := s.executor.ListWorkers()
	sendSuccess(w, r, workers, http.StatusOK)
}

func (s *APIServer) getWorkflowGraph(w http.ResponseWriter, r *http.Request) {
	tasks := s.scheduler.ListTasks(scheduler.TaskFilter{})

	taskMap := make(map[string]*scheduler.Task)
	for _, t := range tasks {
		taskMap[t.ID] = t
	}

	depths := make(map[string]int)
	var getDepth func(id string) int
	getDepth = func(id string) int {
		if d, exists := depths[id]; exists {
			return d
		}
		t, exists := taskMap[id]
		if !exists || len(t.Dependencies) == 0 {
			depths[id] = 0
			return 0
		}
		maxDep := 0
		for _, dep := range t.Dependencies {
			depDepth := getDepth(dep)
			if depDepth > maxDep {
				maxDep = depDepth
			}
		}
		depths[id] = maxDep + 1
		return maxDep + 1
	}

	for _, t := range tasks {
		getDepth(t.ID)
	}

	depthGroups := make(map[int][]string)
	maxDepth := 0
	for id, d := range depths {
		depthGroups[d] = append(depthGroups[d], id)
		if d > maxDepth {
			maxDepth = d
		}
	}

	type Position struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	type Node struct {
		ID       string                 `json:"id"`
		Type     string                 `json:"type"`
		Position Position               `json:"position"`
		Data     map[string]interface{} `json:"data"`
	}
	type Edge struct {
		ID       string `json:"id"`
		Source   string `json:"source"`
		Target   string `json:"target"`
		Animated bool   `json:"animated"`
	}

	var nodes []Node
	var edges []Edge

	for d := 0; d <= maxDepth; d++ {
		ids := depthGroups[d]
		colX := float64(100 + d*300)
		numInCol := len(ids)

		for idx, id := range ids {
			t := taskMap[id]
			rowY := float64(100 + idx*180)
			if numInCol > 1 {
				rowY = float64(100 + idx*(500/numInCol))
			}

			nodeType := "wasmNode"
			if t.IsAgent {
				nodeType = "agentNode"
			}

			nodes = append(nodes, Node{
				ID:       t.ID,
				Type:     nodeType,
				Position: Position{X: colX, Y: rowY},
				Data: map[string]interface{}{
					"label": t.Type,
					"task":  t,
				},
			})

			for _, depID := range t.Dependencies {
				edges = append(edges, Edge{
					ID:       fmt.Sprintf("e-%s-%s", depID, t.ID),
					Source:   depID,
					Target:   t.ID,
					Animated: t.State == scheduler.StateRunning || t.State == scheduler.StateQueued,
				})
			}
		}
	}

	sendSuccess(w, r, map[string]interface{}{
		"nodes": nodes,
		"edges": edges,
	}, http.StatusOK)
}

func (s *APIServer) streamEventsLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	types := []event.EventType{
		event.EventTaskCreated,
		event.EventTaskStarted,
		event.EventTaskCompleted,
		event.EventTaskFailed,
		event.EventTaskRetried,
		event.EventTaskCancelled,
		event.EventPluginExecuted,
		event.EventSkillInvoked,
		event.EventResearchFinding,
		event.EventWorkflowStepCompleted,
		event.EventWorkflowStarted,
		event.EventWorkflowCompleted,
		event.EventWorkflowFailed,
	}

	var subs []*event.Subscriber
	for _, t := range types {
		subs = append(subs, s.eventBus.Subscribe(t, 100))
	}

	defer func() {
		for _, sub := range subs {
			s.eventBus.Unsubscribe(sub)
		}
	}()

	mergedCh := make(chan event.Event, 500)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	for _, sub := range subs {
		go func(ch <-chan event.Event) {
			for {
				select {
				case <-ctx.Done():
					return
				case evt, ok := <-ch:
					if !ok {
						return
					}
					select {
					case mergedCh <- evt:
					case <-ctx.Done():
						return
					}
				}
			}
		}(sub.Chan())
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		case evt := <-mergedCh:
			payloadJSON, _ := json.Marshal(evt.Payload())

			var taskData []byte
			if payloadMap, ok := evt.Payload().(map[string]interface{}); ok {
				if taskID, ok := payloadMap["task_id"].(string); ok {
					if t, exists := s.scheduler.GetTask(taskID); exists {
						var marshalErr error
						taskData, marshalErr = json.Marshal(t)
						if marshalErr != nil {
							// 记录错误但不阻塞响应
							logger.Warn("failed to marshal task", zap.Error(marshalErr))
						}
					}
				}
			}
			if len(taskData) == 0 {
				taskData = payloadJSON
			}

			fmt.Fprintf(w, "event: %s\n", string(evt.Type()))
			fmt.Fprintf(w, "data: %s\n\n", string(taskData))
			flusher.Flush()
		}
	}
}

func (s *APIServer) addTaskDependency(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	type DepReq struct {
		DependencyID string `json:"dependency_id"`
	}
	var data DepReq
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		sendError(w, r, err, http.StatusBadRequest)
		return
	}

	if err := s.scheduler.AddDependency(r.Context(), taskID, data.DependencyID); err != nil {
		sendError(w, r, err, http.StatusInternalServerError)
		return
	}

	sendSuccess(w, r, map[string]string{"status": "dependency_added"}, http.StatusOK)
}

// listWorkflows returns all registered workflows from the WorkflowEngine.
func (s *APIServer) listWorkflows(w http.ResponseWriter, r *http.Request) {
	if s.wfe == nil {
		sendError(w, r, fmt.Errorf("workflow engine not available"), http.StatusServiceUnavailable)
		return
	}

	workflows := s.wfe.ListWorkflows()
	result := make([]map[string]interface{}, len(workflows))
	for i, wf := range workflows {
		result[i] = map[string]interface{}{
			"id":     wf.ID,
			"name":   wf.Name,
			"status": wf.GetStatus(),
		}
	}
	sendSuccess(w, r, map[string]interface{}{"workflows": result}, http.StatusOK)
}

// executeWorkflow triggers execution of a workflow by ID.
func (s *APIServer) executeWorkflow(w http.ResponseWriter, r *http.Request) {
	type ExecuteReq struct {
		WorkflowID string `json:"workflow_id"`
	}

	var data ExecuteReq
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		sendError(w, r, err, http.StatusBadRequest)
		return
	}

	if data.WorkflowID == "" {
		sendError(w, r, fmt.Errorf("workflow_id is required"), http.StatusBadRequest)
		return
	}

	// Execute workflow in background (in-memory, synchronous engine)
	utils.GoSafe(r.Context(), func(ctx context.Context) {
		if s.wfe == nil {
			return
		}
		if err := s.wfe.Execute(ctx, data.WorkflowID); err != nil {
			sendError(w, r, err, http.StatusInternalServerError)
			return
		}
	})

	sendSuccess(w, r, map[string]interface{}{
		"workflow_id": data.WorkflowID,
		"status":      "executing",
		"message":     "workflow execution started",
	}, http.StatusAccepted)
}

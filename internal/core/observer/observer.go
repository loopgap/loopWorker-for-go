package observer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"loopworker/pkg/event"
	pkglogger "loopworker/pkg/logger"
)

type MetricType string

const (
	MetricCounter   MetricType = "counter"
	MetricGauge     MetricType = "gauge"
	MetricHistogram MetricType = "histogram"

	// Maximum number of traces/logs retained in memory before oldest are evicted.
	maxTraces = 1000
	maxLogs   = 5000
)

// unknownLabel is the label value used when an event carries no usable
// discriminator. It is a fixed string, never per-event data, so it does not
// create a new time series per task.
const unknownLabel = "unknown"

// workerLabel normalises a worker id for use as a label value. Worker ids come
// from a bounded pool, unlike task ids and error strings.
func workerLabel(workerID string) string {
	if workerID == "" {
		return unknownLabel
	}
	return workerID
}

type Metric struct {
	Name      string            `json:"name"`
	Type      MetricType        `json:"type"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels"`
	Timestamp time.Time         `json:"timestamp"`
}

type MetricFamily struct {
	Name    string     `json:"name"`
	Type    MetricType `json:"type"`
	Metrics []Metric   `json:"metrics"`
}

type TraceSpan struct {
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Operation  string            `json:"operation"`
	StartTime  time.Time         `json:"start_time"`
	EndTime    *time.Time        `json:"end_time,omitempty"`
	Status     string            `json:"status"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Duration   time.Duration     `json:"duration"`
}

type LogEntry struct {
	Level     string                 `json:"level"`
	Message   string                 `json:"message"`
	Timestamp time.Time              `json:"timestamp"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

var (
	GlobalLogger *zap.Logger
)

func init() {
	// Use the project's global logger instead of creating a duplicate production logger.
	// Callers that need a different logger can set GlobalLogger before NewObserver().
	GlobalLogger = pkglogger.Get()
}

type Observer struct {
	eventBus *event.EventBus
	logger   *zap.Logger

	// Keep old data for TUI/Dashboard compatibility
	metrics map[string]*metricAccumulator
	traces  []TraceSpan
	logs    []LogEntry
	mu      sync.RWMutex
	logMu   sync.RWMutex
	traceMu sync.RWMutex

	// Prometheus Metrics
	tasksCreated   *prometheus.CounterVec
	tasksStarted   *prometheus.CounterVec
	tasksCompleted *prometheus.CounterVec
	tasksFailed    *prometheus.CounterVec
	taskDuration   *prometheus.HistogramVec
}

type metricAccumulator struct {
	name  string
	mType MetricType
	count int64
	sum   float64
	min   float64
	max   float64
}

func NewObserver(eventBus *event.EventBus) *Observer {
	o := &Observer{
		eventBus: eventBus,
		logger:   GlobalLogger,
		metrics:  make(map[string]*metricAccumulator),
	}

	o.tasksCreated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "loopworker_tasks_created_total",
			Help: "Total number of tasks created",
		},
		[]string{"type"},
	)
	o.tasksStarted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "loopworker_tasks_started_total",
			Help: "Total number of tasks started",
		},
		[]string{"type"},
	)
	o.tasksCompleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "loopworker_tasks_completed_total",
			Help: "Total number of tasks completed",
		},
		[]string{"type"},
	)
	o.tasksFailed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "loopworker_tasks_failed_total",
			Help: "Total number of tasks failed",
		},
		[]string{"type"},
	)
	o.taskDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "loopworker_task_duration_seconds",
			Help:    "Duration of tasks in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"type"},
	)

	o.registerCollectors()

	return o
}

// registerCollectors publishes the metric vectors to the default registry.
// A second Observer (tests, or a second server in one process) collides with the
// first, and prometheus signals that with AlreadyRegisteredError. Discarding that
// error leaves the new Observer incrementing collectors nobody gathers, so its
// metrics silently disappear; reusing the already-registered collector keeps
// every Observer writing to the same, visible series.
func (o *Observer) registerCollectors() {
	registry := prometheus.DefaultRegisterer

	if existing, err := registerCounterVec(registry, o.tasksCreated); err == nil {
		o.tasksCreated = existing
	}
	if existing, err := registerCounterVec(registry, o.tasksStarted); err == nil {
		o.tasksStarted = existing
	}
	if existing, err := registerCounterVec(registry, o.tasksCompleted); err == nil {
		o.tasksCompleted = existing
	}
	if existing, err := registerCounterVec(registry, o.tasksFailed); err == nil {
		o.tasksFailed = existing
	}
	if existing, err := registerHistogramVec(registry, o.taskDuration); err == nil {
		o.taskDuration = existing
	}
}

func registerCounterVec(registry prometheus.Registerer, vec *prometheus.CounterVec) (*prometheus.CounterVec, error) {
	if err := registry.Register(vec); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if reused, ok := already.ExistingCollector.(*prometheus.CounterVec); ok {
				return reused, nil
			}
		}
		return nil, fmt.Errorf("register counter vec: %w", err)
	}
	return vec, nil
}

func registerHistogramVec(registry prometheus.Registerer, vec *prometheus.HistogramVec) (*prometheus.HistogramVec, error) {
	if err := registry.Register(vec); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if reused, ok := already.ExistingCollector.(*prometheus.HistogramVec); ok {
				return reused, nil
			}
		}
		return nil, fmt.Errorf("register histogram vec: %w", err)
	}
	return vec, nil
}

func (o *Observer) Start(ctx context.Context) error {
	if o.eventBus == nil {
		return nil
	}

	eventTypes := []event.EventType{
		event.EventTaskCreated,
		event.EventTaskStarted,
		event.EventTaskCompleted,
		event.EventTaskFailed,
		event.EventTaskRetried,
		event.EventTaskCancelled,
		event.EventWorkerSpawned,
		event.EventWorkerExited,
		event.EventPluginLoaded,
		event.EventPluginUnloaded,
		event.EventPluginExecuted,
		event.EventSkillInvoked,
		event.EventResearchFinding,
		event.EventWorkflowStepCompleted,
		event.EventWorkflowStarted,
		event.EventWorkflowCompleted,
		event.EventWorkflowFailed,
		event.EventSystemHealth,
		event.EventSystemStarted,
		event.EventSystemStopped,
	}

	for _, eventType := range eventTypes {
		sub := o.eventBus.Subscribe(eventType, 100)
		go o.processEvents(ctx, sub)
	}
	return nil
}

func (o *Observer) processEvents(ctx context.Context, sub *event.Subscriber) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub.Chan():
			if !ok {
				return
			}
			o.handleEvent(evt)
		}
	}
}

func (o *Observer) handleEvent(evt event.Event) {
	o.logger.Debug("Received event", zap.String("type", string(evt.Type())), zap.String("id", evt.ID()))

	switch evt.Type() {
	case event.EventTaskCreated:
		if payload, ok := evt.Payload().(event.TaskCreatedPayload); ok {
			o.tasksCreated.WithLabelValues(payload.TaskType).Inc()
			o.IncrementCounter("tasks.created", map[string]string{"type": payload.TaskType})
		} else {
			o.tasksCreated.WithLabelValues(unknownLabel).Inc()
			o.IncrementCounter("tasks.created", nil)
		}
	case event.EventTaskStarted:
		if payload, ok := evt.Payload().(event.TaskStartedPayload); ok && payload.WorkerID != "" {
			o.tasksStarted.WithLabelValues(payload.WorkerID).Inc()
			o.IncrementCounter("tasks.started", map[string]string{"worker": payload.WorkerID})
		} else {
			o.tasksStarted.WithLabelValues(unknownLabel).Inc()
			o.IncrementCounter("tasks.started", nil)
		}
	case event.EventTaskCompleted:
		// The result payload is deliberately NOT a label value: it is unique per
		// task, so labelling by it creates one time series per completed task and
		// the registry grows forever. Duration and count are the useful signals.
		if payload, ok := evt.Payload().(event.TaskCompletedPayload); ok {
			o.tasksCompleted.WithLabelValues(unknownLabel).Inc()
			o.taskDuration.WithLabelValues(unknownLabel).Observe(payload.Duration.Seconds())
			o.ObserveHistogram("task.duration", float64(payload.Duration.Milliseconds()), nil)
		} else {
			o.tasksCompleted.WithLabelValues(unknownLabel).Inc()
		}
		o.IncrementCounter("tasks.completed", nil)
	case event.EventTaskFailed:
		// WorkerID, not the error text: the message is unique per failure.
		if payload, ok := evt.Payload().(event.TaskFailedPayload); ok {
			o.tasksFailed.WithLabelValues(workerLabel(payload.WorkerID)).Inc()
			o.IncrementCounter("tasks.failed", map[string]string{"worker": payload.WorkerID})
		} else {
			o.tasksFailed.WithLabelValues(unknownLabel).Inc()
			o.IncrementCounter("tasks.failed", nil)
		}
	case event.EventTaskRetried:
		o.IncrementCounter("tasks.retried", nil)
	case event.EventTaskCancelled:
		o.IncrementCounter("tasks.cancelled", nil)
	case event.EventWorkerSpawned:
		if payload, ok := evt.Payload().(event.WorkerSpawnedPayload); ok {
			o.IncrementCounter("workers.spawned", map[string]string{"plugin": payload.PluginID})
		} else {
			o.IncrementCounter("workers.spawned", nil)
		}
	case event.EventWorkerExited:
		if payload, ok := evt.Payload().(event.WorkerExitedPayload); ok {
			o.IncrementCounter("workers.exited", map[string]string{"exit_code": fmt.Sprintf("%d", payload.ExitCode)})
		} else {
			o.IncrementCounter("workers.exited", nil)
		}
	case event.EventPluginLoaded:
		if payload, ok := evt.Payload().(event.PluginLoadedPayload); ok {
			o.IncrementCounter("plugins.loaded", map[string]string{"plugin": payload.PluginID})
		} else {
			o.IncrementCounter("plugins.loaded", nil)
		}
	case event.EventPluginUnloaded:
		o.IncrementCounter("plugins.unloaded", nil)
	case event.EventPluginExecuted:
		if payload, ok := evt.Payload().(event.PluginExecutedPayload); ok {
			status := "ok"
			if !payload.Success {
				status = "error"
			}
			o.IncrementCounter("plugins.executed", map[string]string{
				"plugin": payload.PluginID,
				"status": status,
			})
		} else {
			o.IncrementCounter("plugins.executed", nil)
		}
	case event.EventSkillInvoked:
		if payload, ok := evt.Payload().(event.SkillInvokedPayload); ok {
			status := "ok"
			if !payload.Success {
				status = "error"
			}
			o.IncrementCounter("skills.invoked", map[string]string{
				"skill":  payload.SkillName,
				"status": status,
			})
		} else {
			o.IncrementCounter("skills.invoked", nil)
		}
	case event.EventResearchFinding:
		if payload, ok := evt.Payload().(event.ResearchFindingPayload); ok {
			// Type only. TaskID/FindingID are unique per entity and would make
			// one accumulator per finding in the in-memory map.
			o.IncrementCounter("research.findings", map[string]string{"type": payload.Type})
		} else {
			o.IncrementCounter("research.findings", nil)
		}
	case event.EventWorkflowStarted:
		o.IncrementCounter("workflows.started", nil)
	case event.EventWorkflowCompleted:
		o.IncrementCounter("workflows.completed", nil)
	case event.EventWorkflowFailed:
		o.IncrementCounter("workflows.failed", nil)
	case event.EventWorkflowStepCompleted:
		if payload, ok := evt.Payload().(event.WorkflowStepCompletedPayload); ok {
			// Status only: WorkflowID is unique per run and would be unbounded.
			status := "ok"
			if !payload.Success {
				status = "error"
			}
			o.IncrementCounter("workflow.steps.completed", map[string]string{"status": status})
		} else {
			o.IncrementCounter("workflow.steps.completed", nil)
		}
	case event.EventSystemHealth:
		if payload, ok := evt.Payload().(event.SystemHealthPayload); ok {
			o.SetGauge("system.cpu", payload.CPU, nil)
			o.SetGauge("system.memory", payload.Memory, nil)
			o.SetGauge("system.workers", float64(payload.Workers), nil)
			o.SetGauge("system.tasks", float64(payload.Tasks), nil)
		}
	case event.EventSystemStarted:
		o.IncrementCounter("system.started", nil)
	case event.EventSystemStopped:
		o.IncrementCounter("system.stopped", nil)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (o *Observer) IncrementCounter(name string, labels map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	key := metricKey(name, labels)
	acc, exists := o.metrics[key]
	if !exists {
		acc = &metricAccumulator{name: name, mType: MetricCounter}
		o.metrics[key] = acc
	}
	atomic.AddInt64(&acc.count, 1)
	acc.sum += 1
}

func (o *Observer) SetGauge(name string, value float64, labels map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	key := metricKey(name, labels)
	acc, exists := o.metrics[key]
	if !exists {
		acc = &metricAccumulator{name: name, mType: MetricGauge}
		o.metrics[key] = acc
	}
	acc.sum = value
	atomic.StoreInt64(&acc.count, 1)
}

func (o *Observer) ObserveHistogram(name string, value float64, labels map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	key := metricKey(name, labels)
	acc, exists := o.metrics[key]
	if !exists {
		acc = &metricAccumulator{name: name, mType: MetricHistogram, min: value, max: value}
		o.metrics[key] = acc
	}
	acc.sum += value
	atomic.AddInt64(&acc.count, 1)
	if value < acc.min {
		acc.min = value
	}
	if value > acc.max {
		acc.max = value
	}
}

func (o *Observer) GetMetrics() []Metric {
	o.mu.RLock()
	defer o.mu.RUnlock()

	var result []Metric
	for _, acc := range o.metrics {
		m := Metric{
			Name:      acc.name,
			Type:      acc.mType,
			Value:     acc.sum,
			Timestamp: time.Now(),
		}
		result = append(result, m)
	}
	return result
}

func (o *Observer) GetMetricFamilies() []MetricFamily {
	o.mu.RLock()
	defer o.mu.RUnlock()

	families := make(map[string]*MetricFamily)
	for _, acc := range o.metrics {
		family, exists := families[acc.name]
		if !exists {
			family = &MetricFamily{Name: acc.name, Type: acc.mType}
			families[acc.name] = family
		}
		family.Metrics = append(family.Metrics, Metric{
			Name:      acc.name,
			Type:      acc.mType,
			Value:     acc.sum,
			Timestamp: time.Now(),
		})
	}

	result := make([]MetricFamily, 0, len(families))
	for _, f := range families {
		result = append(result, *f)
	}
	return result
}

func (o *Observer) StartTrace(operation string) *TraceSpan {
	span := &TraceSpan{
		TraceID:    generateID(),
		SpanID:     generateID(),
		Operation:  operation,
		StartTime:  time.Now(),
		Status:     "ok",
		Attributes: make(map[string]string),
	}

	o.traceMu.Lock()
	o.traces = append(o.traces, *span)
	// Evict oldest traces to prevent unbounded memory growth.
	if len(o.traces) > maxTraces {
		o.traces = o.traces[len(o.traces)-maxTraces:]
	}
	o.traceMu.Unlock()

	return span
}

func (o *Observer) EndTrace(span *TraceSpan, status string) {
	o.traceMu.Lock()
	defer o.traceMu.Unlock()

	now := time.Now()
	span.EndTime = &now
	span.Status = status
	span.Duration = now.Sub(span.StartTime)

	for i := range o.traces {
		if o.traces[i].SpanID == span.SpanID {
			o.traces[i].EndTime = &now
			o.traces[i].Status = status
			o.traces[i].Duration = span.Duration
			break
		}
	}
}

func (o *Observer) GetTraces() []TraceSpan {
	o.traceMu.RLock()
	defer o.traceMu.RUnlock()

	result := make([]TraceSpan, len(o.traces))
	copy(result, o.traces)
	return result
}

func (o *Observer) Log(level, message string, fields map[string]interface{}) {
	o.logMu.Lock()
	defer o.logMu.Unlock()

	entry := LogEntry{
		Level:     level,
		Message:   message,
		Timestamp: time.Now(),
		Fields:    fields,
	}

	o.logs = append(o.logs, entry)

	// Evict oldest logs to prevent unbounded memory growth.
	if len(o.logs) > maxLogs {
		o.logs = o.logs[len(o.logs)-maxLogs:]
	}

	var zapFields []zap.Field
	for k, v := range fields {
		zapFields = append(zapFields, zap.Any(k, v))
	}
	switch level {
	case "debug":
		o.logger.Debug(message, zapFields...)
	case "info":
		o.logger.Info(message, zapFields...)
	case "warn":
		o.logger.Warn(message, zapFields...)
	case "error":
		o.logger.Error(message, zapFields...)
	default:
		o.logger.Info(message, zapFields...)
	}
}

func (o *Observer) GetLogs() []LogEntry {
	o.logMu.RLock()
	defer o.logMu.RUnlock()

	result := make([]LogEntry, len(o.logs))
	copy(result, o.logs)
	return result
}

func (o *Observer) GetHealth() map[string]interface{} {
	o.mu.RLock()
	metricsCount := len(o.metrics)
	o.mu.RUnlock()

	o.traceMu.RLock()
	tracesCount := len(o.traces)
	o.traceMu.RUnlock()

	o.logMu.RLock()
	logsCount := len(o.logs)
	o.logMu.RUnlock()

	return map[string]interface{}{
		"metrics_count": metricsCount,
		"traces_count":  tracesCount,
		"logs_count":    logsCount,
	}
}

func (o *Observer) Reset() {
	o.mu.Lock()
	o.metrics = make(map[string]*metricAccumulator)
	o.mu.Unlock()

	o.traceMu.Lock()
	o.traces = nil
	o.traceMu.Unlock()

	o.logMu.Lock()
	o.logs = nil
	o.logMu.Unlock()
}

func metricKey(name string, labels map[string]string) string {
	key := name
	for k, v := range labels {
		key += "|" + k + "=" + v
	}
	return key
}

var observerIDCounter int64

func generateID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&observerIDCounter, 1))
}

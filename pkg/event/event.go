package event

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type EventType string

const (
	EventTaskCreated    EventType = "task.created"
	EventTaskStarted    EventType = "task.started"
	EventTaskCompleted  EventType = "task.completed"
	EventTaskFailed     EventType = "task.failed"
	EventTaskRetried    EventType = "task.retried"
	EventTaskCancelled  EventType = "task.cancelled"
	EventPluginLoaded   EventType = "plugin.loaded"
	EventPluginUnloaded EventType = "plugin.unloaded"
	EventWorkerSpawned  EventType = "worker.spawned"
	EventWorkerExited   EventType = "worker.exited"
	EventSystemHealth   EventType = "system.health"
	EventSystemStarted  EventType = "system.started"
	EventSystemStopped  EventType = "system.stopped"
	EventPluginExecuted           EventType = "plugin.executed"
	EventSkillInvoked             EventType = "skill.invoked"
	EventResearchFinding          EventType = "research.finding"
	EventWorkflowStepCompleted    EventType = "workflow.step.completed"
	EventWorkflowStarted          EventType = "workflow.started"
	EventWorkflowCompleted        EventType = "workflow.completed"
	EventWorkflowFailed           EventType = "workflow.failed"
)

type Event interface {
	ID() string
	Type() EventType
	Timestamp() time.Time
	Payload() interface{}
	Metadata() map[string]string
}

type BaseEvent struct {
	id        string
	eventType EventType
	timestamp time.Time
	payload   interface{}
	metadata  map[string]string
}

func NewEvent(eventType EventType, payload interface{}, metadata map[string]string) *BaseEvent {
	if metadata == nil {
		metadata = make(map[string]string)
	}
	return &BaseEvent{
		id:        generateID(),
		eventType: eventType,
		timestamp: time.Now(),
		payload:   payload,
		metadata:  metadata,
	}
}

func (e *BaseEvent) ID() string                  { return e.id }
func (e *BaseEvent) Type() EventType             { return e.eventType }
func (e *BaseEvent) Timestamp() time.Time        { return e.timestamp }
func (e *BaseEvent) Payload() interface{}        { return e.payload }
func (e *BaseEvent) Metadata() map[string]string { return e.metadata }

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type TaskCreatedPayload struct {
	TaskID   string
	TaskType string
	Config   map[string]interface{}
}

type TaskStartedPayload struct {
	TaskID   string
	WorkerID string
}

type TaskCompletedPayload struct {
	TaskID   string
	WorkerID string
	Duration time.Duration
	Result   interface{}
}

type TaskFailedPayload struct {
	TaskID   string
	WorkerID string
	Error    string
	Retry    int
}

type PluginLoadedPayload struct {
	PluginID   string
	PluginType string
	Version    string
}

type WorkerSpawnedPayload struct {
	WorkerID string
	PluginID string
}

type WorkerExitedPayload struct {
	WorkerID string
	ExitCode int
}


type PluginExecutedPayload struct {
	PluginID   string
	TaskID     string
	Duration   time.Duration
	Success    bool
	Error      string
	InputSize  int
	OutputSize int
}

type SkillInvokedPayload struct {
	SkillName  string
	TaskID     string
	Duration   time.Duration
	Success    bool
	Error      string
}

type ResearchFindingPayload struct {
	FindingID  string
	Type       string
	Confidence float64
	TaskID     string
	DataPoints int
}

type WorkflowStepCompletedPayload struct {
	WorkflowID string
	StepID     string
	Duration   time.Duration
	Success    bool
	Error      string
}

type WorkflowStartedPayload struct {
	WorkflowID string
	Status     string
}

type WorkflowCompletedPayload struct {
	WorkflowID string
	StepsTotal int
	Duration   time.Duration
}

type WorkflowFailedPayload struct {
	WorkflowID string
	Error      string
	StepID     string
	StepsTotal int
}

type SystemHealthPayload struct {
	CPU       float64
	Memory    float64
	Workers   int
	Tasks     int
	QueueSize int
}

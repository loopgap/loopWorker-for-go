package api

import (
	"context"

	"loopworker/internal/core/executor"
	"loopworker/internal/core/observer"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	"loopworker/pkg/workflow"
)

// TaskStore is the mutating slice of the scheduler this package needs.
type TaskStore interface {
	CreateTask(ctx context.Context, taskType string, config map[string]any, input []byte) (*scheduler.Task, error)
	CreateTaskWithPriority(ctx context.Context, taskType string, priority scheduler.TaskPriority, config map[string]any, input []byte) (*scheduler.Task, error)
	SaveTask(task *scheduler.Task) error
	QueueTask(ctx context.Context, taskID string) error
	CancelTask(ctx context.Context, taskID string) error
	AddDependency(ctx context.Context, taskID, dependencyID string) error
	GetTask(taskID string) (*scheduler.Task, bool)
	GetStats() map[string]any
}

// TaskLister is the unbounded listing capability.
type TaskLister interface {
	ListTasks(filter scheduler.TaskFilter) []*scheduler.Task
}

// PagedTaskLister is the optional push-down pagination capability. When the
// store implements it, limit/offset reach the database query. Otherwise the API
// requests limit+offset rows and slices them, which still avoids loading the
// whole table.
type PagedTaskLister interface {
	ListTasksPaged(filter scheduler.TaskFilter, offset, limit int) (tasks []*scheduler.Task, total int64, err error)
}

// TaskCounter is the optional filtered-count capability used to fill "total".
type TaskCounter interface {
	CountTasks(filter scheduler.TaskFilter) (int64, error)
}

// TaskOwnershipLister is the optional owner-scoped listing capability. Until the
// scheduler stores a first-class owner column, ownership is enforced in the API
// layer from task metadata (OwnerMetadataKey).
type TaskOwnershipLister interface {
	ListTasksForOwner(owner string, filter scheduler.TaskFilter, offset, limit int) (tasks []*scheduler.Task, total int64, err error)
}

// WorkerCatalog is the executor surface.
type WorkerCatalog interface {
	ListWorkers() []*executor.Worker
}

// EventBusView is the event bus surface used by the SSE endpoint.
type EventBusView interface {
	Subscribe(eventType event.EventType, bufferSize int) *event.Subscriber
	Unsubscribe(sub *event.Subscriber)
}

// WorkflowCatalog is the workflow engine surface.
type WorkflowCatalog interface {
	ListWorkflows() []*workflow.Workflow
	GetWorkflow(id string) (*workflow.Workflow, bool)
	Execute(ctx context.Context, workflowID string) error
}

// ObserverView is the observer surface behind the admin listener.
type ObserverView interface {
	GetMetrics() []observer.Metric
	GetLogs() []observer.LogEntry
}

// Dependencies are the collaborators the API server needs. Only Tasks is
// required: endpoints whose dependency is nil answer 503 with a code naming the
// missing subsystem instead of panicking.
type Dependencies struct {
	Tasks     TaskStore
	Lister    TaskLister
	Workers   WorkerCatalog
	Events    EventBusView
	Workflows WorkflowCatalog
	Observer  ObserverView
}

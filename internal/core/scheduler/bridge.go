package scheduler

import (
	"context"
	"time"

	"loopworker/pkg/workflow"
)

// SchedulerBridge implements workflow.TaskDispatcher using the Scheduler as the backing store.
type SchedulerBridge struct {
	scheduler *Scheduler
}

// NewSchedulerBridge creates a new bridge between WorkflowEngine and Scheduler.
func NewSchedulerBridge(s *Scheduler) *SchedulerBridge {
	return &SchedulerBridge{scheduler: s}
}

// CreateTask creates a task via the scheduler.
func (b *SchedulerBridge) CreateTask(ctx context.Context, taskType string, config map[string]interface{}, input []byte) (*workflow.TaskRef, error) {
	task, err := b.scheduler.CreateTask(ctx, taskType, config, input)
	if err != nil {
		return nil, err
	}

	return &workflow.TaskRef{
		ID:       task.ID,
		Type:     task.Type,
		State:    string(task.State),
		Result:   task.Result,
		Error:    task.Error,
		Metadata: task.Metadata,
	}, nil
}

// WaitForTask polls the scheduler for task state until it reaches a terminal state.
func (b *SchedulerBridge) WaitForTask(ctx context.Context, taskID string) (*workflow.TaskRef, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			task, exists := b.scheduler.GetTask(taskID)
			if !exists {
				return &workflow.TaskRef{ID: taskID, State: "not_found"}, nil
			}

			state := string(task.State)
			switch state {
			case "completed", "failed", "cancelled", "dead_letter":
				return &workflow.TaskRef{
					ID:       task.ID,
					Type:     task.Type,
					State:    state,
					Result:   task.Result,
					Error:    task.Error,
					Metadata: task.Metadata,
				}, nil
			}
		}
	}
}

// GetTask returns the current state of a task by ID.
func (b *SchedulerBridge) GetTask(taskID string) (*workflow.TaskRef, bool) {
	task, exists := b.scheduler.GetTask(taskID)
	if !exists {
		return nil, false
	}

	return &workflow.TaskRef{
		ID:       task.ID,
		Type:     task.Type,
		State:    string(task.State),
		Result:   task.Result,
		Error:    task.Error,
		Metadata: task.Metadata,
	}, true
}

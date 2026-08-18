package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"loopworker/pkg/event"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/utils"
)

type StepStatus int

const (
	StepPending StepStatus = iota
	StepRunning
	StepCompleted
	StepFailed
	StepSkipped
	StepRetrying
)

func (s StepStatus) String() string {
	switch s {
	case StepPending:
		return "pending"
	case StepRunning:
		return "running"
	case StepCompleted:
		return "completed"
	case StepFailed:
		return "failed"
	case StepSkipped:
		return "skipped"
	case StepRetrying:
		return "retrying"
	default:
		return "unknown"
	}
}

type WorkflowStatus int

const (
	WorkflowPending WorkflowStatus = iota
	WorkflowRunning
	WorkflowCompleted
	WorkflowFailed
	WorkflowCancelled
)

type Step struct {
	ID          string
	Name        string
	Action      func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error)
	RetryPolicy *RetryPolicy
	Timeout     time.Duration
	DependsOn   []string
	Condition   func(state map[string]interface{}) bool
	OnFailure   func(ctx context.Context, err error) error
}

type RetryPolicy struct {
	MaxRetries  int
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
}

type Workflow struct {
	ID          string
	Name        string
	Steps       map[string]*Step
	StepOrder   []string
	State       map[string]interface{}
	Status      WorkflowStatus
	StepStatus  map[string]StepStatus
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Error       error
	mu          sync.RWMutex
}

type WorkflowEngine struct {
	workflows  map[string]*Workflow
	eventBus   *event.EventBus
	dispatcher TaskDispatcher
	mu         sync.RWMutex
}

// TaskDispatcher abstracts task execution for workflow steps.
// Implementations (e.g., Scheduler) translate workflow steps into tasks.
type TaskDispatcher interface {
	// CreateTask creates a task representing a workflow step.
	CreateTask(ctx context.Context, taskType string, config map[string]interface{}, input []byte) (*TaskRef, error)
	// WaitForTask blocks until the task reaches a terminal state or ctx is cancelled.
	WaitForTask(ctx context.Context, taskID string) (*TaskRef, error)
	// GetTask returns the current state of a task by ID.
	GetTask(taskID string) (*TaskRef, bool)
}

// TaskRef is a workflow-agnostic reference to a scheduled task.
type TaskRef struct {
	ID       string
	Type     string
	State    string
	Result   []byte
	Error    string
	Metadata map[string]string
}

// SetDispatcher sets the task dispatcher for executing workflow steps as tasks.
func (we *WorkflowEngine) SetDispatcher(d TaskDispatcher) {
	we.mu.Lock()
	defer we.mu.Unlock()
	we.dispatcher = d
}

func (we *WorkflowEngine) GetDispatcher() TaskDispatcher {
	we.mu.RLock()
	defer we.mu.RUnlock()
	return we.dispatcher
}

// NewWorkflowEngine creates a WorkflowEngine with optional functional options.
func NewWorkflowEngine(opts ...WorkflowEngineOption) *WorkflowEngine {
	we := &WorkflowEngine{
		workflows: make(map[string]*Workflow),
	}
	for _, opt := range opts {
		opt(we)
	}
	return we
}

// WorkflowEngineOption is a functional option for WorkflowEngine.
type WorkflowEngineOption func(*WorkflowEngine)

// WithEventBus sets the event bus for publishing workflow step events.
func WithEventBus(bus *event.EventBus) WorkflowEngineOption {
	return func(we *WorkflowEngine) {
		we.eventBus = bus
	}
}

// WithDispatcher sets the task dispatcher for executing workflow steps as tasks.
func WithDispatcher(d TaskDispatcher) WorkflowEngineOption {
	return func(we *WorkflowEngine) {
		we.dispatcher = d
	}
}

func NewWorkflow(id, name string) *Workflow {
	return &Workflow{
		ID:         id,
		Name:       name,
		Steps:      make(map[string]*Step),
		State:      make(map[string]interface{}),
		StepStatus: make(map[string]StepStatus),
		Status:     WorkflowPending,
		CreatedAt:  time.Now(),
	}
}

func (w *Workflow) AddStep(step *Step) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Steps[step.ID] = step
	w.StepOrder = append(w.StepOrder, step.ID)
	w.StepStatus[step.ID] = StepPending
}

func (w *Workflow) SetState(key string, value interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.State[key] = value
}

func (w *Workflow) GetState(key string) (interface{}, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	val, ok := w.State[key]
	return val, ok
}

func (we *WorkflowEngine) Register(workflow *Workflow) {
	we.mu.Lock()
	defer we.mu.Unlock()
	we.workflows[workflow.ID] = workflow
}

func (we *WorkflowEngine) Execute(ctx context.Context, workflowID string) error {
	we.mu.RLock()
	workflow, exists := we.workflows[workflowID]
	we.mu.RUnlock()

	if !exists {
		return fmt.Errorf("%w: %s", lwerrors.ErrWorkflowNotFound, workflowID)
	}

	return we.executeWorkflow(ctx, workflow)
}

func (we *WorkflowEngine) executeWorkflow(ctx context.Context, workflow *Workflow) error {
	workflow.mu.Lock()
	workflow.Status = WorkflowRunning
	now := time.Now()
	workflow.StartedAt = &now
	workflow.mu.Unlock()

	// Publish workflow started event
	if we.eventBus != nil {
		we.publishWorkflowEvent(ctx, workflow.ID, event.EventWorkflowStarted, event.WorkflowStartedPayload{
			WorkflowID: workflow.ID,
			Status:     "running",
		})
	}

	// Compute topological order based on step dependencies
	execOrder, err := we.computeExecutionOrder(workflow)
	if err != nil {
		workflow.mu.Lock()
		workflow.Status = WorkflowFailed
		workflow.Error = err
		completedTime := time.Now()
		workflow.CompletedAt = &completedTime
		workflow.mu.Unlock()

		// Publish workflow failed event
		if we.eventBus != nil {
			we.publishWorkflowEvent(ctx, workflow.ID, event.EventWorkflowFailed, event.WorkflowFailedPayload{
				WorkflowID: workflow.ID,
				Error:      err.Error(),
			})
		}

		return err
	}

	for _, stepID := range execOrder {
		select {
		case <-ctx.Done():
			workflow.mu.Lock()
			workflow.Status = WorkflowCancelled
			workflow.Error = ctx.Err()
			completedTime := time.Now()
			workflow.CompletedAt = &completedTime
			workflow.mu.Unlock()
			return ctx.Err()
		default:
		}

		step := workflow.Steps[stepID]

		// Verify all dependencies completed successfully
		if !we.checkDependencies(workflow, step) {
			workflow.mu.Lock()
			workflow.StepStatus[stepID] = StepSkipped
			workflow.mu.Unlock()
			continue
		}

		if step.Condition != nil && !step.Condition(workflow.State) {
			workflow.mu.Lock()
			workflow.StepStatus[stepID] = StepSkipped
			workflow.mu.Unlock()
			continue
		}

		if err := we.executeStep(ctx, workflow, step); err != nil {
			workflow.mu.Lock()
			workflow.Status = WorkflowFailed
			workflow.Error = err
			now := time.Now()
			workflow.CompletedAt = &now
			workflow.mu.Unlock()

			// Publish workflow failed event
			if we.eventBus != nil {
				we.publishWorkflowEvent(ctx, workflow.ID, event.EventWorkflowFailed, event.WorkflowFailedPayload{
					WorkflowID: workflow.ID,
					Error:      err.Error(),
					StepID:     stepID,
					StepsTotal: len(workflow.StepOrder),
				})
			}

			return err
		}
	}

	workflow.mu.Lock()
	workflow.Status = WorkflowCompleted
	completedTime := time.Now()
	workflow.CompletedAt = &completedTime
	workflow.mu.Unlock()

	// Publish workflow completed event
	if we.eventBus != nil {
		we.publishWorkflowEvent(ctx, workflow.ID, event.EventWorkflowCompleted, event.WorkflowCompletedPayload{
			WorkflowID: workflow.ID,
			StepsTotal: len(workflow.StepOrder),
			Duration:   0,
		})
	}

	return nil
}

// computeExecutionOrder returns a topologically sorted order of step IDs
// based on their DependsOn relationships using Kahn's algorithm.
func (we *WorkflowEngine) computeExecutionOrder(workflow *Workflow) ([]string, error) {
	workflow.mu.RLock()
	defer workflow.mu.RUnlock()

	// Build in-degree map
	inDegree := make(map[string]int)
	for _, stepID := range workflow.StepOrder {
		inDegree[stepID] = 0
	}
	for _, stepID := range workflow.StepOrder {
		step := workflow.Steps[stepID]
		for _, dep := range step.DependsOn {
			inDegree[stepID]++
			_ = dep // dep is the dependency step ID
		}
	}

	// Queue steps with no dependencies
	var queue []string
	for _, stepID := range workflow.StepOrder {
		if inDegree[stepID] == 0 {
			queue = append(queue, stepID)
		}
	}

	var sorted []string
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		sorted = append(sorted, current)

		// Find steps that depend on current
		for _, stepID := range workflow.StepOrder {
			step := workflow.Steps[stepID]
			for _, dep := range step.DependsOn {
				if dep == current {
					inDegree[stepID]--
					if inDegree[stepID] == 0 {
						queue = append(queue, stepID)
					}
				}
			}
		}
	}

	if len(sorted) != len(workflow.StepOrder) {
		return nil, lwerrors.ErrWorkflowCycle
	}

	return sorted, nil
}

func (we *WorkflowEngine) checkDependencies(workflow *Workflow, step *Step) bool {
	for _, depID := range step.DependsOn {
		status, exists := workflow.StepStatus[depID]
		if !exists || status != StepCompleted {
			return false
		}
	}
	return true
}

func (we *WorkflowEngine) executeStep(ctx context.Context, workflow *Workflow, step *Step) error {
	workflow.mu.Lock()
	workflow.StepStatus[step.ID] = StepRunning
	workflow.mu.Unlock()

	var stepCtx context.Context
	var cancel context.CancelFunc

	if step.Timeout > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, step.Timeout)
	} else {
		stepCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	maxRetries := 1
	if step.RetryPolicy != nil {
		maxRetries = step.RetryPolicy.MaxRetries + 1
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			workflow.mu.Lock()
			workflow.StepStatus[step.ID] = StepRetrying
			workflow.mu.Unlock()

			wait := we.calculateRetryWait(step.RetryPolicy, attempt)
			select {
			case <-stepCtx.Done():
				return stepCtx.Err()
			case <-time.After(wait):
			}
		}

		result, err := step.Action(stepCtx, workflow.State)
		if err == nil {
			workflow.mu.Lock()
			for k, v := range result {
				workflow.State[k] = v
			}
			workflow.StepStatus[step.ID] = StepCompleted
			workflow.mu.Unlock()

			// Publish step completed event
			if we.eventBus != nil {
				stepEvent := event.NewEvent(event.EventWorkflowStepCompleted, event.WorkflowStepCompletedPayload{
					WorkflowID: workflow.ID,
					StepID:     step.ID,
					Duration:   0, // Duration tracking could be added with start time
					Success:    true,
				}, nil)
				_ = we.eventBus.Publish(ctx, stepEvent)
			}

			return nil
		}

		lastErr = err

		if step.OnFailure != nil {
			if failErr := step.OnFailure(stepCtx, err); failErr != nil {
				return failErr
			}
		}
	}

	workflow.mu.Lock()
	workflow.StepStatus[step.ID] = StepFailed
	workflow.mu.Unlock()

	// Publish step failed event
	if we.eventBus != nil {
		errMsg := ""
		if lastErr != nil {
			errMsg = lastErr.Error()
		}
		stepEvent := event.NewEvent(event.EventWorkflowStepCompleted, event.WorkflowStepCompletedPayload{
			WorkflowID: workflow.ID,
			StepID:     step.ID,
			Success:    false,
			Error:      errMsg,
		}, nil)
		_ = we.eventBus.Publish(ctx, stepEvent)
	}

	return fmt.Errorf("step %s failed after %d attempts: %w", step.ID, maxRetries, lastErr)
}

func (we *WorkflowEngine) calculateRetryWait(policy *RetryPolicy, attempt int) time.Duration {
	if policy == nil {
		return time.Second
	}

	wait := policy.InitialWait
	for i := 1; i < attempt; i++ {
		wait = time.Duration(float64(wait) * policy.Multiplier)
		if wait > policy.MaxWait {
			wait = policy.MaxWait
			break
		}
	}
	return wait
}

func (we *WorkflowEngine) GetWorkflow(id string) (*Workflow, bool) {
	we.mu.RLock()
	defer we.mu.RUnlock()
	w, exists := we.workflows[id]
	return w, exists
}

func (we *WorkflowEngine) ListWorkflows() []*Workflow {
	we.mu.RLock()
	defer we.mu.RUnlock()

	result := make([]*Workflow, 0, len(we.workflows))
	for _, w := range we.workflows {
		result = append(result, w)
	}
	return result
}

func (w *Workflow) GetStatus() WorkflowStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.Status
}

func (w *Workflow) GetStepStatus(stepID string) StepStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.StepStatus[stepID]
}

func (w *Workflow) IsComplete() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.Status == WorkflowCompleted || w.Status == WorkflowFailed || w.Status == WorkflowCancelled
}

func (w *Workflow) GetError() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.Error
}

type DAGWorkflow struct {
	*Workflow
	adjacency map[string][]string
}

func NewDAGWorkflow(id, name string) *DAGWorkflow {
	return &DAGWorkflow{
		Workflow:  NewWorkflow(id, name),
		adjacency: make(map[string][]string),
	}
}

func (d *DAGWorkflow) AddEdge(from, to string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.adjacency[from] = append(d.adjacency[from], to)
}

func (d *DAGWorkflow) TopologicalSort() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	inDegree := make(map[string]int)
	for _, step := range d.StepOrder {
		inDegree[step] = 0
	}

	for _, deps := range d.adjacency {
		for _, dep := range deps {
			inDegree[dep]++
		}
	}

	var queue []string
	for step, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, step)
		}
	}

	var sorted []string
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		sorted = append(sorted, current)

		for _, neighbor := range d.adjacency[current] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if len(sorted) != len(d.StepOrder) {
		return nil, lwerrors.ErrWorkflowCycle
	}

	return sorted, nil
}

type ParallelWorkflow struct {
	*Workflow
	maxConcurrency int
}

func NewParallelWorkflow(id, name string, maxConcurrency int) *ParallelWorkflow {
	if maxConcurrency <= 0 {
		maxConcurrency = 4
	}
	return &ParallelWorkflow{
		Workflow:       NewWorkflow(id, name),
		maxConcurrency: maxConcurrency,
	}
}

func (pw *ParallelWorkflow) ExecuteParallel(ctx context.Context) error {
	pw.mu.Lock()
	pw.Status = WorkflowRunning
	now := time.Now()
	pw.StartedAt = &now
	pw.mu.Unlock()

	sem := make(chan struct{}, pw.maxConcurrency)
	errChan := make(chan error, 1)
	doneChan := make(chan string, len(pw.StepOrder))

	// Track completed steps
	completed := make(map[string]bool)
	var completedMu sync.Mutex
	totalSteps := len(pw.StepOrder)

	// Launch all steps that have their dependencies met
	launchReady := func() {
		completedMu.Lock()
		defer completedMu.Unlock()

		for _, stepID := range pw.StepOrder {
			step := pw.Steps[stepID]
			status := pw.StepStatus[stepID]

			// Skip if already started/completed/failed/skipped
			if status != StepPending {
				continue
			}

			// Check if all dependencies are completed
			allDepsDone := true
			for _, depID := range step.DependsOn {
				if !completed[depID] {
					allDepsDone = false
					break
				}
			}

			if !allDepsDone {
				continue
			}

			// Mark as running to prevent double-launch
			pw.StepStatus[stepID] = StepRunning

			// Launch step in goroutine
			stepCopy := step
			utils.GoSafe(ctx, func(innerCtx context.Context) {
				sem <- struct{}{} // acquire concurrency slot
				defer func() { <-sem }()

				result, err := stepCopy.Action(innerCtx, pw.State)
				if err != nil {
					completedMu.Lock()
					pw.StepStatus[stepCopy.ID] = StepFailed
					completedMu.Unlock()
					select {
					case errChan <- fmt.Errorf("step %s: %w", stepCopy.ID, err):
					default:
					}
					return
				}

				completedMu.Lock()
				for k, v := range result {
					pw.State[k] = v
				}
				pw.StepStatus[stepCopy.ID] = StepCompleted
				completed[stepCopy.ID] = true
				completedMu.Unlock()

				doneChan <- stepCopy.ID
			})
		}
	}

	// Initial launch of ready steps
	launchReady()

	finished := 0
	for finished < totalSteps {
		select {
		case <-ctx.Done():
			pw.mu.Lock()
			pw.Status = WorkflowCancelled
			pw.Error = ctx.Err()
			pw.mu.Unlock()
			return ctx.Err()
		case err := <-errChan:
			pw.mu.Lock()
			pw.Status = WorkflowFailed
			pw.Error = err
			now := time.Now()
			pw.CompletedAt = &now
			pw.mu.Unlock()
			return err
		case stepID := <-doneChan:
			finished++
			// Mark remaining dependencies as done for skipped steps
			completedMu.Lock()
			completed[stepID] = true
			completedMu.Unlock()
			// Try to launch newly-ready steps
			launchReady()
		}
	}

	pw.mu.Lock()
	pw.Status = WorkflowCompleted
	completedTime := time.Now()
	pw.CompletedAt = &completedTime
	pw.mu.Unlock()

	return nil
}

func (pw *ParallelWorkflow) checkDependencies(step *Step) bool {
	for _, depID := range step.DependsOn {
		status, exists := pw.StepStatus[depID]
		if !exists || status != StepCompleted {
			return false
		}
	}
	return true
}

// publishWorkflowEvent publishes a workflow lifecycle event if eventBus is set.
func (we *WorkflowEngine) publishWorkflowEvent(ctx context.Context, workflowID string, eventType event.EventType, payload interface{}) {
	if we.eventBus != nil {
		e := event.NewEvent(eventType, payload, nil)
		_ = we.eventBus.Publish(ctx, e)
	}
}

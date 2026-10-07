// Package workflow implements a workflow engine supporting sequential, DAG, and parallel patterns.
//
// Architecture Pattern: Workflow Engine with DAG Execution
// =========================================================
// The WorkflowEngine orchestrates multi-step workflows where steps can have dependencies:
//
//	┌─────────┐     ┌─────────────┐     ┌───────────┐
//	│  extract │────►│  transform  │────►│   load    │
//	└─────────┘     └─────────────┘     └───────────┘
//	     │                                      ▲
//	     └──────────── validate ────────────────┘
//
// Three workflow types are supported:
//  1. Workflow: Sequential execution with topological ordering (Kahn's algorithm)
//  2. DAGWorkflow: Explicit edge-based dependency graph
//  3. ParallelWorkflow: Concurrent execution with semaphore-based concurrency control
//
// Teaching Note: Topological Sort (Kahn's Algorithm)
// ===================================================
// The computeExecutionOrder uses Kahn's algorithm:
//  1. Calculate in-degree for each node
//  2. Queue nodes with in-degree 0
//  3. Process queue: remove node, decrease neighbors' in-degree
//  4. If sorted count ≠ node count → cycle detected
//
// This is O(V + E) where V = steps, E = dependencies.
//
// Teaching Note: Retry with Exponential Backoff
// ==============================================
// Steps can specify a RetryPolicy with exponential backoff:
//
//	wait = initialWait * multiplier^attempt
//
// This prevents overwhelming a failing downstream service with rapid retries.
package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/event"
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
	// execMu is held for the whole of one execution. A registered Workflow is a
	// single shared instance and executeWorkflow writes State and StepStatus
	// from inside the step actions, so two overlapping runs would be two
	// goroutines writing one map - "concurrent map writes", which kills the
	// process rather than returning an error. It is a lock, not a latch: the
	// next run acquires it once the previous one reaches a terminal state.
	execMu sync.Mutex
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

// TaskQueuer is the second half of creating a runnable task.
//
// A task that has been created but never queued is picked up by nobody: it sits
// in "pending" forever and WaitForTask blocks until the caller's context dies.
// The scheduler's own REST path pairs CreateTask with QueueTask
// (pkg/api/handlers_task_mutate.go), so a workflow step must pair them too.
//
// This is a separate interface rather than a method on TaskDispatcher because a
// dispatcher that cannot queue is still useful for lookups, and because the
// scheduler bridge has to grow the method before this one can be folded in.
type TaskQueuer interface {
	// QueueTask moves a created task into the run queue and wakes the workers.
	QueueTask(ctx context.Context, taskID string) error
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

// StepIDs returns a snapshot of the registered step ids in registration order.
// Callers that iterate steps while a run may still be adding them must read
// through this, not the StepOrder field: AddStep appends to that slice under mu
// and a bare read of it is a data race.
func (w *Workflow) StepIDs() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, len(w.StepOrder))
	copy(out, w.StepOrder)
	return out
}

// Step returns the registered step, or nil when no step has that id. Same
// reasoning as StepIDs: the Steps map is written by AddStep under mu.
func (w *Workflow) Step(id string) *Step {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.Steps[id]
}

// SetStepStatus records a step's status under the same lock AddStep uses. Every
// writer of StepStatus outside this method is a potential race with AddStep.
func (w *Workflow) SetStepStatus(stepID string, status StepStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.StepStatus == nil {
		w.StepStatus = make(map[string]StepStatus)
	}
	w.StepStatus[stepID] = status
}

// StateSnapshot returns a shallow copy of the shared state map.
//
// A step's Action receives this copy rather than the live map. The map was
// previously handed over by reference while two different mutexes guarded it,
// which could kill the process with "concurrent map writes". The copy keeps the
// documented contract intact: a step reads the state as it was when it started
// and returns an incremental result, which MergeState folds in. It is shallow,
// so nested values are still shared - exactly as SetState's merges were.
func (w *Workflow) StateSnapshot() map[string]interface{} {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make(map[string]interface{}, len(w.State))
	for k, v := range w.State {
		out[k] = v
	}
	return out
}

// MergeState folds a step's result into the shared state under the lock
// SetState uses, so one mutex governs one map.
func (w *Workflow) MergeState(result map[string]interface{}) {
	if len(result) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, v := range result {
		w.State[k] = v
	}
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
	// Refuse an overlapping run rather than corrupt the one in flight. This
	// guard only became reachable when the server started registering
	// workflows: before that nothing in production called Execute at all.
	if !workflow.execMu.TryLock() {
		return fmt.Errorf("workflow %s is already running\n"+
			"  cause: a registered workflow holds one execution at a time, and run is in progress\n"+
			"  fix:   poll GET /api/v1/workflow/%s until its status is completed or failed, then submit again", workflow.ID, workflow.ID)
	}
	defer workflow.execMu.Unlock()

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

		// AddStep is exported and takes no execMu, so a caller can add a step
		// while a run is in flight; read Steps under the lock that AddStep writes it.
		workflow.mu.RLock()
		step := workflow.Steps[stepID]
		workflow.mu.RUnlock()

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
		var wfDuration time.Duration
		if workflow.StartedAt != nil {
			wfDuration = completedTime.Sub(*workflow.StartedAt)
		}
		we.publishWorkflowEvent(ctx, workflow.ID, event.EventWorkflowCompleted, event.WorkflowCompletedPayload{
			WorkflowID: workflow.ID,
			StepsTotal: len(workflow.StepOrder),
			Duration:   wfDuration,
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

// checkDependencies reports whether every step this one depends on completed.
//
// StepStatus is read through GetStepStatus, not directly: AddStep writes the same
// map under w.mu and takes no execMu, so a step registered while this run is in
// flight makes a bare map read a data race. A dependency that is not in the map
// reads back as the zero value StepPending, which is not StepCompleted, so the
// "missing" and "not completed" cases collapse into one comparison.
func (we *WorkflowEngine) checkDependencies(workflow *Workflow, step *Step) bool {
	for _, depID := range step.DependsOn {
		if workflow.GetStepStatus(depID) != StepCompleted {
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

// GetStartedAt and GetCompletedAt read the run's timestamps under the workflow
// mutex, which is where executeWorkflow writes them. Both return nil before the
// first execution. The pointees are never mutated - a new time.Time is published
// by pointer on every write - so handing the pointer out is safe.
func (w *Workflow) GetStartedAt() *time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.StartedAt
}

func (w *Workflow) GetCompletedAt() *time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.CompletedAt
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

// ExecuteParallel runs every step whose dependencies are met, bounded by
// maxConcurrency.
//
// Locking rule for this function: completedMu guards the local completed map,
// and mu (via the Workflow accessors) guards the workflow's own maps. The two
// are never held at the same time. This function is called only from the
// coordinating goroutine below - once before the wait loop and once per
// completed step - so deciding what to launch and then marking those steps
// running can safely happen in two phases.
func (pw *ParallelWorkflow) ExecuteParallel(ctx context.Context) error {
	pw.mu.Lock()
	pw.Status = WorkflowRunning
	now := time.Now()
	pw.StartedAt = &now
	pw.mu.Unlock()

	// Snapshot the plan once. A step registered while this run is in flight is
	// not part of this run: totalSteps is fixed from here, so the wait loop has
	// a bound it cannot drift away from.
	order := pw.StepIDs()
	totalSteps := len(order)

	sem := make(chan struct{}, pw.maxConcurrency)
	errChan := make(chan error, 1)
	doneChan := make(chan string, len(order))

	completed := make(map[string]bool)
	launched := make(map[string]bool)
	var completedMu sync.Mutex

	launchReady := func() {
		// Phase 1 - decide, holding only completedMu. Nothing here touches the
		// workflow's own maps except through the accessors, which take mu
		// briefly and never while completedMu is held by another goroutine.
		var toLaunch []*Step
		completedMu.Lock()
		for _, stepID := range order {
			if launched[stepID] {
				continue
			}
			step := pw.Step(stepID)
			if step == nil {
				continue
			}
			if pw.GetStepStatus(stepID) != StepPending {
				continue
			}
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
			launched[stepID] = true
			toLaunch = append(toLaunch, step)
		}
		completedMu.Unlock()

		// Phase 2 - start them. completedMu is released, so SetStepStatus is free
		// to take mu without any lock-ordering hazard.
		for _, step := range toLaunch {
			pw.SetStepStatus(step.ID, StepRunning)
			stepCopy := step
			utils.GoSafe(ctx, func(innerCtx context.Context) {
				sem <- struct{}{} // acquire concurrency slot
				defer func() { <-sem }()

				// A snapshot, not the live map: this read happens while other
				// steps are running and SetState may be writing.
				result, err := stepCopy.Action(innerCtx, pw.StateSnapshot())
				if err != nil {
					pw.SetStepStatus(stepCopy.ID, StepFailed)
					select {
					case errChan <- fmt.Errorf("step %s: %w", stepCopy.ID, err):
					default:
					}
					return
				}

				pw.MergeState(result)
				pw.SetStepStatus(stepCopy.ID, StepCompleted)

				completedMu.Lock()
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

// publishWorkflowEvent publishes a workflow lifecycle event if eventBus is set.
func (we *WorkflowEngine) publishWorkflowEvent(ctx context.Context, workflowID string, eventType event.EventType, payload interface{}) {
	if we.eventBus != nil {
		e := event.NewEvent(eventType, payload, nil)
		_ = we.eventBus.Publish(ctx, e)
	}
}

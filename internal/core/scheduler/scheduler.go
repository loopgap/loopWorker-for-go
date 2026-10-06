// Package scheduler implements priority-based task scheduling with persistent storage.
//
// Architecture Pattern: Priority Queue with Persistent State
// ===========================================================
// The Scheduler uses a max-heap (container/heap) for O(log n) insertion and O(log n)
// extraction of the highest-priority task. Every state transition is persisted before it
// is announced, so a crash loses at most the transition that was in flight.
//
// Task Lifecycle:
//
//	PENDING ──(QueueTask)──► QUEUED ──(StartTask)──► RUNNING ──(CompleteTask)──► COMPLETED
//	                                      │                                          │
//	                                      ├──(FailTask, retries left)──► QUEUED ◄────┘
//	                                      │
//	                                      └──(FailTask, retries exhausted)──► DEAD_LETTER
//
// Storage contract
// ================
// NewSchedulerWithStorage is the production entry point: the caller injects the database
// path (config data_dir is honoured, the package never guesses), takes an exclusive
// heartbeat lock for that directory, applies SQLite pragmas, runs the versioned migration
// set and recovers QUEUED/RUNNING tasks. A storage failure is returned as an error so the
// process refuses to start; degrading to memory-only is an explicit opt-in
// (StorageConfig.AllowEphemeral) that is logged as an error, never a warning.
//
// NewScheduler keeps the old signature and means what it now says: an ephemeral, in-memory
// scheduler for tests and throw-away runs. Nothing is written to disk by it.
//
// Teaching Note: container/heap Interface
// ========================================
// Go's container/heap requires implementing 5 methods: Len, Less, Swap, Push, Pop.
// We use a max-heap (higher Priority value = higher priority) by reversing the Less
// comparison: data[i].Priority > data[j].Priority.
//
// Teaching Note: Dependency Resolution
// =====================================
// When a task completes, unblockDependents() loads pending tasks to find those depending
// on the completed task. When all dependencies are satisfied (DependsOn map is empty),
// the task is promoted to QUEUED and inserted into the priority queue.
package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
)

// ---- heap-based priority queue ----

// taskQueue implements heap.Interface as a max-heap by priority.
type taskQueue struct {
	data []*Task
}

func (tq taskQueue) Len() int { return len(tq.data) }

// Max-heap: higher Priority value comes first.
func (tq taskQueue) Less(i, j int) bool {
	return tq.data[i].Priority > tq.data[j].Priority
}

func (tq taskQueue) Swap(i, j int) {
	tq.data[i], tq.data[j] = tq.data[j], tq.data[i]
}

func (tq *taskQueue) Push(x any) {
	tq.data = append(tq.data, x.(*Task))
}

func (tq *taskQueue) Pop() any {
	old := tq.data
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid memory leak
	tq.data = old[:n-1]
	return item
}

type TaskState string

const (
	StatePending    TaskState = "pending"
	StateQueued     TaskState = "queued"
	StateRunning    TaskState = "running"
	StateCompleted  TaskState = "completed"
	StateFailed     TaskState = "failed"
	StateCancelled  TaskState = "cancelled"
	StateRetrying   TaskState = "retrying"
	StateDeadLetter TaskState = "dead_letter"
)

type TaskPriority int

const (
	PriorityLow      TaskPriority = 0
	PriorityNormal   TaskPriority = 1
	PriorityHigh     TaskPriority = 2
	PriorityCritical TaskPriority = 3
)

func (p TaskPriority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	case PriorityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

type AgentConfig struct {
	SystemPrompt   string `json:"system_prompt"`
	Model          string `json:"model"`
	ResponseSchema string `json:"schema"`
}

type Task struct {
	ID           string
	Type         string
	State        TaskState
	Priority     TaskPriority
	Config       map[string]interface{}
	Input        []byte
	Result       []byte
	Error        string
	Retry        int
	MaxRetry     int
	CreatedAt    time.Time
	StartedAt    *time.Time
	EndedAt      *time.Time
	Metadata     map[string]string
	Dependencies []string
	DependsOn    map[string]bool

	// AI Agent
	IsAgent     bool
	AgentConfig *AgentConfig
}

type TaskFilter struct {
	States   []TaskState
	Types    []string
	Priority *TaskPriority
	Limit    int
}

type Scheduler struct {
	eventBus     *event.EventBus
	queue        *taskQueue
	mu           sync.RWMutex
	taskNotifyCh chan struct{}
	store        taskStore
	logger       *zap.Logger
	cfg          StorageConfig

	lock         *instanceLock
	ephemeral    bool
	degradedFrom string

	closed          atomic.Bool
	retentionCancel context.CancelFunc
	retentionDone   chan struct{}
	closeOnce       sync.Once
	closeErr        error
}

// NewScheduler returns an EPHEMERAL scheduler: tasks live only in this process and are
// gone when it exits. Production callers must use NewSchedulerWithStorage.
func NewScheduler(eventBus *event.EventBus) *Scheduler {
	logger.Warn("scheduler using EPHEMERAL in-memory storage: queued tasks will NOT survive a restart. " +
		"Production must call NewSchedulerWithStorage with the configured data directory")
	s := newScheduler(eventBus, newMemStore(), nil, StorageConfig{})
	s.ephemeral = true
	return s
}

// NewSchedulerWithStorage opens (and migrates) the task database at cfg.Path, refuses to
// share it with a second instance, and recovers queued tasks. A storage failure is
// returned as an error: callers must exit rather than run without persistence.
func NewSchedulerWithStorage(eventBus *event.EventBus, cfg StorageConfig) (*Scheduler, error) {
	cfg = cfg.withDefaults()
	if cfg.Path == "" {
		return nil, fmt.Errorf("scheduler: storage path must not be empty: pass the task database from config, "+
			"for example scheduler.StorageConfigForDataDir(dataDir) (%w)", ErrStorageUnavailable)
	}

	store, lock, err := openPersistentStorage(context.Background(), cfg)
	if err != nil {
		if !cfg.AllowEphemeral {
			return nil, fmt.Errorf("%w\n  LoopWorker refuses to start without persistence, because every task queued in "+
				"memory is lost when this process exits.\n  Fix the path above, or accept the loss explicitly with "+
				"allow_ephemeral_tasks=true (development only)", err)
		}
		logger.Error("TASK DATABASE UNAVAILABLE AND allow_ephemeral_tasks IS SET: running in memory only, "+
			"tasks WILL be lost on restart",
			zap.String("path", filepath.Clean(cfg.Path)), zap.Error(err))
		s := newScheduler(eventBus, newMemStore(), nil, cfg)
		s.ephemeral = true
		s.degradedFrom = err.Error()
		return s, nil
	}

	s := newScheduler(eventBus, store, lock, cfg)
	if err := s.recoverTasks(); err != nil {
		_ = s.Close()
		if !cfg.AllowEphemeral {
			return nil, fmt.Errorf("%w\n  LoopWorker refuses to start: its own task database could not be read.\n"+
				"  Fix the path above, or accept the loss explicitly with allow_ephemeral_tasks=true (development only)", err)
		}
		logger.Error("TASK DATABASE UNREADABLE AND allow_ephemeral_tasks IS SET: starting with an empty in-memory queue",
			zap.String("path", filepath.Clean(cfg.Path)), zap.Error(err))
		degraded := newScheduler(eventBus, newMemStore(), nil, cfg)
		degraded.ephemeral = true
		degraded.degradedFrom = err.Error()
		return degraded, nil
	}
	s.startRetention()
	return s, nil
}

// openPersistentStorage prepares the directory, claims the instance lock and opens the
// database. Any failure releases what it already claimed.
func openPersistentStorage(ctx context.Context, cfg StorageConfig) (taskStore, *instanceLock, error) {
	dbPath := filepath.Clean(cfg.Path)
	dir := filepath.Dir(dbPath)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("scheduler: create data directory %s: %w "+
			"(is it writable? point data_dir at a writable directory)", dir, ErrStorageUnavailable)
	}

	lock, err := acquireInstanceLock(dir, dbPath)
	if err != nil {
		return nil, nil, err
	}

	store, err := openSQLite(ctx, cfg)
	if err != nil {
		return nil, nil, errors.Join(err, lock.release())
	}
	return store, lock, nil
}

func newScheduler(eventBus *event.EventBus, store taskStore, lock *instanceLock, cfg StorageConfig) *Scheduler {
	q := &taskQueue{data: make([]*Task, 0)}
	heap.Init(q)
	return &Scheduler{
		eventBus:     eventBus,
		queue:        q,
		taskNotifyCh: make(chan struct{}, 1),
		logger:       logger.Named("scheduler"),
		store:        store,
		lock:         lock,
		cfg:          cfg,
	}
}

// recoverTasks reloads QUEUED/RUNNING tasks after a restart. Anything RUNNING when we
// crashed is re-queued, because its worker died with the previous process.
func (s *Scheduler) recoverTasks() error {
	tasks, err := s.store.list(TaskFilter{States: []TaskState{StateQueued, StateRunning}})
	if err != nil {
		return fmt.Errorf("scheduler: recover queued tasks from %s: %w (your task list is unreadable; "+
			"copy the database file aside and restore it from a backup if the error persists)", s.store.info().Path, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range tasks {
		if t.State == StateRunning {
			t.State = StateQueued
			if err := s.store.save(t); err != nil {
				return fmt.Errorf("scheduler: re-queue recovered task %s: %w", t.ID, err)
			}
		}
		heap.Push(s.queue, t)
	}
	if s.queue.Len() > 0 {
		s.notifyWorkers()
	}
	if len(tasks) > 0 {
		s.logger.Info("recovered tasks from storage", zap.Int("count", len(tasks)))
	}
	return nil
}

func (s *Scheduler) checkOpen() error {
	if s.closed.Load() {
		return fmt.Errorf("scheduler: %w", ErrSchedulerClosed)
	}
	return nil
}

// saveTask persists one task and returns any storage error.
func (s *Scheduler) saveTask(task *Task) error {
	if task == nil {
		return fmt.Errorf("scheduler: save: %w", lwerrors.ErrTaskInvalid)
	}
	if err := s.store.save(task); err != nil {
		return err
	}
	return nil
}

// SaveTask persists a task snapshot. Callers must check the error: silently dropping a
// write is how "my task disappeared" tickets start.
func (s *Scheduler) SaveTask(task *Task) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveTask(task)
}

func (s *Scheduler) NotifyCh() <-chan struct{} {
	return s.taskNotifyCh
}

func (s *Scheduler) CreateTask(ctx context.Context, taskType string, config map[string]interface{}, input []byte) (*Task, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}

	task := &Task{
		ID:        generateTaskID(),
		Type:      taskType,
		State:     StatePending,
		Priority:  PriorityNormal,
		Config:    config,
		Input:     input,
		MaxRetry:  3,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]string),
		DependsOn: make(map[string]bool),
	}

	s.mu.Lock()
	err := s.saveTask(task)
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("scheduler: create task %s (type %s): %w", task.ID, taskType, err)
	}

	s.publish(ctx, event.EventTaskCreated, event.TaskCreatedPayload{
		TaskID:   task.ID,
		TaskType: taskType,
		Config:   config,
	}, nil, "create task "+task.ID)
	return task, nil
}

func (s *Scheduler) CreateTaskWithPriority(ctx context.Context, taskType string, priority TaskPriority, config map[string]interface{}, input []byte) (*Task, error) {
	task, err := s.CreateTask(ctx, taskType, config, input)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	task.Priority = priority
	err = s.saveTask(task)
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("scheduler: set priority of task %s: %w", task.ID, err)
	}
	return task, nil
}

func (s *Scheduler) AddDependency(ctx context.Context, taskID, dependencyID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	// A self-edge is always a caller bug, never a user request: it can never
	// become ready. Untrusted input is rejected earlier, in pkg/api.
	if taskID == dependencyID {
		return fmt.Errorf("%w: task %s depends on itself", lwerrors.ErrWorkflowCycle, taskID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.getTaskLocked(taskID)
	if err != nil {
		return fmt.Errorf("scheduler: add dependency %s -> %s: %w", taskID, dependencyID, err)
	}
	dep, err := s.getTaskLocked(dependencyID)
	if err != nil {
		return fmt.Errorf("scheduler: add dependency %s -> %s: %w", taskID, dependencyID, err)
	}
	if dep.State == StateCompleted {
		return nil
	}

	if task.DependsOn == nil {
		task.DependsOn = make(map[string]bool)
	}
	task.Dependencies = append(task.Dependencies, dependencyID)
	task.DependsOn[dependencyID] = true
	if err := s.saveTask(task); err != nil {
		return fmt.Errorf("scheduler: add dependency of task %s: %w", taskID, err)
	}
	return nil
}

func (s *Scheduler) QueueTask(ctx context.Context, taskID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.getTaskLocked(taskID)
	if err != nil {
		return fmt.Errorf("scheduler: queue task %s: %w", taskID, err)
	}
	if task.State != StatePending && task.State != StateRunning {
		return fmt.Errorf("%w: queue task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	for depID := range task.DependsOn {
		dep, err := s.getTaskLocked(depID)
		if err != nil {
			if !errors.Is(err, lwerrors.ErrTaskNotFound) {
				return fmt.Errorf("scheduler: queue task %s: check dependency %s: %w", taskID, depID, err)
			}
			continue
		}
		if dep.State != StateCompleted {
			return fmt.Errorf("%w: task %s depends on incomplete %s", lwerrors.ErrTaskInvalid, taskID, depID)
		}
	}

	task.State = StateQueued
	if err := s.saveTask(task); err != nil {
		return fmt.Errorf("scheduler: queue task %s: %w", taskID, err)
	}
	s.insertByPriority(task)
	return nil
}

func (s *Scheduler) insertByPriority(task *Task) {
	heap.Push(s.queue, task)
	s.notifyWorkers()
}

func (s *Scheduler) notifyWorkers() {
	select {
	case s.taskNotifyCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) removeTaskFromQueue(taskID string) {
	for i, t := range s.queue.data {
		if t.ID == taskID {
			heap.Remove(s.queue, i)
			return
		}
	}
}

func (s *Scheduler) inQueue(taskID string) bool {
	for _, t := range s.queue.data {
		if t.ID == taskID {
			return true
		}
	}
	return false
}

func (s *Scheduler) StartTask(ctx context.Context, taskID, workerID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()

	task, err := s.getTaskLocked(taskID)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: start task %s: %w", taskID, err)
	}
	if task.State != StateQueued {
		s.mu.Unlock()
		return fmt.Errorf("%w: start task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	now := time.Now()
	task.State = StateRunning
	task.StartedAt = &now
	if err := s.saveTask(task); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: start task %s: %w", taskID, err)
	}
	s.removeTaskFromQueue(taskID)
	s.mu.Unlock()

	s.publish(ctx, event.EventTaskStarted, event.TaskStartedPayload{
		TaskID:   taskID,
		WorkerID: workerID,
	}, nil, "start task "+taskID)
	return nil
}

func (s *Scheduler) CompleteTask(ctx context.Context, taskID, workerID string, result []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()

	task, err := s.getTaskLocked(taskID)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: complete task %s: %w", taskID, err)
	}
	if task.State != StateRunning {
		s.mu.Unlock()
		return fmt.Errorf("%w: complete task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	now := time.Now()
	task.State = StateCompleted
	task.EndedAt = &now
	task.Result = result
	if err := s.saveTask(task); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: complete task %s: %w", taskID, err)
	}

	var duration time.Duration
	if task.StartedAt != nil {
		duration = now.Sub(*task.StartedAt)
	}
	err = s.unblockDependentsLocked(taskID)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("scheduler: complete task %s: unblock dependents: %w", taskID, err)
	}

	s.publish(ctx, event.EventTaskCompleted, event.TaskCompletedPayload{
		TaskID:   taskID,
		WorkerID: workerID,
		Duration: duration,
		Result:   result,
	}, nil, "complete task "+taskID)
	return nil
}

// unblockDependentsLocked promotes pending tasks whose last dependency just completed.
func (s *Scheduler) unblockDependentsLocked(completedTaskID string) error {
	pending, err := s.store.list(TaskFilter{States: []TaskState{StatePending}})
	if err != nil {
		return err
	}

	for _, task := range pending {
		if !task.DependsOn[completedTaskID] {
			continue
		}
		delete(task.DependsOn, completedTaskID)
		// Dependencies stays intact for data lineage; only the gate is released.
		if len(task.DependsOn) == 0 {
			task.State = StateQueued
		}
		if err := s.saveTask(task); err != nil {
			return fmt.Errorf("unblock task %s after %s completed: %w", task.ID, completedTaskID, err)
		}
		if task.State == StateQueued {
			s.insertByPriority(task)
		}
	}
	return nil
}

func (s *Scheduler) FailTask(ctx context.Context, taskID, workerID, errMsg string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()

	task, err := s.getTaskLocked(taskID)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: fail task %s: %w", taskID, err)
	}
	// A task can fail without ever reaching StateRunning: the dispatcher pops it,
	// the worker refuses it, and the task is left queued. Accepting that state is
	// what lets a failed start be recorded instead of surviving until restart.
	switch task.State {
	case StateRunning, StateQueued:
	default:
		s.mu.Unlock()
		return fmt.Errorf("%w: fail task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	task.Error = errMsg
	task.Retry++
	retry := task.Retry <= task.MaxRetry

	if retry {
		task.State = StateQueued
	} else {
		now := time.Now()
		task.State = StateDeadLetter
		task.EndedAt = &now
		// Otherwise a dead-lettered task stays in the heap and the next
		// dispatcher pops a task that has already given up.
		s.removeTaskFromQueue(taskID)
	}

	if err := s.saveTask(task); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: fail task %s (retry %d): %w", taskID, task.Retry, err)
	}
	if retry {
		// Only re-queue a task the dispatcher already popped. Pushing a task that
		// is still in the heap runs the same task twice, concurrently.
		if !s.inQueue(taskID) {
			s.insertByPriority(task)
		}
	}
	s.mu.Unlock()

	if retry {
		s.publish(ctx, event.EventTaskRetried, event.TaskFailedPayload{
			TaskID:   taskID,
			WorkerID: workerID,
			Error:    errMsg,
			Retry:    task.Retry,
		}, nil, "retry task "+taskID)
		return nil
	}
	s.publish(ctx, event.EventTaskFailed, event.TaskFailedPayload{
		TaskID:   taskID,
		WorkerID: workerID,
		Error:    errMsg,
		Retry:    task.Retry,
	}, nil, "fail task "+taskID)
	return nil
}

func (s *Scheduler) CancelTask(ctx context.Context, taskID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()

	task, err := s.getTaskLocked(taskID)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: cancel task %s: %w", taskID, err)
	}
	if isTerminal(task.State) {
		s.mu.Unlock()
		return fmt.Errorf("%w: cancel task %s in terminal state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	now := time.Now()
	task.State = StateCancelled
	task.EndedAt = &now
	if err := s.saveTask(task); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: cancel task %s: %w", taskID, err)
	}
	s.removeTaskFromQueue(taskID)
	s.mu.Unlock()

	s.publish(ctx, event.EventTaskCancelled, nil, map[string]string{"task_id": taskID}, "cancel task "+taskID)
	return nil
}

// publish sends a lifecycle event. A store failure is logged loudly with the operation it
// belongs to: the transition itself already succeeded, so it must not be reported as one.
func (s *Scheduler) publish(ctx context.Context, ty event.EventType, payload interface{}, metadata map[string]string, op string) {
	if s.eventBus == nil {
		return
	}
	if err := s.eventBus.Publish(ctx, event.NewEvent(ty, payload, metadata)); err != nil {
		s.logger.Error("failed to publish event", zap.String("event_type", string(ty)),
			zap.String("operation", op), zap.Error(err))
	}
}

func (s *Scheduler) getTaskLocked(taskID string) (*Task, error) {
	task, err := s.store.get(taskID)
	if err != nil {
		if errors.Is(err, lwerrors.ErrTaskNotFound) {
			return nil, fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, taskID)
		}
		return nil, err
	}
	return task, nil
}

// GetTask returns a task by ID. Read failures are counted in StorageInfo, not hidden.
func (s *Scheduler) GetTask(taskID string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, err := s.getTaskLocked(taskID)
	if err != nil {
		if !errors.Is(err, lwerrors.ErrTaskNotFound) {
			s.logger.Error("task read failed", zap.String("task_id", taskID), zap.String("op", "GetTask"), zap.Error(err))
		}
		return nil, false
	}
	return task, true
}

// ListTasks returns tasks newest-first. Prefer ListTasksErr when the caller must know
// whether an empty result means "no tasks" or "storage unreadable".
func (s *Scheduler) ListTasks(filter TaskFilter) []*Task {
	tasks, err := s.ListTasksErr(filter)
	if err != nil {
		s.logger.Error("task list failed", zap.String("op", "ListTasks"), zap.Error(err))
		return nil
	}
	return tasks
}

func (s *Scheduler) ListTasksErr(filter TaskFilter) ([]*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks, err := s.store.list(filter)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list tasks: %w", err)
	}
	return tasks, nil
}

func (s *Scheduler) DequeueTask() *Task {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.queue.Len() == 0 {
		return nil
	}
	return heap.Pop(s.queue).(*Task)
}

func (s *Scheduler) QueueSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queue.Len()
}

func (s *Scheduler) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := map[string]interface{}{
		"queued":          s.queue.Len(),
		"pending":         0,
		"running":         0,
		"completed":       0,
		"failed":          0,
		"cancelled":       0,
		"total":           0,
		"in_memory_queue": s.queue.Len(),
		"persistent":      !s.ephemeral,
	}

	counts, err := s.store.counts()
	if err != nil {
		stats["storage_error"] = fmt.Sprintf("count tasks: %v", err)
		return stats
	}
	var total int
	for state, n := range counts {
		stats[state] = n
		total += n
	}
	stats["total"] = total
	stats["storage"] = s.store.info()
	return stats
}

// StorageInfo reports where the tasks live, how large the store is and what retention has
// reclaimed. It is the answer to "the database grew forever" and "my tasks vanished".
func (s *Scheduler) StorageInfo() StorageInfo {
	info := s.store.info()
	info.Persistent = !s.ephemeral
	if s.degradedFrom != "" {
		info.LastError = s.degradedFrom
	}
	return info
}

// PruneNow applies the retention policy immediately instead of waiting for the background
// pruner.
func (s *Scheduler) PruneNow() (PruneResult, error) {
	res, err := s.store.prune(s.cfg.Retention)
	if err != nil {
		return res, fmt.Errorf("scheduler: prune: %w", err)
	}
	return res, nil
}

// BackupTo writes a consistent copy of the task database to destPath. The copy is the
// documented safe-reset path: back up, then delete the file to start empty.
func (s *Scheduler) BackupTo(destPath string) error {
	if err := s.store.backup(destPath); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) startRetention() {
	if !s.cfg.Retention.enabled() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.retentionCancel = cancel
	s.retentionDone = make(chan struct{})

	go func() {
		defer close(s.retentionDone)
		ticker := time.NewTicker(s.cfg.Retention.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				res, err := s.store.prune(s.cfg.Retention)
				if err != nil {
					s.logger.Error("task retention failed", zap.Error(err))
					continue
				}
				if res.DeletedTasks > 0 || res.ResultsCleared > 0 {
					s.logger.Info("task retention reclaimed storage",
						zap.Int("tasks_deleted", res.DeletedTasks),
						zap.Int("results_cleared", res.ResultsCleared))
				}
			}
		}
	}()
}

// Close stops the retention pruner, closes the database and releases the instance lock.
// Idempotent; the server must call it on shutdown or the lock takes ~10s to expire.
func (s *Scheduler) Close() error {
	s.closed.Store(true)

	s.closeOnce.Do(func() {
		var errs []error
		if s.retentionCancel != nil {
			s.retentionCancel()
			if s.retentionDone != nil {
				<-s.retentionDone
			}
		}
		if err := s.store.close(); err != nil {
			errs = append(errs, err)
		}
		if s.lock != nil {
			if err := s.lock.release(); err != nil {
				errs = append(errs, err)
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

var taskIDCounter atomic.Int64

func generateTaskID() string {
	return fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), taskIDCounter.Add(1))
}

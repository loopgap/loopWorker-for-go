package scheduler

import (
	"container/heap"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
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
	db           *gorm.DB // Using GORM
	logger       *zap.Logger
}

func NewScheduler(eventBus *event.EventBus) *Scheduler {
	q := &taskQueue{data: make([]*Task, 0)}
	heap.Init(q)

	dbPath := "loopworker_tasks.db"
	if flag.Lookup("test.v") != nil {
		dbPath = ":memory:"
	}

	// Note: We use a local sqlite file for persistence or memory for tests
	db, err := initDB(dbPath)
	if err != nil {
		logger.Warn("failed to init db, using memory only", zap.Error(err))
	}

	s := &Scheduler{
		eventBus:     eventBus,
		queue:        q,
		taskNotifyCh: make(chan struct{}, 1),
		logger:       logger.Named("scheduler"),
		db:           db,
	}

	if s.db != nil {
		s.recoverTasks()
	}

	return s
}

func (s *Scheduler) recoverTasks() {
	var models []TaskModel
	// Recover anything that was queued or pending
	s.db.Where("state IN ?", []string{string(StateQueued), string(StateRunning)}).Find(&models)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range models {
		t := modelToTask(&m)
		// If it was running when we crashed, it needs to be queued again
		if t.State == StateRunning {
			t.State = StateQueued
			s.saveTaskTx(s.db, t)
		}
		heap.Push(s.queue, t)
	}
	if s.queue.Len() > 0 {
		s.notifyWorkers()
	}
}

func modelToTask(m *TaskModel) *Task {
	t := &Task{
		ID:        m.ID,
		Type:      m.Type,
		State:     TaskState(m.State),
		Priority:  TaskPriority(m.Priority),
		Input:     m.Input,
		Result:    m.Result,
		Error:     m.Error,
		Retry:     m.Retry,
		MaxRetry:  m.MaxRetry,
		CreatedAt: time.Unix(0, m.CreatedAt),
		DependsOn: make(map[string]bool),
	}

	json.Unmarshal(m.ConfigJSON, &t.Config)
	json.Unmarshal(m.MetadataJSON, &t.Metadata)
	json.Unmarshal(m.Dependencies, &t.Dependencies)

	t.IsAgent = m.IsAgent
	if len(m.AgentConfigJSON) > 0 {
		var ac AgentConfig
		if err := json.Unmarshal(m.AgentConfigJSON, &ac); err == nil {
			t.AgentConfig = &ac
		}
	}

	for _, d := range t.Dependencies {
		t.DependsOn[d] = true
	}

	if m.StartedAt != nil {
		ts := time.Unix(0, *m.StartedAt)
		t.StartedAt = &ts
	}
	if m.EndedAt != nil {
		te := time.Unix(0, *m.EndedAt)
		t.EndedAt = &te
	}
	return t
}

func (s *Scheduler) saveTask(task *Task) {
	if s.db == nil {
		return
	}
	m, _ := taskToModel(task)
	s.db.Save(m)
}

func (s *Scheduler) SaveTask(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveTask(task)
}

func (s *Scheduler) saveTaskTx(tx *gorm.DB, task *Task) {
	if tx == nil {
		return
	}
	m, _ := taskToModel(task)
	tx.Save(m)
}

func (s *Scheduler) NotifyCh() <-chan struct{} {
	return s.taskNotifyCh
}

func (s *Scheduler) CreateTask(ctx context.Context, taskType string, config map[string]interface{}, input []byte) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	s.saveTask(task)

	if s.eventBus != nil {
		createEvent := event.NewEvent(event.EventTaskCreated, event.TaskCreatedPayload{
			TaskID:   task.ID,
			TaskType: taskType,
			Config:   config,
		}, nil)
		if err := s.eventBus.Publish(ctx, createEvent); err != nil {
			// 记录错误但不阻塞任务创建
			logger.Warn("failed to publish task created event", zap.Error(err))
		}
	}

	return task, nil
}

func (s *Scheduler) CreateTaskWithPriority(ctx context.Context, taskType string, priority TaskPriority, config map[string]interface{}, input []byte) (*Task, error) {
	task, err := s.CreateTask(ctx, taskType, config, input)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	task.Priority = priority
	s.saveTask(task)
	s.mu.Unlock()

	return task, nil
}

func (s *Scheduler) AddDependency(ctx context.Context, taskID, dependencyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task := s.getTaskInternal(taskID)
	if task == nil {
		return fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, taskID)
	}

	dep := s.getTaskInternal(dependencyID)
	if dep == nil {
		return fmt.Errorf("%w: dependency %s", lwerrors.ErrTaskNotFound, dependencyID)
	}

	if dep.State == StateCompleted {
		return nil
	}

	task.Dependencies = append(task.Dependencies, dependencyID)
	task.DependsOn[dependencyID] = true
	s.saveTask(task)

	return nil
}

func (s *Scheduler) QueueTask(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task := s.getTaskInternal(taskID)
	if task == nil {
		return fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, taskID)
	}

	if task.State != StatePending && task.State != StateRunning {
		return fmt.Errorf("%w: task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	if len(task.Dependencies) > 0 {
		for depID := range task.DependsOn {
			dep := s.getTaskInternal(depID)
			if dep != nil && dep.State != StateCompleted {
				return fmt.Errorf("%w: task %s depends on incomplete %s", lwerrors.ErrTaskInvalid, taskID, depID)
			}
		}
	}

	task.State = StateQueued
	s.saveTask(task)
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

func (s *Scheduler) StartTask(ctx context.Context, taskID, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task := s.getTaskInternal(taskID)
	if task == nil {
		return fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, taskID)
	}

	if task.State != StateQueued {
		return fmt.Errorf("%w: task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	now := time.Now()
	task.State = StateRunning
	task.StartedAt = &now

	s.saveTask(task)
	s.removeTaskFromQueue(taskID)

	if s.eventBus != nil {
		startEvent := event.NewEvent(event.EventTaskStarted, event.TaskStartedPayload{
			TaskID:   taskID,
			WorkerID: workerID,
		}, nil)
		if err := s.eventBus.Publish(ctx, startEvent); err != nil {
			// 记录错误但不阻塞任务启动
			logger.Warn("failed to publish task started event", zap.Error(err))
		}
	}

	return nil
}

func (s *Scheduler) CompleteTask(ctx context.Context, taskID, workerID string, result []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task := s.getTaskInternal(taskID)
	if task == nil {
		return fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, taskID)
	}

	if task.State != StateRunning {
		return fmt.Errorf("%w: task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	now := time.Now()
	task.State = StateCompleted
	task.EndedAt = &now
	task.Result = result

	s.saveTask(task)

	if s.eventBus != nil {
		completeEvent := event.NewEvent(event.EventTaskCompleted, event.TaskCompletedPayload{
			TaskID:   taskID,
			WorkerID: workerID,
			Duration: now.Sub(*task.StartedAt),
			Result:   result,
		}, nil)
		if err := s.eventBus.Publish(ctx, completeEvent); err != nil {
			// 记录错误但不阻塞任务完成
			logger.Warn("failed to publish task completed event", zap.Error(err))
		}
	}

	s.unblockDependents(ctx, taskID)

	return nil
}

func (s *Scheduler) unblockDependents(ctx context.Context, completedTaskID string) {
	// Need to query pending tasks that depend on this
	var models []TaskModel
	if s.db != nil {
		s.db.Where("state = ?", string(StatePending)).Find(&models)
	}

	for _, m := range models {
		task := modelToTask(&m)
		if task.DependsOn[completedTaskID] {
			delete(task.DependsOn, completedTaskID)

			// Remove completedTaskID from DependsOn map to unblock, but do not clear Dependencies to allow data lineage
			if len(task.DependsOn) == 0 {
				task.State = StateQueued
				s.insertByPriority(task)
			}
			s.saveTask(task)
		}
	}
}

func (s *Scheduler) FailTask(ctx context.Context, taskID, workerID, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task := s.getTaskInternal(taskID)
	if task == nil {
		return fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, taskID)
	}

	if task.State != StateRunning {
		return fmt.Errorf("%w: task %s in state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	task.Error = errMsg
	task.Retry++

	if task.Retry <= task.MaxRetry {
		task.State = StateQueued
		s.saveTask(task)
		if s.eventBus != nil {
			retryEvent := event.NewEvent(event.EventTaskRetried, event.TaskFailedPayload{
				TaskID:   taskID,
				WorkerID: workerID,
				Error:    errMsg,
				Retry:    task.Retry,
			}, nil)
			if err := s.eventBus.Publish(ctx, retryEvent); err != nil {
				// 记录错误但不阻塞任务重试
				logger.Warn("failed to publish task retried event", zap.Error(err))
			}
		}
		s.insertByPriority(task)
	} else {
		now := time.Now()
		// Moved to DLQ instead of just Failed if retries exhausted
		task.State = StateDeadLetter
		task.EndedAt = &now
		s.saveTask(task)

		if s.eventBus != nil {
			failEvent := event.NewEvent(event.EventTaskFailed, event.TaskFailedPayload{
				TaskID:   taskID,
				WorkerID: workerID,
				Error:    errMsg,
				Retry:    task.Retry,
			}, nil)
			if err := s.eventBus.Publish(ctx, failEvent); err != nil {
				// 记录错误但不阻塞任务失败
				logger.Warn("failed to publish task failed event", zap.Error(err))
			}
		}
	}

	return nil
}

func (s *Scheduler) CancelTask(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task := s.getTaskInternal(taskID)
	if task == nil {
		return fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, taskID)
	}

	if task.State == StateCompleted || task.State == StateFailed || task.State == StateCancelled || task.State == StateDeadLetter {
		return fmt.Errorf("%w: task %s in terminal state %s", lwerrors.ErrTaskInvalid, taskID, task.State)
	}

	now := time.Now()
	task.State = StateCancelled
	task.EndedAt = &now

	s.saveTask(task)
	s.removeTaskFromQueue(taskID)

	if s.eventBus != nil {
		cancelEvent := event.NewEvent(event.EventTaskCancelled, nil, map[string]string{"task_id": taskID})
		if err := s.eventBus.Publish(ctx, cancelEvent); err != nil {
			// 记录错误但不阻塞任务取消
			logger.Warn("failed to publish task cancelled event", zap.Error(err))
		}
	}

	return nil
}

func (s *Scheduler) getTaskInternal(taskID string) *Task {
	if s.db == nil {
		return nil
	}
	var m TaskModel
	if err := s.db.First(&m, "id = ?", taskID).Error; err != nil {
		return nil
	}
	return modelToTask(&m)
}

func (s *Scheduler) GetTask(taskID string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task := s.getTaskInternal(taskID)
	return task, task != nil
}

func (s *Scheduler) ListTasks(filter TaskFilter) []*Task {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var models []TaskModel
	query := s.db.Model(&TaskModel{})

	if len(filter.States) > 0 {
		statesStr := make([]string, len(filter.States))
		for i, st := range filter.States {
			statesStr[i] = string(st)
		}
		query = query.Where("state IN ?", statesStr)
	}

	if len(filter.Types) > 0 {
		query = query.Where("type IN ?", filter.Types)
	}

	if filter.Priority != nil {
		query = query.Where("priority = ?", *filter.Priority)
	}

	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}

	query.Order("created_at desc").Find(&models)

	var result []*Task
	for _, m := range models {
		result = append(result, modelToTask(&m))
	}

	return result
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
		"queued":    s.queue.Len(),
		"pending":   0,
		"running":   0,
		"completed": 0,
		"failed":    0,
		"cancelled": 0,
		"total":     0,
	}

	if s.db != nil {
		type StateCount struct {
			State string
			Count int
		}
		var counts []StateCount
		s.db.Model(&TaskModel{}).Select("state, count(*) as count").Group("state").Scan(&counts)

		var total int
		for _, c := range counts {
			stats[c.State] = c.Count
			total += c.Count
		}
		stats["total"] = total
	}

	return stats
}

var taskIDCounter int64

func generateTaskID() string {
	taskIDCounter++
	return fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), taskIDCounter)
}

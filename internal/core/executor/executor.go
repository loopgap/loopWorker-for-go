// Package executor implements the Worker Pool pattern — the execution engine of LoopWorker.
//
// Architecture Pattern: Worker Pool with Event-Driven Dispatch
// =============================================================
// The Executor manages a pool of Worker goroutines that execute tasks:
//
//	┌─────────────┐     ┌──────────────┐     ┌─────────────┐
//	│  Scheduler   │────►│  Dispatcher  │────►│   Worker 1  │
//	│ (priority    │     │ (match task  │     │   Worker 2  │
//	│  queue)      │     │  to worker)  │     │   Worker N  │
//	└─────────────┘     └──────────────┘     └─────────────┘
//	       ▲                                        │
//	       └──────────(result/events)───────────────┘
//
// Design Decisions:
//   - Workers are long-lived goroutines (not per-task goroutines) to avoid GC pressure
//   - Each worker has a buffered task channel (capacity 1) for handoff
//   - The central Run() loop is purely event-driven (no polling) using select{}
//   - taskCancel() MUST be called at end of each iteration, not deferred in the loop
//     (defer would only execute when the goroutine exits, leaking contexts)
//
// Teaching Note: Goroutine Lifecycle
// ===================================
// Each worker goroutine follows this lifecycle:
//  1. IDLE: waiting on taskCh or stopCh
//  2. BUSY: executing a task with timeout context
//  3. STOPPED: stopCh closed, goroutine returns
//
// The doneCh channel ensures StopWorker() blocks until the goroutine has fully exited,
// preventing resource leaks during graceful shutdown.
package executor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/skill"
	"loopworker/pkg/utils"
)

const (
	workerIdle int32 = iota
	workerBusy
	workerStopped
)

type Worker struct {
	ID          string
	PluginID    string
	state       int32
	tasksRun    int64
	tasksFailed int64
	totalTime   time.Duration
	stopCh      chan struct{}
	doneCh      chan struct{}
	taskCh      chan *scheduler.Task
	lastActive  time.Time
	currentTask string
	mu          sync.Mutex
}

type Executor struct {
	provider          dispatcher.TaskProvider
	dispatcher        *dispatcher.Dispatcher
	sandbox           *sandbox.Sandbox
	eventBus          *event.EventBus
	selfHealer        *selfheal.SelfHealer
	workers           map[string]*Worker
	mu                sync.RWMutex
	wg                sync.WaitGroup
	stats             *ExecutorStats
	workerFree        chan struct{}
	llmClient         *LLMClient
	skillCtx          skill.SkillContext
	llmCircuitBreaker *selfheal.CircuitBreaker
	taskTimeout       time.Duration // 全局任务执行超时
	logger            *zap.Logger
}

// ExecutorOption 定义Executor的函数选项
type ExecutorOption func(*Executor)

// WithTaskTimeout 设置全局任务执行超时
func WithTaskTimeout(timeout time.Duration) ExecutorOption {
	return func(e *Executor) {
		e.taskTimeout = timeout
	}
}

// WithLLMClient 设置LLM客户端（函数选项模式）
func WithLLMClient(client *LLMClient) ExecutorOption {
	return func(e *Executor) {
		e.llmClient = client
	}
}

type ExecutorStats struct {
	TotalTasksRun    int64
	TotalTasksFailed int64
	TotalExecTime    time.Duration
	ActiveWorkers    int32
	IdleWorkers      int32
}

func NewExecutor(provider dispatcher.TaskProvider, disp *dispatcher.Dispatcher, sandbox *sandbox.Sandbox, eventBus *event.EventBus, healer *selfheal.SelfHealer, opts ...ExecutorOption) *Executor {
	e := &Executor{
		provider:    provider,
		dispatcher:  disp,
		sandbox:     sandbox,
		eventBus:    eventBus,
		selfHealer:  healer,
		workers:     make(map[string]*Worker),
		stats:       &ExecutorStats{},
		workerFree:  make(chan struct{}, 1000), // Buffer to avoid blocking
		taskTimeout: 30 * time.Minute,          // 默认30分钟超时
		logger:      logger.Named("executor"),
	}

	// 应用函数选项
	for _, opt := range opts {
		opt(e)
	}

	return e
}

func (e *Executor) SetLLMClient(client *LLMClient) {
	e.mu.Lock()
	e.llmClient = client
	e.mu.Unlock()
}

func (e *Executor) WithLLMClient(client *LLMClient) *Executor {
	e.SetLLMClient(client)
	return e
}

// SetSkillContext sets the skill context injected into plugins during execution.
func (e *Executor) SetSkillContext(ctx skill.SkillContext) {
	e.mu.Lock()
	e.skillCtx = ctx
	e.mu.Unlock()
}

func (e *Executor) getSkillContext() skill.SkillContext {
	e.mu.RLock()
	ctx := e.skillCtx
	e.mu.RUnlock()
	return ctx
}

func (e *Executor) getLLMClient() *LLMClient {
	e.mu.RLock()
	client := e.llmClient
	e.mu.RUnlock()

	if client == nil {
		return NewLLMClient("", "")
	}
	return client
}

func (e *Executor) StartWorker(ctx context.Context, workerID, pluginID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.workers[workerID]; exists {
		return fmt.Errorf("%w: %s", lwerrors.ErrWorkerDuplicate, workerID)
	}

	worker := &Worker{
		ID:         workerID,
		PluginID:   pluginID,
		state:      workerIdle,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
		taskCh:     make(chan *scheduler.Task, 1),
		lastActive: time.Now(),
	}

	e.workers[workerID] = worker

	if err := e.dispatcher.RegisterWorker(ctx, workerID, pluginID); err != nil {
		delete(e.workers, workerID)
		return fmt.Errorf("register worker: %w", err)
	}

	e.wg.Add(1)
	utils.GoSafe(ctx, func(innerCtx context.Context) {
		e.workerLoop(innerCtx, worker)
	})

	atomic.AddInt32(&e.stats.ActiveWorkers, 1)
	atomic.AddInt32(&e.stats.IdleWorkers, 1)

	if e.eventBus != nil {
		evt := event.NewEvent(event.EventWorkerSpawned, event.WorkerSpawnedPayload{
			WorkerID: workerID,
			PluginID: pluginID,
		}, nil)
		if err := e.eventBus.Publish(ctx, evt); err != nil {
			// 记录错误但不阻塞worker启动
			logger.Warn("failed to publish worker spawned event", zap.Error(err))
		}
	}

	e.notifyWorkerFree()
	return nil
}

func (e *Executor) StopWorker(ctx context.Context, workerID string) error {
	e.mu.Lock()
	worker, exists := e.workers[workerID]
	if !exists {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", lwerrors.ErrWorkerNotFound, workerID)
	}

	atomic.StoreInt32(&worker.state, workerStopped)
	close(worker.stopCh)
	delete(e.workers, workerID)
	e.mu.Unlock()

	<-worker.doneCh

	atomic.AddInt32(&e.stats.ActiveWorkers, -1)
	atomic.AddInt32(&e.stats.IdleWorkers, -1)

	if e.eventBus != nil {
		evt := event.NewEvent(event.EventWorkerExited, event.WorkerExitedPayload{
			WorkerID: workerID,
			ExitCode: 0,
		}, nil)
		if err := e.eventBus.Publish(ctx, evt); err != nil {
			// 记录错误但不阻塞worker停止
			logger.Warn("failed to publish worker exited event", zap.Error(err))
		}
	}

	return e.dispatcher.UnregisterWorker(ctx, workerID)
}

func (e *Executor) StopAllWorkers(ctx context.Context) error {
	e.mu.RLock()
	workerIDs := make([]string, 0, len(e.workers))
	for id := range e.workers {
		workerIDs = append(workerIDs, id)
	}
	e.mu.RUnlock()

	for _, id := range workerIDs {
		if err := e.StopWorker(ctx, id); err != nil {
			return fmt.Errorf("stop worker %s: %w", id, err)
		}
	}

	e.wg.Wait()
	return nil
}

func (e *Executor) workerLoop(ctx context.Context, worker *Worker) {
	defer e.wg.Done()
	defer close(worker.doneCh)

	for {
		select {
		case <-worker.stopCh:
			return
		case <-ctx.Done():
			return
		case task := <-worker.taskCh:
			worker.mu.Lock()
			worker.currentTask = task.ID
			worker.mu.Unlock()

			atomic.StoreInt32(&worker.state, workerBusy)
			atomic.AddInt32(&e.stats.IdleWorkers, -1)

			start := time.Now()

			// 创建任务超时context（每轮迭代独立cancel，避免defer在循环中积压）
			taskCtx, taskCancel := context.WithTimeout(ctx, e.taskTimeout)

			// Execute task
			var output []byte
			var execErr error

			if task.IsAgent {
				upstreamResults := make(map[string]string)
				for _, depID := range task.Dependencies {
					if gt, ok := e.provider.(interface {
						GetTask(string) (*scheduler.Task, bool)
					}); ok {
						if depTask, found := gt.GetTask(depID); found {
							upstreamResults[depTask.ID] = string(depTask.Result)
						}
					}
				}

				cb := NewContextBuilder(16000)
				userPrompt := cb.BuildUserPrompt(upstreamResults, string(task.Input))

				llm := e.getLLMClient()
				systemPrompt := "You are a helpful AI assistant in a workflow execution. Provide a structured response."
				model := "gpt-4o"
				schema := ""
				if task.AgentConfig != nil {
					if task.AgentConfig.SystemPrompt != "" {
						systemPrompt = task.AgentConfig.SystemPrompt
					}
					if task.AgentConfig.Model != "" {
						model = task.AgentConfig.Model
					}
					schema = task.AgentConfig.ResponseSchema
				}

				var llmOut string
				if e.llmCircuitBreaker == nil && e.selfHealer != nil {
					e.llmCircuitBreaker = e.selfHealer.GetCircuitBreaker("llm")
				}
				if e.llmCircuitBreaker != nil && !e.llmCircuitBreaker.AllowRequest() {
					execErr = lwerrors.ErrCircuitOpen
				} else {
					// LLM调用超时独立设置，但受全局任务超时约束
					llmTimeout := 30 * time.Second
					if llmTimeout > e.taskTimeout {
						llmTimeout = e.taskTimeout
					}
					llmCtx, llmCancel := context.WithTimeout(taskCtx, llmTimeout)
					llmOut, execErr = llm.GenerateStructured(llmCtx, model, systemPrompt, userPrompt, schema)
					llmCancel()
					if execErr == nil {
						output = []byte(llmOut)
					}
					if e.llmCircuitBreaker != nil {
						if execErr != nil {
							e.llmCircuitBreaker.RecordFailure()
						} else {
							e.llmCircuitBreaker.RecordSuccess()
						}
					}
				}
			} else {
				if e.selfHealer != nil {
					execErr = e.selfHealer.ExecuteWithRecovery(taskCtx, worker.PluginID, func(innerCtx context.Context) error {
						plugins := e.sandbox.ListPlugins()
						if len(plugins) == 0 {
							return lwerrors.ErrPluginNotFound
						}
						var err error
						skillCtx := e.getSkillContext()
						output, err = e.sandbox.Execute(innerCtx, worker.PluginID, task.Input, skillCtx)

						// Publish skill invoked event when LLM skill is available
						if err == nil && skillCtx.Bus != nil && skillCtx.Config != nil {
							if llmClient, ok := skillCtx.Config["llm"].(interface{}); ok && llmClient != nil {
								if publishErr := skillCtx.Bus.Publish(ctx, event.NewEvent(event.EventSkillInvoked, event.SkillInvokedPayload{
									SkillName: "llm.chat",
									TaskID:    task.ID,
									Success:   true,
								}, nil)); publishErr != nil {
									// 记录错误但不阻塞执行
									logger.Warn("failed to publish skill invoked event", zap.Error(publishErr))
								}
							}
						}

						return err
					})
				} else {
					plugins := e.sandbox.ListPlugins()
					if len(plugins) == 0 {
						execErr = lwerrors.ErrPluginNotFound
					} else {
						skillCtx := e.getSkillContext()
						output, execErr = e.sandbox.Execute(taskCtx, worker.PluginID, task.Input, skillCtx)

						// Publish skill invoked event when LLM skill is available
						if execErr == nil && skillCtx.Bus != nil && skillCtx.Config != nil {
							if llmClient, ok := skillCtx.Config["llm"].(interface{}); ok && llmClient != nil {
								_ = skillCtx.Bus.Publish(ctx, event.NewEvent(event.EventSkillInvoked, event.SkillInvokedPayload{
									SkillName: "llm.chat",
									TaskID:    task.ID,
									Success:   true,
								}, nil))
							}
						}
					}
				}
			}

			// 检查是否是超时错误
			if taskCtx.Err() == context.DeadlineExceeded && execErr == nil {
				execErr = fmt.Errorf("%w: after %v", lwerrors.ErrTaskTimeout, e.taskTimeout)
			}

			if execErr != nil {
				if err := e.dispatcher.FailTask(ctx, task.ID, worker.ID, execErr.Error()); err != nil {
					// 记录错误但不阻塞执行
					logger.Warn("failed to fail task", zap.String("taskID", task.ID), zap.Error(err))
				}
				atomic.AddInt64(&e.stats.TotalTasksFailed, 1)
				atomic.AddInt64(&worker.tasksFailed, 1)
			} else {
				if err := e.dispatcher.CompleteTask(ctx, task.ID, worker.ID, output); err != nil {
					// 记录错误但不阻塞执行
					logger.Warn("failed to complete task", zap.String("taskID", task.ID), zap.Error(err))
				}
			}

			elapsed := time.Since(start)
			taskCancel() // 释放本轮taskCtx资源，不能defer（循环体内）

			atomic.AddInt64(&worker.tasksRun, 1)
			atomic.AddInt64(&e.stats.TotalTasksRun, 1)
			atomic.AddInt64((*int64)(&e.stats.TotalExecTime), int64(elapsed))

			atomic.StoreInt32(&worker.state, workerIdle)
			atomic.AddInt32(&e.stats.IdleWorkers, 1)

			worker.mu.Lock()
			worker.currentTask = ""
			worker.lastActive = time.Now()
			worker.mu.Unlock()

			// Mark worker as free in dispatcher and notify central loop
			e.dispatcher.MarkWorkerFree(worker.ID)
			e.notifyWorkerFree()
		}
	}
}

func (e *Executor) notifyWorkerFree() {
	select {
	case e.workerFree <- struct{}{}:
	default:
	}
}

// Run is now a central pure event-driven dispatch loop
func (e *Executor) Run(ctx context.Context) error {
	notifyCh := e.provider.NotifyCh()

	for {
		select {
		case <-ctx.Done():
			return e.StopAllWorkers(ctx)
		case <-notifyCh:
			e.pumpQueue(ctx)
		case <-e.workerFree:
			e.pumpQueue(ctx)
		}
	}
}

func (e *Executor) pumpQueue(ctx context.Context) {
	for {
		if e.dispatcher.AvailableWorkerCount() == 0 {
			return
		}

		task, workerInfo, err := e.dispatcher.Dispatch(ctx)
		if err != nil {
			return // Queue empty or no workers
		}

		e.mu.RLock()
		worker, exists := e.workers[workerInfo.ID]
		e.mu.RUnlock()

		if !exists {
			// The worker vanished between Dispatch and here. The task is already
			// dequeued and marked running, so it has to be failed explicitly or it
			// sits in `running` until the watchdog kills it.
			_ = e.dispatcher.FailTask(ctx, task.ID, workerInfo.ID, "worker disappeared before the task could be handed off")
			continue
		}

		select {
		case worker.taskCh <- task:
			// Handed off.
		default:
			// The channel is full. This is back pressure, not a failure: the task
			// is already dequeued and marked running, so failing it here burned a
			// retry on work that never got a chance to run. Wait for room.
			select {
			case worker.taskCh <- task:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (e *Executor) WorkerCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.workers)
}

func (e *Executor) ListWorkers() []*Worker {
	e.mu.RLock()
	defer e.mu.RUnlock()

	workers := make([]*Worker, 0, len(e.workers))
	for _, w := range e.workers {
		workers = append(workers, w)
	}
	return workers
}

func (e *Executor) GetStats() *ExecutorStats {
	return &ExecutorStats{
		TotalTasksRun:    atomic.LoadInt64(&e.stats.TotalTasksRun),
		TotalTasksFailed: atomic.LoadInt64(&e.stats.TotalTasksFailed),
		TotalExecTime:    time.Duration(atomic.LoadInt64((*int64)(&e.stats.TotalExecTime))),
		ActiveWorkers:    atomic.LoadInt32(&e.stats.ActiveWorkers),
		IdleWorkers:      atomic.LoadInt32(&e.stats.IdleWorkers),
	}
}

// GetTaskTimeout 获取当前的任务超时配置
func (e *Executor) GetTaskTimeout() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.taskTimeout
}

// SetTaskTimeout 动态更新任务超时配置
func (e *Executor) SetTaskTimeout(timeout time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.taskTimeout = timeout
}

func (w *Worker) IsBusy() bool {
	return atomic.LoadInt32(&w.state) == workerBusy
}

func (w *Worker) TasksRun() int {
	return int(atomic.LoadInt64(&w.tasksRun))
}

func (w *Worker) TasksFailed() int {
	return int(atomic.LoadInt64(&w.tasksFailed))
}

func (w *Worker) State() int32 {
	return atomic.LoadInt32(&w.state)
}

func (w *Worker) LastActive() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastActive
}

func (e *Executor) StartWatchdog(ctx context.Context) {
	utils.GoSafe(ctx, func(innerCtx context.Context) {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-innerCtx.Done():
				return
			case <-ticker.C:
				e.sweepZombies(innerCtx)
			}
		}
	})
}

func (e *Executor) sweepZombies(ctx context.Context) {
	e.mu.RLock()
	type zombie struct {
		workerID    string
		currentTask string
	}
	var zombies []zombie
	for id, worker := range e.workers {
		// If worker is busy and hasn't updated lastActive in 60s
		if atomic.LoadInt32(&worker.state) != workerBusy {
			continue
		}
		// lastActive and currentTask share worker.mu, so one lock covers both.
		worker.mu.Lock()
		stale := time.Since(worker.lastActive) > 60*time.Second
		currentTask := worker.currentTask
		worker.mu.Unlock()
		if stale {
			zombies = append(zombies, zombie{workerID: id, currentTask: currentTask})
		}
	}
	e.mu.RUnlock()

	for _, z := range zombies {
		id := z.workerID
		logger.Warn("zombie worker detected, forcefully terminating", zap.String("workerID", id))
		if err := e.StopWorker(ctx, id); err != nil {
			// 记录错误但不阻塞watchdog
			logger.Warn("failed to stop zombie worker", zap.String("workerID", id), zap.Error(err))
		}

		// DeadLetter queue logic: mark the task as failed with Zombie status
		if z.currentTask != "" {
			if err := e.dispatcher.FailTask(ctx, z.currentTask, id, "zombie task forcefully terminated by watchdog"); err != nil {
				// 记录错误但不阻塞watchdog
				logger.Warn("failed to fail task", zap.String("taskID", z.currentTask), zap.Error(err))
			}
		}

		// Respawn the worker to maintain the pool
		if err := e.StartWorker(ctx, id+"-reborn", "default"); err != nil {
			// 记录错误但不阻塞watchdog
			logger.Warn("failed to respawn worker", zap.String("workerID", id+"-reborn"), zap.Error(err))
		}
	}
}

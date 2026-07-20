package executor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/ai"
	"loopworker/pkg/event"
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
	provider        dispatcher.TaskProvider
	dispatcher      *dispatcher.Dispatcher
	sandbox         *sandbox.Sandbox
	eventBus        *event.EventBus
	selfHealer      *selfheal.SelfHealer
	workers         map[string]*Worker
	mu              sync.RWMutex
	wg              sync.WaitGroup
	stats           *ExecutorStats
	workerFree      chan struct{}
	llmClient       *ai.LLMClient
	skillCtx        skill.SkillContext
	llmCircuitBreaker *selfheal.CircuitBreaker
}

type ExecutorStats struct {
	TotalTasksRun    int64
	TotalTasksFailed int64
	TotalExecTime    time.Duration
	ActiveWorkers    int32
	IdleWorkers      int32
}

func NewExecutor(provider dispatcher.TaskProvider, disp *dispatcher.Dispatcher, sandbox *sandbox.Sandbox, eventBus *event.EventBus, healer *selfheal.SelfHealer) *Executor {
	return &Executor{
		provider:   provider,
		dispatcher: disp,
		sandbox:    sandbox,
		eventBus:   eventBus,
		selfHealer: healer,
		workers:    make(map[string]*Worker),
		stats:      &ExecutorStats{},
		workerFree: make(chan struct{}, 1000), // Buffer to avoid blocking
	}
}

func (e *Executor) SetLLMClient(client *ai.LLMClient) {
	e.mu.Lock()
	e.llmClient = client
	e.mu.Unlock()
}

func (e *Executor) WithLLMClient(client *ai.LLMClient) *Executor {
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

func (e *Executor) getLLMClient() *ai.LLMClient {
	e.mu.RLock()
	client := e.llmClient
	e.mu.RUnlock()

	if client == nil {
		return ai.NewLLMClient("", "")
	}
	return client
}

func (e *Executor) StartWorker(ctx context.Context, workerID, pluginID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.workers[workerID]; exists {
		return fmt.Errorf("worker %s already exists", workerID)
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
		_ = e.eventBus.Publish(ctx, evt)
	}

	e.notifyWorkerFree()
	return nil
}

func (e *Executor) StopWorker(ctx context.Context, workerID string) error {
	e.mu.Lock()
	worker, exists := e.workers[workerID]
	if !exists {
		e.mu.Unlock()
		return fmt.Errorf("worker %s not found", workerID)
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
		_ = e.eventBus.Publish(ctx, evt)
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

				cb := ai.NewContextBuilder(16000)
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
					execErr = fmt.Errorf("circuit breaker open: LLM temporarily unavailable")
				} else {
					llmCtx, llmCancel := context.WithTimeout(ctx, 30*time.Second)
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
					execErr = e.selfHealer.ExecuteWithRecovery(ctx, worker.PluginID, func(innerCtx context.Context) error {
						plugins := e.sandbox.ListPlugins()
						if len(plugins) == 0 {
							return fmt.Errorf("no plugins available")
						}
						var err error
						skillCtx := e.getSkillContext()
						output, err = e.sandbox.Execute(innerCtx, worker.PluginID, task.Input, skillCtx)

						// Publish skill invoked event when LLM skill is available
						if err == nil && skillCtx.Bus != nil && skillCtx.Config != nil {
							if llmClient, ok := skillCtx.Config["llm"].(interface{}); ok && llmClient != nil {
								_ = skillCtx.Bus.Publish(ctx, event.NewEvent(event.EventSkillInvoked, event.SkillInvokedPayload{
									SkillName: "llm.chat",
									TaskID:    task.ID,
									Success:   true,
								}, nil))
							}
						}

						return err
					})
				} else {
					plugins := e.sandbox.ListPlugins()
					if len(plugins) == 0 {
						execErr = fmt.Errorf("no plugins available")
					} else {
						skillCtx := e.getSkillContext()
						output, execErr = e.sandbox.Execute(ctx, worker.PluginID, task.Input, skillCtx)

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

			if execErr != nil {
				_ = e.dispatcher.FailTask(ctx, task.ID, worker.ID, execErr.Error())
				atomic.AddInt64(&e.stats.TotalTasksFailed, 1)
				atomic.AddInt64(&worker.tasksFailed, 1)
			} else {
				_ = e.dispatcher.CompleteTask(ctx, task.ID, worker.ID, output)
			}

			elapsed := time.Since(start)
			atomic.AddInt64(&worker.tasksRun, 1)
			atomic.AddInt64(&e.stats.TotalTasksRun, 1)
			atomic.AddInt64((*int64)(&e.stats.TotalExecTime), int64(elapsed))

			worker.lastActive = time.Now()

			atomic.StoreInt32(&worker.state, workerIdle)
			atomic.AddInt32(&e.stats.IdleWorkers, 1)

			worker.mu.Lock()
			worker.currentTask = ""
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

		if exists {
			select {
			case worker.taskCh <- task:
				// Successfully handed off
			default:
				// Worker channel full, this shouldn't happen if dispatcher tracking is correct
				_ = e.dispatcher.FailTask(ctx, task.ID, workerInfo.ID, "worker channel full unexpectedly")
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
	var zombies []string
	var currentTasks []string
	for id, worker := range e.workers {
		// If worker is busy and hasn't updated lastActive in 60s
		if atomic.LoadInt32(&worker.state) == workerBusy && time.Since(worker.lastActive) > 60*time.Second {
			zombies = append(zombies, id)
			worker.mu.Lock()
			currentTasks = append(currentTasks, worker.currentTask)
			worker.mu.Unlock()
		}
	}
	e.mu.RUnlock()

	for i, id := range zombies {
		fmt.Printf("[Watchdog] Worker %s is a zombie, forcefully terminating it\n", id)
		_ = e.StopWorker(ctx, id)

		// DeadLetter queue logic: mark the task as failed with Zombie status
		if currentTasks[i] != "" {
			_ = e.dispatcher.FailTask(ctx, currentTasks[i], id, "zombie task forcefully terminated by watchdog")
		}

		// Respawn the worker to maintain the pool
		_ = e.StartWorker(ctx, id+"-reborn", "default")
	}
}

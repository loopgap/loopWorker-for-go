package dispatcher

import (
	"context"
	"fmt"
	"sync"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
)

type WorkerInfo struct {
	ID       string
	PluginID string
	Busy     bool
	TasksRun int
}

type TaskProvider interface {
	DequeueTask() *scheduler.Task
	StartTask(ctx context.Context, taskID, workerID string) error
	CompleteTask(ctx context.Context, taskID, workerID string, result []byte) error
	FailTask(ctx context.Context, taskID, workerID, errMsg string) error
	NotifyCh() <-chan struct{}
}

type Dispatcher struct {
	provider TaskProvider
	eventBus *event.EventBus
	workers  map[string]*WorkerInfo
	mu       sync.RWMutex
}

func NewDispatcher(provider TaskProvider, eventBus *event.EventBus) *Dispatcher {
	return &Dispatcher{
		provider: provider,
		eventBus: eventBus,
		workers:  make(map[string]*WorkerInfo),
	}
}

func (d *Dispatcher) RegisterWorker(ctx context.Context, workerID, pluginID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workers[workerID]; exists {
		return fmt.Errorf("worker %s already registered", workerID)
	}

	d.workers[workerID] = &WorkerInfo{
		ID:       workerID,
		PluginID: pluginID,
	}

	if d.eventBus != nil {
		spawnEvent := event.NewEvent(event.EventWorkerSpawned, event.WorkerSpawnedPayload{
			WorkerID: workerID,
			PluginID: pluginID,
		}, nil)
		if err := d.eventBus.Publish(ctx, spawnEvent); err != nil {
			// 记录错误但不阻塞注册
			fmt.Printf("Warning: failed to publish worker spawned event: %v\n", err)
		}
	}

	return nil
}

func (d *Dispatcher) UnregisterWorker(ctx context.Context, workerID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	worker, exists := d.workers[workerID]
	if !exists {
		return fmt.Errorf("worker %s not found", workerID)
	}

	if worker.Busy {
		return fmt.Errorf("worker %s is busy", workerID)
	}

	delete(d.workers, workerID)

	if d.eventBus != nil {
		exitEvent := event.NewEvent(event.EventWorkerExited, event.WorkerExitedPayload{
			WorkerID: workerID,
			ExitCode: 0,
		}, nil)
		if err := d.eventBus.Publish(ctx, exitEvent); err != nil {
			// 记录错误但不阻塞注销
			fmt.Printf("Warning: failed to publish worker exited event: %v\n", err)
		}
	}

	return nil
}

func (d *Dispatcher) Dispatch(ctx context.Context) (*scheduler.Task, *WorkerInfo, error) {
	// Need to check worker availability BEFORE dequeuing to avoid unneeded dequeuing
	d.mu.Lock()
	var worker *WorkerInfo
	for _, w := range d.workers {
		if !w.Busy {
			worker = w
			worker.Busy = true
			break
		}
	}
	d.mu.Unlock()

	if worker == nil {
		return nil, nil, fmt.Errorf("no available workers")
	}

	task := d.provider.DequeueTask()
	if task == nil {
		// No tasks, release the worker
		d.MarkWorkerFree(worker.ID)
		return nil, nil, fmt.Errorf("no tasks in queue")
	}

	if err := d.provider.StartTask(ctx, task.ID, worker.ID); err != nil {
		// 记录错误但不阻塞分发
		fmt.Printf("Warning: failed to start task %s: %v\n", task.ID, err)
	}

	return task, worker, nil
}

func (d *Dispatcher) MarkWorkerFree(workerID string) {
	d.mu.Lock()
	if worker, exists := d.workers[workerID]; exists {
		worker.Busy = false
	}
	d.mu.Unlock()
}

func (d *Dispatcher) CompleteTask(ctx context.Context, taskID, workerID string, result []byte) error {
	d.mu.Lock()
	worker, exists := d.workers[workerID]
	if exists {
		worker.Busy = false
		worker.TasksRun++
	}
	d.mu.Unlock()

	return d.provider.CompleteTask(ctx, taskID, workerID, result)
}

func (d *Dispatcher) FailTask(ctx context.Context, taskID, workerID, errMsg string) error {
	d.mu.Lock()
	worker, exists := d.workers[workerID]
	if exists {
		worker.Busy = false
	}
	d.mu.Unlock()

	return d.provider.FailTask(ctx, taskID, workerID, errMsg)
}

func (d *Dispatcher) ListWorkers() []*WorkerInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()

	workers := make([]*WorkerInfo, 0, len(d.workers))
	for _, w := range d.workers {
		workers = append(workers, w)
	}
	return workers
}

func (d *Dispatcher) WorkerCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.workers)
}

func (d *Dispatcher) AvailableWorkerCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()

	count := 0
	for _, w := range d.workers {
		if !w.Busy {
			count++
		}
	}
	return count
}

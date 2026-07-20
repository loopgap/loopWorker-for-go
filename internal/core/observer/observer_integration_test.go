package observer

import (
	"context"
	"testing"
	"time"

	"loopworker/internal/core/dispatcher"
	"loopworker/internal/core/executor"
	"loopworker/internal/core/sandbox"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
)

func TestEventStoreObserverCallChain(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := event.NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	bus := event.NewEventBus(store)
	defer bus.Close()

	o := NewObserver(bus)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := o.Start(ctx); err != nil {
		t.Fatalf("start observer: %v", err)
	}

	lifecycleEvents := []struct {
		et      event.EventType
		payload interface{}
	}{
		{event.EventTaskCreated, event.TaskCreatedPayload{TaskID: "task-1", TaskType: "echo"}},
		{event.EventTaskStarted, event.TaskStartedPayload{TaskID: "task-1", WorkerID: "w-1"}},
		{event.EventTaskCompleted, event.TaskCompletedPayload{TaskID: "task-1", WorkerID: "w-1", Duration: 150 * time.Millisecond}},
		{event.EventWorkerSpawned, event.WorkerSpawnedPayload{WorkerID: "w-1", PluginID: "echo"}},
		{event.EventWorkerExited, event.WorkerExitedPayload{WorkerID: "w-1", ExitCode: 0}},
		{event.EventPluginExecuted, event.PluginExecutedPayload{PluginID: "echo", TaskID: "task-1", Duration: 100 * time.Millisecond, Success: true, InputSize: 5, OutputSize: 5}},
		{event.EventSkillInvoked, event.SkillInvokedPayload{SkillName: "llm.chat", TaskID: "task-1", Duration: 50 * time.Millisecond, Success: true}},
	}

	for _, le := range lifecycleEvents {
		evt := event.NewEvent(le.et, le.payload, nil)
		if err := bus.Publish(ctx, evt); err != nil {
			t.Fatalf("publish %s: %v", le.et, err)
		}
	}

	time.Sleep(300 * time.Millisecond)

	metrics := o.GetMetrics()
	metricNames := make(map[string]bool)
	for _, m := range metrics {
		metricNames[m.Name] = true
	}

	requiredMetrics := []string{"tasks.created", "tasks.started", "tasks.completed", "workers.spawned", "workers.exited", "plugins.executed", "skills.invoked"}
	for _, name := range requiredMetrics {
		if !metricNames[name] {
			t.Errorf("missing metric %q", name)
		}
	}

	persisted, err := store.Load(ctx, event.EventFilter{})
	if err != nil {
		t.Fatalf("load from store: %v", err)
	}
	if len(persisted) != len(lifecycleEvents) {
		t.Errorf("expected %d persisted, got %d", len(lifecycleEvents), len(persisted))
	}

	store2, err := event.NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reloaded, err := store2.Load(ctx, event.EventFilter{})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded) != len(lifecycleEvents) {
		t.Errorf("expected %d after restart, got %d", len(lifecycleEvents), len(reloaded))
	}

	health := o.GetHealth()
	if health["metrics_count"] == 0 {
		t.Error("observer should show recorded metrics")
	}
}

func TestTaskExecutionEventFlow(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	s := scheduler.NewScheduler(bus)
	d := dispatcher.NewDispatcher(s, bus)
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{MaxMemoryMB: 64, MaxCPUSeconds: 5, MaxOutputMB: 1, MaxConcurrent: 2})
	e := executor.NewExecutor(s, d, sb, bus, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	o := NewObserver(bus)
	if err := o.Start(ctx); err != nil {
		t.Fatalf("start observer: %v", err)
	}

	expectedEvents := map[event.EventType]bool{
		event.EventTaskCreated:   false,
		event.EventTaskStarted:   false,
		event.EventTaskCompleted: false,
		event.EventWorkerSpawned: false,
		// Note: EventWorkerExited only fires on explicit StopWorker,
		// EventPluginExecuted requires sandbox execution path,
		// EventSkillInvoked requires LLM config in skillCtx
	}

	for evtType := range expectedEvents {
		sub := bus.Subscribe(evtType, 10)
		go func(et event.EventType, s *event.Subscriber) {
			for evt := range s.Chan() {
				if evt.Type() == et {
					expectedEvents[et] = true
					return
				}
			}
		}(evtType, sub)
	}

	go e.Run(ctx)

	plugin := sandbox.NewMockPlugin("echo", "1.0", func(ctx context.Context, input []byte, sc skill.SkillContext) ([]byte, error) {
		return []byte("echo-result"), nil
	})
	if err := sb.LoadPlugin("echo", plugin); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	if err := e.StartWorker(ctx, "w-1", "echo"); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	task, err := s.CreateTask(ctx, "echo", nil, []byte("hello"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.QueueTask(ctx, task.ID); err != nil {
		t.Fatalf("queue task: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		allReceived := true
		for _, received := range expectedEvents {
			if !received {
				allReceived = false
				break
			}
		}
		if allReceived {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	for evtType, received := range expectedEvents {
		if !received {
			t.Errorf("event %s never received", evtType)
		}
	}

	metrics := o.GetMetrics()
	if len(metrics) == 0 {
		t.Error("observer should have metrics")
	}

	completedTask, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatal("task should exist")
	}
	if completedTask.State != "completed" {
		t.Errorf("expected completed, got %s", completedTask.State)
	}

	stats := e.GetStats()
	if stats.TotalTasksRun < 1 {
		t.Errorf("expected >=1 task run, got %d", stats.TotalTasksRun)
	}

	_ = e.StopAllWorkers(ctx)
}
func TestObserverAllEventTypes(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := o.Start(ctx); err != nil {
		t.Fatalf("start observer: %v", err)
	}

	allEventTypes := []struct {
		et      event.EventType
		payload interface{}
	}{
		{event.EventTaskCreated, event.TaskCreatedPayload{TaskID: "t1", TaskType: "test"}},
		{event.EventTaskStarted, event.TaskStartedPayload{TaskID: "t1", WorkerID: "w1"}},
		{event.EventTaskCompleted, event.TaskCompletedPayload{TaskID: "t1", WorkerID: "w1", Duration: time.Millisecond}},
		{event.EventTaskFailed, event.TaskFailedPayload{TaskID: "t1", WorkerID: "w1", Error: "test"}},
		{event.EventTaskRetried, nil},
		{event.EventTaskCancelled, nil},
		{event.EventWorkerSpawned, event.WorkerSpawnedPayload{WorkerID: "w1", PluginID: "p1"}},
		{event.EventWorkerExited, event.WorkerExitedPayload{WorkerID: "w1", ExitCode: 0}},
		{event.EventPluginLoaded, event.PluginLoadedPayload{PluginID: "p1", PluginType: "wasm"}},
		{event.EventPluginUnloaded, event.PluginLoadedPayload{PluginID: "p1", PluginType: "wasm"}},
		{event.EventPluginExecuted, event.PluginExecutedPayload{PluginID: "p1", Duration: time.Millisecond, Success: true, InputSize: 1, OutputSize: 1}},
		{event.EventSkillInvoked, event.SkillInvokedPayload{SkillName: "llm.chat", TaskID: "t1", Success: true}},
		{event.EventResearchFinding, event.ResearchFindingPayload{FindingID: "f1", Type: "anomaly", Confidence: 0.9, TaskID: "t1", DataPoints: 10}},
		{event.EventWorkflowStepCompleted, event.WorkflowStepCompletedPayload{WorkflowID: "wf1", StepID: "s1", Duration: time.Millisecond, Success: true}},
		{event.EventWorkflowStarted, event.WorkflowStartedPayload{WorkflowID: "wf1", Status: "running"}},
		{event.EventWorkflowCompleted, event.WorkflowCompletedPayload{WorkflowID: "wf1", StepsTotal: 3, Duration: time.Second}},
		{event.EventWorkflowFailed, event.WorkflowFailedPayload{WorkflowID: "wf1", Error: "test", StepID: "s1", StepsTotal: 3}},
		{event.EventSystemHealth, event.SystemHealthPayload{CPU: 0.5, Memory: 128, Workers: 2, Tasks: 5, QueueSize: 1}},
		{event.EventSystemStarted, nil},
		{event.EventSystemStopped, nil},
	}

	for _, item := range allEventTypes {
		evt := event.NewEvent(item.et, item.payload, nil)
		if err := bus.Publish(ctx, evt); err != nil {
			t.Fatalf("publish %s: %v", item.et, err)
		}
	}

	time.Sleep(300 * time.Millisecond)

	metrics := o.GetMetrics()
	if len(metrics) == 0 {
		t.Error("observer should have recorded metrics from all event types")
	}

	health := o.GetHealth()
	if health["metrics_count"] == 0 {
		t.Error("observer health should show recorded metrics")
	}
}

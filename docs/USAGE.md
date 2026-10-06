# LoopWorker Usage Guide (使用指南)

> Comprehensive guide for Go SDK usage, plugin development, workflow authoring, and API integration.

---

## Table of Contents

1. [Go SDK Quick Start](#1-go-sdk-quick-start)
2. [Creating Tasks Programmatically](#2-creating-tasks-programmatically)
3. [Writing Custom Plugins](#3-writing-custom-plugins)
4. [Building Workflows](#4-building-workflows)
5. [Event Subscription](#5-event-subscription)
6. [Configuration Reference](#6-configuration-reference)
7. [REST API Examples](#7-rest-api-examples)
8. [Error Handling Patterns](#8-error-handling-patterns)
9. [Testing Your Plugins](#9-testing-your-plugins)

---

## 1. Go SDK Quick Start

LoopWorker is a library-first project. You can embed the full engine in your Go application:

```go
package main

import (
    "context"
    "fmt"
    "loopworker/internal/core/sandbox"
    "loopworker/pkg/event"
    "loopworker/pkg/skill"
)

func main() {
    // 1. Create the event bus (system nervous system)
    bus := event.NewEventBus(nil)
    defer bus.Close()

    // 2. Create sandbox with resource limits
    sb := sandbox.NewSandbox(sandbox.SandboxConfig{
        MaxMemoryMB:   256,  // WASM memory limit
        MaxCPUSeconds: 30,   // Per-execution timeout
        MaxOutputMB:   64,   // Max output size
        MaxConcurrent: 10,   // Per-plugin concurrency limit
    })
    sb.SetEventBus(bus)

    // 3. Register a plugin
    plugin := sandbox.NewGoPlugin("echo", "1.0",
        func(ctx context.Context, input []byte, output io.Writer, skillCtx skill.SkillContext) error {
            _, err := output.Write(input)
            return err
        },
    )
    sb.LoadPlugin("echo", plugin)

    // 4. Execute
    result, err := sb.Execute(context.Background(), "echo", []byte("hello"), skill.SkillContext{})
    fmt.Println(string(result)) // "hello"
}
```

## 2. Creating Tasks Programmatically

```go
import (
    "loopworker/internal/core/scheduler"
    "loopworker/pkg/event"
)

// Create scheduler with persistence (SQLite)
bus := event.NewEventBus(nil)
sched := scheduler.NewScheduler(bus)

// Create a task with default priority (Normal)
task, err := sched.CreateTask(ctx, "echo", map[string]interface{}{
    "timeout": "30s",
}, []byte("input data"))

// Create a high-priority task
task, err = sched.CreateTaskWithPriority(ctx, "critical-job",
    scheduler.PriorityHigh, nil, []byte("urgent"))

// Add dependencies (task B waits for task A)
sched.AddDependency(ctx, taskB.ID, taskA.ID)

// Queue for execution
sched.QueueTask(ctx, task.ID)

// Task lifecycle: pending → queued → running → completed/failed/dead_letter
```

## 3. Writing Custom Plugins

### Go Plugin (Native)

```go
// GoPlugin uses stream-based I/O — best for data processing
plugin := sandbox.NewGoPlugin("transform", "1.0",
    func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error {
        // Read input
        data, err := io.ReadAll(input)
        if err != nil {
            return err
        }

        // Process (example: uppercase)
        result := strings.ToUpper(string(data))

        // Write output
        _, err = output.Write([]byte(result))
        return err
    },
)
```

### Mock Plugin (Testing)

```go
// MockPlugin is ideal for unit tests
plugin := sandbox.NewMockPlugin("test-plugin", "1.0",
    func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
        return []byte("mock output"), nil
    },
)

// With skill requirements
plugin := sandbox.NewMockPluginWithSkills("llm-plugin", "1.0",
    []string{"llm.chat"},  // declares required skills
    func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
        // Use skillCtx.Bus, skillCtx.Config, etc.
        return []byte("result"), nil
    },
)
```

### WASM Plugin

```go
// Compile and load a WASM module
wasmBytes, _ := os.ReadFile("plugin.wasm")
wasmPlugin, err := sandbox.NewWasmPlugin(ctx, sb.Runtime(), "my-wasm", "1.0", wasmBytes)
if err != nil {
    log.Fatal(err)
}
sb.LoadPlugin("my-wasm", wasmPlugin)
```

## 4. Building Workflows

### Sequential Workflow (ETL Pipeline)

```go
import "loopworker/pkg/workflow"

engine := workflow.NewWorkflowEngine(workflow.WithEventBus(bus))
wf := workflow.NewWorkflow("etl", "ETL Pipeline")

wf.AddStep(&workflow.Step{
    ID: "extract",
    Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
        data := extractFromDB(ctx)
        return map[string]interface{}{"raw_data": data}, nil
    },
})

wf.AddStep(&workflow.Step{
    ID:        "transform",
    DependsOn: []string{"extract"},
    Timeout:   30 * time.Second,
    RetryPolicy: &workflow.RetryPolicy{
        MaxRetries:  3,
        InitialWait: time.Second,
        MaxWait:     10 * time.Second,
        Multiplier:  2.0,
    },
    Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
        raw := state["raw_data"]
        cleaned := transform(raw)
        return map[string]interface{}{"clean_data": cleaned}, nil
    },
})

wf.AddStep(&workflow.Step{
    ID:        "load",
    DependsOn: []string{"transform"},
    Condition: func(state map[string]interface{}) bool {
        // Skip loading if data is empty
        data, ok := state["clean_data"]
        return ok && data != nil
    },
    Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
        return nil, loadToWarehouse(ctx, state["clean_data"])
    },
})

engine.Register(wf)
err := engine.Execute(ctx, "etl")
```

### Parallel Workflow

```go
pwf := workflow.NewParallelWorkflow("parallel-tasks", "Parallel Processing", 4)

pwf.AddStep(&workflow.Step{
    ID: "task-a",
    Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
        return map[string]interface{}{"a": processA()}, nil
    },
})

pwf.AddStep(&workflow.Step{
    ID: "task-b",
    Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
        return map[string]interface{}{"b": processB()}, nil
    },
})

err := pwf.ExecuteParallel(ctx)
```

## 5. Event Subscription

```go
import "loopworker/pkg/event"

bus := event.NewEventBus(nil)

// Subscribe to specific event types
sub := bus.Subscribe(event.EventTaskCompleted, 100) // buffer size 100
defer bus.Unsubscribe(sub)

// Process events in a goroutine
go func() {
    for evt := range sub.Chan() {
        payload := evt.Payload().(event.TaskCompletedPayload)
        log.Printf("Task %s completed in %v", payload.TaskID, payload.Duration)
    }
}()

// Subscribe to multiple types
for _, eventType := range []event.EventType{
    event.EventTaskCreated,
    event.EventTaskCompleted,
    event.EventTaskFailed,
} {
    sub := bus.Subscribe(eventType, 100)
    go processEvents(sub)
}
```

## 6. Configuration Reference

### JSON Config File

```json
{
  "port": 19527,
  "plugins_dir": "./plugins",
  "data_dir": "./data",
  "log_level": "info",
  "sandbox": {
    "max_memory_mb": 256,
    "max_cpu_seconds": 30,
    "max_output_mb": 64,
    "max_concurrent": 10
  },
  "workers": {
    "count": 4
  }
}
```

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `LOOPWORKER_PORT` | Server port | `19527` |
| `LOOPWORKER_PLUGINS_DIR` | Plugin directory | `./plugins` |
| `LOOPWORKER_DATA_DIR` | Data directory | `./data` |
| `LOOPWORKER_LOG_LEVEL` | Log level (debug/info/warn/error) | `info` |
| `LOOPWORKER_WORKERS` | Worker count | `4` |
| `LOOPWORKER_SANDBOX_MAX_MEMORY` | Max WASM memory (MB) | `256` |
| `LOOPWORKER_SANDBOX_MAX_CPU_SECONDS` | Max CPU seconds | `30` |
| `LOOPWORKER_SANDBOX_MAX_CONCURRENT` | Max concurrent executions | `10` |

## 7. REST API Examples

```bash
# Health check
curl http://localhost:19527/api/v1/health

# Create a task
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{"type":"echo","input":"aGVsbG8=","config":{}}'

# Create an AI agent task
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "type": "agent",
    "input": "Summarize the data",
    "is_agent": true,
    "agent_config": {
      "system_prompt": "You are a data analyst.",
      "model": "gpt-4o"
    }
  }'

# List tasks with filters
curl "http://localhost:19527/api/v1/tasks?state=completed&type=echo&limit=10"

# Get task details
curl http://localhost:19527/api/v1/tasks/<task-id>

# Add task dependency
curl -X POST http://localhost:19527/api/v1/tasks/<task-id>/dependencies \
  -H "Content-Type: application/json" \
  -d '{"dependency_id":"<dep-task-id>"}'

# Cancel a task
curl -X DELETE http://localhost:19527/api/v1/tasks/<task-id>

# List workflows
curl http://localhost:19527/api/v1/workflow/list

# Execute a workflow
curl -X POST http://localhost:19527/api/v1/workflow/execute \
  -H "Content-Type: application/json" \
  -d '{"workflow_id":"my-workflow"}'

# Get metrics (Prometheus format).
# Metrics live on the loopback-only admin listener, not on the API port, and
# they need admin credentials. On the API port this path is a 404.
ADMIN_PORT=19528
API_KEY=your-admin-key
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:$ADMIN_PORT/metrics
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:$ADMIN_PORT/runtime/stats
# Change the port with server.admin_port in the config file, or
# LOOPWORKER_API_ADMIN_PORT in the environment.

# SSE event stream
curl -N http://localhost:19527/api/v1/events/live

# Get workflow DAG graph
curl http://localhost:19527/api/v1/workflow/graph
```

## 8. Error Handling Patterns

LoopWorker uses sentinel errors for all error conditions:

```go
import lwerrors "loopworker/pkg/errors"

task, err := sched.CreateTask(ctx, "test", nil, input)
if err != nil {
    if errors.Is(err, lwerrors.ErrTaskNotFound) {
        // Handle not found
    } else if errors.Is(err, lwerrors.ErrTaskInvalid) {
        // Handle invalid state transition
    }
    return fmt.Errorf("create task: %w", err)
}
```

### Sentinel Error Reference

| Error | Description |
|-------|-------------|
| `ErrTaskNotFound` | Task ID does not exist |
| `ErrTaskInvalid` | Invalid state transition |
| `ErrTaskTimeout` | Execution exceeded timeout |
| `ErrWorkerNotFound` | No worker available |
| `ErrWorkerBusy` | All workers are busy |
| `ErrPluginNotFound` | Plugin not registered |
| `ErrSandboxTimeout` | Plugin execution timeout |
| `ErrSandboxOversized` | Output exceeds size limit |
| `ErrCircuitOpen` | Circuit breaker is open |
| `ErrWorkflowCycle` | Circular dependency detected |
| `ErrRateLimited` | API rate limit exceeded |

## 9. Testing Your Plugins

```go
func TestMyPlugin(t *testing.T) {
    plugin := sandbox.NewMockPlugin("my-plugin", "1.0", myHandler)

    sb := sandbox.NewSandbox(sandbox.SandboxConfig{
        MaxCPUSeconds: 5,
        MaxOutputMB:   1,
    })
    sb.LoadPlugin("my-plugin", plugin)

    // Test success case
    output, err := sb.Execute(context.Background(), "my-plugin",
        []byte("test input"), skill.SkillContext{})
    if err != nil {
        t.Fatalf("execute: %v", err)
    }
    if string(output) != "expected" {
        t.Errorf("expected 'expected', got '%s'", string(output))
    }

    // Test timeout case
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    _, err = sb.Execute(ctx, "slow-plugin", nil, skill.SkillContext{})
    if !errors.Is(err, lwerrors.ErrSandboxTimeout) {
        t.Errorf("expected timeout error, got: %v", err)
    }
}
```

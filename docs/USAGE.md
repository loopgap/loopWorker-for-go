# LoopWorker Usage Guide (使用指南)

> Comprehensive guide for Go SDK usage, plugin development, workflow authoring, and API integration.

> ⚠️ **先读这一段，否则下面的 Go 示例会让你浪费时间。**
> LoopWorker 是**服务 + WASM 插件**，不是一个可以被你的程序 import 的库。
> 本文 §1–§5、§8、§9 的示例导入 `loopworker/internal/...`，Go 工具链会拒绝：
> `use of internal package loopworker/internal/core/sandbox not allowed`。
> 这些包**只在仓库内部可用**，它们是 server 的实现细节。
>
> 模块外唯一能 import 的是 `loopworker/pkg/...`：`pkg/client`（HTTP 客户端）、
> `pkg/event`、`pkg/workflow`、`pkg/errors`、`pkg/skill`。要跑任务就用 REST API
> 或 `pkg/client`（见 §7 与 [API 参考](api/api-reference.md#go-sdk)）。
> 真正的上手路径是 [QUICKSTART.md](QUICKSTART.md)。

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

在**本仓库内部**（`cmd/` 或 `internal/` 下的代码）可以把引擎装配起来。
模块外的程序做不到 —— 下面这些导入路径都在 `internal/` 下：

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

// Task lifecycle:
//   pending → queued → running → completed
//                            ↘ failed
//   任何非终态 → cancelled
//   重试耗尽 → dead_letter（这是状态，不是事件类型）
//
// 8 个合法状态（pkg/api 的 state 查询参数就是这 8 个）：
//   pending, queued, running, completed, failed, cancelled, retrying, dead_letter
```

用持久化存储的版本是 `scheduler.NewSchedulerWithStorage(bus, scheduler.StorageConfig{...})`。

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

`NewWasmPlugin` 收一个配置结构体，不是五个位置参数；它为这个插件**单独建一个
wazero 运行时**，`Close` 释放它（`sb` 没有 `Runtime()` 方法）：

```go
// Compile and load a WASM module
wasmBytes, err := os.ReadFile("plugin.wasm")
if err != nil {
    log.Fatal(err)
}
wasmPlugin, err := sandbox.NewWasmPlugin(ctx, sandbox.WasmPluginConfig{
    Name:    "my-wasm",
    Version: "1.0",
    Wasm:    wasmBytes,
    Limits: sandbox.WasmLimits{
        MemoryMB:      64,
        MaxCPUSeconds: 5,
        MaxOutputMB:   1,
        AllowedHosts:  []string{"api.example.com"},
    },
})
if err != nil {
    log.Fatal(err)
}
defer wasmPlugin.Close(ctx)
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

err := pwf.ExecuteParallel(ctx)   // 返回 error，不是 (*Workflow, error)
```

`maxConcurrency` 参数是**真的**并行度上限（信号量）。
与之相对，配置键 `workflow.max_concurrent` 与 `workflow.timeout` 被
`internal/config` 接受并校验，但**本版本不强制执行** —— 启动自检会把它列进
`unapplied_keys`，不要以为配了就生效。

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
    defer bus.Unsubscribe(sub)
    go processEvents(sub)
}
```

`pkg/event` 共定义 **20 种**事件类型（`event.go` 的常量表）；其中 13 种是
SSE 流的白名单，另 7 种（`plugin.loaded` / `plugin.unloaded` /
`worker.spawned` / `worker.exited` / `system.health` / `system.started` /
`system.stopped`）只进 observer，不能通过 `?types=` 订阅。

**类型断言会 panic。** `evt.Payload()` 是 `interface{}`，失败任务的负载是
`event.TaskFailedPayload` 而不是 `TaskCompletedPayload`。用
`payload, ok := evt.Payload().(event.TaskCompletedPayload)` 断言。

Payload 结构体**没有 json tag**，直接序列化时字段名是 Go 的大写形式 ——
这就是 SSE 帧里 `TaskID` / `TaskType` 长那样的原因（`data.task` 里才是小写的
任务视图）。

## 6. Configuration Reference

### Config file

The schema is **nested**. These are the real key paths (`config/config.example.yaml`
is the full example; `internal/config` is the authority):

```json
{
  "server": {
    "port": 19527,
    "admin_port": 19528
  },
  "plugins": {
    "dir": "~/.loopworker/plugins"
  },
  "data": {
    "dir": "~/.loopworker/data"
  },
  "logging": {
    "level": "info"
  },
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

Two spellings of the same settings are accepted. The nested form above is the
canonical one, and a top-level `port`, `plugins_dir`, `data_dir` or `log_level`
still works as a legacy alias — an earlier revision of this guide showed only
those flat keys, so they were kept rather than breaking configs that were
already written that way. Setting both spellings of one value is an error, not
a silent choice of one over the other.

A key the loader does not recognise is a **startup failure**, not a warning. The
error names the file, the offending key, the closest match it did recognise, and
the full list of accepted keys:

```
unknown configuration key "loging.level" in /etc/loopworker/config.yaml (did you mean logging.level?)
  accepted keys: work_dir, server.host, server.port, port, ...
```

So a setting you wrote either took effect or stopped the server from starting;
there is no third case where a typo quietly leaves the default in place.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `LOOPWORKER_PORT` / `LOOPWORKER_SERVER_PORT` | Server port | `19527` |
| `LOOPWORKER_SERVER_HOST` | Bind address | `127.0.0.1` |
| `LOOPWORKER_API_ADMIN_PORT` | Admin listener port（固定绑 127.0.0.1） | `19528` |
| `LOOPWORKER_PLUGINS_DIR` | Plugin directory | `~/.loopworker/plugins` |
| `LOOPWORKER_DATA_DIR` | Data directory | `~/.loopworker/data` |
| `LOOPWORKER_LOG_LEVEL` | Log level (debug/info/warn/error) | `info` |
| `LOOPWORKER_WORKERS` / `LOOPWORKER_WORKERS_COUNT` | Worker count | `4` |
| `LOOPWORKER_SANDBOX_MAX_MEMORY` / `..._MAX_MEMORY_MB` | Max WASM memory (MB) | `256` |
| `LOOPWORKER_SANDBOX_MAX_CPU_SECONDS` | Max CPU seconds | `30` |
| `LOOPWORKER_SANDBOX_MAX_OUTPUT_MB` | Max output (MB) | `64` |
| `LOOPWORKER_SANDBOX_MAX_CONCURRENT` | Max concurrent executions | `10` |
| `LOOPWORKER_PLUGINS_VERIFY_CHECKSUM` | Verify each artifact against its manifest digest | `false` |
| `LOOPWORKER_API_KEYS` | Register credentials (`id:role:sha256hex`) | none |
| `LOOPWORKER_API_KEY` | The key the CLIs send (`pkg/client`) | none |
| `LOOPWORKER_LLM_API_KEY` / `OPENAI_API_KEY` | LLM key | none |

**Defaults for `plugins.dir` / `data.dir` are under `~/.loopworker`, not `./`.**
`LOOPWORKER_PLUGINS_DIR` 这个名字没有隐含任何工作目录。

### Admin listener

Metrics, logs, runtime statistics and the lifecycle endpoints are served on a
separate listener bound to `127.0.0.1:19528` by default, and every route on it
requires an administrator credential:

| Route | Method | Purpose |
|---|---|---|
| `/metrics` | GET | Prometheus exposition |
| `/runtime/stats` | GET | Scheduler and stream counters |
| `/logs` | GET | Observer's in-memory ring; nothing feeds it in production, so it answers `{"logs":[]}` |
| `/events/stats` | GET | Event bus counters |
| `/statusz` | GET | Process status: version, uptime, component state |
| `/shutdown` | POST | Graceful shutdown; responds before draining |

None of these are on the public listener. `/healthz` is the only
unauthenticated probe there.

## 7. REST API Examples

**每个示例都要凭据。** 下面把 `API_KEY` 展开只是为了可读；不加
`-H "X-API-Key: $API_KEY"` 的调用会得到 `401`（实测：零配置服务上无凭据
`POST /api/v1/tasks` → 401）。只有 `GET /healthz`、`GET /api/v1/health`、
`GET /api/v1/openapi.json` 匿名可用。

```bash
API_KEY=<your-key>

# Health check (anonymous)
curl http://localhost:19527/api/v1/health

# Create a task. Without input_encoding, "input" is literal text;
# for base64 payloads use input_b64 or input_encoding:"base64".
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{"type":"echo","input":"hello","config":{}}'

# Create an AI agent task (needs llm.api_key or agent tasks fail at runtime)
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
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
curl -H "X-API-Key: $API_KEY" \
  "http://localhost:19527/api/v1/tasks?state=completed&type=echo&limit=10"

# Get task details
curl -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/tasks/<task-id>

# Add task dependency
curl -X POST http://localhost:19527/api/v1/tasks/<task-id>/dependencies \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{"dependency_id":"<dep-task-id>"}'

# Cancel a task (POST .../cancel). NOTE: DELETE on the same path is the SAME
# handler — it sets state to "cancelled" too, it does not delete the row.
curl -X POST -H "X-API-Key: $API_KEY" \
  http://localhost:19527/api/v1/tasks/<task-id>/cancel

# List workflows
curl -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/workflow/list

# Execute a workflow (202 Accepted; poll the path in the "poll" field)
curl -X POST http://localhost:19527/api/v1/workflow/execute \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{"workflow_id":"builtin.anomaly-review"}'

# Get metrics (Prometheus format).
# Metrics live on the loopback-only admin listener, not on the API port, and
# they need admin credentials. On the API port this path is a 404.
ADMIN_PORT=19528
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:$ADMIN_PORT/metrics
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:$ADMIN_PORT/runtime/stats
# Change the port with server.admin_port in the config file, or
# LOOPWORKER_API_ADMIN_PORT in the environment.

# SSE event stream
curl -N -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/events/live

# Get workflow DAG graph
curl -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/workflow/graph
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
    if err := sb.LoadPlugin("my-plugin", plugin); err != nil {
        t.Fatalf("load plugin: %v", err)
    }

    // Test success case
    output, err := sb.Execute(context.Background(), "my-plugin",
        []byte("test input"), skill.SkillContext{})
    if err != nil {
        t.Fatalf("execute: %v", err)
    }
    if string(output) != "expected" {
        t.Errorf("expected 'expected', got '%s'", string(output))
    }

    // Test timeout case. "slow-plugin" must be LOADED first — executing an
    // unregistered name returns ErrPluginNotFound, not a timeout.
    slow := sandbox.NewMockPlugin("slow-plugin", "1.0",
        func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
            select {
            case <-time.After(10 * time.Second):
                return []byte("done"), nil
            case <-ctx.Done():
                return nil, lwerrors.ErrSandboxTimeout
            }
        })
    if err := sb.LoadPlugin("slow-plugin", slow); err != nil {
        t.Fatalf("load slow-plugin: %v", err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    _, err = sb.Execute(ctx, "slow-plugin", nil, skill.SkillContext{})
    if !errors.Is(err, lwerrors.ErrSandboxTimeout) {
        t.Errorf("expected timeout error, got: %v", err)
    }
}
```

（`internal/core/sandbox` 对模块外不可见，所以这个测试只能在仓库内跑。）

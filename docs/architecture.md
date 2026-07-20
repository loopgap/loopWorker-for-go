# LoopWorker 平台架构深度分析

> 基于源代码逐层解构的工业级 WASM 工作流引擎架构探索

---

## 目录

1. [项目全景](#1-项目全景)
2. [核心架构原则](#2-核心架构原则)
3. [数据流主线：任务的生命周期](#3-数据流主线任务的生命周期)
4. [Event Bus — 系统的神经系统](#4-event-bus--系统的神经系统)
5. [Scheduler — 优先级调度与 DAG 依赖管理](#5-scheduler--优先级调度与-dag-依赖管理)
6. [Dispatcher — 任务分发与负载均衡](#6-dispatcher--任务分发与负载均衡)
7. [Executor — Worker 池与执行引擎](#7-executor--worker-池与执行引擎)
8. [Sandbox — WASM 隔离执行层](#8-sandbox--wasm-隔离执行层)
9. [Workflow Engine — 多范式工作流编排](#9-workflow-engine--多范式工作流编排)
10. [SelfHeal — 自愈与弹性机制](#10-selfheal--自愈与弹性机制)
11. [Security — 安全防护体系](#11-security--安全防护体系)
12. [Observer — 可观测性体系](#12-observer--可观测性体系)
13. [Debugger — 调试与诊断](#13-debugger--调试与诊断)
14. [Plugin & Skill — 可扩展能力层](#14-plugin--skill--可扩展能力层)
15. [AI & Research — 智能分析层](#15-ai--research--智能分析层)
16. [Code Generator — 脚手架生成](#16-code-generator--脚手架生成)
17. [UI & Theme — 多主题系统](#17-ui--theme--多主题系统)
18. [API 层 — REST 接口与前端](#18-api-层--rest-接口与前端)
19. [架构模式总结](#19-架构模式总结)
20. [关键设计决策评析](#20-关键设计决策评析)

---

## 1. 项目全景

### 1.1 基本信息

| 指标 | 数值 |
|------|------|
| 语言 | Go 1.26.1 |
| 总代码量 | ~15,736 行 |
| 源文件数 | 54 个 `.go` 文件 |
| 测试文件数 | 25 个 `*_test.go` |
| 核心模块代码量 | ~4,011 行（9 个核心文件） |
| 默认端口 | 19527 |
| WASM 运行时 | wazero (纯 Go 实现，无需 CGO) |
| 持久化 | SQLite (GORM) |
| 前端 | React + ReactFlow (Liquid Glass 主题) |

### 1.2 目录结构

```
loopWorker-for-go/
├── cmd/loopworker/              # CLI 入口 (cobra)
│   ├── main.go                  # 主入口，配置初始化
│   └── main_test.go
├── pkg/                         # 公共包（可独立使用）
│   ├── event/                   # 事件系统 (EventBus + EventStore)
│   ├── workflow/                # 工作流引擎 (732 LOC)
│   ├── plugin/                  # 插件管理
│   ├── skill/                   # 技能注册与上下文
│   ├── security/                # 安全 (RBAC + 限流)
│   ├── api/                     # REST API (chi router)
│   ├── config/                  # 配置管理 (JSON + Env)
│   ├── dashboard/               # Web 仪表板 (SSE 推送)
│   ├── debugger/                # 调试工具
│   ├── research/                # 异常检测
│   ├── server/                  # Server 组装（上帝对象）
│   ├── service/                 # 外部进程管理
│   └── ui/                      # 主题系统
├── internal/                    # 内部核心包
│   ├── core/
│   │   ├── scheduler/           # 调度器 (641 LOC，最重模块)
│   │   ├── dispatcher/          # 分发器 (180 LOC)
│   │   ├── executor/            # 执行器 (495 LOC)
│   │   ├── sandbox/             # 沙箱 (479 LOC)
│   │   ├── observer/            # 可观测性 (563 LOC)
│   │   ├── selfheal/            # 自愈 (410 LOC)
│   │   └── config/              # 内部配置
├── web/canvas/                  # React 前端
│   └── src/
│       ├── App.jsx              # 主应用
│       ├── SkillNode.jsx        # 技能节点可视化
│       └── ...
├── examples/                    # 示例
│   ├── workflow/main.go         # 工作流使用示例
│   └── wasm/                    # Rust WASM 代理示例
├── integration/                 # 集成测试
└── docs/                        # 文档
    └── architecture.md          # 架构设计文档（已有）
```

### 1.3 核心数据概览

代码行数分布（核心文件）：

| 模块 | 文件 | 行数 | 职责权重 |
|------|------|------|----------|
| `workflow` | `workflow.go` | 732 | 最复杂，多范式编排 |
| `scheduler` | `scheduler.go` | 641 | 状态机 + 持久化 |
| `observer` | `observer.go` | 563 | 指标 + 追踪 + 日志 |
| `sandbox` | `sandbox.go` | 479 | WASM 隔离 |
| `executor` | `executor.go` | 495 | Worker 池 |
| `selfheal` | `selfheal.go` | 410 | 熔断 + 恢复 |
| `event` | `bus.go` | 293 | 事件总线 |
| `security` | `security.go` | 218 | RBAC + 认证 |
| `dispatcher` | `dispatcher.go` | 180 | 分发逻辑 |

---

## 2. 核心架构原则

### 2.1 事件驱动一切（Event-Driven Everything）

整个平台围绕一个核心的 **EventBus** 构建。几乎所有的模块间通信都通过事件完成，而非直接调用。这带来三个关键优势：

1. **松耦合**：模块间无硬依赖，通过订阅事件类型进行通信
2. **可观测性内建**：Observer 只需订阅所有事件类型即可自动采集指标
3. **可扩展性**：新模块只需订阅感兴趣的事件即可接入

### 2.2 分层架构

```
┌──────────────────────────────────────────────────────────────────┐
│                     Interface Layer                              │
│   TUI (bubbletea) │ Web Dashboard │ REST API │ CLI (cobra)       │
├──────────────────────────────────────────────────────────────────┤
│                     Core Engine                                  │
│   Scheduler → Dispatcher → Executor → Sandbox                   │
│        ↕ (EventBus)                                              │
│   WorkflowEngine ──→ SchedulerBridge                            │
├──────────────────────────────────────────────────────────────────┤
│                     Infrastructure Layer                         │
│   SelfHeal │ Observer │ Security │ Service │ Debugger │ Skill   │
├──────────────────────────────────────────────────────────────────┤
│                     Storage Layer                                │
│   EventStore (SQLite) │ Config (JSON) │ State (In-Memory)       │
└──────────────────────────────────────────────────────────────────┘
```

### 2.3 组合优于继承

整个代码库几乎没有使用继承。所有扩展点都通过：
- **接口**（如 `Plugin`, `SkillProvider`, `Detector`）
- **函数选项模式**（如 `WorkflowEngineOption`, `PluginManagerOption`）
- **回调/闭包**（如 `Step.Action`, `OnFailure`）

### 2.4 并发安全优先

每个共享状态的结构体都明确标注了其并发安全策略：

| 策略 | 使用位置 | 方式 |
|------|----------|------|
| `sync.RWMutex` | Scheduler, Executor, Observer, PluginManager | 读写分离锁 |
| `atomic` 操作 | ExecutorStats, Worker state/counters | 无锁原子操作 |
| Channel | Worker.taskCh, EventBus subscriber channels | 消息传递 |
| `sync.Mutex` | Dashboard throttle, Observer logs | 互斥锁 |

---

## 3. 数据流主线：任务的生命周期

理解 LoopWorker 架构的最佳方式是追踪一个任务从创建到完成的完整生命周期。

### 3.1 完整生命周期图

```
                    ┌─────────────────────────────────────────────────────────┐
                    │                    Event Bus                            │
                    │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────┐ │
                    │  │Observer  │  │Dashboard │  │SelfHeal  │  │ ...    │ │
                    │  └──────────┘  └──────────┘  └──────────┘  └────────┘ │
                    └────────────────────────┬────────────────────────────────┘
                                             │ Publish/Subscribe
    ┌────────┐    ┌────────┐    ┌────────┐   │    ┌────────────────────┐
    │  User  │───▶│  API   │───▶│Schedu- │   │    │    Executor        │
    │Request │    │ Server │    │  ler   │   │    │ ┌────────────────┐ │
    └────────┘    └────────┘    │(Create │   │    │ │  Worker Pool   │ │
                                  │ Task)  │   │    │ │ (Go routines)  │ │
                                  └───┬────┘   │    │ │ + Watchdog     │ │
                                      │        │    │ └───────┬────────┘ │
                                      │ Queue  │    │         │          │
                                      │ Task   │    │    ┌────┴────────┐ │
                                      ▼        │    │    │   Sandbox   │ │
                               ┌──────────┐   │    │    │ (WASM/Go)   │ │
                               │Dispatcher│◀──│────│────│              │ │
                               │(Dispatch)│   │    │    └──────────────┘ │
                               └────┬─────┘   │    └────────────────────┘
                                    │ Send to  │
                                    │ Worker   │
                                    ▼          │
                            ┌──────────────┐  │
                            │    Worker    │──┘ (Complete/Fail → Event)
                            │  (taskCh)    │
                            └──────────────┘
```

### 3.2 阶段详解

#### 阶段 1：任务创建

```go
// scheduler.go:246
func (s *Scheduler) CreateTask(ctx, taskType, config, input) (*Task, error)
```

- 生成唯一 ID，初始状态 `Pending`
- 持久化到 SQLite（通过 GORM）
- 发布 `EventTaskCreated` 事件到 EventBus
- Observer 收到后递增 `tasks_created` Counter

#### 阶段 2：入队与优先级调度

```go
// scheduler.go:316
func (s *Scheduler) QueueTask(ctx, taskID) error
```

- 检查依赖是否全部完成（DAG 依赖检查）
- 状态流转：`Pending` → `Queued`
- 插入优先级堆（`container/heap`，四级优先级）
- 通过 `taskNotifyCh` 通知 Executor 有新任务

#### 阶段 3：分发

```go
// dispatcher.go:92
func (d *Dispatcher) Dispatch(ctx) (*Task, *WorkerInfo, error)
```

- Executor 的 `Run` 循环监听 `NotifyCh()` 和 `workerFree` 通道
- 调用 `Dispatcher.Dispatch()` 获取下一个任务 + 最优 Worker
- Dispatcher 使用原子性查找-预留模式避免竞态

#### 阶段 4：执行

```go
// executor.go:206
func (e *Executor) workerLoop(ctx, worker)
```

Worker 通过 `worker.taskCh` 接收任务后：

**Agent 模式**（AI 任务）：
- 收集上游依赖结果
- 构建上下文（`ContextBuilder`，16K token 窗口）
- 通过 LLM Client 调用大模型（带 Circuit Breaker 保护）
- 超时 30 秒

**插件模式**（普通任务）：
- 通过 `SelfHealer.ExecuteWithRecovery` 包裹执行
- Sandbox 中加载并执行插件（WASM 或 Go）
- 注入 `SkillContext`（含 EventBus、LLM Client 等能力）

#### 阶段 5：完成或失败

**成功**：
- `Scheduler.CompleteTask` → 状态 `Completed`
- 发布 `EventTaskCompleted`
- 触发 `unblockDependents` → 自动解锁下游依赖任务

**失败**：
- 检查重试次数（默认最多 3 次）
- 未超限：状态回 `Queued`，重新入堆，发布 `EventTaskRetried`
- 已超限：状态 → `DeadLetter`（死信队列），发布 `EventTaskFailed`

---

## 4. Event Bus — 系统的神经系统

### 4.1 核心设计

```go
// pkg/event/bus.go:19
type EventBus struct {
    subscribers  map[EventType][]*Subscriber  // 按类型索引的订阅者
    allSubs      []*Subscriber                // 全部订阅者（用于 Close）
    store        EventStore                   // 可选的持久化层
    mu           sync.RWMutex
    nextID       int
    stats        *BusStats
}
```

### 4.2 事件类型体系

平台定义了 **21 种事件类型**，覆盖完整的系统生命周期：

**任务生命周期**（7 种）：
- `task.created` → `task.started` → `task.completed`
- `task.failed` → `task.retried` → `task.cancelled`

**Worker 生命周期**（2 种）：
- `worker.spawned` → `worker.exited`

**插件生命周期**（3 种）：
- `plugin.loaded` → `plugin.executed` → `plugin.unloaded`

**工作流生命周期**（5 种）：
- `workflow.started` → `workflow.step.completed` → `workflow.completed`
- `workflow.failed`

**技能与研究**（2 种）：
- `skill.invoked` → `research.finding`

**系统事件**（2 种）：
- `system.health` → `system.started` / `system.stopped`

### 4.3 订阅模式

支持两种订阅模式：

```go
// 异步订阅（默认，非阻塞）
eb.Subscribe(eventType, bufferSize)       // 满缓冲时丢弃

// 同步订阅（阻塞，确保处理）
eb.SubscribeSync(eventType)               // 单缓冲，发送方阻塞
```

### 4.4 事件持久化

EventBus 通过 `EventStore` 接口支持事件持久化。该接口是一个完整的 Event Sourcing 基础：

```go
// pkg/event/store.go:21
type EventStore interface {
    Append(ctx, event) error                          // 追加事件
    Load(ctx, EventFilter) ([]Event, error)           // 按条件查询
    Snapshot(ctx, stateID string, state []byte) error // 快照
    LoadSnapshot(ctx, stateID string) ([]byte, error) // 加载快照
    Replay(ctx, from, to time.Time) ([]Event, error)  // 时间范围重放
}
```

`LocalEventStore` 是文件系统实现：
- 每个事件写入独立 JSON 文件（`events/{timestamp}_{id[:8]}.json`）
- 启动时通过 `loadIndex` 加载索引（按修改时间排序，支持增量恢复）
- `Snapshot` 将状态快照写入 `snapshots/` 目录
- `LoadSnapshot` 返回最新的快照
- `Replay` 支持按时间范围重放事件流

`EventFilter` 支持按类型、时间范围、数量限制过滤。

### 4.5 批量发布优化

```go
// bus.go:168
func (eb *EventBus) PublishBatch(ctx, events) error
```

`PublishBatch` 按事件类型分组，每种类型只获取一次读锁，大幅减少锁竞争。这是对高吞吐场景的性能优化。

---

## 5. Scheduler — 优先级调度与 DAG 依赖管理

### 5.1 状态机

```
                  ┌──────────┐
                  │ Pending  │
                  └────┬─────┘
                       │ QueueTask
                       ▼
                  ┌──────────┐
                  │  Queued  │ ←── 优先级堆中
                  └────┬─────┘
                       │ StartTask
                       ▼
                ┌─────────────┐
           ┌───▶│   Running   │───┐
           │    └──────┬──────┘   │
           │           │          │ CompleteTask
           │           ▼          │
           │    ┌─────────────┐   │
           │    │  Completed  │◀──┘
           │    └─────────────┘
           │         │ FailTask
           │         ▼
           │  ┌─────────────────┐
           │  │    DeadLetter   │（重试耗尽）
           │  └─────────────────┘
           │         │ Retry (<= MaxRetry)
           └─────────▶ Queued
                       │ CancelTask
                       ▼
                  ┌─────────────┐
                  │  Cancelled  │
                  └─────────────┘
```

### 5.2 优先级堆实现

```go
// scheduler.go:129
type taskQueue struct {
    data []*Task
}
// 实现 container/heap.Interface
```

四级优先级：`Low < Normal < High < Critical`。任务入队时通过 `heap.Push` 按优先级排序，高优先级任务总是先被取出。

### 5.3 DAG 依赖管理

```go
// scheduler.go:431
func (s *Scheduler) unblockDependents(ctx, completedTaskID)
```

当任务完成时，自动扫描所有 `Pending` 状态的任务，检查其 `DependsOn` 集合。如果某个任务的所有依赖都已完成，自动将其状态推进到 `Queued` 并插入优先级堆。

**关键设计**：`Dependencies` 字段保留原始依赖列表（数据血缘），`DependsOn` map 用于运行时依赖检查。两者分离，兼顾了可追溯性和执行效率。

### 5.4 崩溃恢复

```go
// scheduler.go:157
func (s *Scheduler) recoverTasks()
```

启动时从 SQLite 恢复所有 `Queued` 和 `Running` 状态的任务。正在运行的任务被重置为 `Queued`，确保服务重启后不会丢失任务。

### 5.5 SQLite 持久化

所有任务状态变更都通过 `saveTask` 同步写入 SQLite。使用 GORM ORM，`TaskModel` 作为持久化模型，通过 JSON 序列化存储 `Config`、`Metadata`、`Dependencies` 等 map/struct 字段。

---

## 6. Dispatcher — 任务分发与负载均衡

### 6.1 职责边界

Dispatcher 是 Scheduler 和 Executor 之间的桥梁，负责：
1. Worker 注册/注销
2. 任务分发给最优 Worker
3. 任务完成/失败的回传

### 6.2 原子性查找-预留模式

```go
// dispatcher.go:92
func (d *Dispatcher) Dispatch(ctx) (*Task, *WorkerInfo, error)
```

Dispatcher 在单次锁内完成：
1. 从 Scheduler 取出下一个任务
2. 查找最优 Worker
3. 预留 Worker（标记为 busy）

这避免了经典的"检查-执行"竞态条件。

---

## 7. Executor — Worker 池与执行引擎

### 7.1 Worker 模型

```go
// executor.go:26
type Worker struct {
    ID          string
    PluginID    string
    state       int32          // atomic: idle/busy/stopped
    tasksRun    int64          // atomic
    tasksFailed int64          // atomic
    totalTime   time.Duration
    stopCh      chan struct{}   // 关闭信号
    doneCh      chan struct{}   // 退出确认
    taskCh      chan *Task      // 任务接收通道
    lastActive  time.Time
    currentTask string
}
```

每个 Worker 是一个独立的 goroutine，通过 `taskCh` 接收任务。这种设计使得：
- Worker 之间完全隔离
- 任务投递通过 channel 完成（Go 风格的并发通信）
- Worker 状态通过 atomic 操作更新（无锁）

### 7.2 中央调度循环

```go
// executor.go:363
func (e *Executor) Run(ctx) error
```

这是 Executor 的核心事件循环：

```go
for {
    select {
    case <-ctx.Done():
        return e.StopAllWorkers(ctx)
    case <-notifyCh:          // Scheduler 有新任务
        e.pumpQueue(ctx)
    case <-e.workerFree:      // Worker 变为空闲
        e.pumpQueue(ctx)
    }
}
```

事件驱动，非轮询。只有当有新任务或有 Worker 空闲时才尝试分发。

### 7.3 Agent 模式

Executor 支持两种执行模式：

**Agent 模式**（`task.IsAgent == true`）：
- 收集上游依赖任务的结果
- 构建 LLM 上下文（16K token 窗口）
- 调用大模型生成结构化响应
- 通过 Circuit Breaker 保护 LLM 调用

**插件模式**（默认）：
- 通过 `SelfHealer.ExecuteWithRecovery` 包裹执行
- Sandbox 中加载并执行插件（WASM 或 Go）
- 注入 `SkillContext`（含 EventBus、LLM 等能力）

### 7.4 Watchdog 与僵尸检测

```go
// executor.go:452
func (e *Executor) StartWatchdog(ctx)
func (e *Executor) sweepZombies(ctx)
```

每 10 秒检查一次：如果 Worker 处于 busy 状态但 `lastActive` 超过 60 秒未更新，判定为僵尸 Worker。处理流程：
1. 强制终止僵尸 Worker
2. 将其当前任务标记为失败（zombie 原因）
3. 自动重生一个新 Worker 保持池大小

---

## 8. Sandbox — WASM 隔离执行层

### 8.1 插件接口

```go
// sandbox.go:22
type Plugin interface {
    Name() string
    Version() string
    RequiredSkills() []string
    Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)
}
```

### 8.2 三种插件实现

| 类型 | 说明 | 使用场景 |
|------|------|----------|
| `WasmPlugin` | 基于 wazero 的 WASI 运行时 | 外部插件，强隔离 |
| `GoPlugin` | 原生 Go 函数 | 内置插件，高性能 |
| `MockPlugin` | 内存模拟 | 测试 |

### 8.3 资源限制

```go
type SandboxConfig struct {
    MaxMemoryMB    int  // 256 MB
    MaxCPUSeconds  int  // 30 秒
    MaxOutputMB    int  // 64 MB
    MaxConcurrent  int  // 10 并发
    AllowedHosts   []string
}
```

### 8.4 WASM 执行流程

```go
// sandbox.go:388
func (p *WasmPlugin) Execute(ctx, input, skillCtx) ([]byte, error)
```

1. 创建 WASI 模块配置（stdin/stdout/stderr）
2. 实例化模块（wazero runtime）
3. 模块 `_start` 函数自动执行
4. 捕获 stdout 作为输出
5. 模块关闭，资源释放

Rust WASM 插件示例（`examples/wasm/rust_agent/`）展示了如何通过 Host Function 从 WASM 内部调用 Go 侧的 HTTP 请求和日志功能。

---

## 9. Workflow Engine — 多范式工作流编排

### 9.1 三种工作流模式

```go
// workflow.go:53
type Step struct {
    ID          string
    Name        string
    Action      func(ctx, state) (state, error)  // 执行逻辑
    RetryPolicy *RetryPolicy                      // 重试策略
    Timeout     time.Duration                     // 超时
    DependsOn   []string                          // 依赖步骤
    Condition   func(state) bool                  // 条件执行
    OnFailure   func(ctx, err) error              // 失败处理
}
```

**Sequential（顺序）**：由 `WorkflowEngine` 执行，使用 Kahn 拓扑排序确定步骤顺序。每个步骤按序执行，依赖检查通过则执行，否则跳过（`StepSkipped`）。

**DAG（有向无环图）**：`DAGWorkflow` 类型提供显式图构建能力：
```go
type DAGWorkflow struct {
    *Workflow
    adjacency map[string][]string  // 邻接表
}
func (d *DAGWorkflow) AddEdge(from, to string)
func (d *DAGWorkflow) TopologicalSort() ([]string, error)
```
`TopologicalSort` 独立实现 Kahn 算法，检测循环依赖。

**Parallel（并行）**：`ParallelWorkflow` 类型支持并发执行：
```go
type ParallelWorkflow struct {
    *Workflow
    maxConcurrency int  // 默认 4
}
func (pw *ParallelWorkflow) ExecuteParallel(ctx) error
```
使用信号量（`sem := make(chan struct{}, maxConcurrency)`）控制并发度。依赖就绪的步骤自动启动，所有步骤完成后返回。通过 `launchReady` 循环确保依赖满足后才执行。

### 9.2 与调度器的桥接

WorkflowEngine 通过 `TaskDispatcher` 接口（`workflow.go:95`）与调度器解耦：

```go
type TaskDispatcher interface {
    CreateTask(ctx, taskType, config, input) (*TaskRef, error)
    WaitForTask(ctx, taskID) (*TaskRef, error)    // 轮询直到终态
    GetTask(taskID) (*TaskRef, bool)
}
```

`SchedulerBridge`（`scheduler/bridge.go`）实现此接口：
- `CreateTask` → 调用 `Scheduler.CreateTask`，返回 `TaskRef`（ID/Type/State/Result 的轻量视图）
- `WaitForTask` → 每 50ms 轮询一次 `Scheduler.GetTask`，直到任务进入终态（completed/failed/cancelled/dead_letter）
- `GetTask` → 直接查询 Scheduler 返回当前状态

这样工作流步骤能享受调度器的所有能力：优先级、重试、依赖管理、持久化。

### 9.3 状态传递

工作流步骤之间通过共享的 `state map[string]interface{}` 传递数据。每个步骤的 `Action` 函数接收当前状态，返回更新后的状态。这是函数式编程中 State Monad 的简化实现。

---

## 10. SelfHeal — 自愈与弹性机制

### 10.1 熔断器

```go
// selfheal.go:71
type CircuitBreaker struct {
    name             string
    failureCount     int
    failureThreshold int           // 连续失败 5 次触发熔断
    successCount     int
    successThreshold int           // 半开状态下连续成功恢复
    state            CircuitState  // Closed / Open / HalfOpen
    lastFailure      time.Time
    timeout          time.Duration // 30 秒后自动切换到 HalfOpen
    mu               sync.RWMutex
}
```

状态流转：`Closed`（正常）→ 连续失败 5 次 → `Open`（熔断，拒绝所有请求）→ 30 秒后 → `HalfOpen`（试探性放行一个请求）→ 成功则 `Closed`，失败则 `Open`。

每个组件（如 "llm"、"plugin-xxx"）有独立的熔断器实例，通过 `GetCircuitBreaker(name)` 懒初始化。

### 10.2 执行恢复

```go
func (sh *SelfHealer) ExecuteWithRecovery(ctx, pluginID, fn) error
```

包装任意执行函数，自动处理：
- 执行失败时的重试（指数退避：1s → 2s → 4s → ... → 1min 上限）
- 每次失败记录 `Incident`（含组件名、严重级别、时间戳）
- 重试耗尽后返回错误，同时记录 `RecoveryEvent`

### 10.3 健康状态计算

```go
func (sh *SelfHealer) GetHealthStatus() HealthStatus
```

基于最近 5 分钟内的事故严重级别计算：
- 无事故 → `Healthy`
- 有 Critical 级事故 → `Critical`
- 事故数 > 3 → `Unhealthy`
- 其他 → `Degraded`

支持 `RegisterHealthCheck` 注册自定义健康检查函数，定期运行。

### 10.4 与 Executor 的集成

Executor 在 `workerLoop` 中使用 `SelfHealer` 包裹插件执行：
```go
execErr = e.selfHealer.ExecuteWithRecovery(ctx, worker.PluginID, func(innerCtx) error {
    output, err = e.sandbox.Execute(innerCtx, worker.PluginID, task.Input, skillCtx)
    return err
})
```

同时，LLM 调用使用独立的 Circuit Breaker 保护，避免外部 API 故障级联影响整个系统。

---

## 11. Security — 安全防护体系

### 11.1 多层安全模型

```
┌─────────────────────────────────────────┐
│  Layer 1: Rate Limiting (100 req/min)  │ ← 防暴力请求
├─────────────────────────────────────────┤
│  Layer 2: CORS + Request ID            │ ← 跨域 + 请求追踪
├─────────────────────────────────────────┤
│  Layer 3: Authentication (Token)        │ ← 身份认证
├─────────────────────────────────────────┤
│  Layer 4: Authorization (RBAC)          │ ← 权限控制
├─────────────────────────────────────────┤
│  Layer 5: Input Validation              │ ← 输入校验
├─────────────────────────────────────────┤
│  Layer 6: Account Lockout (5 failures)  │ ← 防暴力破解
├─────────────────────────────────────────┤
│  Layer 7: Audit Logging                 │ ← 全操作审计
└─────────────────────────────────────────┘
```

### 11.2 防护组件

**RateLimiter**（滑动窗口，100 请求/分钟/IP）：
```go
type RateLimiter struct {
    visitors map[string]*Visitor  // IP → 访问计数
    rate     int                   // 窗口内最大请求数
    window   time.Duration         // 窗口大小
}
```
后台每 1 分钟清理一次超过 `2×window` 未活跃的 IP。满窗口时非阻塞丢弃。

**AccountLocker**（防暴力破解）：
- 5 次失败尝试 → 锁定 15 分钟
- 成功后自动清除记录
- `IsLocked` 检查时自动过期清除

**InputValidator**（字段级校验）：
- Required / MinLength / MaxLength / Pattern / CustomFunc
- 返回 `ValidationError{Field, Message}`

### 11.3 RBAC 角色

| 角色 | 权限 |
|------|------|
| `admin` | read + write + execute + admin |
| `operator` | read + write + execute |
| `viewer` | read only |

### 11.4 认证流程

```go
// security.go:115
func (sm *SecurityManager) Authenticate(username, password) (*Token, error)
```

1. 查找用户
2. SHA-256 密码哈希比对
3. 生成令牌（24h TTL）
4. 记录审计日志

**注意**：当前实现使用内存存储用户/令牌，生产环境需要持久化。

---

## 12. Observer — 可观测性体系

### 12.1 三重可观测性

**Metrics（指标）**：
- 自定义指标系统（Counter/Gauge/Histogram）
- Prometheus 指标导出（tasks_created_total, task_duration_seconds 等）
- 自动从 EventBus 事件中采集

**Traces（追踪）**：
```go
func (o *Observer) StartTrace(operation) *TraceSpan
func (o *Observer) EndTrace(span, status)
```

**Logs（日志）**：
- 结构化日志（zap 集成）
- 按级别过滤（Debug/Info/Warn/Error）
- 按组件查询

### 12.2 事件驱动的指标采集

Observer 通过订阅 EventBus 的 20 种事件类型，自动将事件转换为指标：

```
EventTaskCompleted → tasks_completed counter + task_duration histogram
EventTaskFailed    → tasks_failed counter
EventWorkerSpawned → workers_spawned counter
EventSkillInvoked  → skills_invoked counter
...
```

这意味着**任何模块只要发布事件，就会自动获得指标**，无需修改 Observer 代码。

---

## 13. Debugger — 调试与诊断

### 13.1 结构化日志

```go
// pkg/debugger/debugger.go:24
type LogEntry struct {
    Timestamp time.Time
    Level     LogLevel    // Debug/Info/Warn/Error/Fatal
    Component string      // 组件名（如 "scheduler", "executor"）
    Message   string
    Fields    map[string]interface{}
    Caller    string      // 自动获取调用者文件:行号
}
```

`Debugger.Log` 自动记录调用者信息（`runtime.Caller(skip)`），支持按级别过滤（`SetMinLevel`）和按组件查询（`GetLogsByComponent`）。

### 13.2 断点系统

```go
type Breakpoint struct {
    ID        string
    Component string
    Condition func() bool    // 条件判断
    Action    func()         // 触发动作
    Enabled   bool
    HitCount  int
}
```

每次日志记录时自动检查匹配组件的断点。条件满足时执行 Action 并递增 HitCount。

### 13.3 内存快照

```go
func (d *Debugger) TakeSnapshot()
```

调用 `runtime.ReadMemStats` 捕获当前内存状态（HeapAlloc, HeapInuse, StackInuse, Goroutines, NumGC），存入快照列表。

### 13.4 变量监视

```go
func (d *Debugger) AddWatcher(name string, fn func() interface{})
func (d *Debugger) GetWatchers() map[string]interface{}
```

注册命名监视器（闭包），调用 `GetWatchers` 时批量执行所有闭包并返回结果。

### 13.5 与 SkillContext 的集成

`Debugger` 实例可注入到 `SkillContext` 中，使插件执行时也能记录调试日志。这是插件可观测性的基础。

---

## 14. Plugin & Skill — 可扩展能力层

### 14.1 技能系统

```go
// skill.go:12
type SkillDefinition struct {
    Name        string            // "llm.chat", "research.anomaly"
    Version     string
    Description string
    InputTypes  []string
    OutputTypes []string
}

type SkillProvider interface {
    Definition() SkillDefinition
    Execute(ctx, input, config) ([]byte, error)
}
```

**SkillRegistry** 管理所有已注册的技能：
- `Register` 注册技能定义 + 可选的提供者
- `CheckDependencies` 检查插件所需的技能是否就绪
- `BuildContext` 构建运行时上下文注入到插件

### 14.2 插件依赖管理

```go
// plugin/manager.go:85
if pm.skillRegistry != nil && len(info.Skills) > 0 {
    missing := pm.skillRegistry.CheckDependencies(info.Skills)
    if len(missing) > 0 {
        return fmt.Errorf("plugin %s requires missing skills: %v", ...)
    }
}
```

插件通过 `plugin.json` 中的 `skills` 字段声明依赖。加载前自动检查依赖是否满足，形成能力声明式组合。

---

## 15. AI & Research — 智能分析层

### 15.1 LLM Client

```go
// pkg/ai/llm.go:46
type LLMClient struct {
    BaseURL string                        // 默认 "https://api.openai.com/v1"
    APIKey  string
    Client  *http.Client                  // 60s 超时
}
```

`GenerateStructured` 方法支持 OpenAI 兼容 API，核心特性：
- 系统/用户消息构造
- JSON Schema 结构化输出（`response_format: json_schema`）
- 低温采样（temperature: 0.2）提升稳定性
- 完整的 HTTP 错误处理

### 15.2 Context Builder

```go
// pkg/ai/llm.go:142
type ContextBuilder struct {
    MaxChars int    // 默认 16000
}
```

`BuildUserPrompt` 将上游任务结果组装成 LLM 上下文：
- 截断过长结果（不超过 `MaxChars/2`）
- 总长度超过限制时采用首尾截断
- 最终输出附带 "Main Instructions" 主提示

### 15.3 Research Engine

```go
// pkg/research/research.go:140
type ResearchEngine struct {
    detectors []Detector
    results   map[string][]Finding
    eventBus  *event.EventBus
}
```

内置三种模式检测器：

| 检测器 | 算法 | 输出 |
|--------|------|------|
| `AnomalyDetector` | Z-Score（均值 + 标准差） | 异常值 Finding |
| `TrendDetector` | 线性回归斜率 | 上升/下降趋势 Finding |
| `CorrelationDetector` | 相关系数 | 相关性 Finding |

每个 Finding 包含：ID、类型、置信度、描述、证据列表、建议。检测结果通过 EventBus 发布 `EventResearchFinding` 事件。

### 15.4 与 Executor 的集成

在 Agent 模式中，Executor 直接使用 LLM Client：
```go
llmCtx, llmCancel := context.WithTimeout(ctx, 30*time.Second)
llmOut, execErr = llm.GenerateStructured(llmCtx, model, systemPrompt, userPrompt, schema)
llmCancel()
```

同时，LLM 调用受 Circuit Breaker 保护（与插件执行共享同一个 SelfHealer 实例）。

---

## 16. Code Generator — 脚手架生成

### 16.1 模板系统

```go
// pkg/generator/generator.go:19
type Template struct {
    Name        string
    Type        TemplateType    // Workflow / Plugin / API / Scheduler / Security
    Description string
    Content     string          // Go 源码模板
    Variables   map[string]string
}
```

### 16.2 内置模板

| 类型 | 模板名 | 说明 |
|------|--------|------|
| `TemplateWorkflow` | `basic-workflow` | 顺序工作流（init → process → cleanup） |
| `TemplateWorkflow` | `parallel-workflow` | 并行工作流（task-a + task-b → merge） |
| `TemplatePlugin` | `basic-plugin` | 基础插件骨架 |
| `TemplateAPI` | `rest-endpoint` | REST 端点（GET/POST） |
| `TemplateScheduler` | `periodic-task` | 定时任务（ticker 驱动） |
| `TemplateSecurity` | `auth-middleware` | 认证中间件 |

### 16.3 生成机制

简单的字符串替换（`{{.Variable}}` → 实际值），不依赖模板引擎。支持：
- `Generate(templateType, name, variables)` — 生成代码
- `ListTemplates(type)` / `ListAllTemplates()` — 浏览模板
- `AddTemplate(tmpl)` — 注册自定义模板

这是一个轻量级的项目脚手架工具，帮助开发者快速生成标准代码。

---

## 17. UI & Theme — 多主题系统

### 17.1 主题接口

```go
// pkg/ui/theme.go:7
type Theme interface {
    Name() string
    Colors() ThemeColors
    IsDark() bool
}
```

### 17.2 三种主题

| 主题 | 说明 | 背景风格 |
|------|------|----------|
| `LightTheme` | 经典浅色 | 纯白背景 |
| `DarkTheme` | 经典深色 | 深灰背景 |
| `GlassTheme` | **毛玻璃（默认）** | 半透明 + 模糊阴影 |

### 17.3 配色系统

```go
type ThemeColors struct {
    // 背景层级
    Primary, Secondary, Tertiary, Elevated color.RGBA
    // 文字
    TextPrimary, TextSecondary, TextTertiary color.RGBA
    // 语义色
    Blue, Green, Orange, Red, Purple, Pink, Teal, Indigo color.RGBA
    // 边框 + 毛玻璃
    Border, BorderLight, GlassBg, GlassBorder, GlassShadow color.RGBA
}
```

Glass 主题是 LoopWorker 的标志性设计：半透明背景（alpha 183/255）、白色边框、柔和阴影，营造出 Apple 风格的毛玻璃效果。前端 React 应用也采用了相同的 Liquid Glass 设计语言（CSS 类名如 `glass-card`、`node-glow-running`）。

### 17.4 配置管理

```go
// pkg/config/settings.go
type SettingsManager struct {
    settings *Settings
    filePath string
    mu       sync.RWMutex
}
```

支持 JSON 持久化 + 环境变量覆盖（`LOOPWORKER_*` 前缀），配置项包括：
- General：语言、端口、插件目录、数据目录、自动启动
- Appearance：主题、字体大小、紧凑模式、动画
- Security：认证开关、令牌过期、最大尝试次数、锁定时长
- Advanced：调试模式、指标开关、日志级别

### 17.5 GoSafe — 跨模块安全原语

```go
// pkg/utils/safego.go:32
func GoSafe(ctx context.Context, fn func(context.Context))
```

这是整个项目的**防崩溃基础原语**。所有 goroutine 启动都通过 `GoSafe` 包装：
- 自动 recover panic
- 打印堆栈信息
- 防止单个 goroutine 的 panic 导致整个进程崩溃

被 `GoSafe` 保护的 goroutine 包括：Worker 循环、Watchdog、Dashboard SSE 监听、EventBus 处理、Service 管理等。

---

## 18. API 层 — REST 接口与前端

### 18.1 API 路由

```
/api/v1
├── GET  /health                    # 健康检查
├── GET  /workflow/graph            # 工作流依赖图
├── GET  /workflow/list             # 工作流列表
├── POST /workflow/execute          # 执行工作流
├── GET  /events/live               # 实时事件流
├── /tasks
│   ├── GET  /                      # 任务列表
│   ├── POST /                      # 创建任务
│   └── /{taskID}
│       ├── GET  /                  # 任务详情
│       ├── DELETE /                # 删除任务
│       └── POST /dependencies      # 添加依赖
└── /workers
    └── GET  /                      # Worker 列表

/metrics                            # Prometheus 指标
/*                                  # 嵌入式前端静态文件
```

### 18.2 中间件链

```go
// api.go:49-61
r.Use(corsMiddleware)           // CORS
r.Use(rateLimitMiddleware)      // 100 req/min/IP
r.Use(requestIDMiddleware)      // X-Request-ID
r.Use(middleware.RealIP)        // 真实 IP
r.Use(middleware.Logger)        // 请求日志
r.Use(middleware.Recoverer)     // panic 恢复
r.Use(middleware.Timeout(60s))  // 全局超时
r.Use(requestLoggingMiddleware) // 自定义日志
```

### 18.3 前端架构

前端使用 React + ReactFlow 构建，支持：
- 工作流可视化编辑（节点 + 连线）
- 技能节点状态展示（idle / invoked / completed / failed）
- 实时 SSE 数据推送
- Liquid Glass 主题（Apple 风格）

---

## 19. 架构模式总结

### 19.1 使用的设计模式

| 模式 | 应用位置 | 说明 |
|------|----------|------|
| **Event Bus** | 全局 | 发布/订阅解耦 |
| **Worker Pool** | Executor | 复用 goroutine 减少创建开销 |
| **Circuit Breaker** | SelfHeal | 防止级联故障 |
| **Strategy** | Plugin, SkillProvider | 可插拔执行策略 |
| **Observer** | Observer | 事件驱动的指标采集 |
| **Command** | Step.Action | 任务作为命令对象 |
| **Bridge** | SchedulerBridge | 解耦 Workflow 和 Scheduler |
| **Factory** | NewScheduler, NewExecutor | 对象创建封装 |
| **Options** | WorkflowEngineOption | 灵活的配置方式 |
| **State Machine** | Task 状态, Worker 状态 | 明确的状态流转 |
| **Priority Queue** | Scheduler | 基于 heap 的优先级调度 |
| **Dead Letter Queue** | Scheduler | 失败任务隔离 |

### 19.2 Go 惯用模式

- **Channel 通信**：Worker 通过 `taskCh` 接收任务，避免共享内存竞态
- **Atomic 操作**：统计信息通过 `atomic` 包操作，避免锁开销
- **Context 传播**：所有异步操作接收 `context.Context`，支持取消和超时
- **defer 清理**：资源释放通过 defer 保证
- **interface 解耦**：大量使用接口定义边界

---

## 20. 关键设计决策评析

### 20.1 优点

1. **Server 对象过大**：`Server` 结构体持有 16 个组件，是典型的 God Object。可考虑拆分为独立的服务层。
2. **内存中状态**：User、Token 等安全相关数据存储于内存，进程重启后丢失。
3. **Dashboard 使用全局 mux**：`http.HandleFunc` 注册到 `DefaultServeMux`，在测试中容易产生冲突。
4. **Error 处理不一致**：部分错误被忽略（`_ = eventBus.Publish`），部分被包装返回。
5. **缺少超时配置**：API 中间件设置了 60 秒超时，但 Worker 执行超时仅在 Agent 模式中设置。
6. **SQLite 单节点**：当前持久化方案不支持多节点部署。

### 19.3 架构演进建议

```
短期：
├── 将 Server 拆分为独立服务（依赖注入容器）
├── 安全数据持久化（用户/令牌到数据库）
└── 完善 error 处理策略

中期：
├── 引入 gRPC 替代部分 REST 调用
├── 支持分布式调度（多节点）
├── 事件溯源（Event Sourcing）完整实现
└── 插件热加载

长期：
├── 支持 K8s 部署（Operator 模式）
├── 多租户支持
├── 插件市场（Plugin Registry）
└── 可视化工作流编辑器（前端增强）
```

---

## 总结

LoopWorker 是一个架构设计优秀的工业级工作流引擎。其核心优势在于：

- **事件驱动架构**提供了卓越的松耦合性和可扩展性
- **完整的生命周期管理**（创建→调度→执行→完成/失败→重试→死信）
- **多层防护体系**（限流→认证→授权→审计）
- **自愈能力**（熔断器→重试→僵尸检测→自动重生）
- **多范式工作流**（顺序/DAG/并行）

代码量适中（~15K LOC），核心模块清晰，测试覆盖率高（25 个测试文件）。适合作为学习 Go 后端架构设计的参考项目。

---

*分析基于 commit `loopWorker-for-go` 的完整源代码*
*日期：2026-07-02*

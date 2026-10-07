# LoopWorker 平台架构深度分析

> ⚠️ **阅读前必读：本文档写于 2026-08 之前，描述的是设计意图，不是当前实现的逐层事实。**
> 已在文中就地标注的偏差：
> - **§11.1 / §11.2 的审计日志、密码登录、账户锁定未接线**（`SecurityManager` 无生产调用方）。
> - **持久化不是 GORM**，是 `database/sql` + `modernc.org/sqlite`（gorm 已移除）。
> - **前端**：`web/canvas/` **有** React 源码（`src/App.jsx`、`SkillNode.jsx`、
>   `WasmNode.jsx`、`AgentNode.jsx`…），但服务端 embed 的是**构建产物**
>   `pkg/api/dist/`（`staticHandler` 提供）。改前端要在 `web/canvas/` 里
>   `npm run build` 之后重新产出 `pkg/api/dist/`，否则改了也不生效。
> - 指标与日志端点在 **loopback admin 监听器**（`server.admin_port`），不在 `/api/v1/*` 上；
>   `pkg/api/openapi.json` 是路由的权威来源，`TestOpenAPISpecMatchesRegisteredRoutes` 会让文档漂移变红。
>
> 逐条实测状态见 [AGENT-COLLABORATION-SPEC.md](../AGENT-COLLABORATION-SPEC.md) §8/§10。

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
| 语言 | Go 1.26.6（`go.mod` 的 `go` 指令） |
| 总代码量 | 22,144 行（非测试 `.go`，`git ls-files '*.go' \| grep -v _test \| xargs wc -l`，2026-10-06） |
| 源文件数 | 91 个非测试 `.go` 文件 |
| 测试文件数 | 63 个 `*_test.go` |
| 默认端口 | 19527（API）/ 19528（admin，固定绑 127.0.0.1） |
| WASM 运行时 | wazero (纯 Go 实现，无需 CGO) |
| 持久化 | SQLite（`database/sql` + `modernc.org/sqlite`，纯 Go 驱动） |
| 前端 | React + ReactFlow（源码在 `web/canvas/src/`，服务端 embed 的是 `pkg/api/dist/`） |

### 1.2 目录结构

```
loopWorker-for-go/
├── cmd/
│   ├── loopworker/              # 产品二进制 (cobra)：doctor / storage / backup / version
│   └── loopctl/                 # 开发者 CLI：task / workflow / status
├── pkg/                         # 模块外可导入
│   ├── api/                     # REST API (chi router)，admin 监听器也在此
│   ├── client/                  # loopctl 用的 HTTP 客户端
│   ├── errors/                  # sentinel 错误
│   ├── event/                   # 事件系统 (EventBus + EventStore)，20 种事件
│   ├── logger/                  # zap 封装
│   ├── plugin/                  # 插件清单/管理器
│   ├── security/                # 认证 + RBAC
│   ├── server/                  # Server 组装 + doctor
│   ├── skill/                   # 技能注册与上下文
│   ├── utils/
│   └── workflow/                # 工作流引擎
├── internal/                    # 内部核心包（模块外不可导入）
│   ├── config/                  # 配置解析（唯一权威：internal/config/spec.go）
│   └── core/
│       ├── scheduler/           # 调度器 (883 LOC，最重模块)
│       ├── selfheal/            # 自愈 (665 LOC)
│       ├── executor/            # 执行器 (630 LOC)
│       ├── observer/            # 可观测性 (634 LOC)
│       ├── sandbox/             # 沙箱 (519 LOC)
│       └── dispatcher/          # 分发器 (180 LOC)
├── web/canvas/                  # React 前端源码（需 build 出 pkg/api/dist/）
│   └── src/{App,SkillNode,WasmNode,AgentNode}.jsx
├── plugins/                     # 插件目录
├── examples/                    # hello-plugin / workflow / wasm/rust_agent
├── integration/                 # 集成测试
├── test/                        # e2e
└── docs/
```

**不存在的目录**（本文早期版本列出过，现已删除，不要照着找）：
`pkg/dashboard`、`pkg/config`、`pkg/debugger`、`pkg/research`、`pkg/service`、
`pkg/ui`。配置在 `internal/config`，不是 `pkg/config`。

### 1.3 核心数据概览

代码行数分布（核心文件，`wc -l` 于 2026-10-06；这是快照不是承诺）：

| 模块 | 文件 | 行数 | 职责权重 |
|------|------|------|----------|
| `scheduler` | `internal/core/scheduler/scheduler.go` | 883 | 状态机 + 持久化 |
| `workflow` | `pkg/workflow/workflow.go` | 819 | 多范式编排 |
| `selfheal` | `internal/core/selfheal/selfheal.go` | 665 | 熔断 + 恢复 |
| `observer` | `internal/core/observer/observer.go` | 634 | 指标 + 日志（无生产追踪，见 §12.1） |
| `executor` | `internal/core/executor/executor.go` | 630 | Worker 池 |
| `sandbox` | `internal/core/sandbox/sandbox.go` | 519 | WASM 隔离 |
| `event` | `pkg/event/bus.go` | 283 | 事件总线 |
| `security` | `pkg/security/security.go` | 252 | RBAC + 认证 |
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
│   REST API (chi) │ admin listener │ Web canvas │ CLI (cobra)    │
├──────────────────────────────────────────────────────────────────┤
│                     Core Engine                                  │
│   Scheduler → Dispatcher → Executor → Sandbox                   │
│        ↕ (EventBus)                                              │
│   WorkflowEngine ──→ SchedulerBridge                            │
├──────────────────────────────────────────────────────────────────┤
│                     Infrastructure Layer                         │
│   SelfHeal │ Observer │ Security │ Plugin │ Skill                │
├──────────────────────────────────────────────────────────────────┤
│                     Storage Layer                                │
│   SQLite (tasks) │ Config (internal/config) │ State (In-Memory)│
└──────────────────────────────────────────────────────────────────┘
```

（没有 TUI/bubbletea，没有独立的 Dashboard / Service / Debugger 进程 ——
`go.mod` 里没有这些依赖，`pkg/dashboard` 等目录也不存在。）

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
| `sync.Mutex` | Workflow（`pkg/workflow` 的运行状态）, Observer logs | 互斥锁 |

---

## 3. 数据流主线：任务的生命周期

理解 LoopWorker 架构的最佳方式是追踪一个任务从创建到完成的完整生命周期。

### 3.1 完整生命周期图

```
                    ┌─────────────────────────────────────────────────────────┐
                    │                    Event Bus                            │
                    │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────┐ │
                    │  │Observer  │  │SelfHeal  │  │ Workflow │  │ ...    │ │
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
- 持久化到 SQLite（`database/sql` + `modernc.org/sqlite`，非 GORM）
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

所有任务状态变更都通过 `saveTask` 同步写入 SQLite。使用标准库 `database/sql` + `modernc.org/sqlite`
（纯 Go 驱动，`CGO_ENABLED=0` 即可静态链接、免 C 工具链交叉编译；gorm 已于 2026-10-05 因零 import
从 `go.mod` 移除）。`TaskModel` 作为持久化模型，通过 JSON 序列化存储 `Config`、`Metadata`、
`Dependencies` 等 map/struct 字段。

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
│  Layer 7: Audit Logging                 │ ← 未接线，见下方说明
└─────────────────────────────────────────┘
```

> ⚠️ **本节（§11.1 / §11.2）描述的是 `pkg/security.SecurityManager` 的设计能力，不是当前生产链路。**
> `NewSecurityManager()` **没有任何生产调用方**（只有 `pkg/security` 与 `integration` 的测试构造它），
> 因此密码登录、账户锁定、审计日志三层在服务端**都不会被触发**。生产实际走的是
> `pkg/api` + `pkg/security/auth.go` 的 API key / bearer token 链路：认证、RBAC、限流、输入校验、
> 请求体上限、任务归属隔离都是活的；审计日志不是。改造方向与证据见
> [AGENT-COLLABORATION-SPEC.md](../AGENT-COLLABORATION-SPEC.md) §8.2。

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

### 12.1 两重可观测性（不是三重）

**Metrics（指标）**：
- 自定义指标系统（Counter/Gauge/Histogram）
- Prometheus 指标导出（`loopworker_tasks_created_total`、
  `loopworker_tasks_started_total`、`loopworker_tasks_completed_total`、
  `loopworker_tasks_failed_total`、`loopworker_task_duration_seconds`）
- 自动从 EventBus 事件中采集

**Logs（日志）**：
- 结构化日志（zap 集成）
- 按级别过滤（Debug/Info/Warn/Error）
- 按组件查询

> ⚠️ **`Observer` 上没有生产可用的追踪。** `StartTrace` / `EndTrace` /
> `GetTraces`（`observer.go:494/515/534`）确实存在，但**唯一的调用方是
> `observer_test.go` 与 `liveness_test.go`**；仓库里没有 OTel 依赖、没有
> exporter、没有上下文传播。判据是「非测试调用方数量」，不是「有没有那个函数」。
> 客户端要分布式追踪请自己在 `pkg/api` 层接。

### 12.2 事件驱动的指标采集

Observer 的 `Start` 订阅 EventBus 的**全部 20 种**事件类型，把事件转换为指标：

```
EventTaskCompleted → tasks_completed counter + task_duration histogram
EventTaskFailed    → tasks_failed counter
EventWorkerSpawned → workers_spawned counter
EventSkillInvoked  → skills_invoked counter
...
```

这意味着**任何模块只要发布事件，就会自动获得指标**，无需修改 Observer 代码。

注意两套指标的区别：上面这些点分名字（`tasks.created`、`workers.spawned`…）
是 `IncrementCounter` 的**进程内**累加器，不导出 Prometheus；只有 §12.1 列出的
5 个 `loopworker_*` 向量出现在 admin 监听器的 `/metrics` 上。

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

> ⚠️ **`pkg/ai` 与 `pkg/research` 都不存在。** LLM 部分搬到了
> `internal/core/executor/llm.go`；**异常检测器（`AnomalyDetector` /
> `TrendDetector` / `CorrelationDetector`）没有任何实现** ——
> `grep -rn 'AnomalyDetector' --include=*.go` 零命中，
> `event.EventResearchFinding` 这个事件类型被定义、被 observer 订阅计数，
> 但**没有任何代码发布它**。§15.3 的检测器表是设计意图，不是现状。

### 15.1 LLM Client

```go
// internal/core/executor/llm.go:46
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
// internal/core/executor/llm.go:142
type ContextBuilder struct {
    MaxChars int    // 默认 16000
}
```

`BuildUserPrompt` 将上游任务结果组装成 LLM 上下文：
- 截断过长结果（不超过 `MaxChars/2`）
- 总长度超过限制时采用首尾截断
- 最终输出附带 "Main Instructions" 主提示

### 15.3 Research Engine — **不存在**

设计里描述的检测器（Z-Score 异常 / 线性回归趋势 / 相关系数）**没有实现**。
本仓库里能查到的只有 `event.ResearchFindingPayload` 这个事件负载类型。
如果你要用异常检测，用内置工作流 `builtin.anomaly-review`（它自带合成数据
与判定逻辑，需要零插件、零 API key），不要指望 §15.3 描述的引擎。

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

> ⚠️ **本节整体不存在。** `pkg/generator` 目录不存在，`Template` /
> `TemplateType` / `TemplateWorkflow` 等类型在仓库里没有任何定义，
> 也没有任何生产调用方。要生成骨架，用 `examples/hello-plugin`（WASM 插件）
> 和 `examples/workflow/main.go`（工作流）当模板改。

### 16.1-16.3（原设计意图，无实现）

```go
// pkg/generator/generator.go:19 —— 这个文件不存在
type Template struct {
    Name        string
    Type        TemplateType
    Description string
    Content     string
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

> ⚠️ **`pkg/ui` 与 `pkg/config` 都不存在**，服务端没有主题系统：
> `Theme` / `LightTheme` / `DarkTheme` / `GlassTheme` / `ThemeColors` /
> `SettingsManager` 全部没有定义。主题是**前端 CSS** 的事 ——
> `web/canvas/src/index.css` 里确实有 `.glass-card`、`.node-glow-running`
> 这类类名，但改它要改 `web/canvas/` 并重新 build `pkg/api/dist/`。
> 配置里也没有 `appearance` 段（写进配置文件会导致启动失败）。

### 17.1-17.3（原设计意图，无服务端实现）

毛玻璃样式是前端 CSS 的实现，不是 Go 侧的主题对象。

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

被 `GoSafe` 保护的生产 goroutine（`grep -rn GoSafe --include=*.go`，非测试共 8 处）：
Executor 的 worker 循环与 watchdog（`executor.go:200,570`）、Sandbox 的执行
（`sandbox.go:377`，用的是会把 panic 作为错误投递的 `GoSafeE`）、
`pkg/api` 的工作流执行（`handlers_system.go:245`）、
`pkg/server` 的两处（`server.go:434,846`）、`pkg/workflow` 的并行执行
（`workflow.go:744`）。

（EventBus 的订阅处理循环不走 `GoSafe`，它用 context 取消；没有 Dashboard 或
Service 模块 —— 那两个目录不存在。）

---

## 18. API 层 — REST 接口与前端

### 18.1 API 路由

权威列表是 `pkg/api/openapi.json`（由 `TestOpenAPISpecMatchesRegisteredRoutes`
锁定）。API 端口（默认 19527）：

```
/healthz                           # 匿名存活探针
/api/v1
├── GET  /health                    # 匿名（auth 恒为 "required"）
├── GET  /openapi.json              # 匿名
├── GET  /workflow/graph            # 工作流依赖图
├── GET  /workflow/list             # 工作流列表
├── GET  /workflow/{workflowID}     # 单个工作流
├── POST /workflow/execute          # 执行工作流（202）
├── GET  /events/live               # SSE 实时事件流
├── GET  /auth/whoami               # 当前身份
├── POST /auth/token                # 换 bearer token
├── GET/POST/DELETE /auth/keys[/{keyID}]  # 密钥管理
├── /tasks
│   ├── GET  /                      # 任务列表
│   ├── POST /                      # 创建任务（201）
│   └── /{taskID}
│       ├── GET  /                  # 任务详情
│       ├── DELETE /                # 与 cancel 同一个处理器
│       ├── POST /cancel            # 取消
│       └── POST /dependencies      # 添加依赖
└── /workers
    └── GET  /                      # Worker 列表

/*                                  # 嵌入式前端（pkg/api/dist/）
```

**`/metrics` 和 `/logs` 不在这个端口上。** 它们在 admin 监听器
（默认 `127.0.0.1:19528`）：`/metrics`、`/runtime/stats`、`/logs`、
`/events/stats`、`/healthz`。API 端口上访问 `/metrics` 是 404。

角色要求：读 = 任意角色；写（`POST /tasks`、`DELETE`、`/cancel`、
`/dependencies`）需要 write；`/workflow/execute` 需要 execute；
`/auth/*` 需要 admin。

### 18.2 中间件链

实际顺序（`pkg/api/api.go:registerRoutes`）：

```go
s.Router.Use(securityHeaders)
s.Router.Use(bundle.cors)
s.Router.Use(bundle.requestID)
s.Router.Use(bundle.accessLog)
s.Router.Use(s.blockConfigError())
s.Router.Use(middleware_Recoverer())
s.Router.Use(bundle.bodyLimit)     // api.max_body_bytes，默认 10 MB
s.Router.Use(bundle.timeout)       // 默认 30s

public := s.Router.With(bundle.rateLimitIP)          // 匿名按 IP 限流
public.Get("/healthz", s.livenessProbe)

r.With(bundle.authMiddleware, bundle.principalLimit).Group(func(authed chi.Router) {
    // 认证按凭据限流；角色检查逐路由（require(PermWrite/Execute/Admin)）
})
```

限流是**加权**的：SSE 计 5 点，`POST`/`DELETE`/`PUT` 计 2 点，其余读计 1 点。

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

### 20.1 已知问题（编号与原文档不一致，这里按现状重列）

1. **`Server` 是 God Object**：持有全部组件，可考虑拆分。
2. **内存中状态**：`pkg/security.SecurityManager` 的 User/Token 存内存，重启丢失
   —— 而且它**根本没有生产调用方**（见 §11.1 的偏差说明）；生产走
   `pkg/security.Authenticator` 的 API key / bearer token 链路。
3. **Error 处理不一致**：部分错误被忽略（`_ = eventBus.Publish`），部分被包装返回。
4. **超时**：`api.request_timeout` 默认 30s（不是 60s），且**写超时被关掉**
   （`WriteTimeout = 0`）以让 SSE 能长连。worker 侧超时是
   `workers.task_timeout`（默认 30m），不是只在 Agent 模式里设置。
5. **SQLite 单节点**：当前持久化方案不支持多节点部署。
6. **两个配置键被接受但不生效**：`workflow.max_concurrent` 与
   `workflow.timeout`。启动自检把它们列进 `unapplied_keys` 并打印修复建议 ——
   这是**刻意**的（不静默忽略），不要当成 bug 去"修"文档。
7. **`security.enabled` 无消费者**：见 §11.1 与 API 参考「认证」。
8. **没有 Dashboard 全局 mux 问题**：`pkg/dashboard` 目录不存在，该顾虑已随
   模块删除而消失。

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

- **事件驱动架构**提供了松耦合性和可扩展性（20 种事件，13 种上 SSE）
- **完整的生命周期管理**（pending→queued→running→completed/failed，
  重试耗尽进 dead_letter）
- **多层防护体系**（限流→认证→授权→输入校验→请求体上限→任务归属隔离）
  —— **审计日志不在其中**：`pkg/security.SecurityManager` 没有生产调用方（§11.1）
- **自愈能力**（熔断器→重试→僵尸检测→自动重生）
- **多范式工作流**（顺序/DAG/并行）

代码量 22,144 行非测试 Go（91 个源文件、63 个测试文件，2026-10-06 实测）。
**不要**把它当参考项目抄：本文开头列的偏差（GORM、审计日志、前端源码、
三层防护的第 3 层）都是它现在不做的事。

---

*本文写于 2026-08 之前，是设计意图分析；文中标注的偏差以代码为准。
逐条实测状态见 [AGENT-COLLABORATION-SPEC.md](../AGENT-COLLABORATION-SPEC.md) §8/§10。*

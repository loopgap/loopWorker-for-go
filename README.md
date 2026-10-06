# LoopWorker

[English](#english) | [中文](#中文)

---

## English

Run untrusted code inside a Go application without giving it your process. LoopWorker loads each
plugin as a WebAssembly module and runs it under sandbox limits — memory, CPU seconds, output
size, concurrency, and the hosts it may reach — on a work loop that schedules, retries, and reports
like any other task.

### What actually ships

- **WASM plugin isolation** - every plugin gets its own wazero runtime, so a plugin cannot
  corrupt another's memory. Memory, CPU-second, output-size and concurrency caps, plus an
  `allowed_hosts` egress list, come from config and are **shared across the sandbox, not per
  plugin** — see [Limits are sandbox-wide](#limits-are-sandbox-wide) before you rely on one
  misbehaving plugin leaving the others alone.
- **Priority scheduling** - 4-level queue (Low/Normal/High/Critical) with retry budgets and a
  dead-letter state for tasks that exhaust them
- **Task dependencies** - DAG with cycle detection at the API boundary
- **Workflow engine** - sequential and DAG steps (each step declares what it depends on, and the
  engine orders them topologically), driven by JSON/YAML definitions. Two workflows ship built in
  and work with no configuration at all; a step can call a plugin or a skill.
- **Self-healing** - circuit breaker, exponential backoff, and health checks on a timer against
  the task database, the event store and the plugin directory. A failing check is named in
  `/healthz` with its cause.
- **Security** - API-key and bearer-token auth, RBAC (`admin`/`operator`/`viewer`), per-key rate
  limiting, request-body limits, and per-task ownership
- **Observability** - Prometheus metrics, a structured event stream on disk, and a loopback admin
  listener for `/metrics` and `/runtime/stats`
- **Self-diagnosis** - `loopworker doctor` checks directories, database, port, plugins,
  authentication and sandbox policy, and names the cause, the fix and a docs anchor for each

#### Limits are sandbox-wide

Every plugin shares the configured caps: a plugin that allocates all 256 MB makes the next plugin
fail for the same reason. Per-plugin manifest limits are implemented in `internal/core/sandbox`
but the plugin loader does not read them yet, so they are not a feature of this release. Loading
them is the first item on the enterprise split.

Not in this release — scaffolding exists, but nothing calls it in a running server:

| Not shipped | Where the code is | Why it is not a feature |
|---|---|---|
| Signed WASM plugin provenance | `internal/core/sandbox/verify.go` `AuditWasmFile` | the auditor exists and is tested, but nothing calls it in a running server, so no manifest checksum or ABI gate is enforced at load time |
| Plugin checksum verification | `sandbox/loader.go checkChecksums` | only the test path reads manifests |
| Distributed tracing | `internal/core/observer` | in-memory span slice, no exporter |
| Audit logging | `pkg/security.SecurityManager` | never constructed outside tests |

See [AGENT-COLLABORATION-SPEC.md](AGENT-COLLABORATION-SPEC.md) §8.2 for what is still open.

### Quick Start

```bash
# Build
make build

# Run
./bin/loopworker

# Run tests
make test

# Docker deployment
make docker
docker run -p 19527:19527 loopworker:latest
```

### Go SDK Usage

```go
// Embed LoopWorker in your Go application
bus := event.NewEventBus(nil)
sb := sandbox.NewSandbox(sandbox.SandboxConfig{MaxMemoryMB: 256})
sb.SetEventBus(bus)

// Register a plugin
plugin := sandbox.NewGoPlugin("echo", "1.0", func(ctx context.Context, input io.Reader, output io.Writer, _ skill.SkillContext) error {
    _, err := io.Copy(output, input)
    return err
})
sb.LoadPlugin("echo", plugin)

// Execute
result, _ := sb.Execute(context.Background(), "echo", []byte("hello"), skill.SkillContext{})
// result = []byte("hello")
```

> 📖 **Full usage guide**: See [docs/USAGE.md](docs/USAGE.md) for comprehensive Go SDK examples, plugin development, workflow authoring, API reference, and configuration details.

### CLI Tools

`make build` produces all six. **Releases ship only `loopworker`** — the release archive and the
container image contain nothing else, so the other five are developer tools you build yourself.

| Tool | What it actually does | In releases | Needs credentials |
|------|-----------------------|-------------|-------------------|
| `loopworker` | The server, plus `doctor` and `version` | yes | writes do |
| `loopctl` | `task list/create/get/cancel/delete`, `workflow list/execute`, `status` | no | yes, for everything but `status` |
| `loopdebug` | Task/workflow inspection, `diagnose`, `profile` | no | yes, for everything but `diagnose` |
| `loopwatch` | `GET /api/v1/health` only | no | no |
| `loopbench` | Local `time.Sleep` loop — never contacts a server | no | n/a |
| `loopsim` | Local `rand` + `time.Sleep` loop — never contacts a server | no | n/a |

`loopwatch --metrics` and `--logs` do not work: `/api/v1/metrics` and `/api/v1/logs` are not
routes. Metrics and logs live on the loopback admin listener on `server.admin_port` (default
19528) and need admin credentials — see [API Endpoints](#api-endpoints). `loopbench` and
`loopsim` report numbers about their own sleep loops; do not quote them as product performance.

All credentialed commands read `LOOPWORKER_API_KEY`.

### Project Structure

```
loopWorker-for-go/
├── cmd/                    # CLI tools
│   ├── loopworker/         # Main server
│   ├── loopctl/            # Task & workflow management CLI
│   ├── loopbench/          # Performance benchmarking
│   ├── loopwatch/          # Real-time monitoring
│   ├── loopsim/            # Load simulation
│   └── loopdebug/          # Debugging & diagnostics
├── pkg/                    # Public packages
│   ├── api/                # REST API
│   ├── event/              # Event system
│   ├── plugin/             # Plugin management
│   ├── security/           # Security (bcrypt, RBAC)
│   ├── server/             # Server management
│   ├── skill/              # Skill management
│   ├── utils/              # Utility functions
│   └── workflow/           # Workflow engine
├── internal/               # Internal core packages
│   ├── config/             # Config management
│   └── core/               # Core logic
│       ├── scheduler/      # Task scheduler with SQLite persistence
│       ├── dispatcher/     # Task dispatcher
│       ├── executor/       # Worker pool and LLM client
│       ├── sandbox/        # WASM sandbox
│       ├── observer/       # Observability
│       └── selfheal/       # Self-healing mechanisms
├── version/                # Version information
├── config/                 # Configuration examples
├── integration/            # Integration tests
├── examples/               # Example workflows
└── docs/                   # Documentation
```

### Building

```bash
# Build all binaries
make build

# Run tests
make test

# Cross-compile for all platforms
make build-all

# Build Docker image
make docker
```

### Configuration

```yaml
# config/config.example.yaml
server:
  port: 19527
  admin_port: 19528      # loopback-only observability listener
  host: "0.0.0.0"

plugins:
  dir: "~/.loopworker/plugins"

data:
  dir: "~/.loopworker/data"
```

Run `loopworker doctor` to see the resolved configuration with the source of
every value (`file:` / `env:` / `default`), plus what is wrong and how to fix
it.

#### Authentication

With no credentials configured the server prints a single-use bootstrap key at
startup. That key is ephemeral: it changes on every restart, and the server
refuses to start on a public bind address while only such a key exists. For
anything durable, supply real keys:

| Variable | Meaning |
|----------|---------|
| `LOOPWORKER_API_KEYS` | `id:role:sha256hex` entries, comma-separated. The key is the SHA-256 hex digest of the plaintext. |
| `LOOPWORKER_API_KEYS_PLAIN` | Same, but `id:role:lwk_...` plaintext; the server hashes it immediately. Easier for a first run. |
| `LOOPWORKER_AUTH_SIGNING_SECRET` | HMAC secret for bearer tokens. Required to use `POST /api/v1/auth/token`. |
| `LOOPWORKER_AUTH_TOKEN_TTL` | Bearer token lifetime, e.g. `1h`. |

Roles are `admin`, `operator`, `viewer`. A malformed value is a startup error,
not a warning — the server will not fall back to defaults.

```bash
export LOOPWORKER_API_KEYS="ci:operator:$(printf '%s' "$MY_KEY" | sha256sum | cut -d' ' -f1)"
loopworker --config config.yaml

curl http://localhost:19527/api/v1/tasks -H "X-API-Key: $MY_KEY"
```

An API key is sent as `X-API-Key`, or exchanged for a bearer token at
`POST /api/v1/auth/token`. Bearer tokens are `lwt_`-prefixed; an API key in an
`Authorization: Bearer` header is rejected.

### API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /healthz | Health check (anonymous) |
| GET | /api/v1/health | Health check (anonymous) |
| GET | /api/v1/openapi.json | OpenAPI document (anonymous) |
| POST | /api/v1/auth/token | Exchange an API key for a bearer token |
| GET | /api/v1/auth/whoami | Show the caller's identity and role |
| GET | /api/v1/workers | List workers |
| GET | /api/v1/tasks | List tasks |
| POST | /api/v1/tasks | Create task |
| GET | /api/v1/tasks/{taskID} | Get task |
| DELETE | /api/v1/tasks/{taskID} | Delete task |
| POST | /api/v1/tasks/{taskID}/cancel | Cancel a task |
| POST | /api/v1/tasks/{taskID}/dependencies | Add a dependency edge |
| GET | /api/v1/workflow/list | List workflows |
| GET | /api/v1/workflow/graph | Dependency graph snapshot |
| GET | /api/v1/workflow/{workflowID} | Get workflow |
| POST | /api/v1/workflow/execute | Execute workflow |
| GET | /api/v1/events/live | SSE event stream |
| POST/GET | /api/v1/auth/keys, DELETE /api/v1/auth/keys/{keyID} | Issue and revoke keys (admin) |

Metrics and logs are **not** on this port. They are on the loopback-only
admin listener (default `127.0.0.1:19528`, configurable via
`server.admin_port`): `/metrics`, `/runtime/stats`, `/logs`,
`/events/stats`, `/healthz`. It requires admin credentials.

This table is generated against the same source as `pkg/api/openapi.json`,
and `pkg/api`'s `TestOpenAPISpecMatchesRegisteredRoutes` fails the build if the
two ever disagree.

### License

MIT

---

## 中文

在 Go 应用里运行不可信代码，而不必把进程交出去。LoopWorker 把每个插件作为 WebAssembly 模块加载，
在**沙箱级限额**下执行——内存、CPU 秒、输出大小、并发数，以及它允许访问的主机名单——
任务则走与其他任务相同的工作循环：调度、重试、上报。

### 实际具备的能力

- **WASM 插件隔离** - 每个插件一个 wazero runtime；内存 / `max_cpu_seconds` / 输出上限 / 并发上限
  以及出网 `allowed_hosts` 白名单，全部可配。限额是**沙箱级而非插件级**，见
  [限额是沙箱级的](#限额是沙箱级的)
- **优先级调度** - 四级队列（低/普通/高/关键），带重试预算；重试耗尽的任务进入 dead-letter 状态
- **任务依赖** - DAG，环检测在 API 边界完成
- **自愈机制** - 熔断器、指数退避，以及按定时器对任务数据库、事件存储与插件目录的健康检查；失败的检查会带原因在 `/healthz` 里点名
- **工作流引擎** - 顺序与 DAG 步骤（每步声明依赖，引擎按拓扑序执行），由 JSON/YAML 定义驱动。内置两个工作流，零配置即可运行；步骤可以调用插件或技能。
- **安全防护** - API key 与 bearer token 鉴权、RBAC（`admin`/`operator`/`viewer`）、按凭据限流、
  请求体大小限制、任务归属隔离
- **可观测性** - Prometheus 指标、落盘的结构化事件流，以及 loopback admin 监听器上的
  `/metrics` 与 `/runtime/stats`
- **自诊断** - `loopworker doctor` 检查目录、数据库、端口、插件、鉴权与沙箱策略，
  每一项都给出原因、修法与文档锚点

#### 限额是沙箱级的

所有插件共用同一组限额：一个插件吃满 256 MB，下一个插件也会因同样的原因失败。
按 manifest 配置的每插件限额已在 `internal/core/sandbox` 中实现，但插件加载器尚未读取它们，
因此不属于本版本能力。加载 manifest 限额是企业版拆分的第一项。

本版本**不含**以下能力——代码存在，但运行中的服务里没有任何地方调用：

| 未交付 | 代码位置 | 原因 |
|---|---|---|
| WASM 产物签名溯源 | `internal/core/sandbox/verify.go` 的 `AuditWasmFile` | 审计器存在且有测试，但运行中的服务器没有任何调用方，所以加载时不强制 manifest 校验和与 ABI 闸门 |
| 插件校验和验证 | `sandbox/loader.go checkChecksums` | 只有测试路径读 manifest |
| 分布式追踪 | `internal/core/observer` | 只写内存切片，无 exporter |
| 审计日志 | `pkg/security.SecurityManager` | 除测试外从未构造 |

仍未闭合的项见 [AGENT-COLLABORATION-SPEC.md](AGENT-COLLABORATION-SPEC.md) §8.2。

### 快速开始

```bash
# 构建
make build

# 运行
./bin/loopworker

# 运行测试
make test

# Docker部署
make docker
docker run -p 19527:19527 loopworker:latest
```

### Go SDK 使用

```go
// 在 Go 应用中嵌入 LoopWorker
bus := event.NewEventBus(nil)
sb := sandbox.NewSandbox(sandbox.SandboxConfig{MaxMemoryMB: 256})
sb.SetEventBus(bus)

// 注册插件
plugin := sandbox.NewGoPlugin("echo", "1.0", func(ctx context.Context, input io.Reader, output io.Writer, _ skill.SkillContext) error {
    _, err := io.Copy(output, input)
    return err
})
sb.LoadPlugin("echo", plugin)

// 执行
result, _ := sb.Execute(context.Background(), "echo", []byte("hello"), skill.SkillContext{})
// result = []byte("hello")
```

> 📖 **完整使用指南**: 参见 [docs/USAGE.md](docs/USAGE.md)，包含 Go SDK 示例、插件开发、工作流编写、API 参考和配置详解。

### 命令行工具

| 工具 | 说明 | 版本 |
|------|------|------|
| `loopworker` | 主服务器 | `loopworker version` |
`make build` 会构建全部六个工具，但**发布产物只含 `loopworker`** —— release 归档与容器镜像里
没有其他五个，其余五个是你需要自己 `make build` 的开发者工具。

| 工具 | 实际做的事 | 在发布产物里 | 需要凭据 |
|------|-----------|-------------|---------|
| `loopworker` | 服务器本体，外加 `doctor` 与 `version` | 是 | 写操作需要 |
| `loopctl` | `task list/create/get/cancel/delete`、`workflow list/execute`、`status` | 否 | 除 `status` 外都需要 |
| `loopdebug` | 任务/工作流查看、`diagnose`、`profile` | 否 | 除 `diagnose` 外都需要 |
| `loopwatch` | 只发 `GET /api/v1/health` | 否 | 不需要 |
| `loopbench` | 本地 `time.Sleep` 循环，从不连服务器 | 否 | 不适用 |
| `loopsim` | 本地 `rand` + `time.Sleep` 循环，从不连服务器 | 否 | 不适用 |

`loopwatch --metrics` 与 `--logs` 不可用：`/api/v1/metrics` 与 `/api/v1/logs` 并不是路由。
指标与日志在 `server.admin_port`（默认 19528）的 loopback admin 监听器上，且需要 admin
凭据 —— 见 [API端点](#api端点)。`loopbench` 与 `loopsim` 报的是它们自己 sleep 循环的数字，
不要当作产品性能引用。

所有需要凭据的命令都读 `LOOPWORKER_API_KEY`。

### 项目结构

```
loopWorker-for-go/
├── cmd/                    # 命令行工具
│   ├── loopworker/         # 主服务器
│   ├── loopctl/            # 任务管理CLI
│   ├── loopbench/          # 性能基准测试
│   ├── loopwatch/          # 实时监控
│   ├── loopsim/            # 负载模拟
│   └── loopdebug/          # 调试工具
├── pkg/                    # 公共包
│   ├── api/                # REST API
│   ├── event/              # 事件系统
│   ├── plugin/             # 插件管理
│   ├── security/           # 安全认证
│   ├── server/             # 服务器管理
│   ├── skill/              # 技能管理
│   ├── utils/              # 工具函数
│   └── workflow/           # 工作流引擎
├── internal/               # 内部核心包
│   ├── config/             # 配置管理
│   └── core/               # 核心逻辑
│       ├── scheduler/      # 任务调度器
│       ├── dispatcher/     # 任务分发器
│       ├── executor/       # 执行器和LLM客户端
│       ├── sandbox/        # WASM沙箱
│       ├── observer/       # 可观测性
│       └── selfheal/       # 自愈机制
├── version/                # 版本信息
├── config/                 # 配置示例
├── integration/            # 集成测试
├── examples/               # 示例代码
└── docs/                   # 文档
```

### 配置

```yaml
# config/config.example.yaml
server:
  port: 19527
  admin_port: 19528      # 仅 loopback 的可观测性监听器
  host: "0.0.0.0"

plugins:
  dir: "~/.loopworker/plugins"

data:
  dir: "~/.loopworker/data"
```

`loopworker doctor` 会打印每项配置的来源（`file:` / `env:` / `default`），
以及哪里不对、怎么改。

#### 鉴权

未配置任何凭据时，服务器启动日志会打印一把一次性的 bootstrap key。它是
易失的：每次重启都会变，且当服务器只持有这种 key 又要绑定公网地址时，
它会**拒绝启动**。要长期使用，请提供正式凭据：

| 变量 | 含义 |
|------|------|
| `LOOPWORKER_API_KEYS` | `id:role:sha256hex` 条目，逗号分隔。key 是明文的 SHA-256 十六进制摘要。 |
| `LOOPWORKER_API_KEYS_PLAIN` | 同上，但写明文 `id:role:lwk_...`，服务器立即哈希。首次运行更省事。 |
| `LOOPWORKER_AUTH_SIGNING_SECRET` | bearer token 的 HMAC 密钥。用 `POST /api/v1/auth/token` 必需。 |
| `LOOPWORKER_AUTH_TOKEN_TTL` | bearer token 有效期，例如 `1h`。 |

角色为 `admin`、`operator`、`viewer`。配置格式错误是**启动错误**而不是警告 ——
服务器不会退回默认配置。

```bash
export LOOPWORKER_API_KEYS="ci:operator:$(printf '%s' "$MY_KEY" | sha256sum | cut -d' ' -f1)"
loopworker --config config.yaml

curl http://localhost:19527/api/v1/tasks -H "X-API-Key: $MY_KEY"
```

API key 通过 `X-API-Key` 头发送，或先用 `POST /api/v1/auth/token` 换成
bearer token。bearer token 以 `lwt_` 开头；把 API key 放进
`Authorization: Bearer` 会被拒绝。

### API端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /healthz | 健康检查（匿名） |
| GET | /api/v1/health | 健康检查（匿名） |
| GET | /api/v1/openapi.json | OpenAPI 文档（匿名） |
| POST | /api/v1/auth/token | 用 API key 换取 bearer token |
| GET | /api/v1/auth/whoami | 查看调用者身份与角色 |
| GET | /api/v1/workers | worker 列表 |
| GET | /api/v1/tasks | 任务列表 |
| POST | /api/v1/tasks | 创建任务 |
| GET | /api/v1/tasks/{taskID} | 获取任务 |
| DELETE | /api/v1/tasks/{taskID} | 删除任务 |
| POST | /api/v1/tasks/{taskID}/cancel | 取消任务 |
| POST | /api/v1/tasks/{taskID}/dependencies | 添加依赖边 |
| GET | /api/v1/workflow/list | 工作流列表 |
| GET | /api/v1/workflow/graph | 依赖图快照 |
| GET | /api/v1/workflow/{workflowID} | 获取工作流 |
| POST | /api/v1/workflow/execute | 执行工作流 |
| GET | /api/v1/events/live | SSE事件流 |
| POST/GET | /api/v1/auth/keys，DELETE /api/v1/auth/keys/{keyID} | 签发与吊销密钥（admin） |

指标与日志**不在**此端口上。它们在仅监听 loopback 的 admin 监听器上
（默认 `127.0.0.1:19528`，可用 `server.admin_port` 修改）：`/metrics`、
`/runtime/stats`、`/logs`、`/events/stats`、`/healthz`，且需要 admin 凭据。

该表格与 `pkg/api/openapi.json` 同源生成；两者一旦不一致，
`pkg/api` 的 `TestOpenAPISpecMatchesRegisteredRoutes` 会让构建变红。

### 许可证

MIT

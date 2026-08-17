# LoopWorker

[English](#english) | [中文](#中文)

---

## English

A Go-native, reusable, extensible, efficient, and lightweight work loop engine with WASM sandbox, priority scheduling, self-healing mechanisms, and cutting-edge workflow paradigms.

### Features

- **Event-Driven Architecture** - Typed event bus with backpressure and metrics
- **Priority Scheduling** - 4-level priority queues (Low/Normal/High/Critical)
- **Task Dependencies** - DAG dependency management with auto-unlock
- **WASM Sandbox** - Plugin isolation with resource limits and concurrency control
- **Self-Healing** - Circuit breaker, exponential backoff, health checks
- **Workflow Engine** - Sequential, DAG, and parallel workflow patterns
- **Security** - RBAC, rate limiting, input validation, audit logging
- **Observability** - Metrics, distributed tracing, structured logging
- **Liquid Glass UI** - Apple-inspired design with non-linear animations

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

### CLI Tools

| Tool | Description |
|------|-------------|
| `loopworker` | Main server |
| `loopctl` | Task & workflow management CLI |
| `loopdebug` | Debugging & diagnostics |
| `loopwatch` | Real-time monitoring |
| `loopbench` | Performance benchmarking |
| `loopsim` | Load simulation |

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
  host: "0.0.0.0"

plugins:
  dir: "~/.loopworker/plugins"

data:
  dir: "~/.loopworker/data"
```

### API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /api/v1/health | Health check |
| GET | /api/v1/tasks | List tasks |
| POST | /api/v1/tasks | Create task |
| GET | /api/v1/tasks/:id | Get task |
| DELETE | /api/v1/tasks/:id | Delete task |
| GET | /api/v1/workflow/list | List workflows |
| POST | /api/v1/workflow/execute | Execute workflow |
| GET | /api/v1/metrics | Get metrics |
| GET | /api/v1/logs | Get logs |
| GET | /api/v1/events/live | SSE event stream |

### License

MIT

---

## 中文

一个Go原生的可复用、可扩展、高效轻量的工作循环引擎，支持WASM沙箱、优先级调度、自愈机制和前沿工作流范式。

### 功能特性

- **事件驱动架构** - 类型化事件总线，支持背压处理和指标统计
- **优先级调度** - 四级优先级队列（低/普通/高/关键）
- **任务依赖** - DAG依赖管理，自动解锁下游任务
- **WASM沙箱** - 插件隔离执行，资源限制和并发控制
- **自愈机制** - 熔断器、指数退避、健康检查
- **工作流引擎** - 顺序、DAG和并行工作流模式
- **安全防护** - RBAC、速率限制、输入验证、审计日志
- **可观测性** - 指标收集、分布式追踪、结构化日志
- **液态玻璃UI** - Apple风格设计，非线性动画

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

### 命令行工具

| 工具 | 说明 |
|------|------|
| `loopworker` | 主服务器 |
| `loopctl` | 任务、工作流管理CLI |
| `loopdebug` | 调试工具 |
| `loopwatch` | 实时监控工具 |
| `loopbench` | 性能基准测试 |
| `loopsim` | 负载模拟 |

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
  host: "0.0.0.0"

plugins:
  dir: "~/.loopworker/plugins"

data:
  dir: "~/.loopworker/data"
```

### API端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /api/v1/health | 健康检查 |
| GET | /api/v1/tasks | 任务列表 |
| POST | /api/v1/tasks | 创建任务 |
| GET | /api/v1/tasks/:id | 获取任务 |
| DELETE | /api/v1/tasks/:id | 删除任务 |
| GET | /api/v1/workflow/list | 工作流列表 |
| POST | /api/v1/workflow/execute | 执行工作流 |
| GET | /api/v1/metrics | 获取指标 |
| GET | /api/v1/logs | 获取日志 |
| GET | /api/v1/events/live | SSE事件流 |

### 许可证

MIT

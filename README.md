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
go build ./cmd/loopworker/

# Run TUI mode
./loopworker -tui

# Run Web mode
./loopworker -port 19527

# Initialize project
./loopctl init
```

### CLI Tools

| Tool | Description |
|------|-------------|
| `loopworker` | Main server (Web) |
| `loopctl` | Task, workflow, and configuration management CLI |
| `loopbench` | Performance benchmarking tool |
| `loopwatch` | Real-time monitoring tool (metrics, logs, health) |
| `loopsim` | Load simulation and stress testing tool |
| `loopdebug` | Debugging tool (task inspection, tracing, diagnostics) |

### Project Structure

```
loopWorker-for-go/
├── cmd/                    # CLI tools
│   ├── loopworker/         # Main execution loop server
│   ├── loopctl/            # Task & workflow management CLI
│   ├── loopbench/          # Performance benchmarking
│   ├── loopwatch/          # Real-time monitoring
│   ├── loopsim/            # Load simulation
│   └── loopdebug/          # Debugging & diagnostics
├── pkg/                    # Public packages
│   ├── event/              # Event system
│   ├── plugin/             # Plugin management
│   ├── workflow/           # Workflow engine
│   ├── security/           # Security (bcrypt, RBAC)
│   ├── api/                # REST API
│   ├── config/             # Configuration
│   ├── ui/                 # UI components
│   └── dashboard/          # Web dashboard
├── internal/               # Internal core packages
│   ├── config/             # Config management
│   └── core/               # Core loop logic
│       ├── scheduler/      # Task scheduler with SQLite persistence
│       ├── dispatcher/     # Task dispatcher
│       ├── executor/       # Worker pool and Sandbox orchestration
│       ├── sandbox/        # WASM sandbox with strict limits
│       ├── observer/       # Observability
│       └── selfheal/       # Self-healing & Watchdog mechanisms
├── integration/            # Full platform integration tests
├── test/                   # Benchmark and E2E tests
├── docs/                   # Documentation
│   ├── api/                # API reference
│   └── guides/             # User guides
├── examples/               # Example workflows and configurations
└── plugins/                # Plugin directory
```

### Configuration

```json
{
  "general": {
    "language": "en",
    "port": 19527,
    "plugins_dir": "./plugins",
    "data_dir": "./data"
  },
  "appearance": {
    "theme": "glass",
    "font_size": 15,
    "animations": true
  },
  "security": {
    "require_auth": true,
    "token_expiry": 24
  }
}
```

### API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /api/tasks | List tasks |
| POST | /api/tasks | Create task |
| GET | /api/metrics | Get metrics |
| GET | /api/logs | Get logs |
| GET | /api/health | Health check |
| GET | /events | SSE event stream |

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
go build ./cmd/loopworker/

# TUI模式运行
./loopworker -tui

# Web模式运行
./loopworker -port 19527

# 初始化项目
./loopctl init
```

### 命令行工具

| 工具 | 说明 |
|------|------|
| `loopworker` | 主服务（Web） |
| `loopctl` | 任务、工作流和配置管理CLI |
| `loopbench` | 性能基准测试工具 |
| `loopwatch` | 实时监控工具（指标、日志、健康状态） |
| `loopsim` | 负载模拟和压力测试工具 |
| `loopdebug` | 调试工具（任务检查、追踪、诊断） |

### 项目结构

```
loopWorker-for-go/
├── cmd/                    # 命令行工具
│   ├── loopworker/         # 核心主循环服务
│   ├── loopctl/            # 任务与工作流管理CLI
│   ├── loopbench/          # 性能基准测试
│   ├── loopwatch/          # 实时监控
│   ├── loopsim/            # 负载模拟
│   └── loopdebug/          # 调试与诊断
├── pkg/                    # 公共包
│   ├── event/              # 事件总线系统
│   ├── plugin/             # 插件生命周期管理
│   ├── workflow/           # 工作流引擎
│   ├── security/           # 认证与授权（bcrypt、RBAC）
│   ├── api/                # REST API 路由
│   ├── config/             # 全局配置解析
│   ├── ui/                 # 终端 UI 组件
│   └── dashboard/          # Web 可视化仪表板
├── internal/               # 内部核心包 (不对外暴露)
│   ├── config/             # 内部配置定义
│   └── core/               # 核心调度逻辑层
│       ├── scheduler/      # 任务队列调度器 (支持 SQLite 持久化与死信队列)
│       ├── dispatcher/     # 无锁事件驱动派发器
│       ├── executor/       # 工作节点执行器与看门狗
│       ├── sandbox/        # WASM 沙箱 (支持信号量并发与硬性资源管控)
│       ├── observer/       # 可观测性与埋点
│       └── selfheal/       # 自愈与降级机制 (熔断器)
├── integration/            # 全平台集成测试
├── test/                   # 性能基准与 E2E 测试
├── docs/                   # 架构与设计文档
│   ├── api/                # API 参考文档
│   └── guides/             # 用户指南
├── examples/               # 示例工作流与配置模板
└── plugins/                # 插件目录
```

### 配置

```json
{
  "general": {
    "language": "zh",
    "port": 19527,
    "plugins_dir": "./plugins",
    "data_dir": "./data"
  },
  "appearance": {
    "theme": "glass",
    "font_size": 15,
    "animations": true
  },
  "security": {
    "require_auth": true,
    "token_expiry": 24
  }
}
```

### API端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /api/tasks | 获取任务列表 |
| POST | /api/tasks | 创建任务 |
| GET | /api/metrics | 获取指标 |
| GET | /api/logs | 获取日志 |
| GET | /api/health | 健康检查 |
| GET | /events | SSE事件流 |

### 许可证

MIT

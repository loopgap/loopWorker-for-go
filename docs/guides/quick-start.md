# LoopWorker 快速开始指南

本指南将帮助您快速上手 LoopWorker，从安装到运行第一个任务。

## 目录

- [安装](#安装)
- [构建](#构建)
- [运行](#运行)
- [创建插件](#创建插件)
- [执行任务](#执行任务)
- [查看指标](#查看指标)
- [配置说明](#配置说明)

## 安装

### 前置要求

- Go 1.21 或更高版本
- Git

### 克隆项目

```bash
git clone https://github.com/your-org/loopWorker-for-go.git
cd loopWorker-for-go
```

## 构建

```bash
# 构建主程序
go build -o loopworker ./cmd/loopworker/

# 或者使用 go install
go install ./cmd/loopworker/
```

## 运行

### 基本运行

```bash
# 使用默认配置运行
./loopworker

# 指定端口运行
./loopworker -port 8080

# 指定配置文件
./loopworker -config /path/to/config.yaml
```

### 配置文件

创建 `~/.loopworker/config.yaml`：

```yaml
port: 19527
plugins_dir: ~/.loopworker/plugins
data_dir: ~/.loopworker/data
log_level: info
sandbox:
  max_memory_mb: 256
  max_cpu_seconds: 30
  max_output_mb: 64
  max_concurrent: 10
workers:
  count: 4
```

## 创建插件

### 插件接口

所有插件必须实现 `sandbox.Plugin` 接口：

```go
type Plugin interface {
    Name() string
    Version() string
    RequiredSkills() []string
    Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)
}
```

### 示例插件

创建 `plugins/echo/main.go`：

```go
package main

import (
    "context"
    "fmt"

    "loopworker/pkg/skill"
)

type EchoPlugin struct{}

func (p *EchoPlugin) Name() string {
    return "echo"
}

func (p *EchoPlugin) Version() string {
    return "1.0.0"
}

func (p *EchoPlugin) RequiredSkills() []string {
    return []string{}
}

func (p *EchoPlugin) Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
    output := fmt.Sprintf("Echo: %s", string(input))
    return []byte(output), nil
}

func main() {
    // 插件入口点
}
```

## 执行任务

### 使用 REST API

```bash
# 创建任务
curl -X POST http://localhost:19527/api/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "type": "echo",
    "input": "Hello, LoopWorker!",
    "priority": 1
  }'

# 查看任务状态
curl http://localhost:19527/api/tasks/<task-id>

# 列出所有任务
curl http://localhost:19527/api/tasks
```

### 使用 Go SDK

```go
package main

import (
    "context"
    "fmt"
    "log"

    "loopworker/internal/core/sandbox"
    "loopworker/pkg/event"
    "loopworker/pkg/skill"
)

func main() {
    // 创建事件总线
    bus := event.NewEventBus(nil)
    defer bus.Close()

    // 创建沙箱
    sb := sandbox.NewSandbox(sandbox.SandboxConfig{
        MaxMemoryMB:   256,
        MaxCPUSeconds: 30,
        MaxOutputMB:   64,
        MaxConcurrent: 10,
    })
    sb.SetEventBus(bus)

    // 加载插件
    plugin := &EchoPlugin{}
    if err := sb.LoadPlugin("echo", plugin); err != nil {
        log.Fatalf("Failed to load plugin: %v", err)
    }

    // 创建技能上下文
    skillRegistry := skill.NewSkillRegistry()
    skillConfig := map[string]interface{}{}
    skillCtx := skillRegistry.BuildContext(nil, bus, nil, skillConfig)

    // 执行任务
    ctx := context.Background()
    input := []byte("Hello, LoopWorker!")
    output, err := sb.Execute(ctx, "echo", input, skillCtx)
    if err != nil {
        log.Fatalf("Failed to execute task: %v", err)
    }

    fmt.Printf("Output: %s\n", string(output))
}
```

## 查看指标

### REST API 端点

```bash
# 获取指标
curl http://localhost:19527/api/metrics

# 获取日志
curl http://localhost:19527/api/logs

# 健康检查
curl http://localhost:19527/api/health

# SSE 事件流
curl http://localhost:19527/events
```

### 指标示例

```json
{
  "tasks_created": 100,
  "tasks_completed": 95,
  "tasks_failed": 5,
  "avg_execution_time": "1.2s",
  "active_workers": 4,
  "queue_size": 10
}
```

## 配置说明

### 完整配置示例

```yaml
# 服务配置
port: 19527
plugins_dir: ~/.loopworker/plugins
data_dir: ~/.loopworker/data
work_dir: ~/.loopworker
log_level: info

# 沙箱配置
sandbox:
  max_memory_mb: 256        # 最大内存限制（MB）
  max_cpu_seconds: 30       # 最大CPU时间（秒）
  max_output_mb: 64         # 最大输出大小（MB）
  max_concurrent: 10        # 最大并发数

# Worker配置
workers:
  count: 4                  # Worker数量

# 安全配置
security:
  require_auth: true        # 是否需要认证
  token_expiry: 24          # Token过期时间（小时）

# 外观配置
appearance:
  theme: glass              # 主题（glass/dark/light）
  font_size: 15             # 字体大小
  animations: true          # 是否启用动画
```

### 环境变量

所有配置都可以通过环境变量覆盖：

```bash
export LOOPWORKER_PORT=8080
export LOOPWORKER_PLUGINS_DIR=/path/to/plugins
export LOOPWORKER_DATA_DIR=/path/to/data
export LOOPWORKER_LOG_LEVEL=debug
export LOOPWORKER_WORKERS=8
export LOOPWORKER_SANDBOX_MAX_MEMORY=512
export LOOPWORKER_SANDBOX_MAX_CPU_SECONDS=60
export LOOPWORKER_SANDBOX_MAX_CONCURRENT=20
```

## 下一步

- 查看 [API 参考文档](../api/api-reference.md) 了解完整的 API 端点
- 查看 [架构设计文档](../architecture.md) 了解系统架构
- 查看 [示例代码](../../examples/) 了解更多使用示例

## 常见问题

### Q: 如何调试插件？

A: 设置日志级别为 debug：

```bash
./loopworker -log-level debug
```

### Q: 如何扩展 Worker 数量？

A: 修改配置文件中的 `workers.count` 或设置环境变量：

```bash
export LOOPWORKER_WORKERS=8
```

### Q: 如何持久化任务数据？

A: 任务数据自动持久化到 `data_dir` 目录下的 SQLite 数据库。

### Q: 如何监控系统状态？

A: 使用 `/api/metrics` 和 `/api/health` 端点，或查看 `/events` SSE 流。
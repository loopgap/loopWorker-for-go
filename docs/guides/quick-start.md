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

- Go 1.26.6 或更高版本（`go.mod` 的 `go` 指令；1.21 之类的旧版本会直接拒绝编译）
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

创建 `~/.loopworker/config.yaml`（键名见下文「配置说明」；**写错的键会让启动
直接失败**，并列出全部合法键）：

```yaml
server:
  port: 19527
plugins:
  dir: ~/.loopworker/plugins
data:
  dir: ~/.loopworker/data
logging:
  level: info
sandbox:
  max_memory_mb: 256
  max_cpu_seconds: 30
  max_output_mb: 64
  max_concurrent: 10
workers:
  count: 4
```

## 创建插件

LoopWorker **没有内置任务实现**：插件目录里必须有一个 `plugin.json` 和一个
`.wasm` 模块，否则每个任务都会进 `dead_letter`，错误是 `plugin not found`。

```bash
GOOS=wasip1 GOARCH=wasm go build -o hello.wasm ./examples/hello-plugin/

mkdir -p ~/.loopworker/plugins/hello
cp hello.wasm examples/hello-plugin/plugin.json ~/.loopworker/plugins/hello/
```

### 插件清单

`plugin.json` 的字段（见 `examples/hello-plugin/plugin.json`）：

```json
{
  "name": "hello",
  "version": "0.1.0-beta.1",
  "description": "Echoes its input.",
  "entry": "hello.wasm"
}
```

目录名不必等于插件名；任务里用的是清单里的 `name`。

### 宿主内实现的插件接口

WASM 插件由沙箱执行。若你要在 Go 代码里**直接**注册一个实现体，接口是
`internal/core/sandbox.Plugin`：

```go
type Plugin interface {
    Name() string
    Version() string
    RequiredSkills() []string
    Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)
}
```

注意它在 `internal/` 下：**模块外的 Go 程序无法导入它**。产品形态是服务 +
WASM 插件，不是可嵌入的库（`docs/USAGE.md` 里的 Go 示例只在仓库内部成立）。

## 执行任务

### 使用 REST API

路径带 `/api/v1` 前缀，且**除三个探针外每个端点都要凭据**：

```bash
API_KEY=<your-key>

# 创建任务
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{"type": "hello", "input": "Hello, LoopWorker!", "priority": 1}'

# 查看任务状态
curl -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/tasks/<task-id>

# 列出所有任务
curl -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/tasks
```

匿名可用的只有 `GET /healthz`、`GET /api/v1/health`、`GET /api/v1/openapi.json`；
其余缺凭据一律 `401`（实测）。

或者用 `loopctl`，它读 `LOOPWORKER_API_KEY`：

```bash
export LOOPWORKER_API_KEY=<your-key>
loopctl task create --type hello --input "Hello, LoopWorker!"
loopctl task list
loopctl task get <task-id>
```

### 使用 Go 客户端

对外可导入的只有 `loopworker/pkg/client`（`internal/` 下的包对模块外不可见）：

```go
c := client.NewAPIClient("")            // 读 LOOPWORKER_URL / LOOPWORKER_API_KEY
task, err := c.CreateTask("hello", "Hello, LoopWorker!", 1)
// 之后用 c.GetTask(id) 自己轮询到终态；没有 WaitForTask
```

完整示例见 [API 参考文档](../api/api-reference.md#go-sdk)。

## 查看指标

### 端点

```bash
API_KEY=<your-key>

# 健康检查（API 端口，匿名可用）
curl http://localhost:19527/api/v1/health

# SSE 事件流（需凭据）
curl -N -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/events/live

# 指标与日志：不在 API 端口上，在 loopback admin 监听器（默认 19528），
# 且需要 admin 凭据。API 端口上的 /api/metrics 与 /api/logs 都是 404。
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:19528/metrics
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:19528/runtime/stats

# /logs 也在这条监听器上，但生产中恒返回 {"logs":[]}：observer 的环形缓冲
# 无人写入。看日志请读进程的标准输出/错误，别把这个空数组当故障排查。
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:19528/logs
```

### 指标示例

`/metrics` 是 Prometheus 文本，不是 JSON：

```
# HELP loopworker_tasks_created_total Total number of tasks created
# TYPE loopworker_tasks_created_total counter
loopworker_tasks_created_total{type="hello"} 1
```

JSON 形态的运行时数据在 admin 监听器的 `/runtime/stats`。

## 配置说明

### 完整配置示例

以下是**全部合法的配置键**。写错任何一个键，服务启动就会失败并把这份清单打出来
（实测：`unknown configuration key "appearance.animations"`）：

```yaml
# 基础目录（plugins.dir / data.dir 都相对它解析）
work_dir: ~/.loopworker

# 服务
server:
  host: "127.0.0.1"                # "0.0.0.0" 需要已配置凭据，否则拒绝绑定
  port: 19527
  read_timeout: 30s
  write_timeout: 0s                 # 0 关闭写超时，SSE 才能长连
  shutdown_timeout: 10s
  admin_port: 19528                 # admin 监听器固定绑 127.0.0.1

# 数据与插件
data:
  dir: ~/.loopworker/data
  db_file: loopworker_tasks.db
plugins:
  dir: ~/.loopworker/plugins
  auto_load: true
  verify_checksum: false            # 打开后按清单声明的 sha256 校验 .wasm

# 日志
logging:
  level: info                       # debug/info/warn/error
  format: text                      # text/json
  output: stdout

# 沙箱
sandbox:
  max_memory_mb: 256                # 上限，清单只能收紧不能放宽
  max_cpu_seconds: 30
  max_output_mb: 64
  max_concurrent: 10
  allowed_hosts: []                 # 空 = wasm 插件可调用任意 URL

# Worker
workers:
  count: 4
  task_timeout: 30m

# 认证
security:
  enabled: true                     # 无生产消费者，见 API 参考「认证」
  auth_required: false              # 只在未配 api_key 时拒绝启动
  api_key: ""                       # 单把永久 admin 密钥

# 自愈
selfheal:
  enabled: true
  circuit_breaker:
    threshold: 5
    timeout: 30s
  retry:
    max_attempts: 3
    backoff: 1s

# 工作流（这两个键被接受但本版本未强制执行，启动摘要会报 unapplied_keys）
workflow:
  max_concurrent: 10
  timeout: 5m

# LLM
llm:
  base_url: https://api.openai.com/v1
  api_key: ""
  model: ""
```

`port`、`plugins_dir`、`data_dir`、`log_level`、`workers` 是合法的**别名**，
映射到 `server.port`、`plugins.dir`、`data.dir`、`logging.level`、`workers.count`。

**不存在**的键（写进配置会让启动失败）：`security.require_auth`、
`security.token_expiry`、以及整个 `appearance` 段 —— 主题/字号/动画不是服务端配置，
要改画布请改 `web/canvas` 前端。

### 环境变量

优先级 **flag > 环境变量 > 配置文件 > 默认值**。清单在
`internal/config/spec.go`：

```bash
export LOOPWORKER_SERVER_HOST=127.0.0.1
export LOOPWORKER_PORT=19527                          # 或 LOOPWORKER_SERVER_PORT
export LOOPWORKER_API_ADMIN_PORT=19528
export LOOPWORKER_PLUGINS_DIR=/path/to/plugins
export LOOPWORKER_DATA_DIR=/path/to/data
export LOOPWORKER_LOG_LEVEL=debug
export LOOPWORKER_WORKERS=8                           # 或 LOOPWORKER_WORKERS_COUNT
export LOOPWORKER_SANDBOX_MAX_MEMORY=512
export LOOPWORKER_SANDBOX_MAX_CPU_SECONDS=60
export LOOPWORKER_SANDBOX_MAX_CONCURRENT=20
export LOOPWORKER_PLUGINS_VERIFY_CHECKSUM=true
export LOOPWORKER_API_KEYS="ops:admin:<sha256hex>"    # 注册凭据
```

`server.admin_host` **不可配置**：admin 监听器暴露 `/metrics` 和 `/logs`，
把它挪到可路由地址等于把它们交给任何能路由到该端口的人。

## 下一步

- 查看 [API 参考文档](../api/api-reference.md) 了解完整的 API 端点
- 查看 [架构设计文档](../architecture.md) 了解系统架构
- 查看 [示例代码](../../examples/) 了解更多使用示例

## 常见问题

### Q: 如何调试插件？

A: `loopworker doctor` 报告插件目录里找到了什么、为什么拒绝某个目录；
设日志级别为 debug 看得更细：

```bash
./loopworker -log-level debug
./loopworker doctor
```

### Q: 如何扩展 Worker 数量？

A: 修改配置文件中的 `workers.count` 或设置环境变量：

```bash
export LOOPWORKER_WORKERS=8
```

### Q: 如何持久化任务数据？

A: 任务数据自动持久化到 `data.dir` 目录下的 SQLite 数据库
（`loopworker_tasks.db`，纯 Go 驱动 `modernc.org/sqlite`，不需要 CGO）。
`loopworker storage` 报告它在哪儿、多大、多少行；`loopworker backup <目标>`
写一份一致的副本。

### Q: 如何监控系统状态？

A: `GET /api/v1/health`（匿名可用）与 `GET /api/v1/events/live` SSE 流。
指标和日志在 admin 监听器上（`http://127.0.0.1:19528/metrics`、`/logs`），
需要 admin 凭据。
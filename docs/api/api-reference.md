# LoopWorker API 参考文档

本文档描述了 LoopWorker REST API 的所有端点、请求参数和响应格式。

## 目录

- [概述](#概述)
- [认证](#认证)
- [任务管理](#任务管理)
- [工作流管理](#工作流管理)
- [系统监控](#系统监控)
- [事件流](#事件流)
- [错误处理](#错误处理)

## 概述

### 基础URL

```
http://localhost:19527/api/v1
```

### 响应格式

所有响应都使用 JSON 格式：

```json
{
  "success": true,
  "data": { ... },
  "error": null
}
```

错误响应：

```json
{
  "success": false,
  "data": null,
  "error": {
    "code": "INVALID_REQUEST",
    "message": "Task type is required"
  }
}
```

## 认证

本节与 `pkg/api/openapi.json` 同源；该文件由 `TestOpenAPISpecMatchesRegisteredRoutes`
与 `pkg/api/openapi_test.go` 双向锁定，路由漂移会让构建变红。

**匿名可访问的端点只有三个**：`GET /healthz`、`GET /api/v1/health`、
`GET /api/v1/openapi.json`。其余每个端点都需要凭据，缺凭据返回 `401`。

### 发送 API Key

```
X-API-Key: lwk_...
```

启动时日志会打印一把一次性的 admin 密钥（仅当进程没有配置任何凭据时）。
永久密钥来自环境变量 `LOOPWORKER_API_KEYS`（`id:role:sha256hex`），
或运行时由 admin 签发。角色为 `admin` / `operator` / `viewer`。

### 换取 Bearer Token

**POST** `/api/v1/auth/token` （需要 admin 角色）

请求：

```json
{}
```

响应：

```json
{
  "success": true,
  "data": {
    "token": "lwt_...",
    "expires_at": "2026-10-06T03:11:00Z"
  }
}
```

### 使用 Token

在请求头中添加：

```
Authorization: Bearer lwt_...
```

注意 token 前缀是 `lwt_`，与 API key 的 `lwk_` 不同。把 API key 放进
`Authorization: Bearer` 会被拒绝（401）—— API key 只能走 `X-API-Key` 头。

### 签发与吊销密钥

**POST** `/api/v1/auth/keys** （admin，明文只在响应里出现一次）
**GET** `/api/v1/auth/keys** （admin，列表不含密钥材料）
**DELETE** `/api/v1/auth/keys/{keyID}` （admin，同时吊销该密钥签发的 token）

## 任务管理

### 创建任务

**POST** `/api/v1/tasks`

请求：

```json
{
  "type": "echo",
  "input": "Hello, World!",
  "priority": 1,
  "config": {
    "timeout": 30
  },
  "metadata": {
    "source": "api"
  }
}
```

参数说明：

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| type | string | 是 | 任务类型（插件名称） |
| input | string | 是 | 任务输入数据 |
| priority | int | 否 | 优先级（0-3，默认1） |
| config | object | 否 | 任务配置 |
| metadata | object | 否 | 元数据 |

响应：

```json
{
  "success": true,
  "data": {
    "id": "task-1704067200000-1",
    "type": "echo",
    "state": "pending",
    "priority": 1,
    "created_at": "2024-01-01T00:00:00Z"
  }
}
```

### 获取任务

**GET** `/api/v1/tasks/{taskId}`

响应：

```json
{
  "success": true,
  "data": {
    "id": "task-1704067200000-1",
    "type": "echo",
    "state": "completed",
    "priority": 1,
    "input": "Hello, World!",
    "output": "Echo: Hello, World!",
    "created_at": "2024-01-01T00:00:00Z",
    "started_at": "2024-01-01T00:00:01Z",
    "completed_at": "2024-01-01T00:00:02Z",
    "duration": "1s"
  }
}
```

### 列出任务

**GET** `/api/v1/tasks`

查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| state | string | 任务状态（pending/queued/running/completed/failed） |
| type | string | 任务类型 |
| priority | int | 优先级 |
| limit | int | 返回数量限制 |
| offset | int | 分页偏移 |

响应：

```json
{
  "success": true,
  "data": {
    "tasks": [
      {
        "id": "task-1704067200000-1",
        "type": "echo",
        "state": "completed",
        "priority": 1,
        "created_at": "2024-01-01T00:00:00Z"
      }
    ],
    "total": 100,
    "limit": 20,
    "offset": 0
  }
}
```

### 取消任务

**POST** `/api/v1/tasks/{taskId}/cancel`

响应：

```json
{
  "success": true,
  "data": {
    "id": "task-1704067200000-1",
    "state": "cancelled"
  }
}
```

### 删除任务

**DELETE** `/api/v1/tasks/{taskId}`

响应：

```json
{
  "success": true,
  "data": null
}
```

## 工作流管理

两个内置工作流在**零配置**下即可使用：`builtin.anomaly-review`（DAG：两条分支
加一个 join，不��要插件）和 `builtin.plugin-smoke`（先准备负载，再在已加载的插件上跑一个真实任务）。
也可以把 JSON/YAML 定义放进 `work_dir/workflows/`，同名定义会覆盖内置的。

注意路径是**单数** `/api/v1/workflow`（旧版文档写成 `workflows`，那是错的）。

### 列出工作流

**GET** `/api/v1/workflow/list`

响应：

```json
{
  "success": true,
  "data": {
    "total": 2,
    "workflows": [
      {
        "id": "builtin.anomaly-review",
        "status": "pending",
        "step_order": ["verify", "steady", "spike"],
        "steps": [
          { "id": "steady", "depends_on": [], "runnable": true },
          { "id": "spike", "depends_on": [], "runnable": true },
          { "id": "verify", "depends_on": ["steady", "spike"], "runnable": true }
        ]
      }
    ]
  }
}
```

`step_order` 是声明顺序，不是执行顺序 —— `verify` 声明在最前但只能最后跑。

### 执行工作流

**POST** `/api/v1/workflow/execute` （需要 execute 权限）

请求体**只接受 `workflow_id`**（多余字段返回 `400 UNKNOWN_FIELD`）：

```json
{
  "workflow_id": "builtin.anomaly-review"
}
```

响应是 **202 Accepted**（异步执行，不是 200）。**没有 `Location` 头**，
轮询路径在 body 的 `poll` 字段里：

```json
{
  "success": true,
  "data": {
    "workflow_id": "builtin.anomaly-review",
    "status": "executing",
    "poll": "/api/v1/workflow/builtin.anomaly-review",
    "message": "execution started; poll the workflow until status is completed or failed"
  },
  "timestamp": "2026-10-05T20:24:08.0878902Z",
  "request_id": "1791231848087890200-2-f208d35a"
}
```

执行是异步的：202 只表示**已开始**，不表示已完成。轮询 `poll` 返回的那个路径
直到 `status` 变成 `completed` 或 `failed`。

### 获取工作流状态

**GET** `/api/v1/workflow/{workflowID}`

响应：

```json
{
  "success": true,
  "data": {
    "id": "wf-1",
    "name": "data-processing",
    "status": "completed",
    "steps": [
      { "id": "step-1", "status": "completed" },
      { "id": "step-2", "status": "completed" }
    ],
    "started_at": "2024-01-01T00:00:00Z",
    "completed_at": "2024-01-01T00:00:05Z",
    "duration": "5s"
  }
}
```

### 依赖图

**GET** `/api/v1/workflow/graph**

返回任务依赖图。环检测在**这个 HTTP 边界**上做（迭代式遍历，
成环时 `meta.cyclic_nodes` 报告而不打死进程）；`scheduler.AddDependency`
自身只拒绝自环。

## 系统监控

指标与日志**不在 API 端口上**。它们在仅监听 loopback 的 admin 监听器上
（默认 `127.0.0.1:19528`，用 `server.admin_port` 或
`LOOPWORKER_API_ADMIN_PORT` 修改），并且需要 admin 凭据。
在 API 端口上访问 `/metrics` 会得到 404 —— 那一层只有 JSON 信封响应，
Prometheus 文本端点放上去会让抓取器拿到无法解析的内容。

admin 监听器上的路径：

| 方法 | 路径 | 说明 | 格式 |
|------|------|------|------|
| GET | /metrics | Prometheus 指标 | Prometheus 文本 |
| GET | /runtime/stats | 运行时统计（JSON） | JSON |
| GET | /logs | 日志 | 见下 |
| GET | /events/stats | 事件存储统计 | JSON |
| GET | /healthz | 存活探针 | 文本 |

### 获取指标

**GET** `http://127.0.0.1:19528/metrics`

```bash
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:19528/metrics
```

响应是 **Prometheus 文本格式**（`Content-Type: text/plain`），不是 JSON：

```
# HELP loopworker_tasks_completed_total Tasks that reached the completed state.
# TYPE loopworker_tasks_completed_total counter
loopworker_tasks_completed_total 950
```

JSON 形式的运行时数据用 `/runtime/stats`：

响应：

```json
{
  "success": true,
  "data": {
    "tasks": {
      "created": 1000,
      "completed": 950,
      "failed": 50,
      "pending": 10,
      "queued": 5,
      "running": 5
    },
    "workers": {
      "active": 4,
      "idle": 2,
      "busy": 2
    },
    "performance": {
      "avg_execution_time": "1.2s",
      "max_execution_time": "10s",
      "total_execution_time": "1200s"
    },
    "system": {
      "uptime": "24h",
      "memory_used": "128MB",
      "cpu_usage": "45%"
    }
  }
}
```

### 获取日志

**GET** `http://127.0.0.1:19528/logs`

查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| level | string | 日志级别（debug/info/warn/error） |
| limit | int | 返回数量限制 |
| offset | int | 分页偏移 |
| start_time | string | 开始时间（ISO 8601） |
| end_time | string | 结束时间（ISO 8601） |

响应：

```json
{
  "success": true,
  "data": {
    "logs": [
      {
        "timestamp": "2024-01-01T00:00:00Z",
        "level": "info",
        "message": "Task completed successfully",
        "task_id": "task-1704067200000-1",
        "worker_id": "worker-1"
      }
    ],
    "total": 1000,
    "limit": 100,
    "offset": 0
  }
}
```

### 健康检查

**GET** `/api/v1/health`

响应：

```json
{
  "success": true,
  "data": {
    "status": "healthy",
    "version": "0.1.0-beta",
    "uptime": "24h",
    "components": {
      "scheduler": "healthy",
      "executor": "healthy",
      "sandbox": "healthy",
      "event_bus": "healthy"
    }
  }
}
```

## 事件流

### SSE 事件流

**GET** `/api/v1/events/live`（需凭据）

响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`。

第一帧总是打开确认，告诉客户端这个连接订阅了哪些类型：

```
event: stream.opened
data: {"caller":"ops","types":13}

: keep-alive

event: task.created
data: {"id":"task-1791212318568906700-1","type":"echo","state":"queued"}

event: task.completed
data: {"id":"task-1791212318568906700-1","state":"completed","result":"hello from wasm: aGVsbG8=","worker_id":"worker-1","duration_ms":69}
```

两个容易踩的点：

- **`data` 不是信封。** 它就是事件负载本身（与任务相关的负载会与任务视图合并）。没有 `success` / `data` / `request_id` 外层。
- **`state` 的取值是 `queued` / `running` / `completed` / `failed` / `cancelled` / `dead_letter`**，没有 `pending`。任务刚创建时是 `queued`。

### 订阅范围与过滤

`?types=` 接受逗号分隔的类型名。不带该参数时默认订阅 13 种：

```
task.created  task.started  task.completed  task.failed  task.retried  task.cancelled
plugin.executed  skill.invoked  research.finding
workflow.step.completed  workflow.started  workflow.completed  workflow.failed
```

`?types=task` 是六个任务事件的简写。不认识的值返回 `400 INVALID_REQUEST`，并在 `details.allowed` 里列出全部合法名字。

服务端记录的完整事件目录是 20 种 —— 上面 13 种之外，还有 `plugin.loaded`、`plugin.unloaded`、`worker.spawned`、`worker.exited`、`system.health`、`system.started`、`system.stopped`。**这 7 种不会出现在流上**，它们只写入磁盘上的事件存储（见 `GET /runtime/stats` 与 data 目录下的 `events/`）。

### 配额与断连

- 并发流有上限，按调用方与全局各一层（`api.max_streams_per_caller` / `api.max_streams_total`）。超限返回 `429 STREAM_LIMIT_REACHED` 并带 `Retry-After: 5`。
- 客户端断开时订阅立刻释放，不需要客户端发取消请求。
- 空闲连接每 `api.stream_keepalive`（默认 15s）收到一行 `: keep-alive` 注释帧。代理若缓冲响应，SSE 会失效 —— 这是 `X-Accel-Buffering: no` 存在的原因。
- **其他调用方的任务事件不会泄露内容**，但仍会投递一帧 `{"hidden":true,"reason":"task_owned_by_another_caller","task_id":"..."}`。这是有意设计：事件确实发生，但内容不属于订阅者。用 `?types=` 收窄订阅范围可以避免这类帧。

## 错误处理

### 错误码

| 错误码 | HTTP状态码 | 说明 |
|--------|-----------|------|
| INVALID_REQUEST | 400 | 请求参数错误 |
| UNAUTHORIZED | 401 | 未认证 |
| FORBIDDEN | 403 | 无权限 |
| NOT_FOUND | 404 | 资源不存在 |
| CONFLICT | 409 | 资源冲突 |
| INTERNAL_ERROR | 500 | 内部错误 |
| SERVICE_UNAVAILABLE | 503 | 服务不可用 |

### 错误响应示例

```json
{
  "success": false,
  "data": null,
  "error": {
    "code": "INVALID_REQUEST",
    "message": "Task type is required",
    "details": {
      "field": "type",
      "value": null
    }
  }
}
```

## 速率限制

API 请求受到速率限制：

- 匿名请求：100 次/分钟
- 认证请求：1000 次/分钟

超限时返回 `429 Too Many Requests`：

```json
{
  "success": false,
  "error": {
    "code": "RATE_LIMIT_EXCEEDED",
    "message": "Rate limit exceeded. Please try again later."
  }
}
```

## SDK 示例

### Go SDK

```go
package main

import (
    "context"
    "fmt"
    "log"

    "loopworker/pkg/client"
)

func main() {
    // 创建客户端
    c := client.NewClient("http://localhost:19527")

    // 认证
    token, err := c.Login("admin", "password123")
    if err != nil {
        log.Fatalf("Login failed: %v", err)
    }

    // 创建任务
    task, err := c.CreateTask(context.Background(), &client.CreateTaskRequest{
        Type:  "echo",
        Input: "Hello, World!",
    })
    if err != nil {
        log.Fatalf("Create task failed: %v", err)
    }

    fmt.Printf("Task created: %s\n", task.ID)

    // 等待任务完成
    result, err := c.WaitForTask(context.Background(), task.ID)
    if err != nil {
        log.Fatalf("Wait for task failed: %v", err)
    }

    fmt.Printf("Task completed: %s\n", result.Output)
}
```

### cURL 示例

```bash
# 创建任务
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "type": "echo",
    "input": "Hello, World!",
    "priority": 1
  }'

# 获取任务状态
curl http://localhost:19527/api/v1/tasks/<task-id> \
  -H "Authorization: Bearer <token>"

# 获取指标（在 admin 监听器上，loopback only，需要 admin 凭据）
curl http://127.0.0.1:19528/metrics \
  -H "X-API-Key: <admin-key>"
```
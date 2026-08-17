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

### 获取 Token

**POST** `/api/v1/auth/login`

请求：

```json
{
  "username": "admin",
  "password": "password123"
}
```

响应：

```json
{
  "success": true,
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "expires_at": "2024-01-01T00:00:00Z"
  }
}
```

### 使用 Token

在请求头中添加：

```
Authorization: Bearer <token>
```

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

### 创建工作流

**POST** `/api/v1/workflows`

请求：

```json
{
  "name": "data-processing",
  "description": "数据处理工作流",
  "steps": [
    {
      "id": "step-1",
      "name": "数据提取",
      "type": "extract",
      "config": {
        "source": "database"
      }
    },
    {
      "id": "step-2",
      "name": "数据转换",
      "type": "transform",
      "depends_on": ["step-1"]
    }
  ]
}
```

响应：

```json
{
  "success": true,
  "data": {
    "id": "workflow-1704067200000-1",
    "name": "data-processing",
    "status": "pending",
    "created_at": "2024-01-01T00:00:00Z"
  }
}
```

### 执行工作流

**POST** `/api/v1/workflows/{workflowId}/execute`

请求：

```json
{
  "input": {
    "date": "2024-01-01"
  }
}
```

响应：

```json
{
  "success": true,
  "data": {
    "execution_id": "exec-1704067200000-1",
    "status": "running"
  }
}
```

### 获取工作流状态

**GET** `/api/v1/workflows/{workflowId}`

响应：

```json
{
  "success": true,
  "data": {
    "id": "workflow-1704067200000-1",
    "name": "data-processing",
    "status": "completed",
    "steps": [
      {
        "id": "step-1",
        "status": "completed",
        "output": { ... }
      },
      {
        "id": "step-2",
        "status": "completed",
        "output": { ... }
      }
    ],
    "started_at": "2024-01-01T00:00:00Z",
    "completed_at": "2024-01-01T00:00:05Z",
    "duration": "5s"
  }
}
```

## 系统监控

### 获取指标

**GET** `/api/v1/metrics`

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

**GET** `/api/v1/logs`

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
    "version": "1.0.0",
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

**GET** `/events`

响应格式（Server-Sent Events）：

```
event: task.created
data: {"id":"task-1704067200000-1","type":"echo","state":"pending"}

event: task.started
data: {"id":"task-1704067200000-1","worker_id":"worker-1"}

event: task.completed
data: {"id":"task-1704067200000-1","output":"Echo: Hello, World!","duration":"1s"}
```

### 事件类型

| 事件类型 | 说明 |
|----------|------|
| task.created | 任务创建 |
| task.started | 任务开始执行 |
| task.completed | 任务完成 |
| task.failed | 任务失败 |
| task.cancelled | 任务取消 |
| task.retried | 任务重试 |
| worker.spawned | Worker启动 |
| worker.exited | Worker退出 |
| workflow.started | 工作流开始 |
| workflow.completed | 工作流完成 |

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

# 获取指标
curl http://localhost:19527/api/v1/metrics \
  -H "Authorization: Bearer <token>"
```
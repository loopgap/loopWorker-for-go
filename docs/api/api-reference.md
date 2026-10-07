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

所有响应都使用同一个信封（`pkg/api.Envelope`）。成功响应**没有 `error` 键**：

```json
{
  "success": true,
  "data": { "...": "..." },
  "timestamp": "2026-10-06T06:02:47.9484572Z",
  "request_id": "1791266567948457200-16-7adaa365"
}
```

错误响应把 `success` 置为 `false`、`data` 置为 `null`（省略），
并把 `request_id` 同时写进 `error`：

```json
{
  "success": false,
  "error": {
    "code": "TASK_NOT_FOUND",
    "message": "no task with id \"nope\" is visible to this credential",
    "request_id": "1791266568283944800-17-b755b187"
  },
  "timestamp": "2026-10-06T06:02:48.2839448Z",
  "request_id": "1791266568283944800-17-b755b187"
}
```

## 认证

本节与 `pkg/api/openapi.json` 同源；该文件由 `TestOpenAPISpecMatchesRegisteredRoutes`
与 `pkg/api/openapi_test.go` 双向锁定，路由漂移会让构建变红。

**匿名可访问的端点只有三个**：`GET /healthz`、`GET /api/v1/health`、
`GET /api/v1/openapi.json`。其余每个端点都需要凭据，缺凭据返回 `401`
（实测：零配置起服务，无凭据 `POST /api/v1/tasks` → `401`）。

**`security.enabled` 与 `security.auth_required` 都不控制这个行为。**
`Security.Enabled` 被读取、校验、在启动摘要里回显，然后**没有任何消费者**
（`grep -rn 'Security\.Enabled' --include=*.go` 除 config 自身的读写器外零命中）。
`AuthRequired` 全仓库只有一个消费者（`pkg/server/server.go:227`），作用是在
「要求鉴权但没给 `security.api_key`」时**拒绝启动**，不是关掉鉴权的开关。

真实规则是「配了任何凭据就强制鉴权」，`/api/v1/health` 因此恒报
`auth:"required"`。想真的不鉴权，唯一有效的办法是不配任何凭据 —— 而那会
打印一次性 bootstrap 密钥，并让非 loopback 绑定被拒绝。

### 发送 API Key

```
X-API-Key: lwk_...
```

启动时日志会打印一把一次性的 admin 密钥（仅当进程没有配置任何凭据时）。
永久密钥来自环境变量 `LOOPWORKER_API_KEYS`（`id:role:sha256hex`），
或运行时由 admin 签发。角色为 `admin` / `operator` / `viewer`。

### 换取 Bearer Token

**POST** `/api/v1/auth/token`

请求体**必须**带一把凭据，空体返回 `400 INVALID_REQUEST`：

```json
{ "api_key": "lwk_..." }
```

响应（实测）：

```json
{
  "success": true,
  "data": {
    "access_token": "lwt_eyJzdWIiOiJib290c3RyYXAiLCJyb2xlIjoiYWRtaW4iLCJpYXQiOjE3OTEyNjczODgsImV4cCI6MTc5MTI3MDk4OCwianRpIjoiNzg5MjY1NzgzNDYyNWI3NzNmZjFlNWM0IiwidmlhIjoiYXBpX2tleSJ9...",
    "expires_at": "2026-10-06T07:16:28.3514355Z",
    "expires_in": 3600,
    "role": "admin",
    "subject": "bootstrap",
    "token_type": "Bearer"
  }
}
```

字段名是 `access_token`，**不是** `token`。

### 使用 Token

在请求头中添加：

```
Authorization: Bearer lwt_...
```

注意 token 前缀是 `lwt_`，与 API key 的 `lwk_` 不同。
`Authorization: Bearer` 同时也接受 API key —— 实测把 `lwk_...` 放进 Bearer 头
在 `GET /tasks`（200）和 `POST /tasks`（201）上都通过。`X-API-Key` 是更明确的
写法，两个头不要同时用。

### 签发与吊销密钥

**POST** `/api/v1/auth/keys` （admin，明文只在响应里出现一次）
**GET** `/api/v1/auth/keys` （admin，列表不含密钥材料）
**DELETE** `/api/v1/auth/keys/{keyID}` （admin，同时吊销该密钥签发的 token）

吊销会**立刻**作废该密钥签出的每一个 bearer token：校验时服务端会拿 token 里的
subject 回查密钥表，密钥不在表里，token 即刻拒绝。这类 token 返回
`TOKEN_INVALID`（凭据被吊销），**不是** `TOKEN_EXPIRED` —— 后者的补救文案是
"重新签发 token 或换一把未过期的 API key"，对一把刚被吊销的密钥毫无意义。
若分不清是哪一种，**必须读 `error.code`，不要只看 401**。

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
| input | string / object / array | 否 | 任务输入数据；省略就是空输入（实测 `{"type":"hello"}` 返回 201） |
| input_text | string | 否 | 明文输入，与 `input` 二选一 |
| input_b64 | string | 否 | base64 输入（二进制载荷） |
| input_encoding | string | 否 | `text`（默认）或 `base64`，只作用于 `input` |
| priority | int | 否 | 优先级（0=low, 1=normal 默认, 2=high, 3=critical） |
| config | object | 否 | 任务配置 |
| metadata | object | 否 | 元数据（`owner` 是服务端字段，写了返回 403） |
| is_agent | bool | 否 | 是否是 agent 任务 |
| agent_config | object | 否 | agent 配置，仅在 `is_agent=true` 时生效 |

超出上表的键返回 `400 UNKNOWN_FIELD`，`details.accepted_fields` 列出该端点接受的键。

响应：

```json
{
  "success": true,
  "data": {
    "id": "task-1704067200000-1",
    "type": "echo",
    "state": "queued",
    "priority": 1,
    "created_at": "2024-01-01T00:00:00Z"
  }
}
```

响应是 `201 Created`，并带 `Location: /api/v1/tasks/{taskID}`（服务器填的是具体任务 ID）。刚创建的任务状态是
`queued`（`POST` 内部已经入队），不是 `pending`。

### 获取任务

**GET** `/api/v1/tasks/{taskID}`

响应（字段集见 `pkg/api.TaskView`，没有 `duration`、`output` 或 `completed_at`；
结束时间是 `ended_at`，结果是 `result` + `result_encoding`）：

```json
{
  "success": true,
  "data": {
    "id": "task-1704067200000-1",
    "type": "echo",
    "state": "completed",
    "priority": 1,
    "priority_name": "normal",
    "input": "Hello, World!",
    "input_encoding": "text",
    "result": "Echo: Hello, World!",
    "result_encoding": "text",
    "retry": 0,
    "max_retry": 3,
    "dependencies": [],
    "is_agent": false,
    "created_at": "2024-01-01T00:00:00Z",
    "started_at": "2024-01-01T00:00:01Z",
    "ended_at": "2024-01-01T00:00:02Z"
  }
}
```

开始与结束时间要自己相减；服务端不下发时长字段。

### 列出任务

**GET** `/api/v1/tasks`

查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| state | string | 任务状态；非法值返回 `400 INVALID_REQUEST`，`details.allowed` 列出全部 8 个 |
| type | string | 任务类型 |
| priority | int | 优先级（0-3） |
| limit | int | 返回数量限制，默认 50，上限 500 |
| offset | int | 分页偏移，上限 100000 |

`state` 的合法取值：`pending` / `queued` / `running` / `completed` / `failed` /
`cancelled` / `retrying` / `dead_letter`。

响应（实测）：

```json
{
  "success": true,
  "data": {
    "tasks": [ "每个元素是完整的 TaskView，与 GET 单个任务相同" ],
    "limit": 2,
    "offset": 0,
    "total": null,
    "total_available": false,
    "has_more": false,
    "next_offset": null,
    "scope": "all",
    "generated_at": "2026-10-06T06:04:33.5199972Z"
  }
}
```

`total` 是 `null` 且 `total_available:false` 表示存储层不支持计数，**不是 0**。
`scope` 在只看到自己任务的凭据下是 `own_tasks`。

### 添加依赖

**POST** `/api/v1/tasks/{taskID}/dependencies`（body：`{"dependency_id":"..."}`）

自依赖或指向不存在的任务返回 `422 DEPENDENCY_INVALID`；成环返回
`409 DEPENDENCY_CYCLE`。带未满足依赖的任务不会被入队：它停在 `pending`，
`POST /tasks` 本身仍返回 `201`。

### 取消任务

**POST** `/api/v1/tasks/{taskID}/cancel`

响应（实测）：

```json
{
  "success": true,
  "data": {
    "id": "task-1704067200000-1",
    "state": "cancelled",
    "action": "cancelled"
  }
}
```

### 删除任务

**DELETE** `/api/v1/tasks/{taskID}`

**这个路由和 cancel 是同一个处理器**（`handlers_task_mutate.go` 的 `cancelOrDelete`），
它**不删行**，只是把状态置为 `cancelled`，响应体与 cancel 完全相同：

```json
{
  "success": true,
  "data": { "id": "task-...", "state": "cancelled", "action": "cancelled" }
}
```

对已经终态的任务再调一次返回 `409 TASK_STATE_CONFLICT`；**没有任何路由返回
`"data": null` 的删除结果**。

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

响应（字段集见 `pkg/api.newWorkflowView`；**没有 `duration`**，两个时间戳要自己相减。
尚未启动的工作流两个时间戳返回空字符串，不是 `null`。下面是实测响应）：

```json
{
  "success": true,
  "data": {
    "id": "builtin.anomaly-review",
    "name": "Anomaly review (DAG: two branches, a join, no plugin or API key required)",
    "status": "completed",
    "steps": [
      {
        "id": "steady",
        "name": "Steady series, no outlier",
        "depends_on": [],
        "status": "completed",
        "timeout_ms": 0,
        "runnable": true
      }
    ],
    "step_order": ["verify", "steady", "spike"],
    "created_at": "2026-10-06T05:57:26.7989316Z",
    "started_at": "2026-10-06T06:02:47.9484572Z",
    "completed_at": "2026-10-06T06:02:47.9499667Z",
    "error": ""
  }
}
```

（上面只列了一个步骤；`steps` 会列出全部步骤，`timeout_ms` 来自该步骤的
`Timeout`，没有设置时是 `0`。）

### 依赖图

**GET** `/api/v1/workflow/graph`

返回任务依赖图（React Flow 形状：`nodes` + `edges` + `meta`）。环检测在**这个
HTTP 边界**上做（迭代式 Kahn 剥离，成环时节点 `data.in_cycle=true` 且
`meta.cyclic_nodes` 报告，而不打死进程）；`scheduler.AddDependency`
自身只拒绝自环。

非 admin 凭据只看到自己的任务（`meta.scope` 会变成 `own_tasks`）。
节点数超过 `api.graph_max_nodes`（默认 5000）时截断，`meta.truncated=true`。

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
| GET | /logs | observer 环形日志快照（生产中恒为空，见下） | JSON |
| GET | /events/stats | 事件存储统计 | JSON |
| GET | /statusz | 进程状态（version / uptime / 组件），**裸对象无信封**，见下 | JSON |
| GET | /healthz | 存活探针 | 文本 |
| POST | /shutdown | 优雅停机，先应答再排空 | JSON |

### 获取指标

**GET** `http://127.0.0.1:19528/metrics`

```bash
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:19528/metrics
```

响应是 **Prometheus 文本格式**（`Content-Type: text/plain`），不是 JSON。
自定义指标只在 `loopworker_` 命名空间下（其余是 Go runtime 的 `go_*` 采集器），
实测输出：

```
# HELP loopworker_tasks_created_total Total number of tasks created
# TYPE loopworker_tasks_created_total counter
loopworker_tasks_created_total{type="hello"} 1
# HELP loopworker_tasks_failed_total Total number of tasks failed
# TYPE loopworker_tasks_failed_total counter
loopworker_tasks_failed_total{type="worker-4"} 1
```

Observer 注册了 5 个 `loopworker_*` 指标（`internal/core/observer/observer.go`）：
`tasks_created_total`、`tasks_started_total`、`tasks_completed_total`、
`tasks_failed_total`、`task_duration_seconds`。**带标签的计数器在标签组合第一次
出现之前不出现在 `/metrics` 里**，所以刚启动看不到 `tasks_completed_total` 是
正常的，不是没接上。

Observer 另有一套进程内的计数器（`tasks.created` / `workers.spawned` / ... ，
点分命名）走 `IncrementCounter`，**不导出 Prometheus** —— 只在 Go 侧可读。

JSON 形式的运行时数据用 `/runtime/stats`（实测响应）：

```json
{
  "success": true,
  "data": {
    "readiness": {
      "auth": "configured",
      "events": "configured",
      "lister": "configured",
      "observer": "configured",
      "tasks": "configured",
      "workers": "configured",
      "workflows": "configured"
    },
    "stats": {
      "cancelled": 0,
      "completed": 0,
      "dead_letter": 1,
      "failed": 0,
      "in_memory_queue": 0,
      "pending": 0,
      "queued": 0,
      "running": 0,
      "total": 1,
      "persistent": true,
      "storage": { "path": "...", "rows": 1, "schema_version": 2, "...": "..." }
    },
    "queue_size": 0,
    "streams": 0,
    "config": {
      "anon_rate": 100,
      "authenticated_rate": 1000,
      "window": "1m0s",
      "max_body_bytes": 10485760,
      "max_input_bytes": 8388608,
      "max_limit": 500,
      "max_streams_per_caller": 2,
      "max_streams_total": 256,
      "graph_max_nodes": 5000,
      "request_timeout": "30s",
      "trust_proxy": false,
      "allowed_origins": ["http://localhost:19527", "http://127.0.0.1:19527"]
    },
    "sampled_at": "2026-10-06T05:58:23.5104645Z"
  }
}
```

这里**没有** `uptime`、`memory_used`、`cpu_usage` 或 `avg_execution_time`：
`readiness` 报告各子系统是否接线（`configured` / `missing`），`config` 是把调用方
挡在门外的那些限流值 —— 两者都不是资源占用统计。

### 调整这些限流值

上面 `config` 块里的**字段名不是设置项**。其中一部分由环境变量控制，其余是编译期
常量，只能改代码重新构建：

| 环境变量 | 调整 | 默认 |
|---|---|---|
| `LOOPWORKER_API_ANON_RATE` | 匿名请求每窗口配额 | `100` |
| `LOOPWORKER_API_AUTH_RATE` | 带凭据请求每窗口配额 | `1000` |
| `LOOPWORKER_API_MAX_BODY_BYTES` | 请求体上限 | `10485760` |
| `LOOPWORKER_API_MAX_STREAMS` | 并发 SSE 全局上限（`max_streams_total`） | `256` |
| `LOOPWORKER_API_ALLOWED_ORIGINS` | CORS 允许来源，逗号分隔 | `http://localhost:19527, http://127.0.0.1:19527` |
| `LOOPWORKER_API_TRUST_PROXY` | 信任 `X-Forwarded-For` | `false` |

`max_streams_per_caller`（默认 2）、`graph_max_nodes`（默认 5000）、
`max_input_bytes` 和 `request_timeout` **没有对应的环境变量或配置键**，只能改
`pkg/api` 的默认值重新构建。想知道当前值就读这个 `config` 块 —— 那是它们唯一
能被看到的地方。

注意这些是**环境变量，不是配置文件键**：写进 `config.yaml` 的 `api:` 段会被
`internal/config` 当作未知键拒绝（该段不存在）。文件键与环境变量的完整对照见
[QUICKSTART 的配置表](../../docs/QUICKSTART.md#configuration)。

### 获取日志

**GET** `http://127.0.0.1:19528/logs`

**没有查询参数。** `adminLogs` 直接返回 observer 内存里现有的全部条目，
不读 `level` / `limit` / `offset` / `start_time` / `end_time`；要过滤就在客户端做。

**生产中它恒定返回空数组**，真实响应是：

```json
{ "success": true, "data": { "logs": [] } }
```

原因要写清楚，免得你把它当故障查：observer 那个最多 5000 条的环形缓冲，
唯一写入口是 `observer.Observer.Log`，而**全仓没有任何生产代码调用它** ——
应用日志走 `pkg/logger` 的 zap，不经过 observer。空数组是当前设计的结果，
不是你的配置错了。

要看日志请读进程自己的标准输出/错误（级别由 `logging.level` 控制）；
要看运行状况用 `/runtime/stats`，要看指标用 `/metrics`。

条目结构是 `observer.LogEntry`：`level`、`message`、`timestamp`，外加一个
`omitempty` 的可选 `fields` map。`fields` **没有约定键名** —— 键完全由调用
`Log()` 的一方决定，而目前没有调用方。`task_id` / `worker_id` 都不是其中
的键（`worker_id` 在整个仓库里不存在），也没有 `total` / `limit` / `offset`
—— 这是环形快照，不是查询接口。

### 进程状态

**GET** `http://127.0.0.1:19528/statusz`

**这个端点不套 `{"success":...,"data":...}` 信封**，直接返回一个裸对象——
它是本服务唯一一个这样的 JSON 端点。照着本文其它端点写客户端的人若统一
拆 `.data`，在这里会拿到 `undefined`。字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | string | 进程状态（`running` / `stopped` 等） |
| `version` | string | 版本串 |
| `uptime` | string | **Go duration 字符串**（`1h2m3.4s`），不是时间戳也不是秒数；要算请自行解析。进程尚未 `Start` 时为 `"not started"` |
| `addr` | string | API 监听地址 |
| `components` | object | `scheduler` / `executor` / `observer` / `sandbox` / `security` 五个键，值都是人读字符串（如 `"4 worker(s)"`、`"3 plugin(s)"`） |
| `stats` | object | 调度器计数，外加 `workers_running`、`workers_configured`、`events` |

需要结构化的运行数据用 `/runtime/stats`；`/statusz` 是给人看的一行摘要。
在 API 端口上访问它会得到 404。

### 优雅停机

**POST** `http://127.0.0.1:19528/shutdown`（admin）

先应答再排空：响应到达时进程已经开始停机，连接随后断开属正常。
这是给无法向进程发信号的运维用的；容器里请用 `docker stop`（镜像的
`STOPSIGNAL` 是 SIGTERM，走的是同一条优雅路径）。

### 健康检查

**GET** `/api/v1/health`

响应（实测）：

```json
{
  "success": true,
  "data": {
    "status": "ok",
    "version": "0.1.0-beta",
    "auth": "required",
    "queue_length": 0,
    "server_time": "2026-10-06T05:57:52.2531436Z"
  }
}
```

三个容易误读的点：`status` 的取值是 `ok`，不是 `healthy`；这个端点**不返回**
`uptime` 或 `components`（子系统接线状态在 admin 监听器的 `/runtime/stats`
的 `readiness` 里）；`auth` 恒为 `required` —— 即使 `security.auth_required=false`
或 `security.enabled=false`，配了任何凭据时 `pkg/api` 都强制鉴权。

## 事件流

### SSE 事件流

**GET** `/api/v1/events/live`（需凭据）

响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`。

第一帧总是打开确认，告诉客户端这个连接订阅了哪些类型：

实测帧（`?types=task.created,task.completed`）：

```
event: stream.opened
data: {"caller":"bootstrap","types":2}

event: task.created
data: {"Config":null,"TaskID":"task-1791267028712016300-2","TaskType":"hello","task":{"id":"task-1791267028712016300-2","type":"hello","state":"pending","priority":1,"priority_name":"normal","owner":"bootstrap","created_at":"2026-10-06T06:10:28.7120163Z","input":"x","input_encoding":"text","retry":0,"max_retry":3,"dependencies":[],"is_agent":false}}
```

三个容易踩的点：

- **`data` 不是信封。** 没有 `success` / `data` / `request_id` 外层，事件负载就是 `data`。
- **任务视图被塞在嵌套的 `task` 键里，不是平铺。** `handlers_events.go` 的
  `mergeJSONObjects` 把 `TaskView` 放进 `data.task`；平铺的字段是事件自己的
  `TaskID` / `TaskType` / `TaskState`（结构体字段名，**大写开头**）。
- **`task.state` 在 `task.created` 帧里是 `pending`**，事件发布早于入队；
  `POST /tasks` 的响应里才是 `queued`。取值域是
  `pending` / `queued` / `running` / `completed` / `failed` / `cancelled` /
  `retrying` / `dead_letter`。

### 订阅范围与过滤

`?types=` 接受逗号分隔的类型名。不带该参数时默认订阅 13 种：

```
task.created  task.started  task.completed  task.failed  task.retried  task.cancelled
plugin.executed  skill.invoked  research.finding
workflow.step.completed  workflow.started  workflow.completed  workflow.failed
```

`?types=task` 是六个任务事件的简写。不认识的值返回 `400 INVALID_REQUEST`，并在 `details.allowed` 里列出全部合法名字。

服务端定义的完整事件目录是 20 种（`pkg/event/event.go`）—— 上面 13 种之外，
还有 `plugin.loaded`、`plugin.unloaded`、`worker.spawned`、`worker.exited`、
`system.health`、`system.started`、`system.stopped`。**这 7 种不能通过 `?types=`
订阅**：`streamEventNames` 只由那 13 种构建，填它们会得到 `400 INVALID_REQUEST`。

注意目录里**没有** `workflow.cancelled` 或 `task.dead_letter`：`dead_letter` 是任务
的**状态**，不是事件类型；对应的事件是 `task.failed`。

### 配额与断连

- 并发流有上限，按调用方与全局各一层（响应里报作 `max_streams_per_caller`，默认 2 / `max_streams_total`，默认 256）。两层都带 `Retry-After: 5` 和 `STREAM_LIMIT_REACHED`，但状态码不同：**超了单调用方的额度是 `429`，超了全局额度是 `503`** —— 后者是服务端没余量了，不是你的客户端有问题。报 503 时可调 `LOOPWORKER_API_MAX_STREAMS` 抬高全局上限；**报 429 时没有可调的旋钮**，只能关掉多余的那条流（这两个名字是 admin 监听器上 `GET /runtime/stats` 的字段，不是配置项）。
- 客户端断开时订阅立刻释放，不需要客户端发取消请求。
- 空闲连接每 `api.stream_keepalive`（默认 15s）收到一行 `: keep-alive` 注释帧。代理若缓冲响应，SSE 会失效 —— 这是 `X-Accel-Buffering: no` 存在的原因。该间隔**不可配置**。
- **其他调用方的任务事件不会泄露内容**，但仍会投递一帧 `{"hidden":true,"reason":"task_owned_by_another_caller","task_id":"..."}`。这是有意设计：事件确实发生，但内容不属于订阅者。用 `?types=` 收窄订阅范围可以避免这类帧。

## 错误处理

### 错误码

| 错误码 | HTTP状态码 | 说明 |
|--------|-----------|------|
| INVALID_REQUEST | 400 / 422 | 请求参数错误为 **400**；工作流定义不可执行（缺步骤、引用了不存在的步骤、成环）为 **422**。同一个 code 带两种状态码 —— 只按状态码分支的客户端会漏掉 422，必须读 `error.code` |
| UNKNOWN_FIELD | 400 | 请求体里出现该端点不接受的键 |
| MALFORMED_JSON | 400 | 请求体不是合法 JSON |
| INPUT_ENCODING_INVALID | 400 | `input_encoding` 不是 text/base64 |
| REQUEST_TOO_LARGE | 413 | 请求体或 input 超过上限 |
| UNAUTHORIZED | 401 | 未认证 |
| TOKEN_INVALID | 401 | 凭据未知、被吊销或格式错误 |
| TOKEN_EXPIRED | 401 | 凭据过期 |
| FORBIDDEN | 403 | 角色权限不足 |
| METHOD_NOT_ALLOWED | 405 | 路由存在但方法不对 |
| NOT_FOUND | 404 | 路由不存在 |
| TASK_NOT_FOUND | 404 | 任务不存在或不属于该凭据 |
| WORKFLOW_NOT_FOUND | 404 | 工作流未注册 |
| WORKER_NOT_FOUND | 404 | 该 id 没有对应的 worker |
| PLUGIN_NOT_FOUND | 404 | 请求的插件未加载 |
| TASK_STATE_CONFLICT | 409 | 当前状态不允许该操作 |
| DEPENDENCY_CYCLE | 409 | 依赖成环 |
| DEPENDENCY_INVALID | 422 | 依赖指向自己或不存在的任务 |
| RATE_LIMITED | 429 | 超过速率限制 |
| STREAM_LIMIT_REACHED | 429 / 503 | 超过单调用方 / 全局流并发上限 |
| STREAMING_UNSUPPORTED | 501 | 响应写入器不支持流式输出 |
| INTERNAL_ERROR | 500 | 内部错误 |
| TIMEOUT | 504 | 超过请求超时 |
| SERVICE_UNAVAILABLE | 503 | 依赖的子系统没跑起来（调度器未运行、或该端点所需的组件缺失）。`GET /api/v1/health` 看哪个组件不在，再重启 |

完整枚举见 `pkg/api/openapi.json` 的 `ErrorResponse.code` 与 `pkg/api/errors.go`。

**有三个 code 在代码里有常量、却没有任何代码路径会发出它们**，不要等它们，
也不要为它们写分支：

* `GRAPH_TOO_LARGE` —— 图节点数超限时返回的是 `200` 加 `meta.truncated=true`，
  不是错误信封。
* `QUEUE_FULL` —— 队列容量目前不拒绝提交，所以 503 只以 `SERVICE_UNAVAILABLE`
  出现，**不会**带 `QUEUE_FULL` 这个 code。
* `TASK_ALREADY_EXISTS` —— `POST /api/v1/tasks` 的请求体里**没有 `id` 字段**
  （见上面的字段表），task id 一律由服务端分配，所以重复提交不会撞 id。
  由此有一个真实后果要提前知道：**任务创建没有幂等键**。POST 超时但服务端其实
  建成了任务时，客户端无法判断该不该重试，而重试会得到**第二个**任务而不是
  409。客户端若需要幂等，请自己在提交成功后按 `metadata` 标记来源，或改用
  「先 `GET` 查后 `POST`」的补偿流程。

### 错误响应示例

```json
{
  "success": false,
  "error": {
    "code": "UNKNOWN_FIELD",
    "message": "the field \"bogus\" is not part of this endpoint's contract Fix: remove or rename this key",
    "details": { "accepted_fields": ["workflow_id"] },
    "request_id": "1791266557134665900-15-d09e4ad2"
  },
  "timestamp": "2026-10-06T06:02:37.1346659Z",
  "request_id": "1791266557134665900-15-d09e4ad2"
}
```

## 速率限制

API 请求受到速率限制（`pkg/api` 默认值，窗口 1 分钟）：

- 匿名请求：100 次/分钟（按来源 IP 计）
- 认证请求：1000 次/分钟（按凭据计）

请求不是等价的：`/api/v1/events/live` 计 5 点，`POST` / `DELETE` / `PUT` 计 2 点，
其余读计 1 点。超限时返回 `429 Too Many Requests`，带 `Retry-After` 头，
`details` 里给出当次的 limit / window / authenticated：

```json
{
  "success": false,
  "error": {
    "code": "RATE_LIMITED",
    "message": "This caller exceeded 100 requests per 1m0s. Fix: back off for the number of seconds in the Retry-After header, batch requests, or use a credentialed key for the larger budget.",
    "details": {
      "limit": 100,
      "window": "1m0s",
      "retry_after_s": 42,
      "authenticated": false
    },
    "request_id": "..."
  }
}
```

## SDK 示例

### Go SDK

`loopworker/pkg/client` 就是 `loopctl` 用的那个客户端。它没有 `Login`、没有
`CreateTaskRequest`、也没有 `WaitForTask` —— 认证靠环境变量或 `APIKey` 字段，
等待要自己轮询。返回的是解完信封的 `map[string]any`：

```go
package main

import (
    "fmt"
    "log"
    "time"

    "loopworker/pkg/client"
)

func main() {
    // 空 baseURL 读 LOOPWORKER_URL，凭据读 LOOPWORKER_API_KEY
    c := client.NewAPIClient("")

    task, err := c.CreateTask("echo", "Hello, World!", 1)
    if err != nil {
        log.Fatalf("create task: %v", err)
    }
    id, _ := task["id"].(string)
    fmt.Printf("created %s state=%v\n", id, task["state"])

    // 没有 WaitForTask：自己轮询到终态
    for i := 0; i < 60; i++ {
        got, err := c.GetTask(id)
        if err != nil {
            log.Fatalf("get task: %v", err)
        }
        switch got["state"] {
        case "completed", "failed", "dead_letter", "cancelled":
            fmt.Printf("state=%v result=%v error=%v\n", got["state"], got["result"], got["error"])
            return
        }
        time.Sleep(time.Second)
    }
    log.Fatal("task did not reach a terminal state in 60s")
}
```

### cURL 示例

```bash
API_KEY=<your-key>

# 创建任务
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{"type": "hello", "input": "Hello, World!", "priority": 1}'

# 获取任务状态
curl -H "X-API-Key: $API_KEY" http://localhost:19527/api/v1/tasks/<task-id>

# 执行工作流，返回 202，轮询 poll 字段给出的路径
curl -X POST http://localhost:19527/api/v1/workflow/execute \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{"workflow_id": "builtin.anomaly-review"}'

# 获取指标（在 admin 监听器上，loopback only，需要 admin 凭据）
curl -H "X-API-Key: $API_KEY" http://127.0.0.1:19528/metrics
```
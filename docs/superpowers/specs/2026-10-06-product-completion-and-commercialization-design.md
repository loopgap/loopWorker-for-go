# 产品完善、验证与商业化优化 — 设计规格

日期：2026-10-06
状态：待评审
分支：`docs/product-completion-design`

## 1. 硬约束

本设计的每一个决定都受这三条约束支配。任何与之冲突的"更优雅"方案一律否决。

1. **零售后**：产品必须在零售后支持的条件下可用。判据不是代码量，而是"这会不会变成一个我们答不上来的工单"。
2. **一次性**：本轮做完，不留待办、不留路线图。所有引入的东西都要能长期无人维护。
3. **整洁**：临时产物只允许落在 `_scratch/`，且不入库。工作区任何时候都不应出现第二个运行时残留位置。

## 2. 现状基线（实测，非推断）

在 `main` @ `161f465` 加 19 个未提交改动的状态下实测：

| 项目 | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./...` | exit 0，24 个包全部 `ok`，零 FAIL |
| 工具链 | go 已装；node v24.14.0 / npm 11.19.0 可用；`web/canvas/node_modules` 不存在 |
| 鉴权来源 | `pkg/security/auth.go:552-569` **只读请求头**（`X-API-Key`、`Authorization: Bearer`），无 cookie、无 query |

基线是可信的，因此下面的每条缺陷都是真实的回归风险或现存缺陷，而非猜测。

## 3. 缺陷清单

每条带证据、影响与修复归属。

### D1 — `ExecuteParallel` 锁域破裂，可导致进程崩溃

`pkg/workflow/workflow.go:696-811`。这是对外文档化的能力（`docs/USAGE.md:244,260`、`docs/architecture.md:636`）。

- **两把锁保护同一个 map**：`ExecuteParallel` 在 761-763 行用 `completedMu` 写 `pw.State`；公开方法 `SetState`（232 行）用 `pw.mu` 写 `pw.State`。二者不互斥 → 并发写触发 Go 运行时 `fatal error: concurrent map writes`，进程直接崩溃，`recover` 无效。
- **无锁读**：748 行 `step.Action(innerCtx, pw.State)` 传入裸 map 引用，读取时一把锁都没拿。
- **裸 map 读**：`pw.StepOrder`（705、710、717）与 `pw.Steps`（718）、`pw.StepStatus`（719）全部无锁读取，而 `AddStep`（224-230 行）正在 `pw.mu` 下写它们。

对照：同一次改动已修复了 319 行与 `checkDependencies`（448 行）中**完全相同模式**的竞态，说明并发主路径被漏掉了。

### D2 — 公共监听器上的未鉴权关停与信息泄露

- `pkg/server/server.go:623` 把 `POST /shutdown` 挂在公共 mux；`HandleShutdown`（838-852 行）只校验 HTTP 方法。任何能连到端口的人 `curl -X POST /shutdown` 即可关停服务。
- `pkg/server/server.go:622` 的 `/statusz` 同样无鉴权，`GetStatus`（810-834 行）吐出调度器/执行器/事件总线计数、版本、运行时长与安全姿态。
- 与 `pkg/api/api.go:155` 的注释"The public listener: no prometheus registry, no runtime stats"**自相矛盾**——注释描述的行为与代码不符。

### D3 — 出厂 GUI 在默认配置下全部 401

`ServeStatic` 默认 `true`（`pkg/api/config.go:115`），但 `/api/v1/*` 除 `/health`、`/openapi.json` 外全部要求凭据（`pkg/api/api.go:181-201`）。而 `web/canvas/src/App.jsx` 的四处调用（41、63、110、150 行）**不带任何凭据**。

关键约束：`EventSource`（63 行）在浏览器中**无法设置请求头**。因此这不是"补个 header"能解决的。鉴权只读请求头（见 §2），所以唯一零服务端改动的路径是用 fetch 流替代 EventSource。

### D4 — GUI 静默损坏任务输入

`App.jsx:138` 发送 `input: btoa(userInput)`，但**不发送 `input_encoding`**。

服务端 `pkg/api/views_input.go:103-188` 的解析路径：payload 是 JSON 字符串 → 不以 `{`/`[` 开头 → `json.Unmarshal` 取到 base64 文本 → `InputEncoding` 为空，不走 base64 解码分支 → 187 行 `return []byte(text)`。

结果：插件收到的是字面量 `aHR0cDovL2xvY2FsaG9zdDo4MDgwLw==` 而非 `http://localhost:8080/`。**无报错、无告警、界面看着正常**——这是最典型的售后工单来源。

正确做法是走 `input_text` 字段（`views_input.go:137`），纯文本默认路径。

### D5 — 提案文档已失真

`.release/SCOPE-PROPOSAL.md` 声称 `/` 是 301 无限重定向（`pkg/api/api.go:248-263` 早已修复，并有 `static_test.go` 覆盖），声称 `cmd/loopworker/main.go` 仍调用扁平的 `config.LoadConfig`（实际已是 `config.Load(config.Options{...})`，`main.go:112`）。这份文件留着比删掉更危险。

### D6 — 工作区残留与忽略规则失效

- `docs/.d1-scratch/`：17 个文件 33.3MB，含 31MB 的 `lw.exe`、SQLite WAL、boot 日志。
- `_scratch/`：空目录残留。
- `.gitignore`：存在两段近乎重复的 Go/IDE/OS 规则；且未覆盖 `.d1-scratch` 类路径——这正是它能存在的原因。

### D7 — 商用级粗糙

`pkg/api/dist/index.html` 的 `<title>canvas</title>`。这个标题会出现在客户浏览器标签页、书签和分享链接里。另：SPA 的 `alert('... ' + resData.error)` 会打印 `[object Object]`，因为 `error` 是 `ErrorBody` 对象。

### 不构成缺陷（已核实排除）

- SSE 事件名与 SPA 订阅列表完全一致（`pkg/event/event.go:31-36` vs `App.jsx:97`），事件分发不会静默失效。
- `StepPending StepStatus = iota`（`workflow.go:51`）确为 0，故 `checkDependencies` 依赖零值的推理成立。

## 4. 工作流设计

五个工作流按文件所有权互斥，可并行。执行顺序：W1/W3/W4/W5 无相互依赖；W2 内部接口契约见下。

### W1 — 并发锁域统一（D1）

**不变**：`ParallelWorkflow` 的公开签名 `ExecuteParallel(ctx) error` 必须保持，因为 `docs/USAGE.md:260` 已向客户承诺该签名。

**规则一：所有共享状态统一由 `pw.mu` 保护。**

- 读：新增三个快照方法，命名与既有 `GetStepStatus`（586 行）风格一致：
  - `StepIDs() []string` — 在 RLock 下返回 `StepOrder` 的副本
  - `Step(id string) *Step` — 在 RLock 下返回
  - `StateSnapshot() map[string]interface{}` — 在 RLock 下返回 `State` 的浅拷贝
- 写：新增 `SetStepStatus(stepID string, s StepStatus)`，持 `pw.mu` 写。替换 740、751、764 行的裸写。
- 710 行的 `totalSteps` 改用 `len(pw.StepIDs())`。

**规则二：`step.Action` 一律接收状态快照，不接收裸 map。**

748 行改为传入 `pw.StateSnapshot()`。

这是本工作流唯一的行为语义变化，必须明确：原代码传引用，但既有设计本来就是"读旧 state → 返回增量 result → 完成后合并"，步骤之间从不共享实时写入。传快照**完全符合既有语义**，同时消除竞态。快照是浅拷贝，嵌套结构仍共享引用，这是可接受的：既有 `SetState` 的合并语义同样如此。

**规则三：锁序，禁止嵌套。**

`completedMu` 只保护 `completed` 这一个局部 map。**任何时候不得同时持有 `completedMu` 与 `pw.mu`。**

这一点在 740 行尤其容易违反——现有的 `launchReady` 在整个循环体（717-770 行）都持着 `completedMu`，而新加的 `SetStepStatus` 需要 `pw.mu`。具体做法：把 717-737 行的**判定**（哪些 step 依赖已满足、哪些可以启动）与 739-740 行的**状态写入**拆成两段。判定段在 `completedMu` 内完成并收集出一个待启动列表，退出 `completedMu` 之后再逐个调用 `SetStepStatus`。

751 与 764 行同理：先退出 `completedMu`，再取 `pw.mu` 写 `StepStatus` 与合并 `State`。若确实需要在一次临界区内同时更新两者，则改为**先写工作流状态、释放、再更新 `completed`**，顺序固定为 `pw.mu` → 释放 → `completedMu`，全函数保持同一方向。

本规则写进代码注释并由评审检查。

**测试**（新增 `pkg/workflow/parallel_race_test.go`）：

1. `TestExecuteParallelConcurrentAddStepAndSetState` — `ExecuteParallel` 运行中并发调用 `AddStep` 与 `SetState`，`go test -race` 必须零告警。
2. `TestExecuteParallelStateSnapshotIsolation` — 断言 `StateSnapshot()` 返回的 map 被修改不会影响工作流内部状态。
3. 保留既有 `workflow_test.go` 中 `ExecuteParallel` 的四个用例（272、429、463、528、552 行）全部通过。

**验收**：`go test -race ./...` 全绿；且 W1 完成后，`git diff` 不应出现任何新的公开字段变更。

### W2 — 安全收口（D2）

**接口契约**（由 `pkg/api` 定义，`pkg/server` 调用，两侧可并行实现）：

在 `pkg/api` 新增：

```go
// AdminControl is the process-lifecycle surface the admin listener exposes.
// pkg/api cannot reach Server.Stop, so the host injects the handlers it owns.
type AdminControl interface {
    AdminShutdown(w http.ResponseWriter, r *http.Request)
    AdminStatus(w http.ResponseWriter, r *http.Request)
}

func WithAdminControl(c AdminControl) Option
func (s *APIServer) SetAdminControl(c AdminControl)  // StartAdmin 之前调用
```

`Config` 增加字段 `AdminControl AdminControl`。

**注册**（`pkg/api/admin.go`）：在 `PermAdmin` 组内（47-57 行）注册，nil 守卫必须写在闭包里，避免空接口取值 panic：

```go
if c := s.cfg.AdminControl; c != nil {
    // Method, not Handle: Handle would register every verb and silently
    // widen the contract openapi.json declares (see the note below).
    authed.Method(http.MethodPost, "/shutdown", c.AdminShutdown)
    authed.Get("/statusz", c.AdminStatus)
}
```

用 `Method` 而非 `Handle`——该文件 51-52 行已有注释说明原因：`Handle` 会注册所有动词，静默放大 openapi 声明的契约宽度。若 `AdminControl == nil` 则**不注册**（保持 `pkg/api` 可独立用于测试与其他宿主，不 panic）。

**移除**（`pkg/server/server.go`）：删除 `handler()`（618-630 行）中的 `/shutdown` 与 `/statusz` 两行。公共 mux 只保留 `/healthz` 与 `/`→APIServer.Router。

**接线**（`pkg/server/server.go`）：在 `Start()` 中 `StartAdmin` 调用之前（402 行之前）调用 `components.APIServer.SetAdminControl(...)`，注入一个持有 `*Server` 的小 adapter，其两个方法分别转调现有的 `HandleShutdown` 与 `StatusHandler`。**保持这两个方法本身不变**，只搬位置。

选 `SetAdminControl` 而非构造时 `WithAdminControl`，是因为 `NewAPIServer` 在 `buildComponents`（190 行）执行，那时尚未构造出 `Server` 实例；事后注入避免时序耦合。

**注释修正**：`pkg/api/api.go:155` 的注释改为准确描述公共监听器实际暴露的内容。

**测试**：

- `pkg/api`：admin handler 在无凭据时 401/403；持 admin 凭据时 `/statusz` 返回 200；`AdminControl == nil` 时这些路由返回 404。
- `pkg/server`：公共 mux 对 `/shutdown`、`/statusz` 返回 404。
- `openapi.json` 只描述 `/api/v1/*`，admin 端点不在其内，**无需修改 openapi.json**，但需在 `docs/USAGE.md` 补一句 admin 端点说明。

### W3 — GUI 真正可用（D3、D4、D7）

**服务端零改动。** 依据 §2：鉴权只读请求头，fetch 可设头，故 fetch 流能带上凭据；无需 query token（会进日志）、无需 cookie（要新增 CSRF 面）。

**新增 `web/canvas/src/api.js`**，只导出三样东西：

1. `getToken()` / `setToken(v)` — 读写 `localStorage['loopworker.apiKey']`
2. `apiFetch(path, opts)` — 注入 `X-API-Key` 头；401 时抛出可识别错误
3. `sseFetch(path, handlers, signal)` — `fetch` + `response.body.getReader()` 解析 SSE 帧，按 `event:` 名分发，支持 `AbortController` 取消

**改 `App.jsx`**：

- 顶层凭据门：未配置 token 时**只渲染输入框，不发任何请求**；配置后再挂载画布与事件流。
- 41、110、150 行改用 `apiFetch`。
- 63-105 行的 `EventSource` 改为 `sseFetch`，事件名列表保持不变（`pkg/event/event.go:31-36` 已核实一致）。
- **138 行 `input: btoa(userInput)` 改为 `input_text: userInput`**，并删除误导性注释。这是消除静默数据损坏的关键一行。
- `alert` 改为提取 `resData.error?.message` 再拼接。
- 清理逻辑：`sseFetch` 的 cleanup 必须真正 abort，否则组件卸载后连接泄漏。

**`pkg/api/dist/index.html` 的 title** 改为产品名 `LoopWorker`。

**构建流程**（长官已批准）：

```
cd web/canvas
npm ci
npm run build          # 产物落在 web/canvas/dist（已被 .gitignore 覆盖）
```

然后**清空 `pkg/api/dist` 后**再拷贝 `web/canvas/dist` 的内容。这一点是整洁要求的硬落点：`//go:embed all:dist`（`api.go:26`）会嵌入目录下**所有**文件，若直接覆盖拷贝，旧 hash 的 js/css 会残留并被一并嵌入二进制，白白增大产物。

构建完成后删除 `web/canvas/node_modules`。

**构建流程必须记录进 `CONTRIBUTING.md`**。否则未来无人知道如何重建 `dist`，GUI 会变成无人敢碰的死资产——这与"零维护"直接冲突。

**验收必须包含行为验证，不能只看"没报错"**：`renderEvent`（`handlers_events.go:221-254`）在调用者无权时会下发 `{"hidden":true,...}` 桩数据，而 `handleUpdate` 对此静默忽略。所以必须确认画布节点状态**真的在变**。

### W4 — 诚实化与整洁（D5、D6）

- 删除 `docs/.d1-scratch/`（17 文件 33.3MB）与空的 `_scratch/`。
- `.gitignore` 去重：删除重复的第二段 Go/IDE/OS 规则；保留 WASM 例外（`!examples/hello-plugin/hello.wasm`、`!pkg/plugin/testdata/hello.wasm`）。
- `.gitignore` 新增 `.d*-scratch/` 模式——这是 `docs/.d1-scratch/` 能存在至今的直接原因。
- 在 `CONTRIBUTING.md` 写明整洁规范：临时与验证产物只允许落在 `_scratch/`，不入库；交付前 `git status --ignored` 不应显示仓库内其他位置的运行时残留。
- 重写 `.release/SCOPE-PROPOSAL.md` 为**状态文档**：已修项标明已修并附证据位置，仍待决项标明待决。删除 301 死循环与 `LoadConfig` 两处失真描述。保留文件而非删除，以保留决策轨迹。
- **防漂移测试**：新增一条测试，断言 `openapi.json` 覆盖了所有已注册的 `/api/v1/*` 路由。这是唯一能防止"文档与代码再次分叉"的自动化投入，一次性成本、长期收益。注意该测试落在 `pkg/api`，属 Agent B 的文件所有权（见 §5），与 W2 一并实现；此条列在 W4 是因为它服务于 W4 的目标，不因为归属。

### W5 — 商业化交付件（D7）

- `.release/PRE-RELEASE-CHECKLIST.md`：逐条可执行的交付前自检清单。每一条必须可判定通过/失败，不得是"检查一下"这类描述。覆盖：构建、`go test`、`go test -race`、`go vet`、lint、打包内容核对、GUI 浏览器验收、路由与文档一致性、零配置启动、关停路径可用。
- 核对 `.goreleaser.yaml` 与 `Dockerfile` 的实际产物内容，确认不含 `web/canvas` 源码、`node_modules`、scratch 目录、数据库文件。
- `README.md` / `SUPPORT.md` 的承诺边界与实现对齐。GUI 现在真正可用，因此可以保留并强化 GUI 声明——但只能声明经过浏览器验收的功能。

## 5. 并行执行策略

按文件所有权分配，互不重叠：

| Agent | 文件所有权 |
|---|---|
| A | `pkg/workflow/**` |
| B | `pkg/api/admin.go`、`pkg/api/config.go`、`pkg/api/api.go`、`pkg/api/*_test.go` |
| C | `pkg/server/server.go`、`pkg/server/*_test.go` |
| D | `web/canvas/**`、`pkg/api/dist/**` |
| E | `docs/**`、`.release/**`、`.gitignore`、`CONTRIBUTING.md` |

W2 的接口契约已在 §4 写死，B 与 C 无需往返协调即可并行。E 与 D 无冲突（`.gitignore` 不涉及 `dist` 提交）。

**验收必须串行由主代理执行**：浏览器验收与全量测试都需要整体启动，无法拆分给子代理。

## 6. 验收矩阵

| 工作流 | 验收方式 |
|---|---|
| W1 | `go test -race ./...` 零告警；既有 `ExecuteParallel` 用例全通过 |
| W2 | `pkg/api` + `pkg/server` 测试断言公共 404 / admin 401-403 / admin 200 |
| W3 | `@browser-use` 真实浏览器：启动服务 → 粘 token → 确认工作流图渲染 → 创建任务 → 确认**画布节点状态真的变化**（非仅无报错） |
| W4 | `git status` 干净；`git status --ignored` 无仓库内非 `_scratch/` 的运行时残留；`docs/` 无失真描述 |
| W5 | 清单逐条可判定通过；打包产物内容核对无泄漏 |

全局：`go build ./...`、`go test ./...`、`go test -race ./...`、`go vet ./...` 全绿。

## 7. 明确不做（YAGNI）

- **不做** cookie 登录与会话机制。它能顺带解决 EventSource 鉴权，但要引入 cookie 鉴权路径与 CSRF 防护，是新的攻击面与长期维护负担，而 fetch 流已零成本解决同一问题。
- **不做** query 参数 token。改动更小，但 token 会进入访问日志与浏览器历史。
- **不做** 抽 `stepRegistry` 重构（W1 的方案 B）。边界更漂亮，但要动 `Workflow` 结构体，而 API 层直读其公开字段（见 `pkg/api/workflow_state_race_test.go`），回归风险显著高于收益。
- **不做** 删减 `ParallelWorkflow`。它是对外文档化的能力，删掉即破坏契约。
- **不做** 多租户配额、计费钩子、审计日志等商业功能。本轮目标是零售后的可交付性，不是功能扩张；功能扩张需要真实需求验证，不该在缺陷未清时叠加。

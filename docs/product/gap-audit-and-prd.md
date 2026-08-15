# LoopWorker 全量缺口审计报告 & 完善 PRD

> 文档版本：v1.0
> 作者：许清楚（产品经理）
> 日期：2026-08-15
> 目标项目：`D:\Destop\test\loopWorker-for-go`（Go 工作循环引擎）
> 目标状态：从"可演示原型"完善为"可编译、可测试、可交付、文档齐全的工业级良好软件"

---

## 第一部分：缺口审计报告

### 1.1 审计结论摘要

| 严重度 | 数量 | 一句话结论 |
|--------|------|-----------|
| **P0（阻断交付）** | 3 | `go build ./...` 直接失败；前端 UI 构建产物断链且当前 UI 是占位符；16 个 Go 文件未通过 gofmt |
| **P1（应有功能）** | 11 | 5 个 CLI 工具源码缺失但文档宣称存在；文档/README 多处与实现不一致；go.mod 未 tidy；race 检测失败；无 CI；安全实现有硬伤；仓库被运行产物污染 |
| **P2（锦上添花）** | 8 | 架构自评的 God Object、错误处理不一致、Worker 无全局超时等；三套 Config 重复；示例不可开箱运行 |

**核心判断**：项目"代码骨架质量尚可"（23 个测试包全部通过），但**交付面（构建、文档、CLI、示例、CI、安全）存在系统性缺口**。当前状态无法作为"良好软件"交付，必须先修复 P0，再收敛 P1，最后处理 P2。

### 1.2 审计方法与验证过程

本报告所有结论均在本机实际执行命令验证，非转述。关键验证命令与结果：

| 验证项 | 命令 | 结果 |
|--------|------|------|
| 编译 | `go build ./...` | ❌ `pkg\api\api.go:27:12: pattern all:dist: no matching files found` |
| gofmt | `gofmt -l .` | ❌ 16 个文件未格式化（与主理人探查列表完全一致） |
| go vet | `go vet ./...` | ❌ 被同一 embed 错误阻断（修复 embed 后应通过） |
| 测试 | `go test ./...`（临时补 dist 后） | ✅ 23 个测试包全部 ok（含 integration 7 个用例） |
| race | `go test -race -short` 核心包 | ❌ `internal/core/observer` 的 `TestTaskExecutionEventFlow` 检测到 **DATA RACE**（测试内 map 无同步） |
| tidy | `go mod tidy -diff` | ❌ go.mod 需大幅重整（所有直接依赖被标为 `// indirect`，残留已删除 TUI 的 charmbracelet 依赖） |
| CLI | `ls cmd/` | ❌ 只有 `loopworker`；loopctl/loopbench/loopwatch/loopsim/loopdebug 均无源码（根目录只有对应 `.exe` 产物） |
| CI | `ls .github` | ❌ 目录不存在 |
| git | `git log --oneline` | 仅 1 个 commit：`6cac897 feat: initial project setup with complete LoopWorker engine` |
| dist | `git status` | `D pkg/api/dist/index.html`（被跟踪的占位文件已在工作区被删除） |
| 占位符 | `git show HEAD:pkg/api/dist/index.html` | 内容仅为 `<html><body>Placeholder</body></html>`，**并非真实前端产物** |
| embed 路由 | `pkg/api/api.go` | `//go:embed all:dist` + `/*` 路由将请求改写为 `/dist/index.html` |
| Dashboard | `pkg/server/server.go` | 生产 `http.Server.Handler = s.apiServer.Router`（chi），而 dashboard 注册在 `http.DefaultServeMux` → 生产中不可达 |
| 路由 | `pkg/api/api.go` | 实际为 `/api/v1/tasks`、`/api/v1/health`、`/metrics` 等；README 写的是 `/api/tasks` 等 |

> 说明：为验证"修复 embed 后测试是否通过"，审计期间临时创建了 `pkg/api/dist/` 占位文件；验证完毕已删除，仓库已恢复到原始状态（`git status` 重新显示 `D pkg/api/dist/index.html`），确保实施团队从真实基线开始。

### 1.3 缺口清单（Gap Table）

| 编号 | 位置 | 严重度 | 影响 | 修复建议 |
|------|------|--------|------|----------|
| G1 | `pkg/api/api.go:27` `//go:embed all:dist` + `pkg/api/dist/` 缺失 | **P0** | `go build ./...` / `go vet` / `make build` 全部失败，项目无法编译，任何交付都无从谈起 | 建立受管的前端产物流程：`make web-build` 构建 `web/canvas/dist` 并复制到 `pkg/api/dist`；或提交一份最小合法 `index.html` 保证无 Node 环境也能编译 |
| G2 | `web/canvas/` ↔ `pkg/api/dist/` 构建链 + 占位符 UI | **P0** | 即使编译通过，`/` 路由服务的只是 `<html><body>Placeholder</body></html>`，**真实的 React Canvas UI 从未被打包**；`web/canvas/dist/` 被 .gitignore 忽略，Makefile `web-build` 不复制到 `pkg/api/dist`，且 `node_modules` 未安装 | 打通 `npm install → npm run build → copy dist → go build` 全链路；`Makefile` 增加 `web-build` 与 `build` 的依赖关系；CI 中验证产物非占位符 |
| G3 | 16 个 Go 文件（api/dashboard/event/plugin/research/server/skill/workflow/executor/observer/sandbox/scheduler/bridge/integration） | **P0** | 未通过 `gofmt -l`，代码风格不统一，会污染后续 diff、触发 lint 失败 | `gofmt -w` 全量格式化并纳入 `make check` 门槛 |
| G4 | `cmd/` 仅 loopworker；root 下 `loopctl/loopbench/loopwatch/loopsim/loopdebug.exe` 无源码 | **P1** | README/docs/test-report/Makefile/fix_encoding.py 均宣称存在 6 个 CLI 工具，**虚假声明**；用户无法从源码构建这些工具 | 二选一：①补齐 5 个 CLI 工具源码（推荐 loopctl 必补，其余可作独立小工具）；②删除所有引用与 .exe 产物，README/docs 只声明真实存在的能力 |
| G5 | `docs/api/`、`docs/guides/`、`test/e2e/`、`test/integration/`、`examples/{advanced,api,plugin,simple}/` 均为空 .gitkeep | **P1** | 承诺的 API 参考文档、使用指南、E2E 测试、示例缺失；用户无从了解 API 契约与最佳实践 | 按 PRD 需求 R8/R9/R10 补齐：API 参考（真实路由）、快速上手指南、可运行示例 |
| G6 | `Makefile` | **P1** | `build-all` 只构建 loopworker；无 `install`/`test-e2e` 目标；`run-tui` 引用不存在的 `-tui` flag；`web-build` 未与 `build` 联动 | 重构 Makefile：`build` 依赖 `web-build`；新增 `install`、`test-e2e`、`test-race` 纳入 `check`；删除/修正 `run-tui` |
| G7 | `go.mod` / `go.sum` | **P1** | 所有直接依赖（chi/cobra/viper/wazero/zap/gorm/prometheus）被错误标为 `// indirect`；残留 charmbracelet/bubbletea（TUI 已删除）等无用依赖；`go mod tidy -diff` 显示需大改 | 执行 `go mod tidy` 并提交结果；此后把 `go mod tidy -diff` 纳入 CI 检查 |
| G8 | `internal/core/observer/observer_integration_test.go`（`TestTaskExecutionEventFlow`） | **P1** | `go test -race` 失败：测试 goroutine 与主测试并发读写共享 map，无同步 → 测试不稳定，CI 加 race 必红 | 给 `expectedEvents` 加 mutex/atomic 或改用 channel；全仓 `go test -race ./...` 须绿灯 |
| G9 | `README.md`（中英双语） | **P1** | API 表（`/api/tasks`）与实际（`/api/v1/tasks`）不符；宣称 `-tui` flag 但 main.go 无此 flag；宣称 `loopctl init` 但无源码；配置 JSON（嵌套 `general.*`）与 viper 扁平 key 不匹配；项目结构缺 `pkg/{ai,debugger,detector,generator,research,service,skill}` | 按真实实现重写 README：真实路由、真实 flags、真实目录结构、真实配置示例；增补"从零构建"一条命令流程 |
| G10 | `docs/test-report.md` | **P1** | 声称 194 测试/16 包/6 个构建工具，实际 326 个 Test 函数/23 包/1 个 cmd 源码；还引用已不存在的 `pkg/dispatcher`；测试报告失真 | 重新生成测试报告（数量、包、CLI 以实际为准）；或标注历史快照并补当前报告 |
| G11 | `pkg/dashboard/dashboard.go` + `pkg/server/server.go` | **P1** | Dashboard 通过 `http.HandleFunc` 注册到 `http.DefaultServeMux`，但生产 `http.Server.Handler` 是 chi router → **dashboard 的 6 个端点（/api/tasks、/api/metrics、/api/logs、/api/health、/api/stats、/events）在生产中不可达**，属于死代码；且与 chi 路由存在两套平行 UI 实现 | 决策单一事实源：保留 React Canvas（chi 路由服务 embed），将 dashboard 的 REST 能力迁移到 chi 路由或删除 DefaultServeMux 注册；所有 handler 改为接收 mux 参数，消除测试冲突 |
| G12 | 仓库根（无 `.github/`，仅 1 个 commit） | **P1** | 无 CI，无法自动验证构建/测试/race/gofmt/tidy；单 commit 无演进历史，无法回滚 | 新增 GitHub Actions（或等价 CI）：build + test + race + vet + gofmt + tidy-diff + 前端构建产物检查；补 LICENSE 合规检查 |
| G13 | `pkg/security/security.go` | **P1** | ①密码用**无盐 SHA-256**（`sha256.Sum256([]byte(password))`）哈希，易受彩虹表攻击；②User/Token/Role 纯内存存储，重启即失 | ①改用 bcrypt/argon2id（加盐）；②安全数据持久化到 SQLite（项目已有 gorm 依赖，成本低）；提供默认管理员引导流程 |
| G14 | 仓库工作区污染：`integration/loopworker_tasks.db`、`internal/core/*/loopworker_tasks.db`、`coverage`（过期且引用已删除的 `pkg/scheduler`）、根目录 6 个 `.exe`、`e2e.test.exe`、`fix_encoding.py`（引用已删除文件） | **P1** | 仓库混乱；`coverage` 是旧树快照会误导；运行期 DB 散落源码目录有误删风险 | 清理工作区；`fix_encoding.py` 删除；coverage 改为 Makefile 动态生成；DB 统一落到 `data/` 或 `t.TempDir()` |
| G15 | `pkg/server/server.go` Server struct（16 组件） | **P2** | God Object：依赖面大、难测试、难扩展（架构文档第 20 节自评） | 短期：构造函数改为显式依赖注入，按域拆分为 server/bootstrap/worker 等子结构；长期：引入容器 |
| G16 | 全仓错误处理不一致 | **P2** | 大量 `_ = eventBus.Publish(...)`（plugin/research/workflow/dispatcher/executor/sandbox）、`_ = s.observer.Start(...)`、`_ = e.executor.StartWorker(...)` 吞掉错误，故障难排查 | 制定错误策略：事件发布失败至少记日志/指标；Start 类调用返回 error 并向上传播；统一日志字段 |
| G17 | `internal/core/executor/executor.go`（Worker 执行无全局超时） | **P2** | 普通任务执行无超时上限（仅 Agent/LLM 30s、sandbox MaxCPUSeconds、workflow Step.Timeout），长任务可能悬挂占住 Worker | 在 Executor 层增加可配置的全局执行超时（默认值 + 任务级覆盖），超时转失败并进入重试/死信 |
| G18 | `pkg/dashboard/dashboard.go` StartEventListening（15 个 goroutine 用 `context.Background()`，无显式 Stop） | **P2** | 生命周期依赖 EventBus.Close 兜底，顺序耦合；若未来 EventBus 不关闭则泄漏 | 为 Dashboard 增加 `Stop()`，goroutine 用 Server ctx 派生；Stop 中 unsubscribe 并等待退出 |
| G19 | `internal/config.Config`、`pkg/config.Settings`、`pkg/server.Config` 三套配置 | **P2** | 同一"端口/数据目录"在 3 处定义，字段不齐、易失一致 | 收敛为单一配置模型（建议以 `internal/config` 为运行时真相 + `pkg/config` 用户设置，删除 server.Config 或改为组装层） |
| G20 | `examples/` | **P2** | `examples/workflow/main.go` 可跑但无说明；`hello-plugin` 只有 plugin.json 元数据，无可加载 .wasm；`examples/wasm/rust_agent` 需 Rust/wasm-pack 手动构建；空目录 4 个 | 提供 1-2 个开箱即用示例（README 说明运行命令）；`examples/simple` 放最小可运行 main；`examples/plugin` 放可构建的 .wasm 与构建脚本 |
| G21 | `pkg/utils/safego.go`（全局 logger 无同步） | **P2** | `SetLogger` 写全局变量与 `GoSafe` 读并发时存在潜在竞态 | 改为包级原子指针/或经 EventBus 上报 panic 事件（代码注释也预留了该方向） |
| G22 | 遗留空目录：`pkg/core/`、`pkg/detector/`、`cmd/loopworker/cmd/`；`web/canvas/src/assets/{react.svg,vite.svg}` 默认模板残留 | **P2** | 空目录误导；模板残留让前端显得未定制 | 删除空目录或放入实际文件；清理 Vite 默认资源 |

---

## 第二部分：完善 PRD

### 2.1 项目信息

- **Language**：中文（技术术语用英文）
- **Programming Language / 技术栈**：保持现状——Go 1.26.1 + chi + wazero + gorm/sqlite + zap；前端 Vite + React（不强制引入 MUI，避免扩大改动面）
- **Project Name**：`loopworker`（维持现有 module 名）
- **原始需求复述**：用户要求"结合工作区内容将其完善为良好的软件，补全所有的缺失考虑部分并进行闭环验证"。即把 LoopWorker 从"能跑 demo"完善为**可编译、可测试、可交付、文档齐全的工业级良好软件**，并对修复结果做闭环验证（build/test/race/vet/gofmt/前端产物逐一验证）。

### 2.2 产品定义

#### Product Goals（3 个正交目标）

1. **可编译可测试**：clone 后一条命令完成构建；`go test ./...`、`go test -race ./...`、`go vet ./...`、`gofmt`、`go mod tidy -diff` 全部绿灯。
2. **可交付可信**：README、docs、CLI、示例与真实实现一致，无虚假声明；前端 UI 真实可用而非占位符；CI 自动守护质量。
3. **可运维可演进**：健康检查、指标、优雅停机可用；安全数据持久化；架构债务（God Object、错误处理、超时、配置收敛）有明确收敛计划与验收。

#### User Stories

1. 作为**开发者**，我希望 clone 后执行一条命令就能构建并跑起服务（含前端），以便零障碍上手。
2. 作为**开发者**，我希望 CI 自动执行 build/test/race/vet/fmt/tidy 检查，以便合入前就知道质量是否达标。
3. 作为**运维人员**，我希望服务提供 `/api/v1/health` 与 `/metrics`，并在重启后保留用户与令牌，以便安全地部署与监控。
4. 作为**使用者**，我希望 README/API 文档/示例与真实行为一致（真实路由、真实 flags、可运行示例），以便按文档即可完成接入。
5. 作为**贡献者**，我希望代码格式统一、错误处理一致、Worker 有超时保护，以便安全地修改与扩展。

### 2.3 技术规范

#### Requirements Pool（P0 必须 / P1 应该 / P2 可以）

| 编号 | 优先级 | 需求 | 验收标准（Acceptance Criteria） |
|------|--------|------|-------------------------------|
| R1 | **P0** | 修复 embed 编译阻断（G1） | `go build ./...` 成功；`go vet ./...` 成功；`make build` 成功 |
| R2 | **P0** | 打通前端构建产物链路（G2） | `make web-build` 将 `web/canvas/dist` 复制到 `pkg/api/dist`；构建出的 `index.html` 非占位符；`/` 路由返回真实 Canvas UI（含脚本资源） |
| R3 | **P0** | gofmt 全量格式化（G3） | `gofmt -l .` 输出为空；`make check` 通过 |
| R4 | **P1** | 补齐或移除 5 个 CLI 工具（G4） | 方案 A：`cmd/loopctl` 等 5 个工具源码存在且 `go build ./cmd/...` 成功；方案 B：README/docs/Makefile/根目录 .exe 全部清理，文档只声明真实能力 |
| R5 | **P1** | 修复 race（G8） | `go test -race ./...` 全绿（含 observer 集成测试） |
| R6 | **P1** | go.mod tidy（G7） | `go mod tidy -diff` 无输出；无 TUI 残留依赖 |
| R7 | **P1** | README 与实现一致（G9） | README 中 API 表=真实 chi 路由；flags=main.go 实际 flags；配置示例=viper 实际 key；目录结构=实际结构 |
| R8 | **P1** | 补 API 参考文档（G5 之一） | `docs/api/` 含每个真实端点的 method/path/参数/响应示例/错误码 |
| R9 | **P1** | 补使用指南（G5 之二） | `docs/guides/` 含：快速开始、配置说明、安全与鉴权、WASM 插件开发、观测与监控 5 篇 |
| R10 | **P1** | 补可运行示例（G5/G20） | `examples/simple` 一个 `go run` 即可跑的最小工作流；README 标注每个示例的运行命令与前置条件 |
| R11 | **P1** | Makefile 完善（G6） | `build` 依赖 `web-build`；新增 `install`、`test-e2e`、`test-all`（fmt+vet+test+race+tidy）；删除/修正 `run-tui` |
| R12 | **P1** | 建立 CI（G12） | 每次 push/PR 自动跑：build、test、race、vet、gofmt、tidy-diff、前端构建；任一步失败则红 |
| R13 | **P1** | 安全加固（G13） | 密码改用 bcrypt/argon2id；User/Token 持久化到 SQLite；重启后用户与令牌仍有效（验收：重启进程后用旧 token 仍可认证） |
| R14 | **P1** | 统一 Dashboard 路由（G11） | dashboard 不再依赖 `http.DefaultServeMux`；删除生产不可达端点或迁移到 chi 路由；`go test ./pkg/dashboard/...` 与 `go test ./pkg/server/...` 互不冲突 |
| R15 | **P1** | 清理仓库污染（G14） | 工作区无 `*.db`/`*.exe`/`coverage`/`fix_encoding.py`；`coverage` 由 Makefile 动态生成 |
| R16 | **P1** | 更新测试报告（G10） | `docs/test-report.md` 数据与当前仓库一致（真实包数/用例数/CLI 数），或标注为历史快照并补当前报告 |
| R17 | **P2** | Server 拆分/依赖注入（G15） | Server 构造函数显式注入依赖；新增 server 相关单元测试覆盖生命周期 |
| R18 | **P2** | 错误处理一致化（G16） | 非测试代码无静默 `_ =` 吞错；事件发布失败至少打日志/记指标；Start 类错误向上传播 |
| R19 | **P2** | Worker 全局执行超时（G17） | Executor 支持可配置超时（默认值+任务级覆盖）；超时任务进入失败/重试/死信流程；有对应测试 |
| R20 | **P2** | Dashboard 生命周期（G18） | Dashboard 提供 `Stop()`；goroutine 使用可取消 ctx；无泄漏（可用 goleak 验证） |
| R21 | **P2** | 配置收敛（G19） | 单一配置模型落地；删除/收敛 `server.Config` 重复字段 |
| R22 | **P2** | 全局 logger 竞态修复 + 空目录清理（G21/G22） | `SetLogger` 并发安全；`pkg/core`/`pkg/detector`/`cmd/loopworker/cmd` 空目录删除；Vite 默认资源清理 |

#### 明确不做（Non-Goals）

以下项属于架构文档长期演进方向，**本期明确不做**（除非实现成本极低且不影响主线）：

- 分布式多节点调度 / 多节点 SQLite 集群
- Kubernetes 部署（Operator 模式）与 Helm Chart
- gRPC 替代 REST
- 插件市场 / 插件热加载 registry
- 多租户
- 事件溯源（Event Sourcing）完整实现
- 可视化工作流编辑器（前端增强）大改版
- 多语言 i18n 框架引入

### 2.4 建议实施顺序（Milestones）

| 阶段 | 范围 | 退出条件 |
|------|------|----------|
| **M1：恢复可交付基线（P0）** | R1、R2、R3 | `go build ./...` ✅；`gofmt -l .` 空 ✅；`/` 返回真实 Canvas UI ✅ |
| **M2：可信度修复（P1）** | R4–R16 | README/docs 与实现一致；CLI 有源码或被清理；`go test -race ./...` 全绿；CI 上线；安全持久化生效；仓库干净 |
| **M3：架构收敛（P2）** | R17–R22 | Server 拆分、错误策略、Worker 超时、配置收敛、生命周期完备均有测试佐证 |
| **M4：闭环验证** | 全量回归 | 按 README 从零 clone→一条命令构建→跑测试→启动服务→curl 健康检查/指标→前端可访问；`docs/test-report.md` 更新为真实数据 |

### 2.5 风险与依赖

- **前端构建依赖 Node 环境**：CI 需安装 Node 18+；本地无 Node 时 `make build` 必须能退化为"提交的最小合法产物"或明确报错（不能静默产出占位符）。
- **安全持久化涉及现有 API**：`SecurityManager` 增加存储层时需保持 `CreateUser/Authenticate` 接口兼容，已有测试需同步更新。
- **README 重写是"真相校对"而非"美化"**：必须以真实路由/真实 flags 为准，避免二次失真。
- **M2 工作量集中在文档与示例**：建议与代码修复并行，避免等待。

---

## 附录 A：审计关键证据（命令输出摘录）

```
$ go build ./...
pkg\api\api.go:27:12: pattern all:dist: no matching files found

$ gofmt -l .
integration\integration_test.go
internal\core\executor\executor.go
...（共 16 个文件，与主理人探查一致）

$ go test -race -short ./internal/core/observer/...
--- FAIL: TestTaskExecutionEventFlow (0.06s)
    testing.go:1712: race detected during execution of test
WARNING: DATA RACE ... observer_integration_test.go:128 (write, goroutine) vs :159 (read, main test)

$ go mod tidy -diff
--- current/go.mod
+++ tidy/go.mod
（chi/cobra/viper/wazero/zap/gorm/prometheus 应改为直接 require；charmbracelet 系列应移除）

$ git log --oneline
6cac897 feat: initial project setup with complete LoopWorker engine

$ git show HEAD:pkg/api/dist/index.html
<html><body>Placeholder</body></html>
```

## 附录 B：验证通过项（非缺口，保留）

- ✅ 23 个测试包在补 dist 后全部通过（含 integration 7 个用例、selfheal 21s 用例）
- ✅ 大多数核心包通过 `-race`（scheduler/selfheal/event/executor/sandbox/dispatcher/workflow/api/dashboard/server/plugin/skill 均 ok，唯一失败为 observer 集成测试）
- ✅ LICENSE 完整（MIT，版权人 loopgap，2026）
- ✅ `.gitignore` 覆盖了大部分产物（`web/canvas/dist/`、`*.db`、`*.exe`、`coverage` 等）
- ✅ EventBus.Close 会关闭所有 subscriber channel，dashboard goroutine 依赖此兜底可退出

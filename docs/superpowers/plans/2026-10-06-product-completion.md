# 产品完善、验证与商业化优化 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 清除五类会导致售后工单或进程崩溃的缺陷，让自托管交付在零售后支持的条件下可用。

**Architecture:** 六个任务分两层。后端（Task 1/2/3）消除并发竞态与未鉴权端点；前端（Task 4）让出厂 GUI 在默认鉴权下真正可用且服务端零改动；交付面（Task 5）清理工作区并让文档与代码一致；Task 6 由主代理串行验收。各任务按文件所有权互斥，可并行。

**Tech Stack:** Go 1.x（chi v5、zap、prometheus）、React 19 + Vite 8（`web/canvas`）、Node v24 / npm 11。

## Global Constraints

以下约束对每个任务都生效，违反即视为实现错误：

- **零售后**：每个新增行为必须能用"客户不会因此发工单"来检验。
- **一次性**：不留 TODO、不留占位实现、不留"后续再做"的半成品。
- **整洁**：临时与验证产物只允许落在 `_scratch/`，且不入库。构建前端后必须清掉 `node_modules` 与 `web/canvas/dist`。
- **诚实**：注释与文档必须描述代码实际做的事。若实现与注释冲突，改注释，不要留矛盾。
- **不加依赖**：不引入任何新的 Go 或 npm 依赖。
- **不动公开契约**：`ParallelWorkflow.ExecuteParallel(ctx) error` 的签名、`Workflow` 的公开字段、`/api/v1/*` 的路径与语义都不得改动。

## Scope Decision

本计划写**一份**而不是五份。理由：五个工作流共享同一个验证门（`go test -race` 全量 + 浏览器验收），拆成五份文档会让人误以为可以各自独立验收，而它们不能——Task 3 在 Task 2 落地前无法编译，Task 6 必须看到全部五项的结果。

## Execution Order

```
阶段一（串行）：Task 2  —— 定义 AdminControl 接口
阶段二（并行）：Task 1 · Task 3 · Task 4 · Task 5   （四个子代理，文件所有权互斥）
阶段三（串行）：Task 6  —— 主代理验收
```

Task 2 必须先行：`pkg/server` 要调用 `SetAdminControl`，接口不存在时 Task 3 无法编译。Task 2 很小（接口 + 两条路由 + 测试），先行不构成瓶颈。

---

## File Structure

**新增：**

| 文件 | 职责 |
|---|---|
| `pkg/workflow/parallel_race_test.go` | 并发写回归测试。文件名独立于 `workflow_test.go`，因为它测的是并发安全，不是工作流语义 |
| `web/canvas/src/api.js` | 前端唯一的数据出口：凭据存取、`apiFetch`、SSE 流。**所有** `fetch` 与 `EventSource` 调用都必须经过这里 |
| `.release/PRE-RELEASE-CHECKLIST.md` | 交付前自检清单，每条可判定通过/失败 |

**修改：**

| 文件 | 改动 |
|---|---|
| `pkg/workflow/workflow.go` | 新增 `StepIDs`/`Step`/`SetStepStatus`/`StateSnapshot`/`MergeState`；重写 `ExecuteParallel` 的锁结构 |
| `pkg/api/admin.go` | 新增 `AdminControl` 接口；在 `PermAdmin` 组内注册 `/shutdown`、`/statusz` |
| `pkg/api/config.go` | `Config` 新增 `AdminControl` 字段；新增 `WithAdminControl` 选项 |
| `pkg/api/api.go` | 新增 `SetAdminControl`；修正 155 行与实际行为矛盾的注释 |
| `pkg/api/admin_control_test.go` | Task 2 的测试（独立文件，便于评审） |
| `pkg/server/server.go` | `handler()` 移除两条路由；`Start()` 注入 `AdminControl` |
| `pkg/server/admin_control_test.go` | Task 3 的测试 |
| `web/canvas/src/App.jsx` | 凭据门；`apiFetch`/`sseFetch` 替换；`input_text` 修复 |
| `web/canvas/index.html` | `<title>` 改为产品名 |
| `pkg/api/dist/**` | 重建产物（整体替换，不保留旧 hash 文件） |
| `.gitignore` | 去重；新增 `.d*-scratch/` |
| `CONTRIBUTING.md` | 新增"重建前端"与"整洁规范"两节 |
| `.release/SCOPE-PROPOSAL.md` | 重写为状态文档 |
| `docs/USAGE.md` | 补 admin 监听器端点说明 |

---

### Task 1: 统一 `ExecuteParallel` 的锁域

修复 D1。`pw.State` 曾被 `completedMu`（761-763 行）和 `pw.mu`（232 行 `SetState`）两把锁分别写，两把锁不互斥，并发写会触发 Go 运行时的 `fatal error: concurrent map writes`——进程崩溃，`recover` 无效。

**Files:**
- Modify: `pkg/workflow/workflow.go`
- Create: `pkg/workflow/parallel_race_test.go`

**Interfaces:**
- Consumes: 无（首个任务）
- Produces: `func (w *Workflow) StepIDs() []string`、`func (w *Workflow) Step(id string) *Step`、`func (w *Workflow) SetStepStatus(stepID string, status StepStatus)`、`func (w *Workflow) StateSnapshot() map[string]interface{}`、`func (w *Workflow) MergeState(result map[string]interface{})`

- [ ] **Step 1: 写失败的并发测试**

创建 `pkg/workflow/parallel_race_test.go`：

```go
package workflow

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestExecuteParallelSurvivesConcurrentMutation is the regression guard for the
// crash this file used to be one line away from. ExecuteParallel read Steps,
// StepOrder, StepStatus and State with no lock at all, and wrote State under
// completedMu while SetState wrote the same map under mu - two mutexes, one map.
// The result was a possible "fatal error: concurrent map writes", which kills the
// process instead of returning an error, so only -race can prove this is fixed.
func TestExecuteParallelSurvivesConcurrentMutation(t *testing.T) {
	pw := NewParallelWorkflow("race-1", "race", 4)

	release := make(chan struct{})
	pw.AddStep(&Step{
		ID:   "s1",
		Name: "blocker",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			<-release
			return map[string]interface{}{"s1": "done"}, nil
		},
	})
	pw.AddStep(&Step{
		ID:        "s2",
		Name:      "follower",
		DependsOn: []string{"s1"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			// s2 must see s1's merged result, which is what proves the snapshot is
			// taken per-step rather than once for the whole run.
			return map[string]interface{}{"s2": state["s1"]}, nil
		},
	})

	done := make(chan error, 1)
	go func() { done <- pw.ExecuteParallel(context.Background()) }()

	// Mutate the way an operator would while a run is in flight.
	for i := 0; i < 500; i++ {
		pw.SetState(fmt.Sprintf("key-%d", i), i)
		pw.AddStep(&Step{
			ID:   fmt.Sprintf("late-%d", i),
			Name: "late",
			Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
				return nil, nil
			},
		})
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteParallel: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ExecuteParallel did not finish")
	}

	if got := pw.GetStepStatus("s1"); got != StepCompleted {
		t.Errorf("s1 status = %v, want completed", got)
	}
	if got := pw.GetStepStatus("s2"); got != StepCompleted {
		t.Errorf("s2 status = %v, want completed", got)
	}
	if v, ok := pw.GetState("s2"); !ok || v != "done" {
		t.Errorf("s2 saw %v, want the value s1 returned - a dependent step must observe its dependency's merged result", v)
	}
}

// TestStateSnapshotIsIsolated proves the copy handed to a step's Action is not
// the live map, which is what removes the unguarded read at the call site.
func TestStateSnapshotIsIsolated(t *testing.T) {
	wf := NewWorkflow("snap-1", "snap")
	wf.SetState("a", "original")

	snap := wf.StateSnapshot()
	if snap["a"] != "original" {
		t.Fatalf("snapshot lost state: %v", snap)
	}

	snap["a"] = "mutated"
	snap["injected"] = true

	if v, _ := wf.GetState("a"); v != "original" {
		t.Errorf("mutating the snapshot changed workflow state to %v", v)
	}
	if _, ok := wf.GetState("injected"); ok {
		t.Error("writing to the snapshot leaked a key into workflow state")
	}
}
```

- [ ] **Step 2: 运行测试，确认它在修复前暴露问题**

```
cd D:\Destop\test\loopWorker-for-go
go test -race -run 'TestExecuteParallelSurvivesConcurrentMutation|TestStateSnapshotIsIsolated' ./pkg/workflow/
```

Expected: FAIL —— `StateSnapshot` 未定义（编译错误）。这正是先写测试的意义：在没有这个方法时任务无法通过。

- [ ] **Step 3: 添加快照与写入方法**

在 `pkg/workflow/workflow.go` 的 `GetState`（238 行）之后插入：

```go
// StepIDs returns a snapshot of the registered step ids in registration order.
// Callers that iterate steps while a run may still be adding them must read
// through this, not the StepOrder field: AddStep appends to that slice under mu
// and a bare read of it is a data race.
func (w *Workflow) StepIDs() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, len(w.StepOrder))
	copy(out, w.StepOrder)
	return out
}

// Step returns the registered step, or nil when no step has that id. Same
// reasoning as StepIDs: the Steps map is written by AddStep under mu.
func (w *Workflow) Step(id string) *Step {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.Steps[id]
}

// SetStepStatus records a step's status under the same lock AddStep uses. Every
// writer of StepStatus outside this method is a potential race with AddStep.
func (w *Workflow) SetStepStatus(stepID string, status StepStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.StepStatus == nil {
		w.StepStatus = make(map[string]StepStatus)
	}
	w.StepStatus[stepID] = status
}

// StateSnapshot returns a shallow copy of the shared state map.
//
// A step's Action receives this copy rather than the live map. The map was
// previously handed over by reference while two different mutexes guarded it,
// which could kill the process with "concurrent map writes". The copy keeps the
// documented contract intact: a step reads the state as it was when it started
// and returns an incremental result, which MergeState folds in. It is shallow,
// so nested values are still shared - exactly as SetState's merges were.
func (w *Workflow) StateSnapshot() map[string]interface{} {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make(map[string]interface{}, len(w.State))
	for k, v := range w.State {
		out[k] = v
	}
	return out
}

// MergeState folds a step's result into the shared state under the lock
// SetState uses, so one mutex governs one map.
func (w *Workflow) MergeState(result map[string]interface{}) {
	if len(result) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, v := range result {
		w.State[k] = v
	}
}
```

- [ ] **Step 4: 重写 `ExecuteParallel` 的锁结构**

替换 `pkg/workflow/workflow.go:696-811` 的整个函数：

```go
// ExecuteParallel runs every step whose dependencies are met, bounded by
// maxConcurrency.
//
// Locking rule for this function: completedMu guards the local completed map,
// and mu (via the Workflow accessors) guards the workflow's own maps. The two
// are never held at the same time. This function is called only from the
// coordinating goroutine below - once before the wait loop and once per
// completed step - so deciding what to launch and then marking those steps
// running can safely happen in two phases.
func (pw *ParallelWorkflow) ExecuteParallel(ctx context.Context) error {
	pw.mu.Lock()
	pw.Status = WorkflowRunning
	now := time.Now()
	pw.StartedAt = &now
	pw.mu.Unlock()

	// Snapshot the plan once. A step registered while this run is in flight is
	// not part of this run: totalSteps is fixed from here, so the wait loop has
	// a bound it cannot drift away from.
	order := pw.StepIDs()
	totalSteps := len(order)

	sem := make(chan struct{}, pw.maxConcurrency)
	errChan := make(chan error, 1)
	doneChan := make(chan string, len(order))

	completed := make(map[string]bool)
	launched := make(map[string]bool)
	var completedMu sync.Mutex

	launchReady := func() {
		// Phase 1 - decide, holding only completedMu. Nothing here touches the
		// workflow's own maps except through the accessors, which take mu
		// briefly and never while completedMu is held by another goroutine.
		var toLaunch []*Step
		completedMu.Lock()
		for _, stepID := range order {
			if launched[stepID] {
				continue
			}
			step := pw.Step(stepID)
			if step == nil {
				continue
			}
			if pw.GetStepStatus(stepID) != StepPending {
				continue
			}
			allDepsDone := true
			for _, depID := range step.DependsOn {
				if !completed[depID] {
					allDepsDone = false
					break
				}
			}
			if !allDepsDone {
				continue
			}
			launched[stepID] = true
			toLaunch = append(toLaunch, step)
		}
		completedMu.Unlock()

		// Phase 2 - start them. completedMu is released, so SetStepStatus is free
		// to take mu without any lock-ordering hazard.
		for _, step := range toLaunch {
			pw.SetStepStatus(step.ID, StepRunning)
			stepCopy := step
			utils.GoSafe(ctx, func(innerCtx context.Context) {
				sem <- struct{}{}
				defer func() { <-sem }()

				// A snapshot, not the live map: this read happens while other
				// steps are running and SetState may be writing.
				result, err := stepCopy.Action(innerCtx, pw.StateSnapshot())
				if err != nil {
					pw.SetStepStatus(stepCopy.ID, StepFailed)
					select {
					case errChan <- fmt.Errorf("step %s: %w", stepCopy.ID, err):
					default:
					}
					return
				}

				pw.MergeState(result)
				pw.SetStepStatus(stepCopy.ID, StepCompleted)

				completedMu.Lock()
				completed[stepCopy.ID] = true
				completedMu.Unlock()

				doneChan <- stepCopy.ID
			})
		}
	}

	launchReady()

	finished := 0
	for finished < totalSteps {
		select {
		case <-ctx.Done():
			pw.mu.Lock()
			pw.Status = WorkflowCancelled
			pw.Error = ctx.Err()
			pw.mu.Unlock()
			return ctx.Err()
		case err := <-errChan:
			pw.mu.Lock()
			pw.Status = WorkflowFailed
			pw.Error = err
			done := time.Now()
			pw.CompletedAt = &done
			pw.mu.Unlock()
			return err
		case stepID := <-doneChan:
			finished++
			completedMu.Lock()
			completed[stepID] = true
			completedMu.Unlock()
			launchReady()
		}
	}

	pw.mu.Lock()
	pw.Status = WorkflowCompleted
	completedTime := time.Now()
	pw.CompletedAt = &completedTime
	pw.mu.Unlock()

	return nil
}
```

改动要点，逐条对应缺陷：
- 705/710/717/718/719 行的无锁读 → 全部经 `StepIDs()` / `Step()` / `GetStepStatus()`
- 748 行传裸 map → 传 `StateSnapshot()`
- 751/764 行的裸写 → `SetStepStatus()` + `MergeState()`
- 761-763 行用 `completedMu` 写 `pw.State` → 改由 `MergeState()` 在 `mu` 下写
- `launchReady` 拆成两段，`completedMu` 与 `mu` 不再嵌套

- [ ] **Step 5: 运行 race 测试确认通过**

```
go test -race -run 'TestExecuteParallel|TestStateSnapshot' ./pkg/workflow/
```

Expected: PASS，零 race 告警。

- [ ] **Step 6: 确认既有测试没有回归**

```
go test ./pkg/workflow/
```

Expected: PASS。既有 `ExecuteParallel` 用例（`workflow_test.go` 的 272、429、463、528、552 行）必须仍然通过——签名没变，它们不该被修改。

- [ ] **Step 7: 提交**

```bash
git add pkg/workflow/workflow.go pkg/workflow/parallel_race_test.go
git commit -m "fix(workflow): give ExecuteParallel one lock domain instead of two

pw.State was written by ExecuteParallel under completedMu while SetState wrote
the same map under mu, so a concurrent caller could kill the process with
'fatal map writes'. Steps now read a StateSnapshot() and write through
SetStepStatus/MergeState, all under mu, and completedMu guards only the local
completed map - the two are never held at once."
```

---

### Task 2: `AdminControl` 接口与 admin 路由

修复 D2 的服务端一半。此任务**必须先于 Task 3**，因为 `pkg/server` 要调用它。

**Files:**
- Modify: `pkg/api/admin.go`、`pkg/api/config.go`、`pkg/api/api.go`
- Create: `pkg/api/admin_control_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type AdminControl interface { AdminShutdown(http.ResponseWriter, *http.Request); AdminStatus(http.ResponseWriter, *http.Request) }`、`func WithAdminControl(AdminControl) Option`、`func (s *APIServer) SetAdminControl(AdminControl)`
- 契约保证：未注入 `AdminControl` 时，admin 监听器**不注册** `/shutdown` 与 `/statusz`（返回 404），绝不 panic

- [ ] **Step 1: 写失败的测试**

创建 `pkg/api/admin_control_test.go`：

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"loopworker/pkg/security"
)

type stubAdminControl struct {
	shutdownCalls *int
	statusCalls   *int
}

func (s stubAdminControl) AdminShutdown(w http.ResponseWriter, r *http.Request) {
	*s.shutdownCalls++
	w.WriteHeader(http.StatusOK)
}

func (s stubAdminControl) AdminStatus(w http.ResponseWriter, r *http.Request) {
	*s.statusCalls++
	w.WriteHeader(http.StatusOK)
}

// TestAdminControlRequiresCredential is the core guarantee these routes lack
// today: shutdown and status must never answer an anonymous caller.
func TestAdminControlRequiresCredential(t *testing.T) {
	shutdownCalls, statusCalls := 0, 0
	server := NewAPIServerWithDependencies(Dependencies{}, WithAuth(security.DefaultAuthConfig()))
	t.Cleanup(server.Close)
	server.SetAdminControl(stubAdminControl{&shutdownCalls, &statusCalls})

	admin := server.AdminHandler()
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/shutdown"},
		{http.MethodGet, "/statusz"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Errorf("anonymous %s %s answered 200 on the admin listener", tc.method, tc.path)
		}
	}
	if shutdownCalls != 0 || statusCalls != 0 {
		t.Fatalf("a handler ran without a credential (shutdown=%d status=%d)", shutdownCalls, statusCalls)
	}

	key, _ := server.Authenticator().BootstrapKey()
	if key == "" {
		t.Fatal("a keyless server must mint an ephemeral bootstrap admin key")
	}

	for _, tc := range []struct {
		method, path string
		want         *int
	}{
		{http.MethodPost, "/shutdown", &shutdownCalls},
		{http.MethodGet, "/statusz", &statusCalls},
	} {
		before := *tc.want
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("admin %s %s = %d, want 200", tc.method, tc.path, w.Code)
		}
		if *tc.want != before+1 {
			t.Errorf("admin %s %s did not reach the injected handler", tc.method, tc.path)
		}
	}
}

// TestAdminControlAbsentRegistersNothing keeps pkg/api usable on its own: a
// build with no injected lifecycle handlers must not expose the routes at all,
// rather than panicking on a nil interface.
func TestAdminControlAbsentRegistersNothing(t *testing.T) {
	server := NewAPIServerWithDependencies(Dependencies{}, WithAuth(security.DefaultAuthConfig()))
	t.Cleanup(server.Close)

	key, _ := server.Authenticator().BootstrapKey()
	admin := server.AdminHandler()
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/shutdown"},
		{http.MethodGet, "/statusz"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 when no AdminControl is injected", tc.method, tc.path, w.Code)
		}
	}
}

// TestAdminShutdownIsPostOnly keeps the verb contract tight: the route is
// registered with Method, not Handle, so it cannot be triggered by a GET that a
// browser or crawler might make.
func TestAdminShutdownIsPostOnly(t *testing.T) {
	shutdownCalls := 0
	server := NewAPIServerWithDependencies(Dependencies{}, WithAuth(security.DefaultAuthConfig()))
	t.Cleanup(server.Close)
	server.SetAdminControl(stubAdminControl{&shutdownCalls, new(int)})

	key, _ := server.Authenticator().BootstrapKey()
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	server.AdminHandler().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /shutdown = %d, want 405", w.Code)
	}
	if shutdownCalls != 0 {
		t.Error("a GET triggered the shutdown handler")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```
go test -run 'TestAdminControl|TestAdminShutdown' ./pkg/api/
```

Expected: FAIL —— `SetAdminControl` 未定义（编译错误）。

- [ ] **Step 3: 在 `pkg/api/admin.go` 定义接口**

在文件顶部 `import` 之后插入：

```go
// AdminControl is the process-lifecycle surface the admin listener exposes.
//
// pkg/api cannot reach Server.Stop - the host owns the process, this package
// only owns the HTTP surface - so the host injects the two handlers it already
// has. Keeping them behind an interface is what lets /shutdown move off the
// public listener without pkg/api depending on pkg/server.
type AdminControl interface {
	AdminShutdown(w http.ResponseWriter, r *http.Request)
	AdminStatus(w http.ResponseWriter, r *http.Request)
}
```

- [ ] **Step 4: 在 `AdminHandler` 的 `PermAdmin` 组内注册路由**

在 `pkg/api/admin.go` 的 `authed.Get("/events/stats", s.adminEventStats)` 之后插入：

```go
		// Lifecycle and status used to hang off the public listener with no
		// credential at all, which let anyone who could reach the port stop the
		// server or read its runtime statistics. They belong here, behind the
		// admin permission, on a listener bound to loopback.
		if c := s.cfg.AdminControl; c != nil {
			// Method, not Handle, for the same reason as /metrics above: Handle
			// would register every verb and widen the contract silently.
			authed.Method(http.MethodPost, "/shutdown", c.AdminShutdown)
			authed.Get("/statusz", c.AdminStatus)
		}
```

- [ ] **Step 5: 在 `pkg/api/config.go` 增加字段与选项**

在 `Config` 结构体的 `ServeStatic bool` 字段之后插入：

```go
	// AdminControl carries the host-owned lifecycle handlers. Nil means this
	// build exposes neither /shutdown nor /statusz on the admin listener.
	AdminControl AdminControl
```

在 `WithStaticCanvas` 函数之后插入：

```go
// WithAdminControl installs the host-owned lifecycle handlers.
func WithAdminControl(c AdminControl) Option { return func(dst *Config) { dst.AdminControl = c } }
```

- [ ] **Step 6: 在 `pkg/api/api.go` 增加注入方法并修正矛盾注释**

在 `APIServer.ConfigError()` 之后插入：

```go
// SetAdminControl installs the lifecycle handlers after construction.
//
// The host needs this rather than WithAdminControl because NewAPIServer runs
// while the components are being built, before the host has a Server value to
// borrow the handlers from. It must be called before StartAdmin, which builds
// the admin router once.
func (s *APIServer) SetAdminControl(c AdminControl) { s.cfg.AdminControl = c }
```

把 `pkg/api/api.go:155` 的注释：

```go
	// Public listener: no prometheus registry, no runtime stats.
```

改为：

```go
	// Public listener: the versioned API, the canvas, and the liveness probe.
	// Runtime statistics and the lifecycle endpoints live on the admin listener,
	// which binds to loopback and requires the admin permission.
```

- [ ] **Step 7: 运行测试确认通过**

```
go test -run 'TestAdminControl|TestAdminShutdown|TestMetricsOnlyAcceptsGet' ./pkg/api/
go test ./pkg/api/
```

Expected: 全部 PASS。`TestMetricsOnlyAcceptsGet` 用来确认 `Method` 而非 `Handle` 的写法没有影响既有 `/metrics` 契约。

- [ ] **Step 8: 确认路由与契约没有漂移**

```
go test -run 'TestOpenAPISpec' ./pkg/api/
```

Expected: PASS。`TestOpenAPISpecMatchesRegisteredRoutes`（`openapi_test.go:55`）已存在并守住这条不变量。**不要新增重复测试。** admin 端点不在 `/api/v1` 之下，因此 `openapi.json` 无需修改。

- [ ] **Step 9: 提交**

```bash
git add pkg/api/admin.go pkg/api/config.go pkg/api/api.go pkg/api/admin_control_test.go
git commit -m "feat(api): move shutdown and status behind the admin permission

Both used to hang off the public listener with no credential, so anyone who
could reach the port could stop the server or read its runtime statistics.
They are now registered on the admin listener inside the existing PermAdmin
group, behind a nil-guarded AdminControl interface so pkg/api still builds and
tests without a host."
```

---

### Task 3: 公共 mux 移除未鉴权端点并接线

修复 D2 的宿主一半。必须在 Task 2 之后执行。

**Files:**
- Modify: `pkg/server/server.go`
- Create: `pkg/server/admin_control_test.go`

**Interfaces:**
- Consumes: `api.AdminControl`、`(*api.APIServer).SetAdminControl`（Task 2 产出）
- Produces: 无新公开 API。`Server.HandleShutdown` 与 `Server.StatusHandler` 保持原签名与实现，只是从公共 mux 搬到 admin 监听器

- [ ] **Step 1: 写失败的测试**

创建 `pkg/server/admin_control_test.go`：

```go
package server

import (
	"net/http"
	"path/filepath"
	"testing"
)

// TestPublicListenerExposesNoLifecycleEndpoints is the regression guard for a
// remote denial of service: /shutdown used to sit on the public mux behind
// nothing but an HTTP method check.
func TestPublicListenerExposesNoLifecycleEndpoints(t *testing.T) {
	srv := newTestServer(t, testConfig(t))

	for _, path := range []string{"/shutdown", "/statusz"} {
		if code := do2(srv.handler(), path); code != http.StatusNotFound {
			t.Errorf("public GET %s = %d, want 404", path, code)
		}
	}
}

// TestHealthzStaysPublic is the other half: the fix must not close the probe
// orchestrators depend on.
func TestHealthzStaysPublic(t *testing.T) {
	srv := newTestServer(t, testConfig(t))

	if code := do2(srv.handler(), "/healthz"); code == http.StatusNotFound {
		t.Error("/healthz must stay reachable on the public listener")
	}
}
```

三个助手都是本包 `server_test.go` 里既有的：`testConfig(t)`（第 37 行，已把 `WorkDir`、`Data.Dir`、`Plugins.Dir` 全部指向 `t.TempDir()`）、`newTestServer(t, cfg)`（第 66 行）、`do2(handler, path)`（第 418 行，发 GET 并返回状态码）。因为旧的 `/shutdown` 是用 `HandleFunc` 注册的、对所有动词生效，用 GET 探测就足以证明路由已被摘除。

- [ ] **Step 2: 运行测试确认失败**

```
go test -run 'TestPublicListenerExposesNoLifecycleEndpoints|TestHealthzStaysPublic' ./pkg/server/
```

Expected: FAIL —— `POST /shutdown` 目前在公共 mux 上返回 200 而非 404。

- [ ] **Step 3: 从公共 mux 移除两条路由**

把 `pkg/server/server.go:618-630` 的 `handler()` 改为：

```go
func (s *Server) handler() http.Handler {
	components := s.components
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.HealthHandler())
	// /shutdown and /statusz used to be registered here, on the listener anyone
	// can reach, with no credential: a POST to /shutdown stopped the process and
	// GET /statusz printed scheduler and security internals. They are served by
	// the admin listener now, behind the admin permission - see Start, where
	// SetAdminControl hands them over.
	//
	// Authentication is pkg/api's job, in one place, with one credential set
	// (see authConfigFor). A second gate here used to read security.api_key
	// independently, which meant the file key and the environment keys were two
	// systems that could disagree about who is allowed in.
	mux.Handle("/", components.APIServer.Router)
	return mux
}
```

- [ ] **Step 4: 注入 AdminControl**

在 `pkg/server/server.go` 的 `import` 块后新增一个类型定义（放在文件末尾的辅助函数区域亦可）：

```go
// adminControl adapts the Server to the lifecycle surface pkg/api exposes on
// the admin listener. The two handlers are the Server's own existing methods,
// unchanged - only the listener they are reachable from moves.
type adminControl struct {
	srv *Server
}

func (a adminControl) AdminShutdown(w http.ResponseWriter, r *http.Request) {
	a.srv.HandleShutdown(w, r)
}

func (a adminControl) AdminStatus(w http.ResponseWriter, r *http.Request) {
	a.srv.StatusHandler()(w, r)
}
```

在 `Start()` 中，把 402 行 `if admin, err := components.APIServer.StartAdmin(ctx); err != nil {` 这一句**之前**插入：

```go
	// Hand the lifecycle handlers to the admin listener before it builds its
	// router. SetAdminControl rather than WithAdminControl because the Server
	// value does not exist yet when BuildComponents constructs the API server.
	components.APIServer.SetAdminControl(adminControl{srv: s})
```

同时把 410-411 行那条日志里的端点列表补全：

```go
		logger.Info("admin listener bound", zap.String("address", admin.Addr),
			zap.String("endpoints", "/metrics, /runtime/stats, /logs, /events/stats, /statusz, POST /shutdown"))
```

- [ ] **Step 5: 运行测试确认通过**

```
go test ./pkg/server/
```

Expected: PASS。

- [ ] **Step 6: 全量回归**

```
cd D:\Destop\test\loopWorker-for-go
go build ./... && go test ./...
```

Expected: 均 exit 0。

- [ ] **Step 7: 提交**

```bash
git add pkg/server/server.go pkg/server/admin_control_test.go
git commit -m "fix(server): take shutdown and status off the public listener

POST /shutdown answered on the public port behind nothing but a method check,
so anyone who could reach it could stop the process; GET /statusz printed
scheduler, executor and security internals. Both now reach the admin listener
via SetAdminControl, which binds to loopback and requires the admin permission."
```

---

### Task 4: 让出厂 GUI 在默认鉴权下真正可用

修复 D3、D4、D7。**服务端零改动** —— `pkg/security/auth.go:552-569` 只读请求头，fetch 能设头，所以用 fetch 流替代 `EventSource` 即可带上凭据，不需要 query token（会进日志）或 cookie（要引入 CSRF 攻击面）。

**Files:**
- Create: `web/canvas/src/api.js`
- Modify: `web/canvas/src/App.jsx`、`web/canvas/index.html`
- Rebuild: `pkg/api/dist/**`

**Interfaces:**
- Consumes: 无后端改动。依赖服务端既有行为：`X-API-Key` 请求头（`security.DefaultAuthConfig`）、`input_text` 字段（`pkg/api/views_input.go:137`）、SSE 命名事件（`pkg/event/event.go:31-36`）
- Produces: `getToken()`、`setToken(v)`、`apiFetch(path, options)`、`ApiError`、`sseFetch(path, handlers, signal)`

- [ ] **Step 1: 创建前端数据出口**

创建 `web/canvas/src/api.js`：

```js
// Every call to the LoopWorker API goes through this module.
//
// The API authenticates each /api/v1 route with a header credential: the Go
// authenticator reads X-API-Key and Authorization, and nothing else. The
// browser's EventSource cannot set headers, which is why the live stream below
// uses fetch plus a streaming reader instead - same credential path, no token in
// the URL and therefore nothing in the access log or browser history.

const TOKEN_KEY = 'loopworker.apiKey';
const API_KEY_HEADER = 'X-API-Key';

export function getToken() {
  try {
    return window.localStorage.getItem(TOKEN_KEY) || '';
  } catch {
    // Private browsing can refuse localStorage; the session simply will not
    // persist across reloads, which is better than failing to load.
    return '';
  }
}

export function setToken(value) {
  try {
    if (value) {
      window.localStorage.setItem(TOKEN_KEY, value);
    } else {
      window.localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    /* see getToken */
  }
}

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// The server always answers with {success, error:{code,message}}; show the
// repair hint it wrote for the customer rather than "[object Object]".
function messageFrom(body, fallback) {
  if (body && body.error && body.error.message) return body.error.message;
  return fallback;
}

export async function apiFetch(path, options = {}) {
  const token = getToken();
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  if (token) headers[API_KEY_HEADER] = token;

  const res = await fetch(path, { ...options, headers });
  const text = await res.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, messageFrom(body, `${res.status} ${res.statusText}`));
  }
  return body;
}

// sseFetch subscribes to the live event stream.
//
// handlers is keyed by event name; a frame with no name goes to handlers.message,
// matching EventSource's own default. The returned promise rejects on a refused
// stream; pass an AbortSignal and cancel it on unmount, or the connection leaks.
export function sseFetch(path, handlers = {}, signal) {
  const token = getToken();
  const headers = { Accept: 'text/event-stream' };
  if (token) headers[API_KEY_HEADER] = token;

  return (async () => {
    const res = await fetch(path, { headers, signal });
    if (!res.ok || !res.body) {
      throw new ApiError(res.status, `event stream refused: ${res.status}`);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let eventName = 'message';
    let dataLines = [];

    const flush = () => {
      if (dataLines.length > 0) {
        const handler = handlers[eventName] || handlers.message;
        if (handler) handler(dataLines.join('\n'), eventName);
      }
      dataLines = [];
      eventName = 'message';
    };

    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let idx;
      while ((idx = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, idx).replace(/\r$/, '');
        buffer = buffer.slice(idx + 1);
        if (line === '') {
          flush();
        } else if (line.startsWith(':')) {
          // keep-alive comment; nothing to dispatch
        } else if (line.startsWith('event:')) {
          eventName = line.slice(6).trim();
        } else if (line.startsWith('data:')) {
          dataLines.push(line.slice(5).trimStart());
        }
      }
    }
  })();
}
```

- [ ] **Step 2: 在 `App.jsx` 加凭据门并改用新模块**

修改 `web/canvas/src/App.jsx`：

1. 首行 import 之后追加：

```jsx
import { getToken, setToken, apiFetch, sseFetch } from './api';
```

2. 把第 24 行的 `export default function App() {` 改名为 `function Canvas() {`，并在组件内新增错误状态：

```jsx
  const [error, setError] = useState('');
```

3. 把 `fetchGraph`（39-54 行）替换为：

```jsx
  const fetchGraph = useCallback(() => {
    setIsLoading(true);
    apiFetch('/api/v1/workflow/graph')
      .then((resData) => {
        setIsLoading(false);
        setError('');
        if (resData.success && resData.data) {
          setNodes(resData.data.nodes || []);
          setEdges(resData.data.edges || []);
        }
      })
      .catch((e) => {
        setIsLoading(false);
        setError(e.message);
      });
  }, [setNodes, setEdges]);
```

4. 把 SSE 的 `useEffect`（61-105 行）替换为（`handleUpdate` 提为 `useCallback`）：

```jsx
  const handleUpdate = useCallback((raw) => {
    let task = null;
    try {
      task = JSON.parse(raw);
    } catch {
      // Not a task payload (stream.opened, a system event); nothing to paint.
      return;
    }
    // A task this caller does not own arrives as {"hidden":true,...} rather than
    // as data, so there is deliberately nothing to update here.
    if (!task || !task.ID) return;

    setNodes((nds) =>
      nds.map((node) =>
        node.id === task.ID ? { ...node, data: { ...node.data, task } } : node
      )
    );
    setSelectedNode((curr) => (curr && curr.ID === task.ID ? task : curr));
  }, [setNodes]);

  useEffect(() => {
    const controller = new AbortController();
    // These names are the server's EventType values; see pkg/event/event.go.
    const names = [
      'task.created',
      'task.started',
      'task.completed',
      'task.failed',
      'task.retried',
      'task.cancelled',
    ];
    const handlers = {};
    names.forEach((name) => {
      handlers[name] = handleUpdate;
    });

    sseFetch('/api/v1/events/live', handlers, controller.signal).catch((e) => {
      if (e.name !== 'AbortError') setError(e.message);
    });

    return () => controller.abort();
  }, [handleUpdate]);
```

5. 把 `onConnect`（108-125 行）里的 fetch 替换为：

```jsx
      apiFetch(`/api/v1/tasks/${params.target}/dependencies`, {
        method: 'POST',
        body: JSON.stringify({ dependency_id: params.source }),
      })
        .then((resData) => {
          if (resData.success) {
            setError('');
            fetchGraph();
          }
        })
        .catch((e) => setError(e.message));
```

6. 把 `handleSubmit`（133-164 行）替换为。**注意 `input_text` 这一行就是 D4 的修复**：

```jsx
  const handleSubmit = (e) => {
    e.preventDefault();
    const payload = {
      type: taskType,
      config: {},
      // input_text is the plain-text path. The old code sent btoa(userInput) with
      // no input_encoding, and the server took the base64 text as the payload
      // verbatim - the task silently ran against "aHR0cDov..." instead of the URL.
      input_text: userInput,
      is_agent: isAgent,
    };

    if (isAgent) {
      payload.agent_config = {
        system_prompt: systemPrompt,
        model: model,
        schema: responseSchema,
      };
    }

    apiFetch('/api/v1/tasks', { method: 'POST', body: JSON.stringify(payload) })
      .then((resData) => {
        if (resData.success) {
          setIsCreating(false);
          setError('');
          fetchGraph();
        }
      })
      .catch((e) => setError(e.message));
  };
```

7. 在 `<Canvas>` 的 JSX 里渲染错误条（放在 `<header>` 之后）：

```jsx
      {error && (
        <div
          role="alert"
          style={{
            position: 'absolute',
            top: '84px',
            left: '20px',
            right: '20px',
            padding: '10px 14px',
            borderRadius: '10px',
            background: 'rgba(120, 20, 20, 0.85)',
            color: '#fff',
            fontSize: '13px',
            zIndex: 10,
          }}
        >
          {error}
        </div>
      )}
```

8. 在文件末尾追加凭据门与默认导出：

```jsx
function TokenGate({ onUnlock }) {
  const [value, setValue] = useState('');

  const submit = (e) => {
    e.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) return;
    setToken(trimmed);
    onUnlock(trimmed);
  };

  return (
    <div
      style={{
        width: '100%',
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <MeshBackground />
      <form
        onSubmit={submit}
        className="glass-panel"
        style={{ position: 'relative', width: '420px', padding: '28px', borderRadius: '16px' }}
      >
        <h2 style={{ margin: '0 0 6px', fontSize: '18px' }}>Connect to LoopWorker</h2>
        <p style={{ margin: '0 0 16px', fontSize: '13px', opacity: 0.75 }}>
          This server requires an API key. It is printed once in the server log when it
          starts; paste it here. It is stored in this browser only.
        </p>
        <input
          type="password"
          value={value}
          autoFocus
          onChange={(e) => setValue(e.target.value)}
          placeholder="X-API-Key"
          style={{
            width: '100%',
            padding: '10px 12px',
            borderRadius: '8px',
            border: '1px solid rgba(255,255,255,0.2)',
            background: 'rgba(0,0,0,0.25)',
            color: 'inherit',
            boxSizing: 'border-box',
          }}
        />
        <button
          type="submit"
          style={{ marginTop: '14px', width: '100%', padding: '10px', cursor: 'pointer' }}
        >
          Connect
        </button>
      </form>
    </div>
  );
}

export default function App() {
  const [token, setTokenState] = useState(getToken());

  if (!token) {
    return <TokenGate onUnlock={setTokenState} />;
  }
  // key remounts the canvas when the credential changes, so no stale state or
  // half-open stream survives a re-connect.
  return <Canvas key={token} />;
}
```

- [ ] **Step 3: 改产品标题**

把 `web/canvas/index.html` 的 `<title>canvas</title>` 改为 `<title>LoopWorker</title>`。

- [ ] **Step 4: 构建并回写嵌入产物**

```
cd D:\Destop\test\loopWorker-for-go\web\canvas
npm ci
npm run build
```

Expected: 构建成功，产物出现在 `web\canvas\dist`。

然后**先覆盖写入、再删除陈旧文件**。`//go:embed all:dist` 会嵌入目录下所有文件，所以旧 hash 的 js/css 必须清掉；但**绝不能先把 `pkg/api/dist` 删空**——那个窗口里 `go:embed` 失败，任何并行的 `go build` 或 `go test ./pkg/api/` 都会报 embed 错误。倒过来做，目录始终有内容：

```
cd D:\Destop\test\loopWorker-for-go
$new = (Resolve-Path web/canvas/dist).Path
$dst = (Resolve-Path pkg/api/dist).Path
Copy-Item -Recurse -Force "$new/*" $dst
$keep = Get-ChildItem -Recurse -File $new | ForEach-Object { $_.FullName.Replace("$new\", '') }
Get-ChildItem -Recurse -File $dst | Where-Object { $keep -notcontains $_.FullName.Replace("$dst\", '') } | Remove-Item -Force
Get-ChildItem -Recurse $dst | ForEach-Object { $_.FullName.Replace("$dst\", '') + '  ' + $_.Length }
```

Expected: 输出里**只有**一份 `index-*.js` 和一份 `index-*.css`，没有第二个 hash 版本的同名文件，且 `index.html`、`favicon.svg`、`icons.svg` 都在。

- [ ] **Step 5: 清理构建残留**

整洁要求：构建产物不得留在工作区。

```
cd D:\Destop\test\loopWorker-for-go
Remove-Item -Recurse -Force web/canvas/node_modules
Remove-Item -Recurse -Force web/canvas/dist
git status --short
```

Expected: `git status --short` 里**没有** `web/canvas/node_modules` 或 `web/canvas/dist`，只有 `App.jsx`、`api.js`、`index.html`、`pkg/api/dist` 的改动。

- [ ] **Step 6: 确认后端仍能编译并通过测试**

嵌入产物变了，`go:embed` 会重新打包，所以必须验证：

```
go build ./... && go test ./pkg/api/ ./pkg/server/
```

Expected: 均 exit 0。

- [ ] **Step 7: 提交**

```bash
git add web/canvas/src/api.js web/canvas/src/App.jsx web/canvas/index.html pkg/api/dist
git commit -m "fix(canvas): make the shipped GUI work against an authenticated server

Every /api/v1 route requires a header credential and the SPA sent none, so a
fresh install showed a canvas that could not load anything. Adds a credential
gate plus apiFetch/sseFetch - the stream uses fetch with a streaming reader
because EventSource cannot set headers, so the server needs no change.

Also sends input_text instead of a bare btoa(): with no input_encoding the
server took the base64 text as the payload verbatim, so tasks silently ran
against 'aHR0cDov...' rather than the text the operator typed."
```

---

### Task 5: 诚实化、工作区整洁与商业化交付件

修复 D5、D6、D7。这是唯一由主代理审阅全文的任务，因为它写的是关于产品的声明。

**Files:**
- Delete: `docs/.d1-scratch/`、`_scratch/`
- Modify: `.gitignore`、`.release/SCOPE-PROPOSAL.md`、`CONTRIBUTING.md`、`docs/USAGE.md`
- Create: `.release/PRE-RELEASE-CHECKLIST.md`

**Interfaces:**
- Consumes: 全部后端与前端任务的最终行为
- Produces: 无代码接口

> **重要**：工作区里已有上一轮会话未提交的文档改动（`docs/USAGE.md`、`docs/api/api-reference.md`、`docs/architecture.md`、`docs/guides/quick-start.md`、`CHANGELOG.md`、`.release/SCOPE-PROPOSAL.md`）。**在既有改动之上修改，不要回退它们。**

- [ ] **Step 1: 删除运行时残留**

```
cd D:\Destop\test\loopWorker-for-go
Remove-Item -Recurse -Force docs/.d1-scratch
if (Test-Path _scratch) { Remove-Item -Recurse -Force _scratch }
Get-ChildItem -Force | Where-Object { $_.Name -like '*scratch*' }
```

Expected: 无输出。删除前先确认 `docs/.d1-scratch` 内没有需要保留的东西——它含 31MB 的 `lw.exe`、SQLite 数据库与 WAL、boot 日志，全部是上次验证的产物。

- [ ] **Step 2: 收紧 `.gitignore`**

删除 `.gitignore` 中**重复的第二段**（从第二个 `# ==================== Go ====================` 注释块开始，到 `*.log` 结束），保留第一段。保留这两条例外，它们是客户可运行的输入而非构建掉落物：

```
!examples/hello-plugin/hello.wasm
!pkg/plugin/testdata/hello.wasm
```

在 "Agent Scratch Space" 一节补充：

```
# Runtime leftovers from earlier verification runs. docs/.d1-scratch held a
# 31 MB binary and a SQLite WAL before this rule existed; catch the pattern, not
# just the one directory.
.d*-scratch/
.d-scratch/
```

- [ ] **Step 3: 重写 `.release/SCOPE-PROPOSAL.md`**

把文件替换为一份状态文档。保留决策轨迹（为什么砍掉那四个 CLI 仍然有效），但每一条必须标注它**今天**是否还成立：

```markdown
# SCOPE — what ships, what was cut, and what is still open

Status: **current as of 2026-10-06.** Every "Still open" item below is a real,
unimplemented gap. Everything under "Settled" was verified against the code in
this tree, not against an earlier plan.

## Settled

### The four dev-scratch CLIs are gone — verified
`loopbench`, `loopsim`, `loopdebug`, `loopwatch` never imported `pkg/client`
and made no HTTP call; they slept locally and printed a table. Deleted. `cmd/`
now holds `loopworker` (the product) and `loopctl` (the one dev CLI whose
commands are all live routes, and which has tests). Recover the old sources
with `git show <rev>:cmd/<name>/main.go`.

### The canvas landing page is fixed — verified
An earlier revision of this file reported that `GET /` was an infinite redirect
loop. It is not. `pkg/api/api.go` reads `dist/index.html` once at init and
serves those bytes directly, because `http.FileServer` 301-redirects any path
ending in `/index.html` back to itself. See `canvasIndex` and
`pkg/api/static_test.go`.

### The documented YAML config schema is the one that loads — verified
An earlier revision reported that `cmd/loopworker/main.go` parsed flat JSON keys
while `config/config.example.yaml` used nested keys. It does not: `main.go:112`
calls `config.Load(config.Options{ConfigFile: cfgFile, Flags: overrides(cmd)})`,
which implements the nested schema in `internal/config/load.go`.

### `LoopOptions.Allowlist` is not a thing — verified
An earlier revision implied digest pinning by allowlist. `LoadOptions.Allowlist`
was removed; `RequireChecksum` and the manifest digest are the whole story.

## Still open

### `cmd/loopctl` cannot read a single workflow
`GET /api/v1/workflow/{id}` is registered (`pkg/api/api.go:188`) but `loopctl`
has `workflow list` and `workflow execute`, not `get`. curl works. Either add a
six-line `loopctl workflow get [id]` or accept that curl is the documented path.

### Runtime verification is manual
There is no automated check that a real browser can drive the canvas end to
end. `go test` covers the API; nothing covers the GUI. `PRE-RELEASE-CHECKLIST.md`
now carries the manual steps, which is weaker than a test but honest about it.
```

- [ ] **Step 4: 在 `CONTRIBUTING.md` 补两节**

追加：

```markdown
## Rebuilding the embedded web canvas

`pkg/api/dist` is a build artifact committed to the repository on purpose: the
server embeds it with `//go:embed all:dist`, so a release binary needs no Node
toolchain and CI needs no frontend build. Rebuild it only when you change
anything under `web/canvas/src`.

```bash
cd web/canvas
npm ci
npm test
npm run build
cd ../..
mkdir -p pkg/api/dist && cp -r web/canvas/dist/. pkg/api/dist/
diff -r web/canvas/dist pkg/api/dist    # must be empty
ls pkg/api/dist/assets/index-*.js pkg/api/dist/assets/index-*.css   # one of each; delete leftovers
rm -rf web/canvas/node_modules web/canvas/dist
go build ./... && go test ./pkg/api/
```

Clear out the stale hashed assets after the new ones are in place. The embed
directive takes every file in the directory, so a stale `index-<hash>.js` left
behind by a previous build is embedded into the binary and shipped to
customers. Copy first and delete second - emptying the directory first breaks
`//go:embed all:dist` for anything compiling concurrently.

## Keeping the tree clean

- Scratch files, verification output and throwaway binaries go in `_scratch/`,
  which is git-ignored. Nowhere else.
- `web/canvas/node_modules` and `web/canvas/dist` are build leftovers; remove
  them when you are done building (see above).
- Before committing, `git status --ignored` must not list a runtime leftover
  anywhere except `_scratch/`.
```

- [ ] **Step 5: 补 `docs/USAGE.md` 的 admin 端点说明**

在描述监听的章节追加：

```markdown
### Admin listener

Metrics, logs, runtime statistics and the lifecycle endpoints are served on a
separate listener bound to `127.0.0.1:19528` by default, and every route on it
requires an administrator credential:

| Route | Method | Purpose |
|---|---|---|
| `/metrics` | GET | Prometheus exposition |
| `/runtime/stats` | GET | Scheduler and stream counters |
| `/logs` | GET | Recent observer output |
| `/events/stats` | GET | Event bus counters |
| `/statusz` | GET | Process status: version, uptime, component state |
| `/shutdown` | POST | Graceful shutdown; responds before draining |

None of these are on the public listener. `/healthz` is the only
unauthenticated probe there.
```

- [ ] **Step 6: 写 `.release/PRE-RELEASE-CHECKLIST.md`**

每一条都必须可判定通过/失败，不得是"检查一下"：

```markdown
# Pre-release checklist

Every line is pass/fail. A release with an unrun line is not a release.

## Build and verify

- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `go test ./...` exits 0 with no FAIL
- [ ] `go test -race ./...` exits 0 with no race report
- [ ] `make lint` exits 0 (or the documented equivalent in `.golangci.yaml`)

## Cleanliness

- [ ] `git status --ignored --short` lists no runtime leftover outside `_scratch/`
- [ ] No file over 1 MB is untracked (build outputs belong in `_scratch/`)
- [ ] `pkg/api/dist` contains exactly one `index-*.js` and one `index-*.css`
- [ ] `web/canvas/node_modules` and `web/canvas/dist` do not exist

## Product surface, verified in a real browser

- [ ] Start the server on a free port with no API keys configured; copy the
      bootstrap key it logs
- [ ] `GET /` returns 200 and renders the canvas, with no redirect loop
- [ ] The credential gate appears; the graph request before any key returns 401
- [ ] Pasting the key loads the workflow graph
- [ ] Creating a task makes a canvas node actually change state (not merely
      "no error appeared")
- [ ] The browser tab title reads LoopWorker, not canvas
- [ ] `POST /shutdown` on the public port returns 404
- [ ] `POST /shutdown` on the admin port with an admin key returns 200
- [ ] `GET /statusz` on the public port returns 404

## Documentation truthfulness

- [ ] Every route in `pkg/api` appears in `docs/api/api-reference.md`
- [ ] `docs/QUICKSTART.md` commands run as written
- [ ] `.release/SCOPE-PROPOSAL.md` describes this tree, not an earlier revision
- [ ] `CHANGELOG.md` describes this build

## Packaging

- [ ] The release archive contains no `web/canvas` sources, no `node_modules`,
      no scratch directories and no database files
- [ ] The Docker image runs `loopworker --help` with no errors
```

- [ ] **Step 7: 验证整洁**

```
cd D:\Destop\test\loopWorker-for-go
git status --ignored --short
```

Expected: 除了 `_scratch/`（若存在）之外没有任何 `!!` 条目。

- [ ] **Step 8: 提交**

```bash
git add -A .gitignore CONTRIBUTING.md docs .release
git commit -m "docs: make the scope document describe this tree, and clean the workspace

SCOPE-PROPOSAL.md reported a redirect loop and a flat-JSON config loader that
were both fixed before this revision; a document that lies about the product is
worse than no document. Adds the release checklist and the canvas rebuild
procedure, so the embedded UI stays rebuildable by someone who has never seen it."
```

---

### Task 6: 串行验收（主代理执行，不派子代理）

浏览器验收与全量测试都需要整体启动，无法拆分给子代理。

- [ ] **Step 1: 全量构建与测试**

```
cd D:\Destop\test\loopWorker-for-go
go build ./... && go vet ./... && go test ./... && go test -race ./...
```

Expected: 全部 exit 0，无 race 报告。

- [ ] **Step 2: 以零配置启动并取凭据**

在 `_scratch/` 下启动，不污染工作区：

```
cd D:\Destop\test\loopWorker-for-go
New-Item -ItemType Directory -Force _scratch/verify | Out-Null
go build -o _scratch/verify/loopworker.exe ./cmd/loopworker
```

以默认端口启动（可换 `--port` 避开占用），从日志中取它打印的 bootstrap key，并记录该 key。

- [ ] **Step 3: 验证公共端点不再暴露关停与状态**

```
curl.exe -i -X POST http://127.0.0.1:19527/shutdown
curl.exe -i http://127.0.0.1:19527/statusz
```

Expected: 两者都返回 **404**。

- [ ] **Step 4: 验证 admin 端点需要 admin 凭据**

```
curl.exe -i http://127.0.0.1:19528/statusz
curl.exe -i -H "X-API-Key: <bootstrap-key>" http://127.0.0.1:19528/statusz
```

Expected: 无凭据时 401 或 403；带凭据时 200。

- [ ] **Step 5: 用 `@browser-use` 在真实浏览器验证 GUI**

加载 `browser-use:control-in-app-browser` 技能，然后：

1. 导航到 `http://127.0.0.1:19527/`
2. 确认**凭据门出现**，且在粘贴 key 之前画布为空（证明没有未鉴权请求被发出）
3. 打开浏览器控制台的 network 面板记录：粘贴 key 前 `/api/v1/workflow/graph` 应为 401
4. 粘贴 bootstrap key 并提交
5. 确认**工作流图真的渲染出节点**（`GET /api/v1/workflow/graph` 返回 200 且 `data.nodes` 非空）
6. 提交一个任务，然后确认**至少一个画布节点的状态真的改变**——不是"页面没报错"
7. 确认浏览器标签页标题是 **LoopWorker** 而非 canvas
8. 确认关闭/刷新后凭据从 localStorage 恢复，无需重新粘贴

第 6 步是本任务的硬性验收点。`renderEvent` 在调用者无权时会下发 `{"hidden":true}` 桩数据，而前端的处理函数对此静默忽略——所以"没有报错"完全可能意味着"什么也没发生"。必须看到节点状态变化。

- [ ] **Step 6: 收尾**

```
cd D:\Destop\test\loopWorker-for-go
Remove-Item -Recurse -Force _scratch/verify
git status --short
```

Expected: 工作区只剩预期的源码改动，无二进制、无数据库、无日志。

- [ ] **Step 7: 汇总验收结果**

逐条对照 `.release/PRE-RELEASE-CHECKLIST.md` 报告，每条写"通过 / 未通过 + 证据"。任何未通过的条目都不得记为完成。

---

## Self-Review

**1. 规格覆盖**

| 规格条目 | 覆盖任务 |
|---|---|
| D1 并发锁域破裂 | Task 1 |
| D2 未鉴权关停与泄露 | Task 2（服务端）+ Task 3（宿主） |
| D3 GUI 全部 401 | Task 4 |
| D4 GUI 静默损坏输入 | Task 4 Step 2.6 |
| D5 提案文档失真 | Task 5 Step 3 |
| D6 残留与忽略规则 | Task 5 Step 1-2 |
| D7 商用级粗糙 | Task 4 Step 3 + Task 5 Step 6 |
| W4 防漂移测试 | **已存在**：`TestOpenAPISpecMatchesRegisteredRoutes`（`pkg/api/openapi_test.go:55`）。Task 2 Step 8 改为验证它仍通过，不新增重复测试 |
| 并行执行策略 | Task 依赖已按文件所有权切开，见 Execution Order |

**2. 占位符扫描**：无 TBD、无 TODO、无"参照 Task N"。所有代码块给出可直接编译的完整实现。

**3. 类型一致性**

- `AdminControl` 的两个方法在 Task 2 定义、Task 3 实现（`adminControl` 结构体），签名一致。
- `SetAdminControl` 在 Task 2 定义、Task 3 调用，名称一致。
- `StepIDs` / `Step` / `SetStepStatus` / `StateSnapshot` / `MergeState` 全部在 Task 1 内定义并使用。
- `GetState(key) (interface{}, bool)` 是既有方法（`workflow.go:238`），Task 1 的测试直接使用，未重复定义。
- 前端 `apiFetch` 返回已解析的 body，`sseFetch` 返回 Promise，Task 4 的三处调用与定义一致。

**已识别的一处规格偏差**：`StateSnapshot` 是浅拷贝，因此嵌套结构仍共享引用。规格 §4 W1 已声明这一点，代码注释与测试（`TestStateSnapshotIsIsolated` 只断言顶层键）均与之一致。

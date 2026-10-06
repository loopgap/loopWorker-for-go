# 测试验证 / Test verification

> **这份文档不复制数字。** 上一个版本在这里逐包列出测试数与覆盖率，写的是一棵
> 不存在的树（`pkg/dashboard`、`pkg/dispatcher`、`pkg/executor` —— 真实路径在
> `internal/core/` 下），声称 194 个测试、16 个包；实测是 **855 个测试、22 个包**。
> 一个会过期的事实性报告，改代码之后只会变成谎言。
>
> **This document does not duplicate numbers.** The previous version listed
> per-package test counts for a tree that does not exist, claiming 194 tests
> across 16 packages. Reality is 855 tests across 22. A factual snapshot goes
> stale the moment code changes; the commands below are the source of truth.

## 当前状态 / Current state (2026-10-06)

| 指标 | 值 | 复现命令 |
|---|---|---|
| 测试函数 | **855** | `go test ./... -list '.*' \| grep -c '^Test'` |
| 有测试的包 | **22** | `go test ./... \| grep -c '^ok'` |
| 全部通过 | ✅ | `go test ./... -count=1` |
| 竞态 | **0 DATA RACE** | `go test -race -count=1 ./...` |
| 语句覆盖率 | **80.3%** | `go tool cover -func`（阈值 80） |
| 发布包覆盖率下限 | 每包 ≥60% | `covergate -pkg ./cmd/loopworker,./cmd/loopctl -pkg-min 60` |
| 已知可达漏洞 | **0** | `govulncheck ./...` |

## 逐包测试数 / Tests per package

```
for p in $(go list ./...); do n=$(go test "$p" -list '.*' 2>/dev/null | grep -c '^Test'); \
  [ "$n" -gt 0 ] && printf "%-42s %s\n" "$p" "$n"; done | sort -k2 -rn
```

最大的几处（2026-10-06 实测）：

| 包 | 测试数 |
|---|---|
| `pkg/api` | 178 |
| `internal/core/scheduler` | 90 |
| `internal/core/sandbox` | 66 |
| `internal/core/executor` | 51 |
| `pkg/server` | 50 |
| `pkg/security` | 49 |
| `internal/core/selfheal` | 48 |
| `pkg/event` | 46 |
| `pkg/workflow` | 39 |

**没有测试文件的包**：`cmd/{loopbench,loopdebug,loopsim,loopwatch}`、
`examples/{hello-plugin,simple,workflow}`。前四个是开发者工具，不进入任何发布
产物（见 `.release/SCOPE-PROPOSAL.md`）。

## 复现全部闸门 / Reproduce every gate

```bash
make fmt-check          # gofmt -l 必须为空
make vet                # go vet ./...
make build              # go build ./cmd/... ./pkg/... ./internal/... ./version/...
make test               # 全树单测
make test-race          # -race
make cover              # 覆盖率 profile + go tool cover -func
make coverage-gate      # 总量 80% + 发布包每包 60%
make boot-smoke         # 6 道真实二进制闸门
```

`make boot-smoke` 是**唯一**会真正起服务、建任务、跑 WASM 插件、洪泛限流、
再发 SIGTERM 的检查。单元测试证明不了「这个产品能用」。

## 计时注意事项 / Timing notes

- `pkg/plugin` 单次约 50 秒，**`-race` 下约 9 分钟**：每个夹具都真的把一个
  2.5 MB 的 Go→wasm 模块编译一次（约 110 次）。这不是挂起，是设计使然。
  `go test` 的默认 10 分钟超时会在 `-race` 下被打到，需要 `-timeout 3600s`。
- 全树 `-race` 约 40 分钟。
- `pkg/security` `-race` 约 3 分钟。
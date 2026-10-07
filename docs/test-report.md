# 测试验证 / Test verification

> **本文不重复数字 / This document does not duplicate numbers.**
>
> 上一版列了一份「每包测试数」，声称某棵并不存在的树上有 194 个测试、16 个包。
> 实际是另一个数量，而且任何写下来的数字都会在代码改动的当天过期。这里只保留
> **产生这些数字的命令**——命令才是真源。
>
> A previous version listed per-package test counts for a tree that does not
> exist. A factual snapshot goes stale the moment code changes, so the commands
> below are the source of truth and this file carries no totals.

## 怎么得到这些数字 / How to produce the numbers

```bash
# 测试函数总数 / number of test functions
go test ./... -list . | grep -c '^Test'

# 有测试的包数 / packages that actually have tests
go test ./... | grep -c '^ok'

# 没有测试文件的包 / packages with no test files
go test ./... -list . | grep 'no test files'

# 覆盖率 / coverage (must be run from the repository root)
# CGO_ENABLED=1 matches what `make cover` does. It is NOT the release setting --
# shipped artifacts are CGO_ENABLED=0 -- so a number produced without it is not
# the number the gate asserts on.
CGO_ENABLED=1 go test -count=1 -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -1

# 每个包的测试数 / tests per package
for p in $(go list ./...); do n=$(go test "$p" -list . 2>/dev/null | grep -c '^Test'); \
  [ "$n" -gt 0 ] && printf "%-42s %s\n" "$p" "$n"; done | sort -k2 -rn

# 闸门是否真的通过 / whether the gates actually pass
go test ./... -count=1
go test -race -count=1 -timeout 20m ./...
(cd .release/tools && go run ./covergate -file ../../coverage.out -min 80)
```

`-coverpkg=./...` 不是可选项。少了它，`go test ./...` 只统计每个包自己的
语句，跨包覆盖率会低到 60% 出头，看起来像回归，其实是根本没测到。
在 PowerShell 里传这个参数要用 `cmd /c` 包一层，否则 `=` 和 `/` 会被拆坏。

## 复现全部闸门 / Reproduce every gate

```bash
make fmt-check          # gofmt -l 必须为空
make vet                # go vet ./...
make build              # go build ./cmd/... ./pkg/... ./internal/... ./version/...
make test               # 全树单测
make test-race          # -race
make cover              # 覆盖率 profile + go tool cover -func
make coverage-gate      # 总量门槛 + 发布包每包门槛
make boot-smoke         # 6 道真实二进制闸门
```

`make boot-smoke` 是**唯一**会真正起服务、建任务、跑 WASM 插件、洪泛限流、
再发 SIGTERM 的检查。单元测试证明不了「这个产品能用」。

## 哪些地方天然慢 / What is legitimately slow

不要把「慢」当成挂起。下面这些是真慢，量级会随机器和代码变化，**所以这里不给
具体分钟数**——要数字就跑上面那条 `time go test -race ...`：

- **`pkg/plugin`** 每个夹具都真的把一个 Go 源编译成 wasm 模块。早期版本每次
  跑要编译一百多次 2.5 MB 的模块，`-race` 下会把 CI runner 的内存打爆
  （`ubuntu-latest` 只有 7 GB，实测峰值 7.2 GB）。现在的做法是：只加载不执行
  的测试用 8 字节的最小合法 wasm，真正要跑的才用真产物。这不是绕过检查，
  是把「这个测试需要真模块」和「这个测试只需要一个合法文件」区分开。
  **这条路径的教训**：`-race` 变慢不一定是代码问题，也可能是夹具在重复做
  昂贵工作。先量，再改。
- **`pkg/security`** 的密码学测试本身就贵（bcrypt 的成本参数是故意调高的）。
  这不是缺陷，不要为了快去调低它。
- **全树 `-race` 会打到 Go 默认的 10 分钟超时**，所以用
  `go test -race -timeout 20m ./...`。超时被撞到时先看是哪个包，不要直接调大
  全局超时了事。

## 覆盖率的口径 / What the coverage number means

覆盖率只是一个**回归信号**，不是质量证明：

- 总量门槛由 `.release/tools/covergate` 断言（CI 里 `-min 80`），并且对随包
  二进制（`cmd/loopctl`、`cmd/loopworker`）额外卡一个每包下限。加一个新二进制
  而不加下限，那道门就是空的。
- 报告出来的百分比会低于「真实质量」，因为前端资源、`examples/` 和没有测试的
  包会拉低分母。**看到 0% 的标识符，先确认它是不是不可达**，再决定要不要测。
- 反过来，**高覆盖率不等于契约正确**。本仓库有过 `pkg/client` 覆盖率 91.5%、
  全绿、却对真实服务端一个端点都不可用的情况：测试夹具是裸 JSON，而服务端一律
  返回信封。夹具互相印证，错得很有默契。**端到端断言（真二进制 + 真插件）
  不能被单元测试替代。**

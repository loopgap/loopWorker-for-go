# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Honesty rule for this file (and it is a rule, not a slogan):** a changelog
> line may only claim something that is verifiable in the shipped artifact.
> Overselling here is what creates support tickets for a product that has no
> support desk. `CI job preflight` in `.github/workflows/release.yml` refuses to
> publish a release whose version has no section in this file.

## [Unreleased]

Nothing here is released yet: **this repository has 0 git tags**, so no version
of LoopWorker has ever shipped. The first release is cut by a human with
`git tag -a v0.1.0-beta && git push origin v0.1.0-beta`, which runs
`.github/workflows/release.yml`. Later betas are `v0.1.0-beta.1`, `-beta.2`, …

### Added

* Release pipeline: `.goreleaser.yaml` (static `CGO_ENABLED=0` builds for
  linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 with a
  real `.exe` inside a `.zip`), deb/rpm packages, `checksums.txt` (sha256),
  SPDX SBOMs via syft, cosign keyless signatures, and a Homebrew tap formula.
* `.github/workflows/release.yml`: preflight (tag/CHANGELOG/placeholder checks),
  build+sign+publish, per-OS artifact verification (download → unpack → run →
  health → SIGTERM), multi-arch container publish to ghcr.io.
* Blocking CI quality gates in `.github/workflows/ci.yml`: `gofmt`, `go vet`,
  `golangci-lint v2.9.0` (config: `.golangci.yaml`), `govulncheck v1.8.0`,
  `go test -race ./...` across **all** packages, a 80% coverage gate
  (`-coverpkg=./...` + `.release/tools/covergate`), a Linux boot smoke test, a
  Windows boot smoke test, a Docker boot/health/SIGTERM smoke test, Trivy image
  scan, DCO sign-off enforcement, `actionlint`, and third-party license
  generation via `go-licenses`.
* `.release/tools/relcheck` — fails the pipeline when go.mod, the Dockerfile,
  CI, `.goreleaser.yaml` ldflags, `.dockerignore` embed inputs and `LICENSE`
  disagree with each other. Stdlib-only, so it runs on Windows too.
* Windows-first build path: `.release/build.ps1` (build/test/race/lint/coverage/
  smoke/release tasks) and `.release/smoke.ps1` (Ctrl+Break → SIGTERM path).
  The Makefile requires bash; PowerShell does not.
* New image: multi-stage `Dockerfile` with a Go builder that matches go.mod,
  `CGO_ENABLED=0`, non-root `USER 10001:10001`, `/data` as the only writable
  path, and a real `HEALTHCHECK` that GETs `/api/v1/health`.
* Licensing/governance: `NOTICE` (attribution obligations for statically linked
  Go binaries), `SECURITY.md` (embargo contact + supported-version table),
  `SUPPORT.md` (explicit self-service-only boundaries), `.github/CODEOWNERS`,
  `.github/dependabot.yml`, `CODE_OF_CONDUCT.md`, DCO in `CONTRIBUTING.md`.
* `.release/SCOPE-PROPOSAL.md`: what should be cut from the shipped product and
  why, for human approval. Nothing was deleted.

### Fixed

* **Windows artifacts were unusable.** `make build` on Windows emitted
  `bin/loopworker` with no `.exe`, which cmd/PowerShell cannot execute. The
  Makefile is now OS-aware (`EXE` derived from `go env GOOS`) and
  `.release/build.ps1` always writes `.exe`.
* **The Dockerfile could not build.** It pinned `golang:1.21-alpine` against a
  `go 1.26.1` module, set `CGO_ENABLED=1` without ever installing gcc, ran on
  the EOL `alpine:3.19`, had no `HEALTHCHECK`, and `.dockerignore` excluded the
  UI assets the server embeds. All five are addressed.
* **CI lied about coverage and races.** `test-coverage` wrote HTML and asserted
  nothing; the race job excluded `pkg/server`, the one package with a verified
  data race. Coverage now has a hard 80% threshold and `-race` runs over every
  package.
* **CI tested Go versions that were never shipped.** The matrix said
  `['1.21','1.22']` while go.mod said `go 1.26.1`. Version pins now come from
  go.mod (`go-version-file: go.mod`) and drift fails `relcheck`.
* `CONTRIBUTING.md` documented a tag-triggered release pipeline that did not
  exist. It now describes the pipeline that actually exists.
* `LICENSE` named `loopgap` as the licensor while all 55 commits are authored by
  `loopgad`. Now an explicit `TODO(owner)` placeholder that blocks publishing
  until a human confirms a real legal name.

### Corrected (claims that were not true of the shipped code)

* "Configuration file support (YAML)" — what the binary actually honors today is
  `--config` parsed as **flat JSON keys** (`internal/config.LoadConfig`) plus the
  `LOOPWORKER_*` environment variables. A nested YAML loader
  (`internal/config.Load`, matching `config/config.example.yaml`) exists in the
  tree but **is not wired into `cmd/loopworker/main.go` yet**, so YAML support is
  not a shipped feature and is not advertised.
* "Observability: … distributed tracing" — there is no tracer (no OpenTelemetry,
  no span propagation). What ships is structured logging (`pkg/logger`, zap),
  Prometheus metrics (`/metrics`), an event bus (`pkg/event`) and
  `/api/v1/health`.
* "Security: RBAC …" — `pkg/security` implements roles, a token store and rate
  limiting, but **no HTTP endpoint issues or validates tokens**, and the API
  routes are registered without auth middleware. Treat the API as
  unauthenticated (SECURITY.md states this plainly).
* "Liquid Glass UI" — `web/canvas` is a Vite/React mock whose served `/` route
  is a verified infinite 301 redirect loop; it is excluded from release bundles
  and flagged for removal (`.release/SCOPE-PROPOSAL.md`).
* "21 packages" — `go list ./...` reports **31** packages.
* "6 CLI tools" as a product feature — only `loopworker` ships in release
  artifacts. `loopbench`/`loopsim` never contact the server (they `time.Sleep`
  locally) and `loopctl`/`loopdebug` call endpoints that return 404; all four are
  cut candidates.

## [0.1.0-beta] — NOT YET RELEASED

Pre-1.0 beta. The version scheme is `0.1.0-beta` for the first public beta and
`0.1.0-beta.N` for each subsequent one — the `.N` counter only increases and the
`0.1.0` base never moves while the beta line is open. Nothing here carries a
compatibility promise: the HTTP API, config schema, and plugin ABI are all still
moving. See `.release/RELEASE-PROCESS.md`.

Planned contents (the section exists so `preflight` can find it once the tag is
cut; no artifacts for 0.1.0-beta exist as of 2026-10-05):

* Event-driven task engine (`pkg/event`) with backpressure and metrics.
* Priority scheduling (`internal/core/scheduler`) and task dependencies
  (`internal/core/dispatcher`).
* WASM plugin sandbox (`internal/core/sandbox`, wazero) with memory/CPU/output
  limits — resource caps, not a security boundary (SECURITY.md).
* Self-healing (`internal/core/selfheal`) with circuit breaker + backoff.
* Workflow patterns: sequential, DAG, parallel (`pkg/workflow`).
* HTTP API (`pkg/api`, chi) at `/api/v1/*` plus `/metrics`; `pkg/client` SDK.
* `loopworker` server binary; static artifacts for linux/darwin/windows and
  deb/rpm; container image; SBOM + signatures; Homebrew formula.
* Structured logging (`pkg/logger`) and sentinel errors (`pkg/errors`).
* Unit tests in 31 packages plus `integration/integration_test.go`.

### Known issues at 0.1.0-beta (do not pretend otherwise)

* `pkg/server` has a verified data race under `-race`; the CI gate is red until
  it is fixed — that is intentional.
* Authentication is implemented in `pkg/security` and enforced by `pkg/api`
  middleware, but `pkg/server` does not wire it up yet — a bare server accepts
  unauthenticated writes. Do not expose it to an untrusted network.
* There is no TLS termination; put the server behind a reverse proxy that does it
  (see SECURITY.md "What LoopWorker does NOT do").
* SQLite data growth has no retention/compaction policy.

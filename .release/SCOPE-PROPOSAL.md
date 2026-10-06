# SCOPE-PROPOSAL — what to stop shipping (one page, for human approval)

Status: **proposal**. Nothing in this repository was deleted. Every item below is
already excluded from release artifacts (`.goreleaser.yaml` ships only the
`loopworker` binary; `web/canvas` is excluded from the Docker build context and
from the archives), so approving this changes packaging and docs, not history.

Business frame: this product must survive with **zero after-sales support**. Each
item below is judged by one question — "does this generate tickets we cannot
answer?" — not by how much code it is.

## Cut from the shipped product

### 1. `web/canvas` (Vite/React "liquid glass" UI) — cut

* What it is: `web/canvas/**` (React SPA, `package.json`, node_modules-sized
  toolchain) plus its pre-built copy embedded by the server
  (`pkg/api/api.go`: `//go:embed all:dist` → `pkg/api/dist`).
* Verified defect: the catch-all handler rewrites extension-less paths to
  `/dist/index.html` and hands them to `http.FileServer`, which 301-redirects
  `.`-prefixed paths back on themselves — `/` is an infinite redirect loop. A
  customer's first click on the product landing page spins.
* Support cost: it is the only GUI promise in `README.md` ("Liquid Glass UI"),
  so every cosmetic bug becomes a ticket, and it is unsuportable: no tests, no
  build in CI, no owner.
* Proposal: (a) stop serving `/` from the server, keep only `/api/v1/*` +
  `/metrics`; (b) delete `web/canvas` + `pkg/api/dist` and drop the `go:embed`
  once the code owner agrees; (c) remove the UI claim from README/SECURITY/SUPPORT.
* Blocked on: an **integration request** — `pkg/api/api.go` must drop the embed
  and the catch-all route. Until then the archives simply do not include the UI
  sources, and `SUPPORT.md` states the UI is out of scope.

### 2. `loopbench`, `loopsim` — cut (dev toys, not tools)

* Verified: neither imports `pkg/client` nor makes any HTTP call; they only
  `time.Sleep` locally (`cmd/loopbench/main.go:114`, `cmd/loopsim/main.go:144,244`)
  and print a table.
* They are indistinguishable from a fake benchmark to a buyer, and "your
  benchmark says 10k/s while production does 200/s" is a ticket.
* Proposal: keep in-repo for developer use, never packaged (already the case);
  remove from README's feature list.

### 3. `loopctl`, `loopdebug` — cut or fix, do not ship broken

* Verified 404s (routes that do not exist in `pkg/api/api.go`'s
  `registerRoutes`): `POST /api/v1/tasks/{id}/cancel`, `POST /api/v1/workflow`,
  `GET|POST /api/v1/config` (`cmd/loopctl/main.go:132,202,254,289`), and
  `GET /api/v1/workflow/{id}` (`cmd/loopdebug/main.go:147,175`).
  The server exposes `/api/v1/workflow/list|graph|execute`, `/api/v1/tasks*`,
  `/metrics`, `/logs`, `/health` — nothing else.
* So the "CLI management tool" advertised in docs fails against the real API.
* Proposal: prefer cutting both from the shipped surface and documenting
  `curl`/`pkg/client` as the interface. If a CLI is commercially necessary, the
  owner must first (a) add the missing endpoints or (b) repoint the CLI at the
  existing ones, and the boot-smoke gate must assert those calls return 2xx.
* Integration request: see below.

### 4. `docs/product/*.md` and `.workbuddy/` — cut from anything customer-facing

* Internal AI-persona planning notes (e.g. `gap-audit-and-prd.md`,
  `delivery-progress-report.md`) that disclose the developer's local filesystem
  path and admit "`P0`: go build 失败".
* Harm today: they are inside the repo a buyer clones, so (a) a personal absolute
  path leaks, (b) a document that says the build fails destroys trust in the
  artifact, and (c) they read like a roadmap of unbuilt features → tickets.
* Proposal: move to a private planning space (or delete). At minimum they stay
  out of the release archives (they already are) and out of README links.
  NOTE: `.gitignore` does not list `docs/product/` or `.workbuddy/`, so removal
  is a git-history question for the owner, not something to hide with ignore rules.

### 5. Nested `config` semantics — ship one honest story

* Verified: `config/config.example.yaml` uses nested keys (`server.port`,
  `plugins.dir`), while `internal/config.LoadConfig` (what `cmd/loopworker/main.go`
  calls today) parses **flat JSON keys** and only when `--config` is given.
  `internal/config.Load` in `load.go` implements the nested YAML schema but is not
  yet wired into the binary.
* Result: a customer copies the example file, runs `loopworker --config`, and gets
  a parse error or silently ignored settings. That is the single most predictable
  support ticket in the product.
* Proposal: until `main.go` calls `config.Load`, the docs and CHANGELOG describe
  only `LOOPWORKER_*` env vars + flags as supported (this is what CHANGELOG.md now
  says), and `config.example.yaml` is labelled as the target schema, not the
  current one.

## Keep (this is the product)

* `cmd/loopworker` — the server binary; the only thing in release artifacts.
* `pkg/api`, `pkg/server`, `pkg/client`, `pkg/event`, `pkg/workflow`,
  `pkg/errors`, `pkg/logger`, `pkg/security`, `pkg/skill`, `pkg/plugin`,
  `pkg/utils`, `internal/config`, `internal/core/*`, `version`.
* `docs/QUICKSTART.md`, `docs/api/api-reference.md`, `docs/guides/`,
  `examples/`, `config/` (once the story above is coherent) — these ship inside
  the archives.
* `cmd/loopwatch` — not verified as broken here; if the owner confirms it hits
  live endpoints it can stay as an operator tool, otherwise it joins item 3.

## Integration requests (code changes I am not allowed to make)

1. `pkg/api/api.go`: remove `//go:embed all:dist` + `webCanvas` + the catch-all
   `/*` FileServer handler (the 301 loop), or make `/` return 200 with a
   plain-text pointer to `docs/api/api-reference.md`.
2. `cmd/loopworker/main.go`: switch `config.LoadConfig(cfgFile)` →
   `config.Load(config.Options{ConfigFile: cfgFile, Flags: …})` so the documented
   YAML schema is real; add a `loopworker config check` subcommand that prints the
   resolved config (self-service diagnosis instead of a ticket).
3. `cmd/loopctl`, `cmd/loopdebug`: repoint at existing routes, or accept removal
   from the shipped surface.
4. Either register `Server.HandleShutdown` (currently dead code, not on any
   route) as `POST /api/v1/shutdown` behind auth, or delete it — the Windows smoke
   test falls back to Ctrl+Break today because there is no graceful-shutdown
   endpoint.
5. `pkg/security` + `pkg/api`: issue/validate tokens so `/api/v1/*` can require
   auth; the smoke scripts already send `Authorization: Bearer $LOOPWORKER_SMOKE_TOKEN`
   when set, so the gates need no further change.
6. Add `docs/product/` and `.workbuddy/` removal (or relocation) as an owner
   decision; also decide whether `web/canvas` is deleted outright.

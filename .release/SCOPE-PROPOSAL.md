# SCOPE — what ships, what was cut, and what is still open

Status: **current as of 2026-10-06.** Every "Still open" item below is a real,
unimplemented gap. Everything under "Settled" was verified against the code in
this tree, not against an earlier plan.

Business frame: this product must survive with **zero after-sales support**. Each
item below is judged by one question — "does this generate tickets we cannot
answer?" — not by how much code it is.

## Settled

### The four dev-scratch CLIs are gone — verified
`loopbench` and `loopsim` never imported `pkg/client` and made no HTTP call; they
slept locally and printed a table, which is indistinguishable from a fake
benchmark to a buyer. `loopdebug` and `loopwatch` did call the API — `loopdebug`
used `GetTask` and `GET /api/v1/workflow/{id}`, `loopwatch` used `HealthCheck`
plus `GetMetrics`/`GetLogs`, the latter two being calls with no route behind them
(`pkg/client/client_test.go:TestGetMetricsHasNoRoute` pins that for `/metrics`).
They were cut as duplicates of work `loopctl` already does, not because they
were unreachable. All four directories are deleted. `cmd/` now holds
`loopworker` (the product) and `loopctl` (the one dev CLI whose commands are all
live routes, and which has `cmd/loopctl/main_test.go`). Recover the old sources
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

### `LoadOptions.Allowlist` is not a thing — verified
An earlier revision implied digest pinning by allowlist. `LoadOptions.Allowlist`
was removed; `RequireChecksum` and the manifest digest are the whole story.

### `loopctl` could not read a single workflow — closed
`GET /api/v1/workflow/{workflowID}` is registered on the `authed` group
(`pkg/api/api.go:204`), and `loopctl` had `workflow list` and `workflow execute`
but no `get`, so a single workflow was readable only with curl. `pkg/client` now
has `GetWorkflow` and `loopctl workflow get [workflow-id]` exists, with tests for
the success path, the 404 path, and a guard that `loopctl workflow --help` cannot
promise a command the binary does not have — the help string used to advertise
both `create` (deliberately absent) and `get` (missing).

## Still open

### No real-browser end-to-end check of the canvas — still open
An earlier revision of this file said nothing automated covered the GUI. That is
no longer true and the revision was wrong: the canvas shipped eight defects —
node cards reading Go field names against a lower-case JSON API, a plain-text
payload run through `atob()` which threw `InvalidCharacterError` and took the
details panel down, a `fetchGraph` that refetched forever because its
`useCallback` depended on unstable setters, a probe guard that could never
release, no request timeout, React Flow v11 against React 19, SSE tasks created
outside the canvas never appearing, and Escape not closing the panel — all of it
under a green CI, because the workflows contained no frontend step at all.

There are now jsdom tests in `web/canvas/src/api.test.js` and
`web/canvas/src/App.test.jsx`, plus a `canvas` job in `ci.yml` that lints, tests,
builds and diffs `web/canvas/dist` against the committed `pkg/api/dist`.

What those tests still cannot do, and why this item stays open: jsdom renders the
component tree, not a browser. Node geometry and pan and zoom are out of reach
until a human opens a tab, and `PRE-RELEASE-CHECKLIST.md` keeps those steps by
hand. If this product ever takes money, that is the first gap to close.

The live-update path is no longer part of that gap, on either side:

- **Server.** `pkg/api/handlers_events_test.go` opens `GET /api/v1/events/live`
  on a real listener and asserts the frame format end to end — `event: <type>` /
  `data: <json>` — plus the headers an intermediary needs, the `?types=` filter,
  the cross-tenant substitution, and that a disconnect returns the stream's slot
  in the limiter. That file did not exist before; the endpoint had only a 503
  entry for a missing event bus and a test proving the response writer exposes a
  Flusher. Both critical assertions were mutation-checked.
- **Client.** The canvas does not use `EventSource` (it cannot send a header); it
  parses frames itself with a fetch streaming reader. The one stream test fed it
  a single chunk, leaving the buffer accumulation, the `\r` strip and the
  multi-line `data` join untested. All three are now covered and each was
  mutation checked.

### `docs/USAGE.md` now ships, and its prose still has no guard
The README linked to it and the link was dead in the release tarball, so it was
added to `archives[].files` — a support-free product should hand the customer its
user guide. That makes its accuracy a release risk it was not before: it predates
the canvas, the config loader and the auth work.

Its API routes are now covered: `relcheck`'s `checkDocumentedRoutes` compares
every `/api/v1` path in a shipped document against `pkg/api/openapi.json`, which
`TestOpenAPISpecMatchesRegisteredRoutes` keeps equal to the router. That check
caught the one drift already present — `docs/api/api-reference.md` documenting
`{taskId}` and `{id}` where the server registers `{taskID}`, so two documents that
both ship in the same archive disagreed with each other.

What is still unverified is its **prose**: claims about the Go SDK, plugin
development and configuration. A static check can prove a documented route exists;
it cannot prove a paragraph is true. The preflight checklist requires re-reading
it against the build.

That re-read has happened once. It found the configuration example using
`port` / `plugins_dir` / `data_dir` / `log_level` where the schema is nested
(`server.port`, `plugins.dir`, `data.dir`, `logging.level`). It also found the
guide asserting that the loader does not reject unknown keys, which was the
opposite of the code and is now corrected. All 25 package-qualified Go
identifiers the guide names, and all 11 sentinel errors it lists, do exist, so
the rest of its API surface held up.

### Unknown config keys are rejected, not ignored — settled
This was recorded here as an open product decision and is now closed. The trade
was whether a config carrying a stale or extra key should be a silent no-op or a
startup failure. It is a startup failure: `internal/config/load.go` refuses an
unrecognised key, names the closest accepted spelling when there is one, and
prints every key it does accept.

For a product with no support desk that is the only defensible answer — silence
is the failure mode that becomes a ticket ("my port setting does nothing"), and
there is no version to be lenient about, because the repository has zero tags and
no config has ever been deployed. The cost is that a config written against an
older schema now stops the server instead of half-working; that is a message
telling the operator exactly which key to fix, not an outage.

`docs/USAGE.md` and `.release/PRE-RELEASE-CHECKLIST.md` have been corrected to
describe the behaviour that ships. `TestUnknownKeyNamesKeyFileAndAccepted` covers
the message.

### The container image is verified; running it needs one command
The image builds, and a full run of it has now been done: with a credential
supplied it stays up, answers `GET /api/v1/health` with 200 and the documented
JSON body, goes Docker-`healthy`, and exits 0 on SIGTERM. The image is 15 MB,
runs as uid 10001, and carries the canvas inside the binary (verified by finding
the `assets/index-` references and the `<title>LoopWorker` string in
`/usr/local/bin/loopworker`, so the whole embed chain — source, committed dist,
`//go:embed`, compiler, image — is confirmed rather than assumed).

Without a credential it exits 1 on purpose: the image binds `0.0.0.0`, and a
public interface with no configured credential is refused. That is the right
behaviour and the message says exactly what to do, but it means the first thing a
new user types is `docker run ghcr.io/.../loopworker` and gets a container that
dies. The fix is documentation, not code: the quick start should show the
credential alongside the run command.

`bash .release/scripts/docker-smoke.sh <image>` is the one-command form, and
`release.yml` calls exactly that. It could not be executed here — this machine
has no bash (WSL has no distribution installed) — so each gate's underlying
docker commands were reproduced by hand and each passed.
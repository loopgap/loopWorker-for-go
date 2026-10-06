# LoopWorker Quick Start

Everything below is a command you can paste. Where a step has a prerequisite that
is easy to miss, it is called out — a server with no plugin and no credentials
starts fine and then fails every task, which is the single most common way to get
stuck here.

## What you need

- Go 1.26.1 or newer (see `go.mod`), or a released archive from the GitHub releases page.
- A plugin folder. LoopWorker runs WebAssembly plugins; it has no built-in task
  implementations. Section 2 builds one from this repository.

## 1. Build

```bash
git clone https://github.com/loopgap/loopWorker-for-go.git
cd loopWorker-for-go

# The server, plus the developer CLIs (loopctl, loopdebug, loopwatch, loopbench, loopsim)
make build
```

Releases ship **only** the `loopworker` binary. The other five are developer tools
you build yourself — see [CLI Tools](#cli-tools).

## 2. Build a plugin

The server needs at least one plugin directory containing `plugin.json` and a
`.wasm` module, otherwise every task you submit ends in `dead_letter` with
`plugin not found`.

```bash
# examples/hello-plugin is a WASI module that echoes stdin to stdout
GOOS=wasip1 GOARCH=wasm go build -o hello.wasm ./examples/hello-plugin/

mkdir -p ~/.loopworker/plugins/hello
cp hello.wasm ~/.loopworker/plugins/hello/
cp examples/hello-plugin/plugin.json ~/.loopworker/plugins/hello/
```

The directory name does not have to match the plugin name in `plugin.json`; the
name inside the manifest is what tasks address. The boot log prints the mapping:

```
workers started {"count": 1, "plugin": "hello"}
```

`loopworker doctor` tells you what the loader found and, when it rejects a
directory, why.

## 3. Configure credentials

Out of the box the server prints a one-time bootstrap key to its log. Two
consequences follow from that, and both surprise people:

1. **Binding to all interfaces is refused.** With only an ephemeral key the
   server will not listen on anything but loopback:

   ```
   loopworker: bind_address: the API would listen on 0.0.0.0:19700 using only an ephemeral bootstrap key
   ```

2. That bootstrap key is not a durable credential. Set your own before anyone
   else can reach the port:

```bash
# format: <key-id>:<role>:<sha256-of-the-key>
export LOOPWORKER_API_KEYS="ops:admin:$(printf '%s' 'choose-a-real-secret' | sha256sum | cut -d' ' -f1)"
export LOOPWORKER_API_KEY='choose-a-real-secret'   # what the CLIs send
```

`LOOPWORKER_API_KEYS` registers credentials; `LOOPWORKER_API_KEY` is what
`loopctl` and `loopdebug` authenticate with. A malformed value is a startup
error, not a warning. Roles are `admin`, `operator`, `viewer`.

Full reference: [README — Authentication](../README.md#authentication).

## 4. Start the server

```bash
# Defaults: API on 127.0.0.1:19527, metrics on 127.0.0.1:19528
loopworker

# Explicit configuration
loopworker --config ./config/config.example.yaml

# Check the resolved configuration and every directory before you trust it
loopworker doctor
```

`doctor` reports each of directories, database, port, plugins, authentication and
sandbox policy, and for anything it flags it names the cause, the fix and where to
read more.

## 5. Submit a task and wait for it

```bash
# loopctl reads LOOPWORKER_API_KEY; everything except `status` needs it
loopctl task create --type echo --input "Hello, World!"
loopctl task list
loopctl task get <task-id>
```

Or with `curl`. Note the `X-API-Key` header — without it you get `401`, and the
only endpoints that work anonymously are `GET /healthz`, `GET /api/v1/health` and
`GET /api/v1/openapi.json`:

```bash
curl -sS http://127.0.0.1:19527/api/v1/health

curl -sS -X POST http://127.0.0.1:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "X-API-Key: choose-a-real-secret" \
  -d '{"type": "echo", "input": "Hello, World!"}'

curl -sS -H "X-API-Key: choose-a-real-secret" \
  "http://127.0.0.1:19527/api/v1/tasks?limit=10"
```

Responses are always envelopes: `{"success":true,"data":{...},"request_id":"..."}`.
Metrics are **not** on this port — they are on the loopback admin listener and
need admin credentials:

```bash
curl -sS -H "X-API-Key: choose-a-real-secret" http://127.0.0.1:19528/metrics
```

## Configuration

Precedence is **flag > environment > file > default**. `doctor` prints the
resolved value and where each one came from.

| File key | Environment variable | Default |
|---|---|---|
| `server.host` | `LOOPWORKER_SERVER_HOST` | `127.0.0.1` |
| `server.port` | `LOOPWORKER_SERVER_PORT`, `LOOPWORKER_PORT` | `19527` |
| `server.admin_port` | `LOOPWORKER_API_ADMIN_PORT` | `19528` |
| `plugins.dir` | `LOOPWORKER_PLUGINS_DIR` | `~/.loopworker/plugins` |
| `data.dir` | `LOOPWORKER_DATA_DIR` | `~/.loopworker/data` |
| `workers.count` | `LOOPWORKER_WORKERS_COUNT` | `4` |
| `log.level` | `LOOPWORKER_LOG_LEVEL` | `info` |

`server.admin_host` is deliberately not configurable. The admin listener exposes
`/metrics` and `/logs`; moving it to a routable address hands those to anyone who
can route to the port.

A minimal file:

```yaml
server:
  host: "127.0.0.1"      # "0.0.0.0" is refused unless you configured credentials
  port: 19527
  admin_port: 19528

plugins:
  dir: "~/.loopworker/plugins"

data:
  dir: "~/.loopworker/data"
```

An unknown key is rejected at startup and the error lists every accepted key, so a
typo never silently disables a setting.

## CLI Tools

None of these are in the release archive or the container image.

| Tool | What it actually does | Needs credentials |
|---|---|---|
| `loopworker` | The server, plus `doctor` and `version` | writes do |
| `loopctl` | `task list/create/get/cancel/delete`, `workflow list/execute`, `status` | all but `status` |
| `loopdebug` | Task and workflow inspection, `diagnose`, `profile` | all but `diagnose` |
| `loopwatch` | Polls `GET /api/v1/health` only | no |
| `loopbench` | Local `time.Sleep` loop — never contacts a server | n/a |
| `loopsim` | Local `rand` + `time.Sleep` loop — never contacts a server | n/a |

`loopwatch --metrics` and `--logs` do not work: `/api/v1/metrics` and
`/api/v1/logs` are not routes. Use the admin listener (section 5). `loopbench`
and `loopsim` report timings of their own sleep loops — do not cite them as
product performance.

## Docker

```bash
# From the repository
make docker
docker run --rm -p 19527:19527 loopworker

# From a release
docker run --rm -p 19527:19527 \
  -v loopworker-data:/data \
  -v "$PWD/hello:/plugins/hello:ro" \
  -e LOOPWORKER_PLUGINS_DIR=/plugins \
  -e LOOPWORKER_API_KEYS="ops:admin:$(printf '%s' 'choose-a-real-secret' | sha256sum | cut -d' ' -f1)" \
  ghcr.io/loopgap/loopworker-for-go/loopworker:0.1.0-beta
```

The image declares `USER 10001:10001`, exposes `19527`, and mounts `/data` as a
volume — so pass a writable volume or the task database cannot be created. There
is no default plugin in the image, which is why the example mounts one.

## Development

```bash
make test          # unit and integration tests
make test-race     # the same under the race detector
make fmt           # gofmt -w
make lint          # golangci-lint
make coverage-gate # refuses to go green below the threshold
make boot-smoke    # end-to-end: real binary, real plugin, task to completion
```

`make boot-smoke` is the check worth running before you believe anything works:
it builds a plugin, starts the server on a free port, asserts anonymous writes
are refused, submits a task and waits for `completed`, then floods the public
surface to confirm the rate limiter answers.

## Support

- [README](../README.md) — features, API reference, authentication
- [Architecture](architecture.md)
- [Issues](https://github.com/loopgap/loopWorker-for-go/issues)
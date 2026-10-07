# LoopWorker Quick Start

Everything below is a command you can paste. Where a step has a prerequisite that
is easy to miss, it is called out — a server with no plugin and no credentials
starts fine and then fails every task, which is the single most common way to get
stuck here.

## What you need

- Go 1.26.6 or newer (the `go` line in `go.mod`; older toolchains refuse to build),
  or a released archive from the GitHub releases page.
- A plugin folder. LoopWorker runs WebAssembly plugins; it has no built-in task
  implementations. Section 2 installs the demo plugin that ships with a release,
  or builds one from this repository.

## 1. Build

```bash
git clone https://github.com/loopgap/loopWorker-for-go.git
cd loopWorker-for-go

# The server, plus the one developer CLI (loopctl)
make build
```

Already unpacked a release? Skip this step and run `loopworker` (on Windows,
`loopworker.exe`) from the archive — everything after section 1 works the same.

Releases ship the `loopworker` binary and nothing else executable: `loopctl` is a
developer tool you build yourself — see [CLI Tools](#cli-tools). Alongside the
docs, the archive also carries `config.example.yaml` and the demo plugin that
section 2 installs.

## 2. Install a plugin

The server needs at least one plugin directory containing `plugin.json` and a
`.wasm` module, otherwise every task you submit ends in `dead_letter` with
`plugin not found`.

A working one ships with the release: `examples/hello-plugin` is a WASI module
that echoes stdin to stdout. From the unpacked release directory:

```bash
mkdir -p ~/.loopworker/plugins/hello
cp examples/hello-plugin/hello.wasm  ~/.loopworker/plugins/hello/
cp examples/hello-plugin/plugin.json ~/.loopworker/plugins/hello/
```

Working from a source checkout instead? Rebuild the module from its source:

```bash
GOOS=wasip1 GOARCH=wasm go build -o examples/hello-plugin/hello.wasm ./examples/hello-plugin/
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
   loopworker: bind_address: the API would listen on 0.0.0.0:19527 using only an ephemeral bootstrap key
   ```

   That is the process's last words on stderr, and the port in it is the one you
   actually bound — substitute whatever `server.port` you set.

2. That bootstrap key is not a durable credential. Set your own before anyone
   else can reach the port:

```bash
# format: <key-id>:<role>:<sha256-of-the-key>
export LOOPWORKER_API_KEYS="ops:admin:$(printf '%s' 'choose-a-real-secret' | sha256sum | cut -d' ' -f1)"
export LOOPWORKER_API_KEY='choose-a-real-secret'   # what the CLIs send
```

`LOOPWORKER_API_KEYS` registers credentials; `LOOPWORKER_API_KEY` is what
`loopctl` authenticates with. A malformed value is a startup
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
| `logging.level` | `LOOPWORKER_LOG_LEVEL` | `info` |

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
| `loopworker` | The server, plus `doctor`, `storage backup`, `version` | writes do |
| `loopctl` | `task list/create/get/cancel/delete`, `workflow list/get/execute`, `status`, `version` | all but `status` |

`make build` produces exactly these two binaries (`Makefile` 的 `BINARIES :=
loopworker loopctl`). `loopdebug`、`loopwatch`、`loopbench`、`loopsim` 已删除，
`cmd/` 下不再有它们 —— 需要时从历史里取回：`git show <rev>:cmd/<name>/main.go`。

## Docker

```bash
# From the repository
make docker
# LOOPWORKER_API_KEYS is not optional here: the image listens on 0.0.0.0, and a
# container with no configured credential exits 1 immediately rather than serving
# an open API.
docker run --rm -p 19527:19527 \
  -e LOOPWORKER_API_KEYS="ops:admin:$(printf '%s' 'choose-a-real-secret' | sha256sum | cut -d' ' -f1)" \
  loopworker

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

## Linux packages (deb / rpm)

Each release also attaches a `.deb` and a `.rpm` for `amd64` and `arm64`. They
are **files on the release page, not entries in a Debian or RPM repository** —
there is no apt source to add, so `apt install loopworker` finds nothing.
Install the file you downloaded:

```bash
# Debian / Ubuntu
sudo apt install ./loopworker_<version>_amd64.deb

# RHEL / Fedora / openSUSE
sudo dnf install ./loopworker-<version>-1.x86_64.rpm
```

| Path | What |
|---|---|
| `/usr/bin/loopworker` | the server, plus `doctor`, `storage backup` and `version` |
| `/etc/loopworker/config.example.yaml` | the annotated example configuration |
| `/var/lib/loopworker/` | an empty data directory, mode `0750`, owned by root |
| `/usr/share/doc/loopworker/` | `copyright` (the licence), `NOTICE`, `README`, `CHANGELOG`, `QUICKSTART`, `USAGE` |

Two things the package leaves to you, and both are visible in `doctor`:

- **No plugin.** The demo plugin is not in the package, so a package install
  still needs section 2. Until then `doctor` reports `plugins … is empty`.
- **The packaged data directory is not the one the server uses.** It defaults to
  `data.dir = ~/.loopworker/data`, and `/var/lib/loopworker` is root-owned, so
  an unprivileged service user cannot write to it. A system-wide install means
  creating a service user, handing it that directory, and pointing `data.dir`
  and `plugins.dir` at the matching paths.

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
- [Usage guide](USAGE.md) — SDK examples, plugin authoring, workflows, configuration
- [API reference](api/api-reference.md)
- [Issues](https://github.com/loopgap/loopWorker-for-go/issues)
#!/usr/bin/env bash
#
# boot-smoke.sh — binary-level boot gate (no Docker required).
#
# Starts the loopworker binary on a free port with an isolated data dir and
# requires, in order:
#
#   1. the process is still alive shortly after exec   (not crash-on-start)
#   2. GET /api/v1/health == HTTP 200 with a "status"   (self-check passed)
#   3. an unauthenticated write is refused with 401     (AC-4: no anonymous writes)
#   4. with LOOPWORKER_SMOKE_API_KEY, that key can create a task AND the task
#      reaches a terminal state                        (AC-2: a task really runs)
#   5. 130 anonymous requests are throttled with 429 and the process survives,
#      then still serves an authenticated call         (AC-4: DoS regression)
#   6. SIGTERM => exit code 0 within GRACEFUL_TIMEOUT  (graceful shutdown)
#
# Gate 4 is the one that matters most and was missing: a server can answer
# /healthz with 200 while every task it accepts dies in dead_letter, which is
# exactly what shipped. It needs a runnable plugin; the harness stages
# examples/hello-plugin (a real WASI module, built on demand) into the isolated
# plugin dir. If that artifact is missing the gate fails loudly rather than
# skipping, because "no plugin" would make the task land in dead_letter and the
# check would pass for the wrong reason.
#
# Gate 5 is the recorded DoS regression: the anonymous budget used to be keyed
# on a value the auth middleware had not populated yet, so 130 requests from one
# address all passed.
#
# Usage: bash .release/scripts/boot-smoke.sh [path-to-binary]
# Env:   BOOT_TIMEOUT (default 45) GRACEFUL_TIMEOUT (default 20)
#        TASK_TIMEOUT (default 60) LOOPWORKER_SMOKE_API_KEY (auth for gate 4)
#        HELLO_WASM (prebuilt examples/hello-plugin/hello.wasm to reuse)
# Exit:  0 pass | 1 gate failed | 2 harness problem

set -uo pipefail

BIN="${1:-bin/loopworker}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-45}"
GRACEFUL_TIMEOUT="${GRACEFUL_TIMEOUT:-20}"
TASK_TIMEOUT="${TASK_TIMEOUT:-60}"
[ -x "$BIN" ] || { echo "FAIL: $BIN is not an executable file"; exit 2; }
BIN="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")"

DATA="$(mktemp -d /tmp/lw-smoke-data.XXXXXX)"
PLUGINS="$(mktemp -d /tmp/lw-smoke-plugins.XXXXXX)"
LOG="$(mktemp /tmp/lw-smoke.log.XXXXXX)"
cleanup() {
  # The server holds the database open; kill it before removing the dir or the
  # removal fails with EBUSY and leaves the harness looking like the culprit.
  if [ -n "${PID:-}" ] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null || true
    for i in $(seq 1 10); do kill -0 "$PID" 2>/dev/null || break; sleep 1; done
    kill -9 "$PID" 2>/dev/null || true
  fi
  rm -rf "$DATA" "$PLUGINS" 2>/dev/null || true
  [ -n "${KEEP_LOG:-}" ] || rm -f "$LOG" 2>/dev/null || true
}
trap cleanup EXIT

# free port (python3 is present on GitHub runners; fall back to the default)
PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()' 2>/dev/null || true)"
[ -z "$PORT" ] && PORT=19527

export LOOPWORKER_PORT="$PORT" LOOPWORKER_DATA_DIR="$DATA" LOOPWORKER_PLUGINS_DIR="$PLUGINS" LOOPWORKER_LOG_LEVEL=debug
# The admin listener uses a fixed default port and the server refuses to start
# without it, so two smoke runs (or a developer's own server) would collide.
# Take a free one.
ADMIN_PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()' 2>/dev/null || true)"
[ -n "$ADMIN_PORT" ] && export LOOPWORKER_API_ADMIN_PORT="$ADMIN_PORT"

# A stable credential so gates 3 and 4 can be told apart: without it, gate 3's
# 401 is indistinguishable from "the API is broken and rejects everything".
SMOKE_KEY="${LOOPWORKER_SMOKE_API_KEY:-lwk_smoke_0123456789abcdef}"
if [ -z "${LOOPWORKER_SMOKE_API_KEY:-}" ]; then
  echo "note: LOOPWORKER_SMOKE_API_KEY unset, using an ephemeral smoke-only key"
fi
# LOOPWORKER_API_KEYS takes id:role:<64-hex sha256 of the key>.
KEY_HASH="$(printf '%s' "$SMOKE_KEY" | sha256sum | cut -d' ' -f1)"
export LOOPWORKER_API_KEYS="smoke:admin:$KEY_HASH"

echo "=== booting $BIN on 127.0.0.1:$PORT (data=$DATA) ==="
"$BIN" >"$LOG" 2>&1 &
PID=$!

fail() {
  echo "FAIL: $1"
  echo "--- binary output ---"; tail -60 "$LOG" 2>/dev/null || true
  kill -9 "$PID" 2>/dev/null || true
  wait "$PID" 2>/dev/null || true
  exit 1
}

# gate 1: process alive shortly after exec
sleep 2
kill -0 "$PID" 2>/dev/null || fail "process exited immediately (crash-on-start class of bug); see output above"

# gate 2: health endpoint
code=""
body="$(mktemp)"
for i in $(seq 1 "$BOOT_TIMEOUT"); do
  kill -0 "$PID" 2>/dev/null || fail "process died at iteration $i before health passed"
  hdr_args=()
  [ -n "${LOOPWORKER_SMOKE_TOKEN:-}" ] && hdr_args=(-H "Authorization: Bearer ${LOOPWORKER_SMOKE_TOKEN}")
  code="$(curl -sS -o "$body" -w '%{http_code}' --max-time 4 ${hdr_args[@]+"${hdr_args[@]}"} "http://127.0.0.1:${PORT}/api/v1/health" 2>/dev/null || echo 000)"
  [ "$code" = "200" ] && break
  sleep 1
done
[ "$code" = "200" ] || fail "GET /api/v1/health returned HTTP $code after ${BOOT_TIMEOUT}s (expected 200)"
echo "PASS  health 200: $(head -c 300 "$body")"
grep -q '"status"' "$body" || fail "health response has no \"status\" field — API contract drift"

# gate 3: no anonymous writes. /api/v1/health is deliberately anonymous, so a
# 200 there says nothing about whether the mutating surface is guarded.
anon="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 4 -X POST \
  -H 'Content-Type: application/json' -d '{"type":"echo","input_text":"anon"}' \
  "http://127.0.0.1:${PORT}/api/v1/tasks" 2>/dev/null || echo 000)"
[ "$anon" = "401" ] || fail "POST /api/v1/tasks without a credential returned HTTP $anon, want 401 (AC-4: no anonymous writes)"
echo "PASS  anonymous write refused with 401"

# gate 4: a task actually runs. Requires a runnable plugin.
API_KEY=(-H "X-API-Key: $SMOKE_KEY")
echo "=== staging examples/hello-plugin into $PLUGINS ==="
REPO_ROOT="$(cd "$(dirname "$BIN")/../.." 2>/dev/null && pwd)"
[ -d "$REPO_ROOT/examples/hello-plugin" ] || REPO_ROOT="$PWD"
WASM="${HELLO_WASM:-$REPO_ROOT/examples/hello-plugin/hello.wasm}"
if [ ! -f "$WASM" ]; then
  echo "building hello.wasm (GOOS=wasip1 GOARCH=wasm)"
  ( cd "$REPO_ROOT" && GOOS=wasip1 GOARCH=wasm go build -trimpath -o "$WASM" ./examples/hello-plugin/ ) \
    || fail "could not build examples/hello-plugin; gate 4 cannot run"
fi
[ -f "$WASM" ] || fail "no hello.wasm at $WASM; gate 4 cannot run"
mkdir -p "$PLUGINS/hello"
cp "$WASM" "$PLUGINS/hello/hello.wasm"
cp "$REPO_ROOT/examples/hello-plugin/plugin.json" "$PLUGINS/hello/plugin.json"

kill -0 "$PID" 2>/dev/null || fail "process died before gate 4"
restart_for_plugin() {
  # The plugin dir was populated after boot, so the running process has nothing
  # loaded. Restart against the same isolated data dir to pick it up.
  echo "=== restarting to load the staged plugin ==="
  kill -TERM "$PID" 2>/dev/null || true
  for i in $(seq 1 "$GRACEFUL_TIMEOUT"); do kill -0 "$PID" 2>/dev/null || break; sleep 1; done
  kill -9 "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true
  rm -f "$DATA"/loopworker.lock
  "$BIN" >>"$LOG" 2>&1 &
  PID=$!
  for i in $(seq 1 "$BOOT_TIMEOUT"); do
    sleep 1
    c="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 4 "http://127.0.0.1:${PORT}/api/v1/health" 2>/dev/null || echo 000)"
    [ "$c" = "200" ] && return 0
    kill -0 "$PID" 2>/dev/null || fail "process died while restarting with the plugin"
  done
  fail "health never returned 200 after the restart"
}
restart_for_plugin
grep -qi 'no-plugin-installed' "$LOG" && echo "note: log mentions no-plugin-installed; check the plugin staging above"

created="$(curl -sS --max-time 8 "${API_KEY[@]}" -H 'Content-Type: application/json' \
  -d '{"type":"echo","input_text":"boot-smoke"}' "http://127.0.0.1:${PORT}/api/v1/tasks" 2>/dev/null || true)"
TASK_ID="$(printf '%s' "$created" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
[ -n "$TASK_ID" ] || fail "could not create a task with the smoke key: ${created:0:300}"
echo "PASS  task created with the configured key: $TASK_ID"

final=""
for i in $(seq 1 "$TASK_TIMEOUT"); do
  kill -0 "$PID" 2>/dev/null || fail "process died while the task was running"
  listing="$(curl -sS --max-time 4 "${API_KEY[@]}" "http://127.0.0.1:${PORT}/api/v1/tasks" 2>/dev/null || true)"
  final="$(printf '%s' "$listing" | tr '{' '\n' | grep "$TASK_ID" \
    | sed -n 's/.*"state"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  case "$final" in
    completed|failed|dead_letter|cancelled) break ;;
  esac
  sleep 1
done
case "$final" in
  completed) echo "PASS  task reached completed (AC-2: a task really runs)" ;;
  *) fail "task $TASK_ID ended in state '${final:-unknown}' after ${TASK_TIMEOUT}s; a healthy server that cannot run a task is not a pass" ;;
esac

# gate 5: a flood is throttled and does not take the process down.
# This is the AC-4 DoS regression. The anonymous budget is
# security.DefaultAnonRate (100 per window) and it only applies to the public
# surface, so /api/v1/health is the endpoint to hammer: an authenticated route
# would answer 401 before the limiter ever ran, which would prove nothing.
echo "=== flooding the anonymous surface with 130 requests ==="
throttled=0
served=0
for i in $(seq 1 130); do
  c="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 4 \
    "http://127.0.0.1:${PORT}/api/v1/health" 2>/dev/null || echo 000)"
  case "$c" in
    200) served=$((served + 1)) ;;
    429) throttled=$((throttled + 1)) ;;
    000) fail "connection died on flood request $i — the flood killed the listener" ;;
    *) fail "unexpected HTTP $c on flood request $i (want 200 or 429)" ;;
  esac
done
kill -0 "$PID" 2>/dev/null || fail "process died during the flood"
[ "$throttled" -gt 0 ] || fail "130 anonymous requests produced 0 throttled responses; the anonymous rate limit is not enforced (AC-4 DoS regression)"
echo "PASS  flood throttled: $served served, $throttled rejected with 429, process alive"

# The flood must not wedge the server: an authenticated call still has to work.
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 "${API_KEY[@]}" \
  "http://127.0.0.1:${PORT}/api/v1/tasks" 2>/dev/null || echo 000)"
[ "$code" = "200" ] || fail "after the flood an authenticated read returned HTTP $code, want 200 — the limiter wedged the server"
echo "PASS  authenticated request still served after the flood"

# gate 6: graceful shutdown on SIGTERM
echo "=== sending SIGTERM to $PID ==="
kill -TERM "$PID" 2>/dev/null || fail "could not signal the process"
rc=0
for i in $(seq 1 "$GRACEFUL_TIMEOUT"); do
  if ! kill -0 "$PID" 2>/dev/null; then break; fi
  sleep 1
done
if kill -0 "$PID" 2>/dev/null; then
  kill -9 "$PID" 2>/dev/null || true
  fail "process still alive ${GRACEFUL_TIMEOUT}s after SIGTERM — graceful shutdown is hanging"
fi
wait "$PID"; rc=$?
if [ "$rc" -ne 0 ]; then
  # 143 = 128+15 means the process died *from* SIGTERM without its handler
  # finishing. That is a real defect on Linux and on Docker (STOPSIGNAL SIGTERM).
  # On Windows under Git Bash, however, `kill -TERM` terminates the process
  # without Go ever observing the signal, so 143 says nothing about our handler.
  # Say which case this is instead of reporting a pass or a failure we cannot
  # justify: an unexplained 143 on a signal-capable platform is a failure.
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) signal_deliverable=no ;;
    *) signal_deliverable=yes ;;
  esac
  if [ "$rc" = "143" ] && [ "$signal_deliverable" = yes ]; then
    fail "process exited with code $rc after SIGTERM (143 means no SIGTERM handler ran; expected 0)"
  fi
  echo "SKIP  exit code $rc: SIGTERM is not deliverable as a catchable signal on $(uname -s);"
  echo "      the drain path itself is covered by .release/scripts/docker-smoke.sh (STOPSIGNAL SIGTERM)"
  echo "      and by TestStartServesAndReportsRealHealth / TestStopBeforeStart in pkg/server."
fi
echo "PASS  process exited on SIGTERM within ${GRACEFUL_TIMEOUT}s"

echo
echo "BOOT SMOKE PASSED: $BIN"
[ -n "${KEEP_LOG:-}" ] && echo "log kept at $LOG"
exit 0

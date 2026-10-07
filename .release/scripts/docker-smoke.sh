#!/usr/bin/env bash
#
# docker-smoke.sh — boots the LoopWorker image and proves three things CI used
# never checked (CI only ran `loopworker version`, which passed on a binary that
# could not start, so a broken artifact shipped):
#
#   1. the container reaches "running" and stays there for BOOT_WAIT seconds
#      (no crash-loop / restart-loop);
#   2. GET /api/v1/health returns HTTP 200 with a JSON body whose status is
#      healthy|degraded. The container is given a throwaway credential, because
#      the image sets LOOPWORKER_SERVER_HOST=0.0.0.0 and the server refuses to
#      start on a public interface without one — so a smoke run that supplied
#      no key never reached the probe at all; it just watched a dead container.
#      LOOPWORKER_SMOKE_TOKEN, if set, is sent as a bearer token as well.
#   3. `docker stop` (SIGTERM) terminates the process with exit code 0 within
#      STOP_TIMEOUT seconds => graceful shutdown really works.
#
# Usage:  bash .release/scripts/docker-smoke.sh [image] [port]
# Exit:   0 all gates passed | 1 a gate failed | 2 harness problem (no docker)
#
# Requires: docker CLI + daemon, curl. Runs on Linux CI; on macOS use Colima/Orb.

set -uo pipefail

IMAGE="${1:-loopworker:smoke}"
PORT="${2:-19527}"
BOOT_WAIT="${BOOT_WAIT:-90}"
STOP_TIMEOUT="${STOP_TIMEOUT:-25}"
NAME="loopworker-smoke-$$"
TOKEN="${LOOPWORKER_SMOKE_TOKEN:-}"

# The container needs a credential to boot at all: the image binds 0.0.0.0, and
# ValidateBindAddress refuses a public interface while only an ephemeral
# per-process key exists. This mirrors .release/scripts/boot-smoke.sh so the two
# gates cannot disagree about what "authenticated" means.
SMOKE_KEY="${LOOPWORKER_SMOKE_API_KEY:-lwk_smoke_0123456789abcdef}"
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$1" | sha256sum | cut -d' ' -f1
  else
    printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1
  fi
}
KEY_HASH="$(sha256_of "$SMOKE_KEY")"
API_KEYS="smoke:admin:$KEY_HASH"

fails=0
step() { printf '\n=== %s ===\n' "$*"; }
ok()   { printf 'PASS  %s\n' "$*"; }
bad()  { printf 'FAIL  %s\n' "$*"; fails=$((fails + 1)); }

command -v docker >/dev/null 2>&1 || { echo "docker CLI not found"; exit 2; }
docker info >/dev/null 2>&1 || { echo "docker daemon unreachable (is it running?)"; exit 2; }

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  rm -f "$health_body" "$hdr" >/dev/null 2>&1 || true
}
trap cleanup EXIT

step "0. image under test"
docker image inspect "$IMAGE" --format '{{.Id}} {{.Config.User}} entrypoint={{.Config.Entrypoint}} health={{.Config.Healthcheck.Test}}' || {
  echo "image $IMAGE not present — build it first (make docker)"; exit 2; }

step "1. start read-only, non-root, no host access beyond the API port"
# --read-only + tmpfs /data is the self-service contract: the image must work
# under a hardened runtime, not only with privileged defaults.
docker run -d --name "$NAME" \
  --read-only \
  --tmpfs /data:rw,size=256m,mode=1777 \
  --cap-drop=ALL \
  --security-opt=no-new-privileges \
  -p "127.0.0.1:${PORT}:19527" \
  -e LOOPWORKER_LOG_LEVEL=debug \
  -e "LOOPWORKER_API_KEYS=${API_KEYS}" \
  "$IMAGE" >/dev/null || { echo "docker run failed"; exit 1; }

running=0
for i in $(seq 1 "$BOOT_WAIT"); do
  running=$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null || echo false)
  [ "$running" = "true" ] || break
  sleep 1
done

step "2. container stayed up (no crash loop)"
running=$(docker inspect -f '{{.State.Running}}' "$NAME")
exited=$(docker inspect -f '{{.State.Status}}' "$NAME")
if [ "$running" = "true" ]; then
  ok "still running after ${BOOT_WAIT}s attempts window"
else
  bad "container is '$exited'; here is why:"
  docker logs --tail 80 "$NAME" || true
fi

step "3. user is not root, /data is the only writable path"
uid=$(docker exec "$NAME" id -u 2>/dev/null || echo unknown)
if [ "$uid" != "0" ] && [ "$uid" != "unknown" ]; then ok "runs as uid=$uid"; else bad "container runs as uid=$uid (expected non-root)"; fi

step "4. GET /api/v1/health must return 200 + JSON status"
# Both capture files are created once, before the poll, and removed by the
# cleanup trap.
#
# The body file cannot start life as an empty string: curl rejects a blank -o
# target outright ("curl: option -o: blank argument where content is expected",
# exit 2) and writes nothing, so the header check below would then read no file
# at all. Because stderr was discarded and `|| echo 000` supplied the fallback,
# the gate reported "HTTP 000" against a container that was answering 200 — this
# script could not pass on any input, and nothing noticed because it had never
# been executed. Creating the files inside the loop instead leaked one pair per
# attempt, and the loop runs thirty times.
health_body="$(mktemp)"
hdr="$(mktemp)"
for i in $(seq 1 30); do
  args=(-sS -o "$health_body" -D "$hdr" -w '%{http_code}' --max-time 5 "http://127.0.0.1:${PORT}/api/v1/health")
  args+=(-H "X-API-Key: ${SMOKE_KEY}")
  [ -n "$TOKEN" ] && args+=(-H "Authorization: Bearer ${TOKEN}")
  health_code=$(curl "${args[@]}" 2>/dev/null || echo "000")
  [ "$health_code" = "200" ] && break
  sleep 2
done
if [ "$health_code" = "200" ]; then
  ok "HTTP 200 from /api/v1/health"
  echo "     body: $(head -c 400 "$health_body" 2>/dev/null)"
  case "$(cat "$health_body" 2>/dev/null)" in
    *'"status"'*) ok "response is the documented HealthResponse JSON" ;;
    *) bad "response body has no \"status\" field — API contract changed without updating the smoke gate" ;;
  esac
else
  bad "GET /api/v1/health returned HTTP $health_code (expected 200)"
  echo "     body: $(head -c 400 "$health_body" 2>/dev/null)"
  docker logs --tail 60 "$NAME" || true
fi

step "5. Docker HEALTHCHECK must go green (not just the process being alive)"
hc=""
for i in $(seq 1 12); do
  hc=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$NAME" 2>/dev/null || echo none)
  [ "$hc" = "healthy" ] && break
  sleep 5
done
if [ "$hc" = "healthy" ]; then ok "health status=healthy"; else bad "health status=$hc (expected healthy; HEALTHCHECK may be misconfigured)"; fi

step "6. SIGTERM must shut down gracefully (exit 0), not via timeout-kill"
docker stop -t "$STOP_TIMEOUT" "$NAME" >/dev/null 2>&1
rc=$(docker inspect -f '{{.State.ExitCode}}' "$NAME" 2>/dev/null || echo "?")
if [ "$rc" = "0" ]; then
  ok "exit code 0 after docker stop (graceful)"
else
  bad "exit code '$rc' after docker stop — SIGTERM handler is missing/hanging (137 means it was SIGKILLed after the timeout)"
  docker logs --tail 80 "$NAME" 2>/dev/null || true
fi

step "result"
if [ "$fails" -gt 0 ]; then
  echo "docker-smoke: FAILED ($fails gate(s))"
  exit 1
fi
echo "docker-smoke: all gates passed for $IMAGE"
exit 0

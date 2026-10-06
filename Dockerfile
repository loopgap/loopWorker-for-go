# syntax=docker/dockerfile:1
#
# LoopWorker container image — multi-stage, non-root, static, health-checked.
#
# Why this file was rewritten (all four were build/startup blockers):
#   * builder was `golang:1.21-alpine` while go.mod declared an older `go`;
#   * `CGO_ENABLED=1` with no gcc/build-base installed => the SQLite CGO build
#     could never link;
#   * runtime base `alpine:3.19` is end-of-life (no security patches);
#   * no HEALTHCHECK, so a crash-looping container reported as "running".
#
# The builder tag below MUST keep matching go.mod's `go` directive. CI enforces
# that (.release/tools/relcheck, job `release-config` in ci.yml) and
# GOTOOLCHAIN=local makes a mismatch fail loudly here instead of silently
# downloading a different toolchain mid-build.

ARG GO_VERSION=1.26.6
FROM golang:${GO_VERSION}-alpine AS builder

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown
# Import prefix for -X flags: must equal `module <path>` in go.mod.
ARG MODULE=loopworker

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    GOFLAGS=-trimpath

WORKDIR /src

# Dependencies first, so source edits don't re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# `.dockerignore` deliberately keeps web/canvas (Node sources, never needed to
# build the server) out, while shipping pkg/api/dist (the assets the binary
# go:embeds) in — relcheck fails the pipeline if those two ever contradict.
COPY . .

RUN go build -trimpath \
      -ldflags "-s -w \
        -X ${MODULE}/version.Version=${VERSION} \
        -X ${MODULE}/version.GitCommit=${GIT_COMMIT} \
        -X ${MODULE}/version.BuildDate=${BUILD_DATE}" \
      -o /out/loopworker ./cmd/loopworker/

# Fail the IMAGE BUILD if the binary is not actually static/free of libc deps:
# this is the check that turns "CGO_ENABLED=0 is a lie" into a red pipeline.
RUN apk add --no-cache binutils file && \
    file /out/loopworker | grep -E 'statically linked|ELF' && \
    (go version -m /out/loopworker | grep -q 'mod loopworker' || \
      echo "::warning::module info missing" ) && \
    /out/loopworker version

# ---------------------------------------------------------------- runtime ---
# Alpine (not distroless) on purpose: distroless/static has no shell, so the
# HEALTHCHECK below could not probe the API and a self-service customer cannot
# `docker exec` in to look around. Cost: a few MB. Tradeoff documented in
# .release/SCOPE-PROPOSAL.md.
FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates tzdata curl && \
    addgroup -g 10001 loopworker && \
    adduser -u 10001 -S -G loopworker -h /data loopworker

COPY --from=builder /out/loopworker /usr/local/bin/loopworker

# /data is the only writable path; run with `--read-only --tmpfs /data` or a
# volume mounted at /data.
#
# LOOPWORKER_SERVER_HOST is set explicitly because the binary defaults to
# loopback. A container is useless if it only listens on 127.0.0.1 inside its
# own network namespace; the bind guard that refuses a public interface without
# credentials still applies, so a container started without
# LOOPWORKER_API_KEYS fails loudly instead of serving an open API.
ENV LOOPWORKER_DATA_DIR=/data \
    LOOPWORKER_PLUGINS_DIR=/data/plugins \
    LOOPWORKER_PORT=19527 \
    LOOPWORKER_SERVER_HOST=0.0.0.0 \
    LOOPWORKER_LOG_LEVEL=info \
    HOME=/data

RUN mkdir -p /data/plugins /data/db && chown -R 10001:10001 /data
WORKDIR /data

USER 10001:10001
EXPOSE 19527
VOLUME ["/data"]

# Real readiness probe, not `true`: HTTP 200 from /api/v1/health.
# start-period covers WASM/runtime warm-up; 3 consecutive failures => unhealthy.
HEALTHCHECK --interval=15s --timeout=5s --start-period=25s --retries=3 \
  CMD curl -fsS --max-time 4 "http://127.0.0.1:${LOOPWORKER_PORT:-19527}/api/v1/health" || exit 1

# Docker must forward SIGTERM (not SIGKILL) so main.go's graceful path runs;
# .release/scripts/docker-smoke.sh asserts the process exits 0 on `docker stop`.
STOPSIGNAL SIGTERM

ENTRYPOINT ["/usr/local/bin/loopworker"]
CMD []

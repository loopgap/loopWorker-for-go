# Security Policy

LoopWorker is shipped as a **self-service** product. This page is the contract:
what we secure, what you must configure, and how to tell us about a problem
without expecting a support hotline.

## Supported versions

Only the two most recent minor release lines receive fixes. Anything older is
community-supported "as-is" under the MIT license — no patches, no exceptions,
because there is no support team behind this product (see SUPPORT.md).

| Version | Supported | Notes |
|---------|-----------|-------|
| latest minor (`v0.x` head) | ✅ | gets CVE fixes within the window below |
| previous minor | ✅ | security fixes only, backported when feasible |
| anything older | ❌ | upgrade; unpatched known CVEs are your responsibility |

There are **no released versions yet** (0 git tags as of 2026-10-05). The first
tag is `v0.1.0-beta`, and later betas are `v0.1.0-beta.N`; that first tag starts
this table. The release pipeline that produces them is
`.github/workflows/release.yml` + `.goreleaser.yaml`.

### Fix turnaround (best effort, not an SLA)

There is no support team and no on-call rotation, so this table is what a
single maintainer aims for, not a contract. Treat it as "when it gets done",
not "when you are entitled to expect it".

| Severity | Target first response | Target fix |
|----------|----------------------|------------|
| Critical (RCE / auth bypass / data exposure by default) | a few days | next patch release |
| High | a couple of weeks | next patch release |
| Moderate / Low | best effort | next scheduled release |
| Dependency CVE (from `govulncheck`, which runs on every PR) | automated | same window |

If you need a guaranteed response time, that is a commercial arrangement, not
something this page can promise.

## Reporting a vulnerability — embargo

**Do not open a public issue for anything exploitable.** Public issues are for
bugs, not for vulnerabilities.

1. Open a
   [private GitHub security advisory](https://github.com/loopgap/loopworker/security/advisories/new).
   That is the only monitored channel: this project has no support inbox, and a
   placeholder address that bounces is worse than no policy at all — CI job
   `release-config` fails the build while this page still carries an
   unconfirmed placeholder.
2. Include: version (`loopworker version`), OS/arch, how to reproduce, and
   whether a workaround exists.
3. We aim to acknowledge within a few days, negotiate a disclosure date
   (default 90 days), and publish a GitHub Security Advisory + a patch
   release. You get credit in the advisory unless you ask not to.
4. During the embargo, no details are posted publicly — including by us.

## What LoopWorker does NOT do (know before you expose it)

Verified against the code as of 2026-10-05, not marketing:

* **The HTTP API requires a credential on every endpoint except three.** Only
  `GET /healthz`, `GET /api/v1/health` and `GET /api/v1/openapi.json` are
  anonymous. A fresh install binds `127.0.0.1` and logs an ephemeral admin key
  on startup; permanent keys come from `LOOPWORKER_API_KEYS` or from an admin
  calling `POST /api/v1/auth/keys`. **The server refuses to start** if it would
  listen on a public interface with no configured credential, so you cannot
  accidentally expose an unauthenticated API by editing one setting.
* **Metrics and logs are on a separate loopback-only listener** (`server.admin_port`,
  default `127.0.0.1:19528`) and require the `admin` role. They are not on the
  API port, and they are not anonymous.
* **No TLS in the server.** Terminate TLS at a reverse proxy.
* **WASM sandbox limits are resource caps, not a security boundary.** Memory /
  CPU-time / output limits exist (`internal/config` defaults 256MB/30s/64MB) and
  are **shared by every plugin in a sandbox**, not per plugin. Plugin code is
  not isolated from the host process's file system access beyond those caps.
  Treat any plugin you load as code you fully trust.
* **SQLite data files are unencrypted** and stored under the data dir; protect
  them with filesystem permissions.
* Secrets: the API key digest lives in the SQLite file, and the **one-time
  bootstrap key is written to the startup log in plaintext** so you can copy it.
  Scrub logs before sharing them. Do not put credentials in
  `config/config.example.yaml` copies.

## Automated controls

| Control | Where | Blocks |
|---------|-------|--------|
| `govulncheck ./...` (official Go vuln DB) | CI job `quality` | any known-vulnerable std-lib or module-graph package the build actually reaches |
| `go mod tidy` diff review | human | dependencies nothing imports (they still ship in the module graph) |
| SBOM per artifact (`syft` → SPDX) | `.goreleaser.yaml` `sboms` | "what's in this binary?" tickets |
| cosign blob signatures over `checksums.txt` | `.goreleaser.yaml` `signs` + CI | tampered artifacts |
| Provenance attestation on the container image | CI (`provenance: mode=max`) | unattributable images |
| Reproducible static builds (`CGO_ENABLED=0`, `-trimpath`) | `.goreleaser.yaml` | drift between source and shipped binary |
| Container image scan (Trivy) | CI job `docker` | critical image CVEs |
| Third-party licence inventory | CI job `third-party-notices` | shipping an unlisted dependency |

## Hardening checklist for operators (self-service)

```bash
# 1. Verify the artifact you downloaded before executing it.
#    The identity is this repository; the pipeline signs with GitHub OIDC, so
#    the issuer is fixed and the regexp matches the repo the workflow ran in.
sha256sum -c checksums.txt
cosign verify-blob --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp 'https://github.com/loopgap/loopworker/.+' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' checksums.txt

# 2. Run read-only, non-root, no host writes.
#    The image name is <registry>/<repository>/<binary> - note the repository
#    layer. Dropping it (writing only owner and binary) names an image that
#    does not exist.
docker run --read-only --tmpfs /tmp --user 65534:65534 \
  -p 127.0.0.1:19527:19527 -v loopworker-data:/data \
  ghcr.io/loopgap/loopworker/loopworker:latest

# 3. Bind to loopback only if a proxy is doing auth/TLS
LOOPWORKER_PORT=19527 loopworker   # then front it with nginx/envoy
```

## Scope of this policy

Bugs in the `web/canvas` UI, the developer-only CLIs, and anything under
`docs/product/` are **out of security scope** — they are not part of the
supported product surface (`.release/SCOPE-PROPOSAL.md`).

Note what that means in practice: **releases ship only the `loopworker`
binary**, so `loopctl` is something you build from source yourself — it is not
in the archive and not in the container image. If you build it, it is your code
to review, not ours to fix. Four other developer CLIs (`loopdebug`, `loopwatch`,
`loopbench`, `loopsim`) existed once and were **deleted**; there is no source
left to build, and the `Removed` section of `CHANGELOG.md` records why.

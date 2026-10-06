# Support

**LoopWorker is a 100% self-service product.** There is no support desk, no
SLA phone number, no hand-holding onboarding, and no ticket queue we answer
manually. That is intentional: the product has to work from the artifact alone.
This page states exactly what that means so nobody is surprised later — and so
that anything we failed to automate is visible as a gap, not an argument.

## What you get (included with every release)

| Need | Where it is answered |
|------|----------------------|
| Install / run on your OS | this file + `docs/QUICKSTART.md` (bundled inside every archive) |
| Verify the download is genuine and untampered | `checksums.txt` (sha256) + cosign signature over it (see `SECURITY.md` for the exact command) |
| Know exactly what is inside the binary | `*.spdx.json` SBOM (one per artifact) + `NOTICE` in the archive + the separate `third-party-notices.tar.gz` asset |
| API contract | `docs/api/api-reference.md`, which is kept in sync with `pkg/api/openapi.json` by a test that fails the build on drift |
| "Is it healthy?" | `GET /api/v1/health` (anonymous) returns component-by-component status. Prometheus metrics are **not** on that port — they are on the loopback-only admin listener (`server.admin_port`, default `127.0.0.1:19528`) and need an `admin` credential. |
| Container deploy | `ghcr.io/loopgap/loopworker/loopworker:<version>` (note the repository layer; same image, digest-pinned in release notes) |
| Linux package install | `loopworker_<ver>_amd64.deb` / `.rpm` in the release assets |
| macOS install | the release `.zip` — a Homebrew tap is configured in `.goreleaser.yaml` but **no tap repository exists yet**, so `brew install` does not work today |
| Windows install | download the `.zip`, run `loopworker.exe` — nothing to compile, no CGO, no VC++ runtime |
| Config reference | `config/config.example.yaml` + `loopworker doctor` (prints every setting and where its value came from) |
| What changed / what is safe to upgrade | `CHANGELOG.md` + generated release notes |
| Vulnerabilities | `SECURITY.md` (private advisory channel + supported-version table) |

## What you do **not** get (self-service only — by design)

We will not answer, and no refund or credit arises from, the following:

* Requests to write, tune, debug, or review **your** workflows, plugins, WASM
  modules, YAML/JSON configs, infrastructure, reverse proxy, or Kubernetes
  manifests.
* "It doesn't run in my environment" cases where the documented platform
  requirements are not met (see matrix below), or where the artifact's
  checksum/signature was never verified.
* Data recovery. We ship no backup, migration, or repair service; the SQLite
  file under your data directory is yours.
* Root-cause analysis on customer machines, log triage, screen shares, remote
  sessions, training, onboarding calls, or documentation translation.
* Custom builds, out-of-band patches, back-ports to unsupported versions,
  private branch maintenance, or feature development for a single customer.
* Legal, compliance, export-control, or tax advice about your use.
* Availability guarantees. There is no uptime SLA for the source repo, the
  GitHub release page, or ghcr.io. Self-hosting is the supported deployment.
* Priority for anything older than the two most recent minor releases, per
  SECURITY.md.

## The supported platform matrix

| OS | Arch | How to run | How it is verified |
|----|------|-----------|--------------------|
| Linux | amd64 | tar.gz / deb / rpm / container | test + boot smoke on `ubuntu-latest` |
| Linux | arm64 | tar.gz / rpm / container | cross-compiled, built on an arm64 runner, boot smoke |
| macOS | amd64 (Intel) | tar.gz | test on `macos-13` (Intel) |
| macOS | arm64 (Apple silicon) | tar.gz | test on `macos-latest` (arm64) |
| Windows | amd64 | zip (`loopworker.exe`) | test + boot smoke on `windows-latest` |

Two honest caveats about that table:

* **arm64 gets build + boot coverage, not the full test suite.** The unit and
  race jobs run on amd64 hosts; arm64 is covered by the cross-compilation
  matrix, by an arm64 container build, and by a boot smoke test.
* **These tests prove the process starts and serves a real task. They do not
  prove your workload.** Nothing in this project is a performance claim.

Not shipped, and therefore not supported: Windows arm64, 32-bit anything,
FreeBSD, Solaris/AIX, `web/canvas` as a product feature, and the extra CLIs
flagged in `.release/SCOPE-PROPOSAL.md`. **Releases ship only the
`loopworker` binary** — the other five (`loopctl`, `loopdebug`, `loopwatch`,
`loopbench`, `loopsim`) are developer tools you build from source. If you
need one of those in a release, that is a feature request in the open-source
community process — not a support case.

## Requirements

* A 64-bit OS from the table above. Beyond that we publish no minimum: the
  process is small and the workload is yours, so measure it rather than
  trusting a number from us.
* Nothing else. No Go toolchain, no C compiler, no Node, no database server:
  the binary is statically linked (`CGO_ENABLED=0`) with an embedded pure-Go
  SQLite driver.
* If you use the container: any runtime with OCI support (Docker, Podman,
  containerd).

## How to get something fixed without support

1. Reproduce with the latest release and `LOOPWORKER_LOG_LEVEL=debug`.
2. Collect: `loopworker version` output, OS/arch, the exact command, the JSON
   from `/api/v1/health`, and the relevant `/metrics` scrape.
3. Search [existing issues](https://github.com/loopgap/loopworker/issues).
4. Open an issue with a **minimal reproducible example** — a single command
   plus a workflow file that shows it. A repro that a maintainer can run in 60
   seconds is the only thing that actually moves a fix along here.
5. Expect nothing on a clock. Critical security issues follow the response
   table in SECURITY.md; everything else is community effort.

## Commercial extras that are not "support"

Things that *can* be bought are separately scoped deliverables, not
after-sales: enterprise-licensed add-ons (a separate repository under a
different licence), a paid deployment audit, or a paid upgrade-assistance
engagement with an explicit deliverable. Anything sold as "unlimited support" contradicts this file — if
such an offer appears anywhere, it is not this product.

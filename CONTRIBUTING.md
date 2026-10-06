# Contributing to LoopWorker

Thanks for your interest. This project is a **self-service product with no
after-sales team**, so the bar for contribution is "automatable and honest":
CI proves it, the docs state it, and nothing claims more than the artifact does.

## Code of Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Enforcement contact is
`TODO(owner)` until a real address is confirmed (see SECURITY.md for the
placeholder policy — we would rather block a release than publish a contact
that bounces).

## Developer Certificate of Origin (required, enforced by CI)

Every commit must carry a `Signed-off-by` trailer (DCO 1.1). CI job `dco` fails
any pull request with an unsigned commit.

```
Signed-off-by: Your Name <you@example.com>
```

This certifies you wrote the change or otherwise have the right to submit it
under this project's MIT license. It is what keeps future relicensing of a
separate enterprise layer legally clean, and it is cheap to enforce now (one
contributor) rather than painful later.

Sign without changing the message:

```bash
git commit --amend --signoff            # last commit
git rebase --signoff origin/main        # every commit on your branch
git commit -s -m "feat: ..."            # going forward (also: git config alias.ci "commit -s")
```

We do **not** use a CLA. No `skip-dco` label should be requested except for a
one-time mechanical import, and a maintainer must record it in the PR.

## How to Contribute

1. Fork, then create a branch: `git switch -c feat/short-topic`
2. Make the change. If it changes behaviour an operator can see, update
   `CHANGELOG.md` and the docs in the same commit.
3. Run the full gate locally (see "Development Setup"): `make check` on
   Linux/macOS/Git Bash, or `pwsh .release/build.ps1 -Task all` on Windows.
4. Commit using [Conventional Commits](https://www.conventionalcommits.org/)
   with `-s`; keep each commit buildable.
5. Push and open a pull request. CI must be green; do not merge red.

### Commit message format

```
<type>(<scope>): <description>   # scope = package or subsystem, e.g. server, sandbox, ci

[optional body: what and why, referencing the file/line behaviour]

[optional footer: Fixes #123, Co-authored-by: ...]
```

Types: `feat` `fix` `docs` `style` `refactor` `test` `chore` `build` `ci` `revert`.

`CHANGELOG.md` and the GitHub release notes are generated from these messages
(`changelog.use: conventional` in `.goreleaser.yaml`), so a message like
`fix: oops` is a bug in the release pipeline.

## Development Setup

### Prerequisites

* **Go, exactly the version in `go.mod`** (`go 1.26.1`). CI derives its
  toolchain from go.mod (`go-version-file: go.mod`) and `relcheck` fails if the
  Dockerfile or any workflow disagrees — do not hardcode a Go version anywhere.
* Git.
* Optional, needed to run the whole gate locally:
  `golangci-lint` v2.9.0, `govulncheck` v1.8.0, `goreleaser` v2.9.0, Docker.
  CI installs these pinned; nothing is required just to build the binary.

### Linux / macOS / Git Bash

```bash
make build            # bin/<name>[.exe] for the host platform
make test             # go test ./...
make test-race        # go test -race ./...  (ALL packages)
make lint             # golangci-lint run (go vet alone is NOT lint)
make fmt-check        # blocking gofmt gate
make coverage-gate    # asserts >= 80% with -coverpkg=./...
make vuln             # govulncheck
make check            # fmt-check + vet + lint + test-race + coverage-gate + vuln
make boot-smoke       # start the binary, require /api/v1/health 200, SIGTERM => exit 0
make docker-smoke     # same, against the container image
make release-snapshot # relcheck + goreleaser check + goreleaser snapshot build
```

### Windows (PowerShell — there is no bash or make requirement)

```powershell
pwsh .release/build.ps1 -Task build          # writes bin\loopworker.exe
pwsh .release/build.ps1 -Task all            # fmt, vet, lint, build, test, race, coverage gate
pwsh .release/build.ps1 -Task smoke          # boots the .exe, checks /api/v1/health, Ctrl+Break => graceful exit
pwsh .release/build.ps1 -Task release -AllClis
```

`.release/build.ps1` exists because a self-service product cannot assume a POSIX
shell: the old Makefile used `for`, `rm -rf` and `date -u`, and wrote a
suffix-less `bin\loopworker` that Windows cannot execute.

## Code Style

* `gofmt` + `goimports` (`-local loopworker`) — enforced by `formatters` in
  `.golangci.yaml`, and by a blocking `gofmt -l` CI step.
* Lint config lives in `.golangci.yaml`. Enabled beyond the standard set:
  `errorlint`, `errname`, `nilerr`, `bodyclose`, `noctx`, `contextcheck`,
  `copyloopvar`. Errors are compared with `errors.Is`/`errors.As`, wrapped with
  `%w`, and sentinels live in `pkg/errors` (name them `ErrXxx`).
* Do not add a linter "just because" — every noisy rule that fires 300 times
  gets the real defects ignored. gosec/revive/funlen are deliberately off
  (rationale is a comment at the top of `.golangci.yaml`).
* Exported identifiers get doc comments. Keep functions small and focused.

## Testing

* Unit tests next to the code; integration tests in `integration/`.
* Concurrency-sensitive code (`pkg/server`, `pkg/event`, `internal/core/*`) must
  pass `-race`; CI runs `go test -race ./...` over every package, so a race in
  any package blocks the merge.
* Coverage is enforced (>= 80%, cross-package). If your change lowers it, add
  tests rather than lowering `COVERAGE_MIN`.
* Behaviour that a customer can observe (a new route, flag, exit code, artifact
  layout) needs a smoke-test assertion in `.release/scripts/boot-smoke.sh`,
  `.release/smoke.ps1` or `.release/scripts/docker-smoke.sh`. That is how this
  project stays support-free.

## Documentation

* Update `README.md`, `docs/QUICKSTART.md`, `docs/api/api-reference.md` when
  behaviour changes; the release archives ship those files.
* Never document a feature that is not in the artifact. If something is
  half-built (e.g. a loader that is not wired into the binary), say so in the
  `Corrected` section of `CHANGELOG.md` instead of advertising it.
* `docs/product/*.md` and `.workbuddy/` are internal planning notes, not product
  docs, and are cut candidates (`.release/SCOPE-PROPOSAL.md`). Do not link them
  from customer-facing material.

## Release Process (this one is real)

Releases are produced by `.goreleaser.yaml` running in
`.github/workflows/release.yml`. **A human cuts the tag; no automation creates
tags or commits.**

1. Land all intended changes on `main` with CI green.
2. Update `CHANGELOG.md`: add a `## [vX.Y.Z]` section (the preflight job fails
   the release if it is missing) and move the relevant `Unreleased` items.
3. Confirm `NOTICE` / third-party obligations: `go-licenses` runs in CI and its
   output is attached to the release; re-run locally if you want to review it.
4. Tag and push the tag only:
   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z      # tag alone triggers the release
   ```
5. Watch `Release` → the pipeline: preflight (`relcheck -strict`,
   `goreleaser check`) → build/sign/SBOM/brew → per-OS artifact verification →
   multi-arch image push + container smoke test.
6. If preflight complains about `TODO(owner)`: that is by design. The license
   holder, security contact, tap owner and package vendor must be real values
   before anything is published.

Rollback: a release is a git tag plus artifacts. Delete the release (keep the
tag if the code is fine), fix forward, re-tag. Unsigned or checksum-mismatched
artifacts must be pulled, not patched quietly.

## Questions?

Open an issue with a minimal reproducible example. There is no support channel
by design — see `SUPPORT.md` for what is and is not answered.

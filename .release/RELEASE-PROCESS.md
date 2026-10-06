# Release Process (operator runbook)

The pipeline is `.github/workflows/release.yml` + `.goreleaser.yaml`. It is
ready; **the human owner cuts the tag**. No automation in this repository
creates commits or tags.

## 1. Prerequisites (one-time, blocks the first release on purpose)

`preflight` runs `relcheck -strict`, which fails while any of these are still
placeholders. That is the design: a published release with a fake licensor or a
fake security contact is worse than no release.

| What | Where | Must become |
|------|-------|-------------|
| Copyright holder | `LICENSE` line 24-ish | real legal entity/person name |
| Security contact | `SECURITY.md` | monitored address (or advisory-only policy) |
| CoC enforcement contact | `CODE_OF_CONDUCT.md` | real address |
| Tap owner + package vendor | `.goreleaser.yaml` (`brews.repository.owner`, `nfpms.vendor/maintainer/homepage`) | real GitHub owner |
| CODEOWNERS handles | `.github/CODEOWNERS` | existing team/username (else branch protection blocks merges) |
| Repo URLs in docs | `SECURITY.md`, `SUPPORT.md`, `CONTRIBUTING.md` (`<owner>`, `TODO(owner)`) | real owner |
| Go module path | `go.mod` | domain-qualified, see `MODULE-PATH.md` |

## 2. Cut a release

```bash
# 1. green main
git switch main && git pull --ff-only
# 2. changelog section for the version (preflight requires it)
$EDITOR CHANGELOG.md && git commit -s -am "docs(changelog): vX.Y.Z"
# 3. verify the whole pipeline without publishing
make release-snapshot                      # linux/macOS/git-bash
pwsh .release/build.ps1 -Task release      # Windows
# 4. tag + push the tag only
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

## 3. What the workflow then does

1. `preflight` — tag is semver and on HEAD, `CHANGELOG.md` has the section,
   `relcheck -strict`, `goreleaser check`.
2. `release` — installs syft `v1.9.0` + cosign `v3.1.3`, generates
   `THIRD-PARTY-LICENSES/` + `THIRD-PARTY-NOTICES.csv/md` with `go-licenses`,
   runs the full test suite with the 80% coverage gate, then
   `goreleaser release --clean`: 5 archives + deb/rpm + `checksums.txt` +
   SBOMs + cosign keyless signature over `checksums.txt` + Homebrew formula push,
   and attaches `third-party-notices.tar.gz`.
3. `verify-artifacts` (ubuntu / windows / macos) — selects the artifact for that
   OS, verifies its sha256 against `checksums.txt`, asserts an SBOM and a
   signature exist, unpacks, asserts `loopworker version` prints the injected
   version (not `dev`), asserts LICENSE/NOTICE/SUPPORT/SECURITY/CHANGELOG are
   inside the archive, asserts the Windows artifact ends in `.exe`, then boots it:
   `GET /api/v1/health` must be 200 and SIGTERM (or Ctrl+Break on Windows) must
   exit 0. On Linux it also runs `cosign verify-blob` against the repo's OIDC
   identity.
4. `docker-publish` — buildx multi-arch (`linux/amd64`, `linux/arm64`) to
   `ghcr.io/<repo>/loopworker:<version>` + `:latest` with `provenance: mode=max`
   and `sbom: true`, then pulls the amd64 image and runs
   `.release/scripts/docker-smoke.sh` (boot, `/api/v1/health`, HEALTHCHECK green,
   graceful `docker stop`).

A dry run (no publish, no signing) is available any time from
**Actions → Release → Run workflow** (`workflow_dispatch`); it uses
`goreleaser release --snapshot --clean --skip=publish,sbom,sign`.

## 4. Local verification commands used while building this pipeline

```bash
goreleaser check --config .goreleaser.yaml                                  # exit 0
goreleaser release --snapshot --clean --skip=publish,sbom,sign              # see below
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.11 .github/workflows/*.yml   # exit 0
(cd .release/tools && go run ./relcheck -root ../..)                        # reports real drift
bash -n .release/scripts/boot-smoke.sh .release/scripts/docker-smoke.sh      # syntax only
docker build .                                                               # needs a Docker daemon
```

`goreleaser` is pinned to **v2.9.0** in CI (`GORELEASER_VERSION`) — do not let it
float; the config schema is versioned (`version: 2`).

## 5. Failed / aborted release

* Preflight red → fix the named file; nothing was published.
* `release` red after the GitHub release was created → `gh release delete vX.Y.Z`
  and re-tag from a fixed commit (never reuse a version string).
* Artifact suspicious post-publish → delete the release assets, publish a
  `-v2` patch release immediately, and note the recall in `CHANGELOG.md`
  (silently patched artifacts destroy the only trust signal a self-service
  product has: the checksum + signature).

## 6. Versioning & support window

Semver with a prerelease suffix. `0.x` means the HTTP contract and config schema
may still change.

LoopWorker ships the `0.1.0-beta` line: the first tag is `v0.1.0-beta`, and
subsequent betas are `v0.1.0-beta.1`, `v0.1.0-beta.2`, … — the `.N` counter only
increases and the `0.1.0` base does not move while the beta line is open. A
`1.0.0` tag is a commitment to the contract and must be preceded by a documented
deprecation policy in `docs/api/api-reference.md`.

Two most-recent beta lines are supported (SECURITY.md). Nothing is
force-pushed over a tag.

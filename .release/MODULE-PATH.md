# Module path: `go install` compatibility (owner decision required)

`go.mod` currently declares:

```
module loopworker
go 1.26.1
```

`loopworker` is **not a valid remote import path**. Consequences for a
self-service product:

* `go install loopworker@latest` / `@v0.1.0` is impossible — Go requires a
  domain-qualified path. So the "install without downloading a release" story
  that SDK users expect does not exist today.
* Anyone who wants to import the engine as a library must add a `replace`
  directive and pin a local path (which is exactly what a scratch test for this
  change hit: `replace loopworker => ../../..`). Every such customer is a
  support ticket waiting to happen.
* Homebrew/goreleaser archives are unaffected (they download a zip/tar.gz), so
  the release pipeline works either way. The change is only about the Go module
  identity.

## Required change (owner executes; I am not allowed to touch go.mod this wave)

```
module github.com/<owner>/loopworker        # or loopworker.dev/loopworker with a vanity import
```

Then, mechanically:

1. Rewrite imports: `r=loopworker/ g=github.com/<owner>/loopworker/` over
   `cmd/`, `internal/`, `pkg/`, `integration/`, `version/` (`gofmt` unaffected).
2. Update the `-X` prefixes: `.goreleaser.yaml` (`builds[].ldflags`),
   `Makefile` (`LDFLAGS`, which already derives `MODULE` from `go list -m`),
   `Dockerfile` (`ARG MODULE=...` default), `.golangci.yaml`
   (`formatters.settings.goimports.local-prefixes`).
3. Update docs that show `go get`/`go install`.
4. `go mod tidy` (and confirm the SQLite driver swap in the same commit, so the
   dependency churn happens once).
5. Verify: `relcheck` asserts the ldflags prefix equals the `module` line, so a
   half-finished rename fails CI instead of shipping `Version=dev`.

`relcheck` reads `module` from go.mod and compares it against the ldflags import
prefix in `.goreleaser.yaml`, so steps 1–2 cannot silently drift. It will *not*
fail on the unqualified path (that would block every other task in this wave);
treat this file as the decision record.

## Options

| Option | Effect |
|--------|--------|
| `github.com/<owner>/loopworker` | standard; `go install` works; forks are obvious; needs a real GitHub owner |
| vanity `loopworker.dev/loopworker` | strongest brand control, survives repo moves; requires running a redirect server (support debt) |
| keep `loopworker` | no library users, no `go install`; release archives + container + brew remain the only install paths |

Recommendation: `github.com/<owner>/loopworker` before `v0.1.0`, because a module
path rename after the first tag breaks every existing consumer.

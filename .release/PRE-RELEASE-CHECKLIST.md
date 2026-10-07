# Pre-release checklist

Every line is pass/fail. A release with an unrun line is not a release.

## Build and verify

- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `go test ./...` exits 0 with no FAIL
- [ ] `go test -race ./...` exits 0 with no race report
- [ ] `make lint` exits 0 (or the documented equivalent in `.golangci.yaml`)
- [ ] `npm --prefix web/canvas test` exits 0
- [ ] `npm --prefix web/canvas run build` reproduces `pkg/api/dist` byte for
      byte (`diff -r web/canvas/dist pkg/api/dist` is empty) — the embed is
      committed, so a frontend change that is not synced still ships the old
      canvas while every Go test stays green

## Cleanliness

- [ ] `git status --ignored --short` lists no runtime leftover outside `_scratch/`
- [ ] No file over 1 MB is untracked (build outputs belong in `_scratch/`)
- [ ] `pkg/api/dist/assets` contains exactly one `index-*.js` and one
      `index-*.css` — a stale pair from a previous build gets embedded into the
      binary and shipped, and the assets live under `assets/`, not at the root
- [ ] `web/canvas/node_modules` and `web/canvas/dist` do not exist

## Product surface, verified in a real browser

These stay manual on purpose. `npm test` covers the component tree under jsdom;
it cannot render node geometry or hold a real `EventSource` open. Do not skip
them because the automated canvas job is green — that job was green while eight
canvas defects shipped.

- [ ] Start the server on a free port with no API keys configured; copy the
      bootstrap key it logs
- [ ] `GET /` returns 200 and renders the canvas, with no redirect loop
- [ ] The credential gate appears; the graph request before any key returns 401
- [ ] Pasting the key loads the workflow graph
- [ ] Creating a task makes a canvas node actually change state (not merely
      "no error appeared")
- [ ] The browser tab title reads LoopWorker, not canvas
- [ ] `POST /shutdown` on the public port returns 404
- [ ] `POST /shutdown` on the admin port with an admin key returns 200
- [ ] `GET /statusz` on the public port returns 404

## Documentation truthfulness

- [ ] Every route in `pkg/api` appears in `docs/api/api-reference.md`
- [ ] `docs/QUICKSTART.md` commands run as written
- [ ] `docs/USAGE.md` — now shipped in the archive — has been re-read against
      this build. Its routes are checked automatically; its configuration example
      and the Go identifiers it names are not. Config keys are *not* silently
      ignored: an unrecognised key refuses startup, so the config example only
      has to be checked for keys that are accepted but not enforced
      (`loopworker doctor` reports those as `unapplied_keys`)
- [ ] `.release/SCOPE-PROPOSAL.md` describes this tree, not an earlier revision
- [ ] `CHANGELOG.md` describes this build
- [ ] No README claim about a CLI that is not in `cmd/` — four were deleted and
      the tables listed all six
- [ ] Dead links are impossible by construction: `relcheck` fails the build when
      a shipped document links to a file that is not itself shipped
- [ ] Documented API paths match the router: `relcheck` fails the build when a
      shipped document names an `/api/v1` path the server does not register, or
      names a path parameter under a different name than `openapi.json`

## Packaging

- [ ] The release archive contains no `web/canvas` sources, no `node_modules`,
      no scratch directories and no database files
- [ ] The archive contains `examples/hello-plugin/plugin.json` **and**
      `examples/hello-plugin/hello.wasm`, and QUICKSTART section 2's two `cp`
      commands run as written from the unpacked archive — without the demo
      plugin a tarball install cannot complete the getting-started path at all
- [ ] The Docker image runs `loopworker --help` with no errors
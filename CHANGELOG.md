# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Honesty rule for this file (and it is a rule, not a slogan):** a changelog
> line may only claim something that is verifiable in the shipped artifact.
> Overselling here is what creates support tickets for a product that has no
> support desk. `CI job preflight` in `.github/workflows/release.yml` refuses to
> publish a release whose version has no section in this file.

## [Unreleased]

Nothing here is released yet: **this repository has 0 git tags**, so no version
of LoopWorker has ever shipped. The first release is cut by a human with
`git tag -a v0.1.0-beta && git push origin v0.1.0-beta`, which runs
`.github/workflows/release.yml`. Later betas are `v0.1.0-beta.1`, `-beta.2`, …

### Added

* **The release configuration has now been executed, not just inspected.** Every
  check on `.goreleaser.yaml` so far compared the three copies of the snapshot
  command against each other, which proves they agree and says nothing about
  whether they run. `goreleaser release --snapshot --clean
  --skip=publish,sbom,sign` was run against goreleaser v2.9.0 and completed in
  21s, producing the full set: five platform archives (linux amd64/arm64, darwin
  amd64/arm64, windows amd64 zip), two `.deb`, two `.rpm`, `checksums.txt`, the
  Homebrew formula and the artifacts metadata.

  Listing the output rather than trusting the config settles claims this file
  already made. The linux tarball carries **nineteen entries — thirteen regular
  files and six directories**: the binary, `LICENSE`, `NOTICE`, `README`,
  `CHANGELOG`, `SECURITY`, `SUPPORT`, the three shipped documents,
  `config.example.yaml`, and the example plugin's two files (`hello.wasm` and
  `plugin.json`). So the attribution travels with the archive. The windows zip
  carries the same set. (An earlier draft of this line said "twelve entries",
  which counted the example plugin as one item; the file count is thirteen and
  the entry count including directories is nineteen. The corrected figures are
  what `tarfile` and the SBOM agree on.)

  Both packages were then opened and their payloads listed. The `.deb` carries
  `/usr/bin/loopworker`, `/etc/loopworker/config.example.yaml` and six files
  under `/usr/share/doc/loopworker/` — `copyright`, `NOTICE`, `README.md`,
  `CHANGELOG.md`, `QUICKSTART.md`, `USAGE.md` — which is the Debian Policy 12.5
  requirement that had been fixed earlier and only read back out of the config
  until now. The `.rpm` needed a different route: the Windows `tar` cannot read
  an RPM, `rpm2cpio` needs an install this network cannot do, and parsing the
  RPM header structure by hand does not survive contact with the real padding
  (an earlier attempt read `nindex` as 0 and wandered off into the payload).
  Skipping the header entirely works, because the payload is the only gzip
  stream in the file: decompressing from its magic and walking the cpio lists
  both `.rpm` files at nine entries each, the same set as the `.deb`, with
  `copyright` among them.

  **Not verified here:** signatures were skipped by the flags used, so
  `cosign` was not exercised — see the SBOM entry under *Fixed*, which re-runs this
  same command with the SBOM leg enabled. The macOS and Windows artifact-verification
  legs of the release workflow still need a host that can run them.

* **The task wire contract is now enforced, and it was not.** `views_task.go`
  claims "Field names are snake_case and explicit, so Go struct refactors cannot
  change the contract" — and nothing backed that sentence. Renaming one `json` tag
  would have handed every client that reads it a `nil` while the API reference
  kept documenting the old name. `TestTaskViewFieldSetIsTheWireContract` asserts
  the exact 21-key set by reflection, so an *added* key is argued about as loudly
  as a renamed one, and it makes the reference's denial of `duration`, `output`
  and `completed_at` true rather than aspirational. Mutation checked: renaming
  `priority_name` turns it red with both sets printed.

* The same technique now pins `GET /logs`. Two tests decode the response off the
  wire against the real observer and assert the entry is exactly `level` /
  `message` / `timestamp` plus an `omitempty` `fields` — the contract the
  reference previously misdescribed. Mutation checked by renaming a struct tag.

* **The splash screen's 30-second ceiling is now covered by a test.** The canvas
  had two tests for the credential probe — 401 shows the key form, a 5xx shows
  the error — and neither for the third case: a server that accepts the
  connection, takes the request and never answers. Neither a status nor a
  rejection arrives, so this is the one path where the code could have left
  "Connecting to LoopWorker…" on screen with no error and no way out but
  reloading. It could not, because `apiFetch` arms an `AbortController` at 30s
  and converts the abort into a message naming the path and the ceiling; the
  new test holds that in place with fake timers, and asserts the splash is still
  up before the ceiling (a server that has not answered yet is not an error, and
  failing early would be its own lie). Mutation checked by raising the ceiling:
  the test fails on the missing error banner.

* **Creating a node from the canvas had no test at all.** It is the one action
  the GUI exists for, and the create path — the "New Node" modal, the payload it
  builds and the POST to `/api/v1/tasks` — was exercised by nothing. That path
  already carried a fixed defect: the payload used to send `btoa(userInput)`
  with no `input_encoding`, so the server read the encoded text verbatim and the
  task ran against `aHR0cDov…` instead of the URL the operator typed, with
  nothing on screen saying so. Three tests now cover it: the request body is
  asserted field by field (so an `input` member fails the build, not production),
  a successful create closes the modal and repaints the graph rather than
  reading as a dead button, and a server refusal surfaces the server's own
  repair hint while leaving the form open. Mutation checked by reintroducing
  `btoa(userInput)`: the body assertion fails with
  `expected undefined to be 'https://example.test/hook'`.

* **The canvas was opened in a real browser for the first time.** The page loads
  from the embedded assets with **zero console warnings and zero errors**, and
  the credential gate appears for `auth: required` with copy that matches the
  server's real behaviour. That the gate appears at all is itself the
  verification: the gate is a reaction to the anonymous probe's 401, not a
  precondition, which is the design it claims to have. This also cross-checks
  the asset rebuild in a way a byte comparison cannot — a stale `index.html`
  pointing at deleted hashed assets would 404 and throw, and nothing was thrown.
  The canvas's own suite already covered the anonymous path end to end (graph
  load, live stream, node cards), and the create path is covered now; what is
  still unverified is the same UI driven by hand in a browser with a real
  credential, which is not claimed here.


  Every quoted key in all 19 `json` examples in `docs/api/api-reference.md` — 98
  distinct keys — was checked against the non-test Go source: all 98 exist. The
  audit found exactly one fiction, the `worker_id` that led to the `/logs` finding
  above, and it is gone. `GET /api/v1/tasks/{taskID}` was additionally checked
  field by field against `TaskView`: the 16 keys its example shows are all real,
  and the 5 it omits (`owner`, `error`, `config`, `metadata`, `agent_config`) are
  all `omitempty`, so their absence from a completed plain task is correct.
  Recorded here because a clean audit is only useful if it has a date on it.

* `lwerrors.ErrPortUnavailable` — the startup self-check now carries the cause
  behind a failed check instead of flattening it into the report text. A host
  that embeds the server can tell "the port is taken" (bind elsewhere and carry
  on) from "the configuration is wrong" (stop and ask the operator) with
  `errors.Is`, instead of matching an English sentence. The report operators
  read is unchanged. It is a sentinel rather than the operating system error on
  purpose: Go reports `WSAEADDRINUSE` on Windows and `EADDRINUSE` on Linux, and
  `syscall.Errno.Is` does not map between them, so the raw error is not
  comparable across the platforms this ships to.

* Four tests for the canvas's hand-written SSE frame parser (`web/canvas`), which
  exists because a browser `EventSource` cannot send an `Authorization` header.
  The existing stream test fed the whole response as a single chunk, so the three
  fragile branches were untested: a frame reassembled across network chunk
  boundaries, CRLF line endings, and a multi-line `data` field. All three are
  mutation checked — breaking the buffer accumulation, the `\r` strip or the
  line join turns exactly one test red each.
* `pkg/api/handlers_events_test.go` — six tests covering
  `GET /api/v1/events/live`, the endpoint the whole canvas live-update path
  depends on and which had no test that opened a stream. They assert the SSE
  frame format end to end, the headers an intermediary needs
  (`Cache-Control: no-cache`, `X-Accel-Buffering: no`), the `?types=` filter, the
  cross-tenant payload substitution, and that a disconnected client gets its
  limiter slot back. The release and ownership assertions were each mutation
  checked.
* `loopctl workflow get [workflow-id]`, backed by a new
  `pkg/client.APIClient.GetWorkflow`, wrapping
  `GET /api/v1/workflow/{workflowID}`. The route has been registered all along;
  it was reachable with curl and with nothing else, which for a product with no
  support desk is a guaranteed ticket.
* Release pipeline: `.goreleaser.yaml` (static `CGO_ENABLED=0` builds for
  linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 with a
  real `.exe` inside a `.zip`), deb/rpm packages, `checksums.txt` (sha256),
  SPDX SBOMs via syft, cosign keyless signatures, and a Homebrew tap formula.
* `.github/workflows/release.yml`: preflight (tag/CHANGELOG/placeholder checks),
  build+sign+publish, per-OS artifact verification (download → unpack → run →
  health → SIGTERM), multi-arch container publish to ghcr.io.
* Blocking CI quality gates in `.github/workflows/ci.yml`: `gofmt`, `go vet`,
  `golangci-lint v2.9.0` (config: `.golangci.yaml`), `govulncheck v1.8.0`,
  `go test -race ./...` across **all** packages, a 80% coverage gate
  (`-coverpkg=./...` + `.release/tools/covergate`), a Linux boot smoke test, a
  Windows boot smoke test, a Docker boot/health/SIGTERM smoke test, Trivy image
  scan, DCO sign-off enforcement, `actionlint`, and third-party license
  generation via `go-licenses`.
* `.release/tools/relcheck` — fails the pipeline when go.mod, the Dockerfile,
  CI, `.goreleaser.yaml` ldflags, `.dockerignore` embed inputs and `LICENSE`
  disagree with each other. Stdlib-only, so it runs on Windows too.
* Windows-first build path: `.release/build.ps1` (build/test/race/lint/coverage/
  smoke/release tasks) and `.release/smoke.ps1` (Ctrl+Break → SIGTERM path).
  The Makefile requires bash; PowerShell does not.
* New image: multi-stage `Dockerfile` with a Go builder that matches go.mod,
  `CGO_ENABLED=0`, non-root `USER 10001:10001`, `/data` as the only writable
  path, and a real `HEALTHCHECK` that GETs `/api/v1/health`.
* Licensing/governance: `NOTICE` (attribution obligations for statically linked
  Go binaries), `SECURITY.md` (embargo contact + supported-version table),
  `SUPPORT.md` (explicit self-service-only boundaries), `.github/CODEOWNERS`,
  `.github/dependabot.yml`, `CODE_OF_CONDUCT.md`, DCO in `CONTRIBUTING.md`.
* `.release/SCOPE-PROPOSAL.md`: what should be cut from the shipped product and
  why, for human approval. (Its dev-CLI items have since been executed — see
  **Removed** below; nothing was deleted when it was written.)
* Frontend test harness for the canvas (`vitest` + `@testing-library/react`, 15
  cases): node cards reading the API's lower-case JSON keys, the payload
  encoding contract, the live event stream, the credential gate and request
  timeouts. The config lives in the existing `vite.config.js` rather than a
  second config file.
* `.github/workflows/ci.yml` `canvas` job. The workflow had **no npm step at
  all**, so the canvas could ship broken with CI green. It now runs `npm ci`,
  lint, the tests and the build, and fails when the committed `pkg/api/dist`
  (embedded by `go:embed`) differs from the build output.
* `pkg/security/authenv_test.go` — the parser behind `LOOPWORKER_API_KEYS`,
  `LOOPWORKER_API_KEYS_PLAIN` and `LOOPWORKER_AUTH_TOKEN_TTL` had no test that
  named it, even though those variables are how the Docker image and both smoke
  scripts configure credentials. Every rejection message it can print — wrong
  field count, empty id, unknown role, a plaintext key in the hashed variable, a
  truncated or non-hex digest, a trailing comma — is text a customer reads while
  setting up their first server. The tests also pin that a plaintext key is
  hashed on the way in and that a rejected environment yields no usable keys.
* `pkg/security/streams_test.go` — `ConcurrencyLimiter`, the per-caller and
  global ceiling that bounds simultaneously open SSE streams, is reached through
  the API tests but nothing exercised its refusal paths or the contract around
  releasing a slot. The tests now cover both ceilings, the unknown-caller
  refusal, the clamped constructor, and that `release` is idempotent: calling it
  twice would otherwise drive the counter negative and hand one caller more
  streams than its limit allows. That last assertion is mutation checked —
  removing the `sync.Once` guard makes exactly that one test fail with a total
  of `-2`.
* **Release archives now carry `examples/hello-plugin`** — its `plugin.json` and
  a working `hello.wasm`. `docs/QUICKSTART.md` section 2 is the first thing a new
  user does and it copies that directory into `~/.loopworker/plugins`, but the
  archive did not contain it, so every install that started from the tarball (the
  documented install path) stopped at the plugin step with no way forward except
  cloning the repository. The shipped module is byte-identical to
  `pkg/plugin/testdata/hello.wasm`, which the test suite executes through a real
  WASM runtime, so what a customer runs is what was tested. Archives grow from
  roughly 7.6-8.6 MB to 8.8 MB.
* `.release/tools/relcheck` gained three checks: every source path named by
  `.goreleaser.yaml` exists in this repository; tool versions pinned in one
  workflow match every other workflow that pins them; and the snapshot command
  stated by `.release/build.ps1`, `release.yml` and the `.goreleaser.yaml`
  header are identical.

### Removed

* **Four developer CLIs: `loopbench`, `loopsim`, `loopdebug`, `loopwatch`.**
  `cmd/` now contains only `loopworker` (the product) and `loopctl`. None of the
  four shipped (`.goreleaser.yaml` builds `./cmd/loopworker` only), none had a
  test file, and none measured the product: `loopbench` and `loopsim` never made
  an HTTP call — they `time.Sleep` locally and print a table;
  `loopwatch --metrics`/`--logs` called `/api/v1/metrics` and `/api/v1/logs`,
  which are not routes (`pkg/client.GetMetrics`/`GetLogs` are marked
  `Deprecated:` for exactly that reason), and its `--health` poll did what
  `loopctl status` already does; `loopdebug diagnose` duplicated `loopworker
  doctor`, `loopdebug profile` measured its own process instead of the server,
  and half its commands were byte-identical duplicates (`task inspect`/`task
  trace`, `workflow inspect`/`workflow trace`). Recover any of them from history
  with `git show <rev>:cmd/<name>/main.go`.
* `Makefile` `DEV_CLIS` and `.release/build.ps1` `$DevClis` now list only
  `loopctl`; `.github/CODEOWNERS` no longer claims the deleted paths. The
  coverage floor (`SHIPPED_PKGS = ./cmd/loopworker,./cmd/loopctl`) is unchanged,
  and `.release/tools/relcheck`'s cmd-directory check passes because the build
  scripts no longer name directories that do not exist.

### Fixed

* **The runbook promised a release gate that was not looking at two of the files
  it named.** `.release/RELEASE-PROCESS.md` §1 tells the human cutting a release
  that `preflight`'s `relcheck -strict` fails while any of seven placeholder
  categories remain, and lists them in a table: the copyright holder, the
  security contact, **the CoC enforcement contact (`CODE_OF_CONDUCT.md`)**, the
  tap owner, the CODEOWNERS handles, **repo URLs in `SECURITY.md` /
  `SUPPORT.md` / `CONTRIBUTING.md`**, and the Go module path.

  `checkPlaceholders` opened six files. `CODE_OF_CONDUCT.md` and
  `CONTRIBUTING.md` were not among them, and both were carrying real
  placeholders — two hits each. So a maintainer could follow the runbook to the
  letter, satisfy everything the gate actually inspected, tag, and publish a
  repository whose Code of Conduct names no enforcement contact and whose
  contributing guide points at `TODO(owner)`. For a product that tells people to
  open issues, that is precisely the ticket it cannot answer. Both files are now
  scanned; the release gate blocks on either, and the PR gate still lets them
  through as warnings, which is the split `-strict` exists to make.

  The same sweep found a third file the gate does not read:
  `.github/dependabot.yml`, whose `TODO(owner)` asked the owner to open a PR for
  grouped weekly updates that the config directly below it had been doing all
  along. That one is a stale note rather than a shipping placeholder — Dependabot
  config is in no customer's hands — so it was corrected rather than gated.

  Warnings on the real tree went from three to five, which is the point: the two
  extra ones are placeholders that were always there and nothing was counting
  them.

* **Every release would have failed while generating its SBOM.** `.goreleaser.yaml`
  set `SYFT_FILE_METADATA_SELECTION=inputs`. That is not one of syft's four legal
  values — `all`, `owned-by-package`, `none`, `""` — and it is rejected in
  `cmd/syft/internal/options/file.go:59-63`. Because the check is a `PostLoad` hook
  on the tool's own configuration, the rejection surfaces late: goreleaser builds
  all five binaries, all five archives, both `.deb` and both `.rpm`, and only then
  dies at "cataloging artifacts", leaving a full `dist/` and nothing published.

  This was reproduced rather than inferred. With syft v1.9.0 on `PATH` (the version
  both workflows pin) the real command failed after 36s:

      7 cataloging artifacts: syft failed: exit status 1:
             invalid application config: invalid file metadata selection: "inputs"

  With the value corrected to `all` the same command completes in 51s and writes
  five `.spdx.json` documents.

  The value is not only a matter of being legal. Measured on the linux-amd64
  archive with syft v1.9.0: `all` lists **19** file entries, syft's default
  `owned-by-package` lists **1** — the bare binary. `LICENSE`, `README.md`,
  `CHANGELOG.md`, `NOTICE`, `SECURITY.md`, `SUPPORT.md`, `config.example.yaml`,
  `docs/**` and `examples/hello-plugin/**` are not Go packages, so the default
  makes every non-binary file in the release invisible to anyone auditing it. The
  package count is 28 either way; only the file inventory widens.

  Nothing static could have caught this. `goreleaser check` validates the schema
  of `.goreleaser.yaml` and never invokes syft; `release.yml`'s dry-run path passes
  `--skip=publish,sbom,sign`; and the publish path has never executed, because the
  repository has 0 git tags. `ci.yml` made it worse by declaring
  `SYFT_VERSION: v1.9.0` and never installing syft at all, so reading the workflow
  said the tool version was under control when nothing in CI fetched the tool.

  Three changes follow from that:

  - **A CI job runs the step for real.** `release-sbom` installs syft, runs
    `goreleaser release --snapshot --clean --skip=publish,sign` with the SBOM leg
    enabled on every push and PR, and then checks each generated SBOM against the
    archive it describes: every real (non-directory) member must appear in the
    document, and none may carry an all-zero digest. It deliberately does not use
    syft's own `fileTypes` to tell directories from files. The first version of
    this check did, and its negative case caught the hole: when syft cannot read
    file contents it marks *every* entry `OTHER`, so a "skip directories" rule
    built on that field skips every file and the zero-digest check never fires —
    it is inert in exactly the case it exists for. It now walks the archive's own
    member list instead.
  - **`relcheck` gained an unreferenced-pin rule.** A `*_VERSION` pin that nothing
    in the same file reads is an error. The search is per file because workflow
    `env:` is file-scoped, so a reference in the sibling workflow is not a consumer.
    Five self-tests cover the dead pin, the cross-file near-miss, a longer name
    built on a pin name (`SYFT_VERSION_SHA` must not satisfy `SYFT_VERSION`),
    the positive case in both consumption forms, and a pin carrying a trailing
    comment. Mutation verified on the real tree: replacing
    `"${SYFT_VERSION}"` with a literal version turns CI red with the pin named.
  - **`ci.yml`'s trivy version is a pin.** It was the only tool installed with an
    inline `v0.75.0` while the other seven were named pins, so it was the one
    version nothing could check for agreement.

  Building the pin rule turned up a hole in an existing one. The pin regex
  required the value to end the line, so `SYFT_VERSION: v1.9.0 # pinned by
  security review` did not match at all — a pin relcheck cannot see is a pin it
  can no longer keep consistent between workflows, and the drift check was skipping
  it in silence. The regex now tolerates a trailing comment, and a comment is not
  treated as a consumer of its own pin.

  **Not verified here:** the `release-sbom` job has not run on GitHub — it runs on
  the next push. Its checker was exercised locally against real documents: a
  syft-generated SBOM passes, and an SBOM with zeroed digests, an SBOM built with
  the default selection, and a missing SBOM each fail it. cosign signing is still
  unexercised, and needs keyless OIDC. One further finding is recorded here
  because it will bite anyone reproducing this on Windows: syft v1.9.0 on
  windows/amd64 emits an all-zero SHA1 for every file, so a *local* Windows
  snapshot produces an SBOM whose file digests are unusable. Releases are built on
  `ubuntu-latest`, where the digests were verified correct against known content.

* **The checksum gate no longer depends on an interpreter nobody had measured.**
  `verify-artifacts`'s "sha256 must match checksums.txt" step shelled out to
  `python3`, in a step whose shell is `bash`, on a job that runs on
  ubuntu-latest, macos-latest **and windows-latest**. Whether `python3` — as
  opposed to `python` — resolves on the Windows runner image could not be
  confirmed here, and it is not the kind of question that can be settled by
  reading a workflow file. Rather than leave an unmeasured dependency in the
  release gate, the step now needs nothing beyond the shell: `awk` to read
  `checksums.txt`, and `sha256sum` or `shasum` to hash, with a named error if
  a runner turns out to have neither.

  This is deliberately **not** written up as "the step was broken". The premise
  was never established; what is established is that the dependency was
  unmeasured, and that is enough reason to remove it. The comparison logic is
  unchanged, including the two things goreleaser's `checksums.txt` format
  requires: a binary marker (`*`) may precede the filename, and the name may
  carry a directory prefix, so the match is on the basename. A first cut that
  just took the second whitespace-separated field would have passed every
  archive this repository has actually produced and still been wrong.

  The committed text was extracted from `release.yml` and executed in Git Bash
  against real goreleaser output — testing a retyped copy would have tested the
  copy — across six cases: the three archives the matrix actually selects
  (linux-amd64, darwin-amd64, windows-amd64, all matching), a name
  `checksums.txt` never mentions, a deliberately wrong hash, and a
  `checksums.txt` written in BSD binary mode with a directory prefix. The
  `shasum` branch, which is the one macOS takes, was then forced on a host that
  only has `sha256sum`, by rebuilding a PATH in which `sha256sum` is provably
  absent; it verified against the real darwin-amd64 archive.

  That last case needed a note worth keeping: the sandbox could resolve
  `basename` with `command -v` and then fail with exit 127 when it ran, because
  Git for Windows' coreutils are Windows executables that load their runtime
  from the executable's own directory. A failure that looks like "command not
  found" for a command that demonstrably exists is worth an hour of anyone's
  time, and it is why the branch was tested at all.

  **Not verified here:** the step has still never run on GitHub. What is verified
  is its logic, on real data, including the hasher macOS would use. Running the
  shipped binary on macOS and Windows remains unverified — that needs a host.

* **The job that proves a customer can install the product was compiling Go
  with whatever the runner image happened to ship.** `verify-artifacts` in
  `release.yml` is described in its own comment as the zero-after-sales test: it
  downloads nothing but the artifact, unpacks it, runs it, requires
  `/api/v1/health`, and requires a clean SIGTERM shutdown. Before it can run any
  of that, `.release/scripts/boot-smoke.sh:146` builds `examples/hello-plugin`
  for `wasip1/wasm` — and that job was the only one in the repository with no
  `actions/setup-go` step. Every other job, including the `docker-publish` leg
  that greps the version out of `go.mod`, derives its toolchain from `go.mod`.
  This one used the ambient image, on the leg whose entire job is to trust the
  artifact.

  It stayed invisible because the existing Go-version check reaches its verdict
  **per file, not per job**: `release.yml` contains `go-version-file: go.mod` in
  the `release` job, so the file passes, and `verify-artifacts` is along for the
  ride. That is the same shape of mistake as the pin regex earlier in this
  release — a check whose unit was too coarse to see the thing it was written
  for.

  Two changes:

  - **`verify-artifacts` now declares its toolchain**, with a comment saying why
    the job compiles Go at all, since that is not visible from the YAML.
  - **`relcheck` gained `checkGoToolchainPerJob`**, which decides per job. It
    also takes a second hop, and the second hop is not optional: the `go build`
    lives in `boot-smoke.sh`, not in the workflow, so a check that read only the
    YAML would classify that job as one that runs `bash` and nothing else. The
    check follows references to tracked scripts under `.release/` and asks
    whether *those* invoke Go. Five self-tests cover the script hop, the inline
    case, an explicit `go-version:` literal counting as a declaration, the
    near-misses, and the negative of the second hop.

    The near-misses are where this rule earns its place, because a gate that
    fires on a job which only asks the toolchain a question gets switched off.
    `go env` is exempt — it proves `go` is on `PATH` and compiles nothing.
    `go-licenses` is exempt because the hyphen makes it a tool name, not a
    subcommand. `go install` is **not** exempt: it builds a binary from source
    and is exactly as version-sensitive as `go build`, even though none of the
    product is compiled. A first draft of the rule included `go env` and
    `go install` on the same side of that line, and a self-test caught it.

  Mutation verified against the real tree twice, including after the subcommand
  list was narrowed: removing the `setup-go` step turns the release gate red
  with the job and the script named.

  **Not verified here:** this job has still never run on GitHub. What changed is
  that when it does, it will compile with the Go version `go.mod` declares.

* **The release gate rejected the tagging scheme its own header documents, and
  the version pattern was wrong in three separate ways.** `release.yml`'s header
  says later betas are `v0.1.0-beta.1`, `v0.1.0-beta.2`. Preflight's semver check
  accepts them — correctly, they are legal pre-release tags — and then its
  CHANGELOG check blocks them, because the changelog carries one heading,
  `## [0.1.0-beta]`. Replayed against this repository: `v0.1.0-beta` passes,
  `v0.1.0-beta.1` is refused with *"CHANGELOG.md has no section for
  v0.1.0-beta.1"*. Following the documented procedure therefore fails at the last
  step, after the tag has been pushed.

  The pattern behind that check was
  `grep -qE "^## \[?${v}\]?"` — the version interpolated raw, and unanchored.
  Three consequences, each verified against a fixture before being fixed:

  - **`.` was a wildcard.** A changelog documenting nothing but
    `## [0x1y0-beta]` satisfied the gate for the tag `v0.1.0-beta`.
  - **The prefix matched either way.** Tagging `v0.1.0-beta` passed on a heading
    written for `v0.1.0-beta.1` — a release shipping with no notes of its own,
    which is the precise failure the step exists to prevent.
  - **`+` is a repetition operator in ERE.** `v1.2.3-rc.1+build` is a legal semver
    tag and could never pass, at all, for any changelog content.

  The version is now escaped before it becomes a pattern, and the pattern is
  anchored so the version must be followed by `]`, whitespace, or end of line.
  The committed text was extracted from `release.yml` and replayed across eleven
  cases: the three above, the unbracketed `## 0.1.0-beta` form Keep a Changelog
  does not use but the gate has always allowed, a version at end of line,
  `## [Unreleased]` correctly not counting as a version, a plain release, and
  this repository's real changelog read both ways.

  The header comment now says what the gate requires — one heading per tag,
  later betas included — because the gap between the two is what made the scheme
  look broken rather than unfinished.

  No new `relcheck` rule was added for this. The check only runs on a tag push,
  so a rule that runs on every PR would catch the mistake earlier, but no such
  drift has happened yet: the changelog does have a section for the version the
  header names. Adding a gate for a mistake nobody has made is how this
  repository ends up with gates that are switched off.

* **The Windows half of the zero-after-sales test had never run, and the two
  halves of it had drifted apart.** `.release/smoke.ps1` is what
  `verify-artifacts` runs on `windows-latest`; `.release/scripts/boot-smoke.sh` is
  what it runs on Linux and macOS. Reading them side by side turned up two
  things.

  **`smoke.ps1` never randomised the admin port.** `boot-smoke.sh` has always
  taken a free one and explained why in a comment. The Windows script grabbed an
  ephemeral port for the API and left the admin listener on its fixed default,
  19528 — while its own comment claimed the ephemeral port existed "so parallel
  jobs never collide". Half of that claim was true on exactly one of the two
  platforms. A collision there is not fatal (`pkg/server/server.go` degrades the
  admin listener to a warning rather than taking down a server whose API port is
  free), so this was never a red build: it was a run that quietly lost
  `/metrics` and `/runtime/stats`, and a comment asserting a safety property the
  script did not have.

  **`boot-smoke.sh`'s comment stated a behaviour the code had stopped
  guaranteeing.** It said the server "refuses to start" without a free admin
  port. That was true once, and stopped being true when the degradation was
  introduced; `server.go` now says the opposite in as many words. The advice —
  take a free port — is still right, so only the justification was corrected,
  with a pointer to the code that superseded it. This is the same defect class as
  a `/logs` endpoint documented as live: a comment outliving the behaviour it
  describes, in a file a support engineer would read while diagnosing something.

  Establishing the four environment variable names the Windows script relies on
  was done by hand, and it is the reason there is now a gate. A name that drifts
  out of the config registry does not fail at startup — the server keeps its
  default — so the script polls the port it chose while the server listens on
  19527, and reports *"GET /api/v1/health did not return 200"*. That is a
  conclusion about the artifact that has nothing to do with the artifact.
  `relcheck`'s `checkSmokeScriptEnvVars` asserts every `LOOPWORKER_*` name a
  shipped smoke script assigns is one the product's non-test sources read. The
  registry is derived from the code rather than restated, because a hand-kept
  list is one more thing to forget and a false positive in a release gate is
  worse than no gate. Assignments only: `${LOOPWORKER_SMOKE_API_KEY:-default}` is
  the script reading its own input, not handing the server a variable. Four
  self-tests, and mutation verified on the real tree by renaming the admin
  variable — the finding names the file, the line and the variable.

  **Not verified here:** neither smoke script was executed. `smoke.ps1` launches
  the built binary, so under the current constraint it was only parsed (AST, 949
  tokens, clean) and `boot-smoke.sh` only syntax-checked with `bash -n`. What is
  verified is that the admin port is now chosen the same way on both platforms,
  and that every variable name involved is one the product reads.

* **A 4 MB compiled binary was sitting in the source tree.** `.release/tools/`
  builds a `relcheck.exe` next to its own `main.go` whenever anyone runs a bare
  `go build` inside the package directory. `git status` cannot see it, because
  `.gitignore` ignores `*.exe` — which is the correct rule, and the reason the
  file was invisible rather than the reason it was allowed to stay. The
  repository's own convention, used by every CI step, is `go run ./relcheck`,
  which leaves nothing behind. The file is gone and `.gitignore` now says why:
  a rule that hides build droppings should also say how to avoid producing them.
  The two `hello.wasm` files in the tree were checked at the same time and are
  *not* droppings — both are tracked assets, and both still hash to the recorded
  `1af46AB9…5CC8A`.

* **`relcheck` gained an admin-listener documentation gate.** The two shipped
  documents had drifted apart in opposite directions and neither noticed:
  `docs/USAGE.md` listed `/statusz` and `/shutdown` on the admin listener, and
  `docs/api/api-reference.md` — the document that defines response contracts —
  listed neither. The gate is one-directional on purpose, and the first version
  of it proved why: "some shipped document names this route" **passes on exactly
  that tree**, because `USAGE.md` is still there saying the same thing. So the
  rule is that the contract document must cover every route
  `pkg/api/admin.go` registers, while other documents are free to document more.
  `/healthz` is the case that keeps it from being noisy: it is served on both
  listeners and only the reference lists it on the admin one. Documented methods
  are checked against the registrations too, because a client built from a
  document that says GET where the server serves POST gets a 405.

  Building it turned up a defect in the check itself, which the self-tests
  caught: a single regex expected the method column before the path, so it
  matched `api-reference.md`'s `| GET | /logs |` and silently matched nothing in
  `USAGE.md`'s `| /logs | GET |`. It was reporting that it had read the whole
  tree while reading half of it. Row parsing is now cell-by-cell, and the fix
  is visible in the gate's own output — the row count went from 13 to 15, the two
  extra rows being the `USAGE.md` entries. Four self-tests cover the missing
  contract entry, the method mismatch, the legitimate one-sided documentation, and
  an unparseable route table (which must fail rather than pass quietly). Both
  mutations verified against the real tree: deleting the two rows from the
  contract document, and flipping `/shutdown` to GET in the guide.


  endpoint that breaks the API's response shape.** Two shipped documents
  disagreed: `docs/USAGE.md` listed `/statusz` and `/shutdown` on the admin
  listener, `docs/api/api-reference.md` — the document that defines response
  contracts — listed neither. The reference now carries both, with a section for
  `/statusz` and its fields.

  The shape is the part worth stating: `/statusz` returns a **bare object**,
  not the `{"success":…,"data":…}` envelope every other JSON endpoint on this
  server uses. A customer who wrote one unwrapping helper from the rest of the
  reference got `undefined` here, with nothing to tell them why. `uptime` is
  likewise a Go duration string rather than a number, which is the second thing
  that breaks a generic parser. Both are now in the reference, and two tests
  pin them: the response must carry the documented keys at the top level and
  must *not* carry `data` or `success`, and `components` must name all five
  subsystems. Mutation checked by wrapping the response in the envelope, which
  is exactly the "fix" that would have broken every existing caller.

* **`GetStatus` reported a saturated uptime for a server that had not started.**
  `time.Since` from the zero time is `2562047h47m16.854775807s` — twenty-three
  digits that read like a measurement. `GetStatus` is exported, so a host that
  asks before `Start()` got a number that looks real and is meaningless; inside
  the running server the field is correct, because `Start` sets `startTime`
  before the admin listener is ever bound. It now reads `not started`, which
  cannot be misparsed as a quantity.

* **The 403 an integrator actually receives was barely tested.** A viewer calling
  a write endpoint is the commonest rejection this server produces, and the
  response carries the refused role, the missing permission and — in
  `details.permissions_for_role` — what that role *can* do, which is the answer
  to "so what may I call with this key?". The only assertion on any of it was
  `strings.Contains(message, "Fix:")`, which passes on almost any string. Two
  tests now pin the contract: the message must name the refused role and the
  permission it reports in `details`, and the details must carry `role`,
  `required_permission` and a non-empty `permissions_for_role`; a 401 must name
  both accepted header forms, since that string is all a customer wiring up
  their first request has to go on. Mutation checked by deleting the details
  map, which is the regression a client surfacing role advice would feel first.

  Reading this turned up three separate 403 texts in the tree — the one
  `RequirePermission` writes, one in the security package's error classifier,
  and the registered catalogue in `pkg/api/errors.go` — which look like they
  disagree about what to advise. They do not disagree in practice, and the
  reason is worth writing down: only `RequirePermission` can produce a 403 at
  all. `AuthenticateRequest` returns only `ErrTokenInvalid` or
  `ErrTokenExpired`, so the classifier's `ErrForbidden` arm is unreachable and
  the registered catalogue's entry is never rendered for this path. A customer
  who gets a 403 always gets the message that names their role and the missing
  permission.


  remedy cannot work.** Key revocation does withdraw the token — every
  verification re-resolves the token's subject against the key table, and a key
  that is gone fails it — but the failure was reported as expiry. `TOKEN_EXPIRED`
  tells the reader to "request a fresh bearer token, or install a permanent API
  key through `LOOPWORKER_API_KEYS`", and the key in question is the one that was
  just revoked: an operator working an incident is told to go back to the
  credential they just withdrew. An absent key and an expired key are now
  reported apart — the first as `TOKEN_INVALID`, whose documented meaning already
  covers "unknown, revoked or malformed", the second still as `TOKEN_EXPIRED`.

  The mechanism was also invisible where a maintainer would look first:
  `RevokeKey`'s comment claimed it invalidated bearer tokens, but the function
  never touches the revoked set — the withdrawal happens at verification time,
  because nothing records which tokens a key minted. Reading only `RevokeKey`
  makes the code look like it contradicts its own comment, which invites
  "fixing" the wrong side. The comment now says where the work is done and why
  it cannot be done there.

  A test drives the whole path through the API: issue a key, exchange it for a
  token, prove the token works, revoke the key, then require that the key is
  refused *and* the token is refused *and* the refusal names `TOKEN_INVALID`.
  The "works before revocation" step is there so a later rejection is provably
  the revocation's doing. Mutation checked by folding the two cases back
  together, which reproduces `TOKEN_EXPIRED` and turns the test red.

* **`GET /logs` on the admin listener answers `{"logs":[]}` in production, and the
  reference documented a payload that cannot occur.** The ring buffer behind that
  endpoint has exactly one writer — `observer.Observer.Log` — and no non-test code
  in the repository calls it. `o.logs` is appended in that method and nowhere else;
  application logging goes straight to `pkg/logger`'s zap and never passes through
  the observer. The observer itself *is* wired in production (`pkg/server/server.go`
  constructs it and hands it to the API server), so the endpoint answers 200 with an
  empty array rather than 503: the wiring is present, the writer is not.

  The documentation made this worse by promising the opposite. The API reference
  showed a populated entry whose `fields` map carried `task_id` and `worker_id`;
  `worker_id` appears in **no** Go file in the repository, and `fields` has no
  agreed key names at all, because its keys are whatever the caller of `Log()` chose
  and there are no callers. An operator parsing `event.fields.worker_id` would have
  read `null` and filed it as a defect.

  The prose now states that the array is always empty, gives the reason, and points
  at the process's own stdout/stderr for logs, `/runtime/stats` for run state and
  `/metrics` for counters. `docs/USAGE.md`, `docs/guides/quick-start.md` and the
  OpenAPI summary said the same false thing and were corrected with it. Two tests in
  `pkg/api` now decode the response off the wire against the real observer and pin
  the entry to exactly `level` / `message` / `timestamp` plus an `omitempty` `fields`
  map — mutation checked by renaming a struct tag, which turns both red. Wiring
  logging into the ring is a product decision (memory, volume, what may appear in
  `fields`) and is left to the owner.

* **The reference told clients to handle two error codes that can never arrive.**
  Both were reachable only through `classify()`, so neither showed up in a grep
  for a producer of its `Code` constant. `TASK_ALREADY_EXISTS` was the worse of
  the two: its `Fix` text said "reuse the existing task id", but
  `POST /api/v1/tasks` has no `id` field at all — `CreateTaskRequest` and
  `CreateTaskFields` both omit it, and the server allocates the id — so two
  submissions can never collide and the remedy's first half is not something a
  client can perform. It also means **task creation has no idempotency key**:
  a POST that times out client-side but succeeded server-side leaves the client
  unable to tell whether to retry, and retrying yields a second task rather than
  the 409 it was promised. That consequence is now written down instead of left
  to be discovered. `QUEUE_FULL` was folded into the `SERVICE_UNAVAILABLE` row as
  "含 `QUEUE_FULL`", reading as a 503 variant that the code never produces;
  `SERVICE_UNAVAILABLE` now names what actually raises it (the scheduler is down,
  or a required subsystem is missing) and carries the remedy its row was
  missing. Together with `GRAPH_TOO_LARGE`, which the reference already handled
  honestly, all three unreachable codes are now listed in one place with an
  explicit "do not write a branch for these".

* **`relcheck` gained an error-code reachability gate.** It judges every
  `ErrorCode` constant two ways, because errors reach clients two ways: named
  directly in an `ErrorBody` (which is how `RATE_LIMITED` is emitted, so a
  sentinel-only reading calls that live code dead) and through `classify()`,
  which maps a returned `lwerrors` sentinel onto its registered code. A
  documented table row naming a code no path can emit is an **error**; an
  unreachable code no shipped document mentions is a **warning**, so it passes
  the PR gate and blocks `release.yml`'s `-strict` run. Five self-tests cover
  both emission paths, the warning, and the prose that legitimately names an
  unreachable code. Getting the gate to agree with a hand measurement took two
  corrections, both recorded in the code: gofmt aligns the constants, so a
  declaration recognised by a single-space prefix matches nothing and every code
  then counts as reachable through its own declaration; and the sentinel's own
  declaration is not a producer, which is what first made `RATE_LIMITED` look
  reachable only via a producer that did not exist.

* **The `.deb` and `.rpm` shipped a binary and no licence text.** Inspected
  rather than assumed: between them the two packages contained exactly three
  entries — `/usr/bin/loopworker`, `config.example.yaml` and the data directory.
  No `LICENSE`, no `NOTICE`, no `CHANGELOG`, no documentation, and no example
  plugin. A deb with no copyright file is a Debian Policy 12.5 violation that
  `lintian` rejects, so anyone who installed by package manager was
  redistributing without the attribution the Apache-2.0 dependencies require.
  Both packages now install `copyright`, `NOTICE`, `README`, `CHANGELOG`,
  `QUICKSTART` and `USAGE` under `/usr/share/doc/loopworker/`.
* **The container image shipped the binary and no licence text either** — the
  same defect on the third channel. The runtime stage copied the executable and
  nothing else, so anyone who pulled the image from a registry held exactly the
  unattributed redistributable the packages were. It now copies `LICENSE`,
  `NOTICE` and `README.md` to `/licenses/`, confirmed by listing the files
  inside the built image rather than by reading the Dockerfile back.
* **The Trivy image scan had never been run, and it passes.** It is a security
  gate on every push that had never produced a single result here, so "the
  pipeline is green" said nothing about it. Run against a locally built image
  with the same `v0.75.0` pin and the same flags: **0 vulnerabilities and 0
  secrets**, exit 0, across both the alpine package set (3.22.6, 27 packages)
  and the Go binary itself. The command carries `--ignore-unfixed`, so a clean
  result can also mean "the findings had no fix". Re-run without that flag and
  at every severity, both targets come back genuinely empty — the green is not
  the flag hiding anything.
* **The Trivy step was named after a threshold it does not use.** The step read
  "fail on CRITICAL" while the command has failed on `CRITICAL,HIGH` since the
  job was written. A gate that is stricter than its name, in a repository whose
  entire premise is that a name must not overstate or understate what runs.
* **The DCO check had never been run either**, and it is correct. Extracted
  verbatim from the workflow and executed against real repositories: an unsigned
  commit is rejected with the full SHA and the remediation heredoc intact (exit
  1), an all-signed branch passes (exit 0), and a genuinely unsigned two-parent
  merge commit passes — which is what `--no-merges` is for. Two apparent
  failures during this check were faults in the test rig, not the gate: piping
  the script through `Select-Object -First 1` truncated the pipeline and reset
  `$LASTEXITCODE` to 0, and `git rev-list --count --parents` prints a count
  rather than the parents, which briefly looked like the merge had not happened.
* **The YAML parse step could pass without running.** It does
  `sys.exit(0)` when PyYAML is missing, printing nothing — and whether PyYAML is
  preinstalled is a property of the GitHub runner image, not of this repository.
  Whether or not that is true today, a step that prints nothing and passes reads
  as "the YAML was checked". It now emits a warning saying it was skipped. The
  files are still really validated by the steps around it (actionlint parses
  every workflow, `goreleaser check` parses `.goreleaser.yaml`, golangci-lint
  refuses to start on a broken `.golangci.yaml`), so this is a redundant early
  signal and never the only one.
* **`QUICKSTART.md` quoted a startup error the product cannot print.** The
  bind-refusal line read `the API would listen on 0.0.0.0:19700 using only an
  ephemeral bootstrap key`. Everything around it was right — `main.go` really
  does print `loopworker: %v` and `FieldError.Error()` really does render
  `field: reason` — so the line passed for a genuine terminal transcript. But
  `19700` appears nowhere in this repository; the default is `19527`. A customer
  who bound `0.0.0.0` on the default port would see `19527` and be unable to
  match it against the error they had just been shown. The port is now `19527`,
  with a line saying the value shown is the one you actually bound.
* **That class of drift now has a gate.** `relcheck` gained
  `checkDocumentedPorts`: every `address:port` in a shipped document must use a
  port the server actually binds by default, and the allowed set is *read* from
  `config/config.example.yaml` rather than hardcoded, so moving the defaults
  moves the rule. Eight self-tests, mutation checked. Two mutations, each caught
  by a different test: widening the pattern to a bare `:<digits>` (which turns
  `09:30` and `{"retries": 3}` into findings), and skipping fenced blocks the way
  the link check does — the line that shipped with the wrong port was a quoted
  transcript *inside* a fence, which is the shape a reader trusts most. That
  second mutation was caught only after the first attempt at it turned out to
  skip the fence markers without skipping what was between them, which is the
  usual way a mutation test passes for the wrong reason.
* **The first real run of that gate reported a false positive, on this entry.**
  A changelog has to be able to quote the wrong value in order to record that it
  was fixed, so `CHANGELOG.md` is exempt — the same exemption the documented-route
  check has had all along, for the same reason. Reading a historical record as a
  specification is the mistake this repository keeps making in other places.
* **`QUICKSTART.md`'s CLI table omitted `loopctl version`.** Not a false
  statement, just an incomplete reference for a table titled "what it actually
  does".
* **`QUICKSTART.md`'s configuration table named a key the server rejects.** The
  row read `| log.level | LOOPWORKER_LOG_LEVEL | info |`. The registry entry is
  `logging.level` — the flat alias is `log_level`, not a `log.` section — so
  `internal/config/load.go` rejects the key outright, prints the accepted list
  and a did-you-mean hint, and exits. The environment-variable column in that
  same row was correct, which is what made the table read as entirely
  trustworthy; a customer copying the file-key column got a hard startup failure
  whose own error text pointed back at the table that caused it. Found by
  cross-checking every dotted key in the shipped documents against
  `internal/config/spec.go`. The other 6 keys in that table, and all 21
  `LOOPWORKER_*` variables named anywhere in the shipped documents, are real.
* **`docs/test-report.md` contradicted itself and had gone stale.** Its own
  header said "This document does not duplicate numbers" and then, twelve lines
  later, listed 855 test functions, 22 packages, 80.3% coverage and a per-package
  table. The numbers were wrong: the tree now carries 911 test functions and
  measures 84.2%. It also listed `cmd/{loopbench,loopdebug,loopsim,loopwatch}`
  among the packages with no tests — those binaries were deleted, as
  QUICKSTART.md and this changelog both say — and gave per-package timings that
  predated the `-race` memory fix. The numbers are gone rather than refreshed,
  because the document's own stated principle is the right one: a fact written
  down is stale the day after the next commit. It now carries only the commands
  that produce those numbers, each one checked against CI and the Makefile
  (`-timeout 20m` matches `ci.yml:201`; `CGO_ENABLED=1` on the coverage command
  matches `make cover`, which is *not* the release setting and is now called
  out), plus the two timing facts that are actionable rather than numeric — that
  `pkg/plugin` was made fast by not compiling a 2.5 MB wasm module in tests that
  only load it, and that `pkg/security` is slow because bcrypt's cost is
  deliberately high.
* **The API reference's error table understated one row.** It listed
  `INVALID_REQUEST` as `400`, but `ErrWorkflowInvalid` is registered as
  `422 UnprocessableEntity` (`errors.go:119`) while a JSON type mismatch is
  `400` (`errors.go:188`) — one code, two statuses. A client written as
  `if status == 400` silently misses the workflow-validation case. The row now
  says `400 / 422` and tells the reader to switch on `error.code`, which is the
  field that is actually unambiguous. The default-code path in `classify`
  (`errors.go:164`, where a `FieldError` with no `Code` becomes
  `INVALID_REQUEST` while keeping its own status) is currently unreachable: all
  54 `FieldError` literals in non-test source set an explicit code.
* **`WORKER_NOT_FOUND` and `PLUGIN_NOT_FOUND` were missing from that table**
  while the note beneath it pointed at the full enum. Both are reachable and
  both are 404; the rows are there now.
* The rest of that table was checked against the emission sites one code at a
  time and is correct. The first attempt to check it mechanically matched each
  code to every HTTP status within 260 characters of it, which produced
  nonsense — `NOT_FOUND` appeared as `200 / 404` and `RATE_LIMITED` as
  `429 / 500`, both pure proximity artifacts. The output was discarded rather
  than reported; the three rows that changed were each confirmed by reading
  their actual emission site.
* **The request-too-large and input-ceiling errors repeated the dead-knob
  defect.** After the `STREAM_LIMIT_REACHED` message was corrected, two more
  were found naming the same kind of thing: one said "raise
  `api.max_body_bytes` **in the config file**" — there is no `api.` section in
  the config at all, and writing one makes the server refuse to start; the knob
  is `LOOPWORKER_API_MAX_BODY_BYTES`. The other told the operator to "raise
  `api.max_input_bytes`", which has no environment variable, no config key and
  no option at all — it is a compile-time constant, set only in `DefaultConfig()`
  and one test. Both now name the real lever, or say plainly that there is none.
* **`relcheck` gained `checkNoAPINamesInCode`**, because the class recurred
  rather than being a one-off. **The first version of that rule was wrong, and
  said so on its first run**: it assumed `api.` was a reserved namespace and
  matched `\bapi\.[a-z_]+\b` directly, then reported `api.openai` in two files —
  which is the host in `https://api.openai.com`. The prefix is not reserved. The
  check now derives the name set from the fields of `pkg/api.Config` and converts
  them to the prose form, so a match is by construction a real field and nothing
  else in the repository can trip it.
* **That derivation had a second bug, caught by its own test rather than by
  review**: the first implementation lower-cased the field name, so
  `MaxBodyBytes` became `maxbodybytes` and never matched the `max_body_bytes`
  the messages actually use — the check matched nothing and reported a clean
  tree. Go field names carry no underscores, so a plain lower-case is not the
  prose form. It converts CamelCase properly now, and the limitation is written
  down: an acronym would need a dictionary, and a new one would show up as a
  gate that quietly stops firing rather than as a wrong verdict. Four
  self-tests, mutation checked — trusting the prefix again turns the URL guard
  red.
* **The `ErrTaskInvalid` error told the reader to call a route with the wrong
  parameter name.** It said `GET /api/v1/tasks/{id}`; the router registers
  `{taskID}`. This is the same drift the shipped documents were corrected for
  earlier — and the error message was missed, because the route check only read
  `.md` files. The message is read by an operator at the exact moment they are
  stuck, which makes it the worst place for a stale name to survive.
* **Two exported SDK methods were still calling routes the server never
  registered.** `pkg/client.GetMetrics` and `GetLogs` issued requests to
  `/api/v1/metrics` and `/api/v1/logs`; neither exists. A caller paid a round
  trip to be told 404, or got a JSON decode failure that reads like a server
  defect. They now fail immediately with a message naming the admin listener
  where that data actually lives. Deletion stays a product decision, so the
  signatures are unchanged and existing callers still compile.
* **Their test was what kept the defect alive.** `TestClientAllMethodsIntegration`
  ran a fake server that registered both routes and returned `{"count": 42}`, so
  the suite "proved" the client worked against a contract the product has never
  had. The fake routes are gone — leaving them would let a future regression
  pass again — and the assertions now require the honest error *and* that it
  name the admin listener, so restoring the old behaviour turns the test red
  rather than green.
* **The Go package doc still described the deleted CLIs.** `pkg/client` opened
  with "the HTTP client the loopctl, loopdebug and loopwatch CLIs use" — both
  had been deleted, and this text is what an SDK user reads on godoc.
* **The route check now reads error messages too**, not just documents:
  `checkErrorRouteReferences` applies the *same* verdict function to the routes
  quoted in the server's own user-facing strings. Sharing one function is the
  point: the defect survived precisely because two places could disagree and
  only one was checked. Comments are out of scope, and that is a narrowing
  rather than a concession — a comment stating "there is no /api/v1/metrics
  route" quotes it precisely in order to deny it, and rewriting a correct
  comment to satisfy a checker is how a repository stops being honest. Six
  self-tests, mutation checked: re-enabling comment scanning turns the
  narrowing test red.
* **The server told the operator to change a setting that does not exist.** When
  an SSE client exceeded the concurrency ceiling, the error's own `fix` field read
  "close an existing stream, or raise `api.max_streams_per_caller` /
  `api.max_streams_total`". Neither name is a config key or an environment
  variable. They are the field names in `GET /api/v1/runtime/stats` — the server
  was quoting its own telemetry as if it were a knob. The knob that actually
  works is `LOOPWORKER_API_MAX_STREAMS`, and it only covers the global ceiling;
  the per-caller ceiling has no knob at all. The message now says exactly that,
  including which of the two cannot be changed. This is load-bearing rather than
  cosmetic: a canvas that reloads a few times is enough to hit it.
* **Six environment variables the API layer reads were documented nowhere.**
  `LOOPWORKER_API_ANON_RATE`, `LOOPWORKER_API_AUTH_RATE`,
  `LOOPWORKER_API_MAX_BODY_BYTES`, `LOOPWORKER_API_MAX_STREAMS`,
  `LOOPWORKER_API_ALLOWED_ORIGINS` and `LOOPWORKER_API_TRUST_PROXY` are read by
  `pkg/api/config.go`, and none of them appeared in any shipped document or in
  `config.example.yaml` — so the limits that decide whether a client is let in
  were the only ones a customer could not find. The inverse of the defect above:
  the documented name did not work, the working name was invisible. They are now
  in a table in `api-reference.md`, with the fields that have **no** knob
  (`max_streams_per_caller`, `graph_max_nodes`, `max_input_bytes`,
  `request_timeout`, the keep-alive interval) marked as fixed at build time —
  because a table of limits that does not say which of them you can change is
  the same defect in a new shape.
* **The documented-route gate caught the line I had just written.** While fixing
  the above, the new text referred to `GET /api/v1/runtime/stats`; the route is
  `/runtime/stats` on the admin listener, and there is no `/api/v1` variant. The
  check that had been sitting there for the whole session turned the new prose
  red on its first read. It is recorded here because that is the intended
  behaviour of a gate — a check nobody has run against a document nobody has
  written yet is not a check.
* **Three shipped documents told the customer to build binaries that no longer
  exist.** `docs/QUICKSTART.md` said `LOOPWORKER_API_KEY` "is what `loopctl` and
  `loopdebug` authenticate with" — while a hundred lines further down the same
  file says `loopdebug` was deleted. `SECURITY.md` and `SUPPORT.md` both told the
  reader that "the other five (`loopctl`, `loopdebug`, `loopwatch`, `loopbench`,
  `loopsim`) are developer tools you build from source". Four of those five are
  gone; there is no source left to build, so a customer following the security
  policy's own scope paragraph would go looking for a repository that does not
  contain what the paragraph promises. `cmd/` holds exactly `loopctl` and
  `loopworker`. All three now say so.
* **That class deliberately did not become a gate**, and the attempt to write
  one is why. Detecting "mentions a deleted binary as if it still exists" needs
  four separate heuristics stacked on top of each other: a list of the deleted
  names, a vocabulary of deletion markers, a proximity window, and markdown
  emphasis stripping — because `已删除` in bold is `已**删除**`, which does not
  contain the marker string. The detector reported sixteen false positives on
  the corrected tree before all four were in place, four of them on the
  sentences that say the tools *were* deleted. A gate with that many moving
  parts is a gate that gets switched off, and a gate that gets switched off is
  worse than the review step it replaced.
* **The configuration-key class has a gate.** `relcheck` gained
  `checkDocumentedConfigKeys`:
  every key in a shipped document's configuration table must be one the loader
  registers, read from `spec.go` rather than a hand-copied list. Seven
  self-tests, two mutations verified: broadening the row pattern to any
  backticked dotted token (which reports `task.created`, `plugin.json` and
  `nope.gone` as missing configuration keys — the reason the pattern is scoped
  to table rows with an environment-variable cell), and dropping the membership
  test.
* **The first attempt at the first of those two mutations proved nothing.** It
  removed a capture group the error message still indexed, so the mutated code
  panicked and took the whole test binary down before the false-positive guard
  could report. A panic is not a failing assertion, and a red suite caused by a
  crash is not evidence that a guard works. The mutation was rewritten to widen
  the scope without changing the shape, and only then did it fail the test it
  was meant to.
* **All three channels now have a gate.** `verify-artifacts` boots the tarball,
  which is why that one could not silently regress; the deb/rpm and the image
  had nothing at all, so the fixes above would have been free to rot. `relcheck`
  gained `checkAttributionChannels`, which requires `LICENSE` and `NOTICE` to be
  named in all three packaging paths — `archives[].files`, `nfpms.contents[].src`
  and a `COPY` in the **final** Dockerfile stage, since a `COPY` into the builder
  never reaches the published image. Eight self-tests, mutation checked: making
  the stage scan stop at the first `FROM`, and matching the nfpms entries on the
  bare filename instead of the `src:`, each turn a test red.
* **The shipped documentation never mentioned the package manager at all.** The
  deb and rpm existed, were published, and `grep` for `apt`/`dnf`/`dpkg`/`rpm`
  across `docs/` and `README.md` returned nothing — so a customer who installed
  that way had no document telling them how. `QUICKSTART.md` now has a section
  for it, which is explicit about the two things that are easy to get wrong:
  the packages are release attachments and **not** entries in an apt or dnf
  repository (so `apt install loopworker` finds nothing), and the packaged
  `/var/lib/loopworker` is neither where the server looks by default nor
  writable by an unprivileged service user.
* **`.release/scripts/boot-smoke.sh` derived the repository root from the wrong
  path.** It used `$(dirname $BIN)/../..`, which is the repository root only for a
  script living at `.release/scripts/`. `ci.yml` passes `bin/loopworker` and
  `release.yml` passes an artifact unpacked somewhere else entirely, so the
  derivation was wrong in both cases and only worked because the next line falls
  back to `$PWD` — a rescue one edit away from silently breaking. It is now
  derived from the script's own location, which is the one path that is the same
  in every invocation. The same script also created a `mktemp` response body and
  never removed it, leaking a file into `/tmp` on every run; it now joins the
  other scratch files and is cleaned by the trap. The script was then run for
  real — six gates, all passed, `BOOT SMOKE PASSED` — which is how the first
  attempt at this fix was caught shipping a `$BODY`/`$body` mismatch that `set -u`
  turned into forty-five straight "unbound variable" errors. It was then run
  again against the *unpacked release artifact* (a binary outside the repository,
  which is the case `release.yml` actually hits), and passed the whole
  `verify-artifacts` sequence: sha256 against `checksums.txt`, a real
  ldflags-injected version string, the attribution files, the example plugin, and
  all six boot gates.
* **`docker-smoke.sh` could not have passed, on any input.** It started with
  `health_body=""` and passed that straight to `curl -o`, which rejects a blank
  target outright — `curl: option -o: blank argument where content is expected`,
  exit 2 — and writes nothing. stderr was discarded and `|| echo "000"` supplied
  the fallback, so the gate reported `HTTP 000` against a container that was
  answering `200`, after a pointless thirty-attempt poll. The capture files are
  now real `mktemp` files created once and removed by the cleanup trap; they
  used to be created *inside* the loop, leaking one pair per attempt. The script
  was then executed for real — `docker-smoke.sh loopworker:smoke-verify` under
  Git bash on Windows — and reports `all gates passed` for the first time: the
  hardened container stays up, runs as uid 10001, answers `/api/v1/health` with
  200 and the documented `HealthResponse` JSON, goes Docker-`healthy`, and
  `docker stop` exits 0 through the SIGTERM drain path, leaving no scratch file
  behind. It had never been run at all, which is how a defect this deterministic
  survived.
* **`.release/build.ps1` ran whichever tool version happened to be in `PATH`,
  so "it passed locally" did not mean "CI would pass".** CI installs pinned
  versions; the Windows script probes and uses what it finds, because it has to
  work on a machine that has never seen CI. On this repository that gap was not
  theoretical: the `govulncheck` in `PATH` was v1.3.0 while CI pins v1.8.0, and
  `-Task vuln` would have reported a clean scan from a scanner three minor
  versions behind — the kind of scanner that reports fewer advisories rather than
  fewer problems. The lint, vuln and release tasks now read the CI pin and
  compare it against the binary's own build info (`go version -m`, so the check
  is offline and cannot pass for want of a network), printing a warning naming
  both versions when they differ.
* **`NOTICE` told customers the third-party license texts were in the binary
  archive. They are not.** The archive ships 13 entries and `THIRD-PARTY-LICENSES/`
  is not one of them; the texts are a separate `third-party-notices.tar.gz` on the
  release page. `NOTICE` ships inside the archive and is the first legal document
  a buyer's counsel reads, so a reader following it was sent looking for a
  directory that is not there. It now states where the texts actually are, and
  why they are not in the archive: GoReleaser builds it from committed files, and
  a path that only exists on the release runner matches nothing there — which
  fails the release, as `.goreleaser.yaml` already records from the
  `./dist/*.spdx.json` incident.
* **`THIRD-PARTY-NOTICES.md` was missing a dependency.** The index is generated
  with `awk -F, 'NR>1 {...}'`, but `go-licenses report` writes no header line, so
  the first record — `github.com/beorn7/perks/quantile`, a Prometheus dependency
  — was silently dropped from the attribution table attached to every release.
  The `NR>1` is gone, and the table now shows the licence type rather than the
  licence URL, which is what its own column heading says.
* **`NOTICE`'s dependency list had drifted twice**, and neither was visible
  anywhere: `golang.org/x/crypto` kept its old version across a bump, and
  `github.com/spf13/pflag` — a direct import — was never listed at all. Both are
  fixed, and `relcheck` now compares the list against go.mod on every build
  (`checkNoticeDirectDependencies`, six self-tests). The check is a comparison
  rather than a judgement: it says nothing about the licence names or copyright
  lines, which are a human's reading of each LICENSE file.
* **`TestRunServerStartsAndServesHealth` failed intermittently under load, with
  nothing in the failure to explain why.** It picks a port by binding `:0`,
  reading the assigned number and closing the listener — so between that and the
  server's own bind, another process can take it. The window is wide: the server
  still has to read its configuration and run the startup self-check, which
  binds two more sockets, before it listens. Under the full-suite coverage run
  (25 packages, every allocation counted) it produced a red build roughly one run
  in five whose only symptom was `server exited early`. The test now retries on
  a fresh pair of ports when — and only when — the failure is a taken port, which
  is the harness's problem rather than the product's; any other early exit is
  still reported as it stands. Verified by holding four consecutive ports and
  watching the test recover on the fifth.
* The health poll in that test used `http.DefaultClient`, which has no timeout.
  A port that is listening but never answering would hang the poll, and the whole
  test with it, instead of failing it.
* **`golang.org/x/crypto` carried two advisories with a fix available.** The
  `govulncheck` gate passes — the code imports `bcrypt` from that module and
  neither advisory is reachable, so this is a module-level finding, not an
  exploitable one. It is still what a customer sees when they scan this
  repository, so `v0.55.0` → `v0.56.0` clears GO-2026-6354 and GO-2026-6355
  (both `ssh` denial-of-service). GO-2026-5932 (`openpgp` unmaintained) remains
  and cannot be fixed; it is reported because the module is present, and the
  package is not imported.
* **The canvas build chain shipped three high-severity advisories.**
  `nanoid`, `postcss` and `source-map-js` are all transitive dependencies of
  vite/vitest, so none of them reach `pkg/api/dist` — the bundle is
  `go:embed`-ed and served to browsers, and it is unaffected. They are fixed by
  in-range lockfile bumps (`nanoid` 3.3.15→3.3.20, `postcss` 8.5.15→8.5.29,
  `source-map-js` 1.2.1→1.2.2); no direct dependency moved and no source file
  changed. `npm audit` now reports 0, and a fresh `npm run build` is
  byte-identical to the committed `pkg/api/dist`, so the CI step that fails on a
  stale embed is unaffected.
* **The `race` CI job would have died of OOM rather than reporting a race.** The
  `pkg/plugin` fixtures wrote the real `examples/hello-plugin` module — 2.5 MB of
  Go-generated bytecode — once per plugin, roughly 140 times per run, about 100 of
  them concurrently inside `TestConcurrentLoadPlugin` and
  `TestConcurrentMixedOperations`. Under `-race` that is wazero compiling 2.5 MB
  per load with every allocation instrumented: the package took **9m37s** and
  peaked at **7.2 GB**, on a job that runs on `ubuntu-latest` — 7 GB of RAM. The
  job could not have completed. Tests that only need a *loadable* plugin now use
  the 8-byte minimal module the specification allows; the two tests that execute a
  plugin and assert on its output still use the real artifact, which
  `TestShippedHelloPluginIsTheTestedArtifact` separately pins to the shipped
  bytes. `pkg/plugin` is now 27s and a few hundred MB under `-race`. The
  concurrency under test — `PluginManager`'s map and lock — never depended on
  wazero's compiler, so nothing was traded away.
* **`.golangci.yaml` named a preset that does not exist**, `stdlib-error-handling120+`.
  golangci-lint v2.9.0 — the version CI installs, pinned in `ci.yml` — rejects the
  config outright (`invalid preset`), so the `quality` job, `make lint` and the
  lint item on the pre-release checklist could never have passed. The first lint
  run that actually loaded surfaced 59 findings.
* **A configuration file that does not exist was reported as a silent default.**
  `internal/config/load.go` collected every path it searched and then threw the
  list away, so a typo in `--config`, or a `loopworker.yaml` in a different
  directory, started the server on defaults with nothing on stderr. `Diagnose` now
  reports the paths it looked in. Found by `staticcheck` SA4010.
* `Server.EnsureDirs("good", "")` created `good` and *then* failed on the empty
  name, leaving a half-applied set of directories behind. It now validates every
  name before creating any of them. The test that should have caught this
  asserted on the wrong path and passed vacuously.
* `StartAdmin` accepted a context and discarded it (`_ = ctx`), binding the admin
  listener with `context.Background()` while the main listener three lines earlier
  in `boot` used the real one. It now uses the context it is given, and
  `Diagnose`'s port probe — a real socket bind — is no longer uncancellable during
  startup.
* Dead code removed rather than left for a linter to ignore: `Worker.totalTime`,
  `loggerOnce`, `principalOf`, `isAdmin`, `storageCmdForTest`, `contains`,
  `metricSeriesCounts`, `accumulatorView`, `importMultiSection`, `writeManifest`,
  `nonFlushingWriter.header` and an unused `errors_test.setup`.
* Three client/server tests asserted that a value was zero and then checked a
  different field, so a truncated response body, a hijacked connection and a
  rejected request that still created its directory all passed. They now make the
  assertion they describe.
* `openStream` handed the caller a body it never closed. It now returns only the
  status and header it was actually being used for.
* **A documented behaviour was the opposite of the code.** `docs/USAGE.md` — which
  ships inside the release archive — said the loader "does not reject unknown
  keys". It does: `internal/config/load.go` refuses to start on an unrecognised
  key, naming the closest match and every accepted key. The same claim appeared
  in `.release/PRE-RELEASE-CHECKLIST.md`.
* Three sandbox tests that execute a real WASI module could never have run.
  `TestLoadWasmDirRealArtifact`, `TestAuditRealToolchainArtifact` and
  `TestSpinningRealWasmPluginIsKilled` read `testdata/go-wasi-echo.wasm` and
  `testdata/go-wasi-spin.wasm` and skipped when the files were absent. They were
  always absent: the skip messages and the helper's own comment told a maintainer
  to build them from `./echo.go` and `./spin.go`, two source files that were never
  committed. So the only tests that ran a module built by a real toolchain — real
  WASI imports, a real runtime, megabytes of linear memory — silently skipped in
  every clone of this repository. The fixtures are now compiled on demand with the
  real toolchain, and a build failure fails the test instead of skipping. The echo
  fixture is `examples/hello-plugin`, the module customers are told to use, so
  these tests now prove that one actually runs; `testdata/spin.go` is the hostile
  never-terminating module, committed as source precisely so it cannot drift from
  what it tests. With them running, the spin test demonstrates the CPU budget
  killing a real Go module after exactly its 2-second budget.
* `TestLoadWasmDirRealArtifact` asked for a 512 MiB plugin and then loaded it under
  `DefaultLoadOptions()`, whose host ceiling is 256 MiB — so the strict loader
  correctly refused it with `ErrLimitTooLarge`. It never surfaced, because the
  test skipped before reaching that line.
* `.release/scripts/boot-smoke.sh` rebuilt `examples/hello-plugin/hello.wasm` into
  the repository whenever that file was missing, so a smoke run could leave a
  tracked binary modified — exactly the kind of unexplained dirty file a smoke
  test must not create. It now builds into the script's own scratch directory.
* `examples/hello-plugin/main.go` told readers that
  `make examples/hello-plugin/hello.wasm` compiles the module. No such Makefile
  target exists; the `go build` line above it does. The comment now also states
  the invariant that is actually enforced (byte-identical to the tested artifact)
  instead of one that is not (identical to a fresh build, which no Go version
  bump preserves).
* **The `verify-artifacts` release job could not have passed.** It checked the
  repository out with a sparse list of `.release` and `docs`, then ran
  `.release/scripts/boot-smoke.sh`, whose fourth gate compiles
  `examples/hello-plugin` to wasip1 and copies its `plugin.json`. Neither the
  plugin directory nor `go.mod` was checked out, so that gate failed with
  "could not build examples/hello-plugin" on all three platforms — the release
  blocked at its own zero-after-sales test. The sparse list is gone; the job
  checks out the whole module.
* **The Docker smoke test could not have passed.** `docker-smoke.sh` started the
  image with no credential, but the image sets `LOOPWORKER_SERVER_HOST=0.0.0.0`
  and the server refuses to start on a public interface without one. The
  container exited 1 before the first probe, so every gate after it was reading a
  dead container. The script now mints the same throwaway credential
  `boot-smoke.sh` uses and sends it on the health probe; its header comment also
  still claimed `/api/v1/*` was unauthenticated, which stopped being true when
  the guard was added. `release.yml` calls this same script, so the release
  inherited the failure.
* `relcheck` gained `checkWorkflowPaths`, because the two bugs above are one
  bug: a workflow asserting something about the filesystem that nothing
  checked. It resolves every path a `run:` block names against that step's
  effective working directory. Steps at the repository root are not checked —
  there a repository-relative path is correct by definition, and the signal is a
  path written for one base and run from another. Reintroducing the `canvas`
  defect turns it red with the real line number.
* **The `canvas` CI job could never have passed.** Its embed-sync step compared
  `web/canvas/dist` with `pkg/api/dist`, but the job sets
  `working-directory: web/canvas`, so both paths resolved to directories that do
  not exist. `diff` failed, the step's `||` branch fired, and every pull request
  would have been turned red with "pkg/api/dist is stale" — a message pointing at
  the one thing that was not wrong. Fixed to paths relative to the working
  directory, matching the `../../` convention the coverage job already uses.

* **Windows artifacts were unusable.** `make build` on Windows emitted
  `bin/loopworker` with no `.exe`, which cmd/PowerShell cannot execute. The
  Makefile is now OS-aware (`EXE` derived from `go env GOOS`) and
  `.release/build.ps1` always writes `.exe`.
* **The Dockerfile could not build.** It pinned `golang:1.21-alpine` against a
  `go 1.26.6` module, set `CGO_ENABLED=1` without ever installing gcc, ran on
  the EOL `alpine:3.19`, had no `HEALTHCHECK`, and `.dockerignore` excluded the
  UI assets the server embeds. All five are addressed.
* **CI lied about coverage and races.** `test-coverage` wrote HTML and asserted
  nothing; the race job excluded `pkg/server`, the one package with a verified
  data race. Coverage now has a hard 80% threshold and `-race` runs over every
  package.
* **CI tested Go versions that were never shipped.** The matrix said
  `['1.21','1.22']` while go.mod said `go 1.26.6`. Version pins now come from
  go.mod (`go-version-file: go.mod`) and drift fails `relcheck`.
* `CONTRIBUTING.md` documented a tag-triggered release pipeline that did not
  exist. It now describes the pipeline that actually exists.
* `LICENSE` named `loopgap` as the licensor while all 55 commits are authored by
  `loopgad`. Now an explicit `TODO(owner)` placeholder that blocks publishing
  until a human confirms a real legal name.
* **The release could not be cut at all.** `.goreleaser.yaml`'s `nfpms.contents`
  listed `src: /usr/bin/loopworker`, which nfpm globs against the **build
  machine** — a path that exists on no runner, on any OS. The release died in the
  packager after the binaries and archives were already built, leaving four
  0-byte `.deb`/`.rpm` files that looked like real artifacts and no
  `checksums.txt`. GoReleaser installs matched binaries into `bindir` by itself,
  so the entry was both unnecessary and fatal.
* **The Windows boot smoke gate could never pass.** `.release/smoke.ps1` passed
  `$null` as `lpApplicationName`, leaving Windows to resolve the executable from
  the command line: 0/10 attempts returned `ERROR_PATH_NOT_FOUND`, while naming
  the executable explicitly succeeded 10/10. The `windows-latest` job in CI runs
  this script.
* **Every worker spawn and exit reached subscribers twice.** The executor
  published `worker.spawned`/`worker.exited` and then called
  `dispatcher.RegisterWorker`/`UnregisterWorker`, which publish the same events
  again. The dispatcher owns the registry and now emits each exactly once.
* **Shutdown recorded nothing.** `Executor.Run` handed its own already-cancelled
  context to `StopAllWorkers`, so the event store rejected every `worker.exited`
  with `context canceled` — eight warnings per shutdown, and no record of it.
  The drain now detaches from the cancellation while keeping its values.
* **The worker drain could close the event store under an in-flight publish.**
  `StopWorker` dropped the worker from the registry *before* publishing its exit
  event, so `WorkerCount()` — which `Server.Stop`'s drain watches — reported "no
  workers left" while the write was still in flight (`event store closed`). The
  removal is now last, and a state check under the same lock keeps concurrent
  `Stop` calls safe.
* **The GUI ignored work started outside it.** `handleUpdate` only patched nodes
  it already held, so a task created by the CLI, a scheduler or another tab never
  appeared until someone pressed Sync. `task.created` now refetches the graph,
  coalesced into one request per 250 ms window.
* **The task detail panel crashed on plain-text payloads.** `input` was run
  through `atob()` regardless of the server's `input_encoding`, throwing
  `InvalidCharacterError` and taking the panel down with it. Decoding now
  follows the server's `*_encoding` tag.
* **The canvas showed every task as PENDING.** The node cards read Go field
  names (`task.State`) against a JSON API that has always answered in lower case
  (`task.state`).
* `Escape` now closes the task detail drawer; previously only the close button
  did, so anyone who reached for Escape first saw a panel that looked stuck.
* `apiFetch` had no timeout, so one stalled connection left the canvas spinning
  with no way to tell it apart from a slow server. It now fails after 30s with a
  readable message.
* The credential gate no longer offers an API-key form for a dead network or a
  5xx — only an explicit 401/403 means a credential is required.
* `fetchGraph` depended on `setNodes`/`setEdges`, which are new references every
  render, so the mount effect re-ran and re-fetched the graph indefinitely.
* `/api/v1/*` answered `text/plain`: `writeJSON` never set a Content-Type.
* The probe effect's "already started" guard deadlocked the splash screen under a
  double-invoked effect, leaving "Connecting to LoopWorker…" with no error.
* React Flow v11 does not support React 19; the canvas moved to `@xyflow/react`
  v12.
* **The README documented four CLIs that no longer exist.** It said `make build`
  produces "all six" and listed `loopdebug`, `loopwatch`, `loopbench` and
  `loopsim` in both the English and Chinese tables and in both project-structure
  trees, with a warning about `loopwatch --metrics` for a binary that is not in
  the tree. All four were deleted; `cmd/` holds `loopworker` and `loopctl`.
* The README listed distributed tracing under "nothing calls it in a running
  server". `internal/core/observer` **is** constructed and started
  (`server.go:160`, `server.go:424`) and feeds metrics, logs and server health.
  What genuinely does not ship is the tracing: no exporter, no span propagation.
* **Three dead links in documents the release archive ships.** `README.md` linked
  to `AGENT-COLLABORATION-SPEC.md` (an internal file, which also broke the
  repository's own rule against linking internal notes from customer-facing
  material) and to `docs/USAGE.md`; `docs/QUICKSTART.md` linked to
  `docs/architecture.md`. None of the three is in `archives[].files`, so every
  customer who unpacked a tarball got a link to a file that was not there. They
  all resolve on GitHub, which is why CI never saw it.
* `docs/USAGE.md` is now in the release archive, so the README's guide link
  resolves for someone holding only the tarball.
* `relcheck` gained `checkShippedDocLinks`: a shipped document may not link to a
  file that is not itself shipped. It found the third dead link on its first run
  against this tree.
* `CONTRIBUTING.md` told contributors to rebuild the embedded canvas with
  `rm -rf pkg/api/dist && ... && cp -r ...` — delete first, copy second, which
  is the exact order the prose directly below it warns against, because emptying
  the directory breaks `//go:embed all:dist` for anything compiling at the time.
  The same broken sequence was repeated in the completion plan.
* **`docs/api/api-reference.md` documented path parameters under the wrong
  names** — `{taskId}` and `{id}` where the server registers `{taskID}`. It is
  itself in `archives[].files`, so it shipped beside `openapi.json`, which is
  kept honest by `TestOpenAPISpecMatchesRegisteredRoutes`; the hand-written
  reference beside it had no such guard, and the two disagreed.
* `relcheck` gained `checkDocumentedRoutes`: every `/api/v1` path in a shipped
  document must be a route the server registers, with the parameter name the
  spec uses. `CHANGELOG.md` is exempt by design — naming a route that does not
  exist is what that file is for. The check verifies route *shape*; it cannot
  know whether a concrete ID substituted into a `{param}` exists.
* `loopctl workflow --help` promised `create` and `get`. Neither existed:
  `create` is deliberately absent (workflows register in-process, and the
  package comment says so) and `get` was simply missing. Help text naming a
  command the binary does not have is the first thing a customer reads.
* **The configuration example in `docs/USAGE.md` taught a flat JSON shape that
  the guide had already been corrected away from.** It showed top-level `port`,
  `plugins_dir`, `data_dir` and `log_level`, which the `Corrected` section below
  had already rejected as fiction. The loader still accepts those four as legacy
  aliases, so a config written either way works — but the guide claimed the
  loader *silently ignored* them, which was backwards: an unrecognised key
  refuses startup with the file name, the key, a "did you mean" suggestion and
  the full list of accepted keys. A customer who trusted that paragraph was told
  to expect a quiet default and instead got a server that would not start.
* `docs/USAGE.md` now documents both accepted spellings and what an unrecognised
  key actually does.

### Corrected (claims that were not true of the shipped code)

* "Configuration file support (YAML)" — an earlier revision of this entry
  corrected itself in the wrong direction. It claimed the binary honored
  `--config` as **flat JSON keys** via `internal/config.LoadConfig`, and that the
  nested YAML loader was "not wired into `cmd/loopworker/main.go` yet". Neither is
  true: `internal/config.LoadConfig` does not exist in this tree, and
  `cmd/loopworker/main.go:112` calls `config.Load(config.Options{ConfigFile:
  cfgFile, Flags: overrides(cmd)})`, which is the nested loader
  (`internal/config/load.go`, `yaml.Unmarshal`, candidates `config.yaml`,
  `config.yml`, `config.json`), and its result is what `server.New` is built from.
  YAML configuration **is** a shipped feature and is advertised in `--help`.
* "Observability: … distributed tracing" — there is no tracer (no OpenTelemetry,
  no span propagation). What ships is structured logging (`pkg/logger`, zap),
  Prometheus metrics (`/metrics`), an event bus (`pkg/event`) and
  `/api/v1/health`.
* "Security: RBAC …" — an earlier revision claimed no HTTP endpoint issues or
  validates tokens, that the routes are registered without auth middleware, and
  that "SECURITY.md states this plainly". All three are false now, and SECURITY.md
  says the opposite: the server refuses to start if it would listen on a public
  interface with no configured credential. What ships is API-key and bearer-token
  authentication on the `authed` group, four permission levels (`PermRead`,
  `PermWrite`, `PermExecute`, `PermAdmin` in `pkg/security/auth.go`) enforced per
  route, and admin-only token and API-key issuance (`/auth/token`, `/auth/keys`).
  Loopback-only admin endpoints require `PermAdmin`; a loopback client with no
  keys configured is exempt (`LocalTrust`), which is what makes the canvas
  credential gate appear instead of failing closed on an empty install.
* "Liquid Glass UI" — the GUI **ships**, and this entry previously said
  otherwise twice over. `pkg/api/api.go` embeds the built canvas with
  `//go:embed all:dist` and serves it, so it is inside the binary even though
  its source is deliberately left out of the release archives. The claim that
  `GET /` was "a verified infinite 301 redirect loop" was wrong and had already
  been corrected in `.release/SCOPE-PROPOSAL.md`: `canvasIndex` reads
  `dist/index.html` once at init specifically to avoid it, and
  `pkg/api/static_test.go` guards that.
* "21 packages" — `go list ./...` reports **25** in this tree.
* "6 CLI tools" as a product feature — only `loopworker` ships in release
  artifacts. `loopbench`/`loopsim` never contact the server (they `time.Sleep`
  locally) and `loopctl`/`loopdebug` called endpoints that return 404. All four
  are now **deleted**; `cmd/` holds only `loopworker` and `loopctl`, and
  `.release/SCOPE-PROPOSAL.md` records the removal.

## [0.1.0-beta] — NOT YET RELEASED

Pre-1.0 beta. The version scheme is `0.1.0-beta` for the first public beta and
`0.1.0-beta.N` for each subsequent one — the `.N` counter only increases and the
`0.1.0` base never moves while the beta line is open. Nothing here carries a
compatibility promise: the HTTP API, config schema, and plugin ABI are all still
moving. See `.release/RELEASE-PROCESS.md`.

Contents (no artifacts for 0.1.0-beta exist as of 2026-10-06 — the section
exists so `preflight` can find it once the tag is cut):

* Event-driven task engine (`pkg/event`) with backpressure and metrics.
* Priority scheduling (`internal/core/scheduler`) with retry budgets and a
  dead-letter state, plus task dependencies (`internal/core/dispatcher`).
* WASM plugin sandbox (`internal/core/sandbox`, wazero) — a plugin runs in its
  own runtime, and memory/CPU/output limits come from config. Resource caps,
  not a security boundary (SECURITY.md).
* Self-healing (`internal/core/selfheal`): circuit breaker, exponential backoff,
  and health checks on a timer against the task database, the event store and
  the plugin directory.
* Workflow engine (`pkg/workflow`): sequential and DAG steps, two built-in
  workflows that run with no configuration, loadable definitions from
  JSON/YAML. Parallel step execution exists as `ParallelWorkflow.ExecuteParallel`
  (`pkg/workflow/workflow.go`) and is tested, but **no production code path
  constructs one** — no route and no engine method reaches it — so a customer
  cannot trigger it. It is not a shipped feature.
* HTTP API (`pkg/api`, chi) at `/api/v1/*`, authentication by API key and bearer
  token, three-level RBAC, per-caller rate limiting and per-task ownership; a
  loopback-only admin listener for `/metrics` and `/runtime/stats`; `pkg/client`
  SDK.
* `loopworker doctor` self-diagnosis, plus `loopworker backup` and
  `loopworker storage`.
* `loopworker` server binary; static artifacts for linux/darwin/windows and
  deb/rpm; container image; SBOM + signatures.
* Structured logging (`pkg/logger`) and sentinel errors (`pkg/errors`).
* 22 test packages plus `integration/integration_test.go`; `-race` clean;
  total coverage 83.9% against a 80% gate.

### Known issues at 0.1.0-beta (do not pretend otherwise)

* **The copyright line in `LICENSE` is an unresolved placeholder.** The release
  pipeline refuses to publish until a legal entity is named there. This is the
  only thing standing between this tree and a release.
* Sandbox limits are a **ceiling**: a plugin's `plugin.json` may tighten them
  but never raise them, and `allowed_hosts` is intersected rather than
  replaced. One plugin exhausting the cap still affects every plugin in that
  sandbox.
* Plugin manifests are not checksum-verified at load time. The verifier
  (`internal/core/sandbox/verify.go`) exists and is tested; nothing calls it in
  a running server.
* No TLS termination; put the server behind a reverse proxy that does it (see
  SECURITY.md, "What LoopWorker does NOT do").
* No audit log and no distributed tracing. Both have library-level scaffolding
  with no production caller.
* Task ownership is enforced in the API layer from task metadata rather than a
  first-class column, so code with write access can forge an owner value.
* `cmd/` holds exactly two binaries. Only `loopworker` is in a release artifact;
  `loopctl` is developer-only and is built by `.release/build.ps1`, not shipped.

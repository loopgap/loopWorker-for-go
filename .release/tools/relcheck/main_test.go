package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These checks were three false positives in a row: comment prose was parsed as
// configuration, ARG defaults were not resolved inside FROM, and "!" negations
// in .dockerignore were read as exclusions. That made the release gate
// permanently red on a correct Dockerfile, which is how gates get switched off.
// Each case below is the bad input the check must still reject.

func fixture(t *testing.T, dockerfile, dockerignore string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module loopworker\n\ngo 1.26.1\n")
	mustWrite(t, filepath.Join(dir, "Dockerfile"), dockerfile)
	mustWrite(t, filepath.Join(dir, ".dockerignore"), dockerignore)
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// goreleaserFixture writes a .goreleaser.yaml and points the tool at it.
func goreleaserFixture(t *testing.T, config string, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(name)), body)
	}
	mustWrite(t, filepath.Join(dir, ".goreleaser.yaml"), config)
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

// TestGoreleaserAbsoluteBinarySourceIsRejected is the defect that shipped:
// `src: /usr/bin/loopworker` reads as correct because the binary does have to
// land in /usr/bin, but nfpm globs `src` against the build machine, where it
// exists on no runner. The release failed 1m35s in — after every binary and
// archive was built — leaving four 0-byte .deb/.rpm files in dist/.
func TestGoreleaserAbsoluteBinarySourceIsRejected(t *testing.T) {
	goreleaserFixture(t, `
nfpms:
  - id: p
    contents:
      - src: /usr/bin/loopworker
        dst: /usr/bin/loopworker
        file_info:
          mode: 0755
`, nil)
	errs := errorsFrom(func(rp *report) { checkGoreleaserSources(rp) })
	if !hasErr(errs, "absolute source") {
		t.Fatalf("an absolute nfpms src slipped past: %v", errs)
	}
}

// TestGoreleaserFindingsCarryRealFileLineNumbers guards a bug that only a run
// against the real file exposed: items were numbered within the section slice,
// so a finding about line 182 was reported as line 25. An error that points at
// the wrong line sends whoever has to fix it to the wrong place.
//
// The raw string opens with a newline, so line 1 is blank and the two findings
// land on lines 7 and 11.
func TestGoreleaserFindingsCarryRealFileLineNumbers(t *testing.T) {
	goreleaserFixture(t, `
version: 2
project_name: loopworker
nfpms:
  - id: p
    contents:
      - src: /usr/bin/loopworker
archives:
  - id: a
    files:
      - NOPE.md
`, nil)
	errs := errorsFrom(func(rp *report) { checkGoreleaserSources(rp) })
	if !hasErr(errs, ".goreleaser.yaml:7 ") {
		t.Fatalf("nfpms finding lost its real line number: %v", errs)
	}
	if !hasErr(errs, ".goreleaser.yaml:11 ") {
		t.Fatalf("archives finding lost its real line number: %v", errs)
	}
}

func TestGoreleaserSymlinkSourceIsNotAnError(t *testing.T) {
	// The one legitimate absolute src: a link target inside the package, which
	// nfpm does not glob against the build machine.
	goreleaserFixture(t, `
nfpms:
  - id: p
    contents:
      - src: /sbin/foo
        dst: /usr/bin/foo
        type: "symlink"
`, nil)
	if errs := errorsFrom(func(rp *report) { checkGoreleaserSources(rp) }); len(errs) != 0 {
		t.Fatalf("a legitimate symlink target was reported: %v", errs)
	}
}

func TestGoreleaserMissingArchiveFileIsRejected(t *testing.T) {
	goreleaserFixture(t, `
archives:
  - id: a
    files:
      - LICENSE
      - SUPPORT.md
`, map[string]string{"LICENSE": "MIT"})
	errs := errorsFrom(func(rp *report) { checkGoreleaserSources(rp) })
	if !hasErr(errs, "matches nothing") {
		t.Fatalf("a missing archives file slipped past: %v", errs)
	}
}

func TestGoreleaserExistingSourcesPass(t *testing.T) {
	goreleaserFixture(t, `
archives:
  - id: loopworker
    ids:
      - loopworker
    formats:
      - tar.gz
    format_overrides:
      - goos: windows
        formats:
          - zip
    files:
      - LICENSE
      - src: config/config.example.yaml
        strip_parent: true
nfpms:
  - id: p
    formats:
      - deb
    contents:
      - src: config/config.example.yaml
        dst: /etc/loopworker/config.example.yaml
        file_info:
          mode: 0644
      - dst: /var/lib/loopworker
        type: dir
        file_info:
          mode: 0750
`, map[string]string{"LICENSE": "MIT", "config/config.example.yaml": "server: {}\n"})
	if errs := errorsFrom(func(rp *report) { checkGoreleaserSources(rp) }); len(errs) != 0 {
		t.Fatalf("a correct release config was reported: %v", errs)
	}
}

// TestGoreleaserProseIsNotConfiguration is the false-positive guard. The real
// file embeds a bash block in its release notes whose lines start with "--", and
// wraps itself in comments that name the exact `src` value this check rejects.
// A gate that reads either as configuration stays permanently red, and a
// permanently red gate gets switched off.
func TestGoreleaserProseIsNotConfiguration(t *testing.T) {
	goreleaserFixture(t, `release:
  header: |
    ## LoopWorker
    `+"```"+`bash
    cosign verify-blob \
      --signature checksums.txt.sig \
      --certificate checksums.txt.pem \
      checksums.txt
    `+"```"+`
  footer: |
    - not a list item
# `+"`src: /usr/bin/loopworker`"+` is what used to be here, and it is wrong.
nfpms:
  - id: p
    contents:
      - src: config/config.example.yaml
        dst: /etc/loopworker/config.example.yaml
`, map[string]string{"config/config.example.yaml": "server: {}\n"})
	if errs := errorsFrom(func(rp *report) { checkGoreleaserSources(rp) }); len(errs) != 0 {
		t.Fatalf("release-notes prose or a comment was parsed as configuration: %v", errs)
	}
}

func errorsFrom(fn func(rp *report)) []string {
	var rp report
	fn(&rp)
	return rp.errs
}

func hasErr(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

const goodDockerfile = `# CGO_ENABLED=1 with no gcc is what this file used to do.
ARG GO_VERSION=1.26.1
FROM golang:${GO_VERSION}-alpine AS builder
ENV CGO_ENABLED=0
RUN go build ./cmd/loopworker/
FROM alpine:3.22
HEALTHCHECK CMD curl -fsS http://127.0.0.1/api/v1/health
USER 10001
`

// releaseScriptFixture lays down the three files that state the release
// conventions independently of each other.
func releaseScriptFixture(t *testing.T, ciYML, releaseYML, buildPS1 string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module loopworker\n\ngo 1.26.1\n")
	mustWrite(t, filepath.Join(dir, ".github", "workflows", "ci.yml"), ciYML)
	mustWrite(t, filepath.Join(dir, ".github", "workflows", "release.yml"), releaseYML)
	mustWrite(t, filepath.Join(dir, ".release", "build.ps1"), buildPS1)
	mustWrite(t, filepath.Join(dir, ".goreleaser.yaml"), "# verified locally with:\n#   goreleaser release --snapshot --clean --skip=publish,sbom,sign\nnfpms:\n  - id: p\n    contents:\n      - src: config/config.example.yaml\n        dst: /etc/loopworker/config.example.yaml\n")
	mustWrite(t, filepath.Join(dir, "config", "config.example.yaml"), "server: {}\n")
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

// pinYML renders a workflow that both declares the tool pins and installs the
// tools. A fixture that only declares them describes the dead-pin shape that
// the unreferenced-pin rule exists to catch, so building the fixtures this way
// is what keeps that rule from being satisfied by its own test data.
func pinYML(goreleaser, syft string) string {
	return "env:\n" +
		"  GORELEASER_VERSION: " + goreleaser + "\n" +
		"  SYFT_VERSION: " + syft + "\n" +
		"jobs:\n" +
		"  release-config:\n" +
		"    steps:\n" +
		"      - run: install-syft \"${SYFT_VERSION}\"\n" +
		"      - uses: goreleaser/goreleaser-action@v6\n" +
		"        with:\n" +
		"          version: ${{ env.GORELEASER_VERSION }}\n"
}

var agreeingPinYML = pinYML("v2.9.0", "v1.9.0")

const agreeingPS1 = `
& $gr release --snapshot --clean --skip=publish,sbom,sign
`

func TestReleaseScriptAgreementPassesWhenAllThreeMatch(t *testing.T) {
	releaseScriptFixture(t,
		agreeingPinYML,
		agreeingPinYML+`        echo "args=release --clean" >> "$GITHUB_OUTPUT"
        echo "args=release --snapshot --clean --skip=publish,sbom,sign" >> "$GITHUB_OUTPUT"
`,
		agreeingPS1)
	if errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) }); len(errs) != 0 {
		t.Fatalf("matching conventions were reported: %v", errs)
	}
}

// A workflow pinning a different goreleaser/syft than its sibling means CI
// validates the pipeline with one tool and publishes with another, and every
// local snapshot stops meaning what CI will do.
func TestToolPinDriftBetweenWorkflowsIsRejected(t *testing.T) {
	releaseScriptFixture(t,
		pinYML("v2.9.0", "v1.9.0"),
		pinYML("v2.10.0", "v1.9.0"),
		agreeingPS1)
	errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) })
	if !hasErr(errs, "GORELEASER_VERSION is pinned as v2.9.0") {
		t.Fatalf("a bumped goreleaser pin slipped past: %v", errs)
	}
}

// This drift already shipped once: build.ps1 skipped only "publish" while the
// CI dry run and the .goreleaser.yaml header skipped publish,sbom,sign, so
// `-Task release` failed on every machine without syft and cosign.
func TestSnapshotCommandDriftBetweenBuildScriptAndCI(t *testing.T) {
	releaseScriptFixture(t,
		agreeingPinYML,
		agreeingPinYML+`        echo "args=release --snapshot --clean --skip=publish,sbom,sign" >> "$GITHUB_OUTPUT"
`,
		"\n& $gr release --snapshot --clean --skip=publish\n")
	errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) })
	if !hasErr(errs, "the snapshot command differs") {
		t.Fatalf("a drifted snapshot command slipped past: %v", errs)
	}
}

// The defect this rule exists for, on a fixture that reproduces it exactly:
// ci.yml declared SYFT_VERSION and never installed syft, so the workflow read as
// though the tool version were pinned while nothing in CI ever fetched it.
func TestUnreferencedToolPinIsRejected(t *testing.T) {
	releaseScriptFixture(t,
		"env:\n  GORELEASER_VERSION: v2.9.0\n  SYFT_VERSION: v1.9.0\njobs:\n  a:\n    steps:\n      - run: goreleaser --version \"$GORELEASER_VERSION\"\n",
		agreeingPinYML,
		agreeingPS1)
	errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) })
	if !hasErr(errs, "ci.yml pins SYFT_VERSION but never reads it") {
		t.Fatalf("a declared-but-unused tool pin slipped past: %v", errs)
	}
}

// Workflow `env:` is file-scoped, so a reference in the sibling workflow is not
// a consumer. Treating it as one is precisely how this rule would start firing
// on correct workflows, so it gets its own test.
func TestToolPinReferencedOnlyInTheSiblingWorkflowIsRejected(t *testing.T) {
	// ci.yml installs syft; release.yml declares the pin and never reads it.
	// ci.yml's install is in ci.yml's env scope and cannot reach release.yml.
	releaseScriptFixture(t,
		agreeingPinYML,
		"env:\n  SYFT_VERSION: v1.9.0\njobs:\n  publish:\n    steps:\n      - run: goreleaser-release\n",
		agreeingPS1)
	errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) })
	if !hasErr(errs, "release.yml pins SYFT_VERSION but never reads it") {
		t.Fatalf("a cross-file reference was accepted as a consumer: %v", errs)
	}
}

// \b has to keep SYFT_VERSION from matching inside a longer name built on it.
func TestToolPinNameIsNotMatchedInsideALongerName(t *testing.T) {
	releaseScriptFixture(t,
		"env:\n  SYFT_VERSION: v1.9.0\n  SYFT_VERSION_SHA: abc123\njobs:\n  a:\n    steps:\n      - run: echo \"${SYFT_VERSION_SHA}\"\n",
		agreeingPinYML,
		agreeingPS1)
	errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) })
	if !hasErr(errs, "SYFT_VERSION but never reads it") {
		t.Fatalf("a longer name was accepted as a reference to its prefix: %v", errs)
	}
	if hasErr(errs, "SYFT_VERSION_SHA but never reads it") {
		t.Fatalf("the longer name, which is genuinely referenced, was reported: %v", errs)
	}
}

// The positive case, and the one that keeps this rule from being switched off.
// Both consumption forms a real workflow uses are covered: a shell expansion and
// the `${{ env.* }}` context.
func TestReferencedToolPinsPass(t *testing.T) {
	releaseScriptFixture(t,
		agreeingPinYML,
		agreeingPinYML+`        echo "args=release --snapshot --clean --skip=publish,sbom,sign" >> "$GITHUB_OUTPUT"
`,
		agreeingPS1)
	if errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) }); len(errs) != 0 {
		t.Fatalf("a workflow that installs what it pins was reported: %v", errs)
	}
}

// A pin carrying a trailing comment is still a pin: relcheck must keep it in
// view for the version-agreement check, and the comment text mentioning the
// name is not a consumer of it. Both halves are load-bearing — a regex that
// skipped such lines would drop the pin from every check in this file, and one
// that counted its own comment would pass a workflow that installs nothing.
func TestToolPinWithTrailingCommentIsStillCheckedAndNotSelfSatisfied(t *testing.T) {
	releaseScriptFixture(t,
		"env:\n  SYFT_VERSION: v1.9.0 # keep in step with release.yml\njobs:\n  a:\n    steps:\n      - run: true\n",
		"env:\n  SYFT_VERSION: v1.0.0\njobs:\n  b:\n    steps:\n      - run: install-syft \"${SYFT_VERSION}\"\n",
		agreeingPS1)
	errs := errorsFrom(func(rp *report) { checkReleaseScriptAgreement(rp) })
	if !hasErr(errs, "ci.yml pins SYFT_VERSION but never reads it") {
		t.Fatalf("a comment on the pin satisfied the check by itself: %v", errs)
	}
	// The two files disagree on the version, and the commented pin must be part
	// of that comparison rather than invisible to it. The message names the
	// first pin seen and then the value that conflicts with it.
	if !hasErr(errs, "SYFT_VERSION is pinned as v1.9.0 in ci.yml") {
		t.Fatalf("a commented pin escaped the cross-workflow version check: %v", errs)
	}
}

// ---------------------------------------------------- Go toolchain per job --

const bootSmokeGo = `#!/usr/bin/env bash
set -euo pipefail
GOOS=wasip1 GOARCH=wasm go build -trimpath -o out.wasm ./examples/hello-plugin/
`

// The defect, on the shape it actually had: release.yml's verify-artifacts job
// never mentions `go` at all. The compile happens inside a tracked script, so a
// check that only read the workflow would call this job toolchain-free and
// correct. The second hop is the whole point.
func TestJobThatCompilesGoViaAScriptMustDeclareAToolchain(t *testing.T) {
	workflowFixture(t, `name: release
on: push
jobs:
  other-job:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
  verify-artifacts:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: bash .release/scripts/boot-smoke.sh "$BIN"
`, map[string]string{".release/scripts/boot-smoke.sh": bootSmokeGo})
	errs := errorsFrom(func(rp *report) { checkGoToolchainPerJob(rp) })
	if !hasErr(errs, `job "verify-artifacts" runs .release/scripts/boot-smoke.sh`) {
		t.Fatalf("a job compiling Go through a script slipped past: %v", errs)
	}
	// The sibling job declaring a toolchain must not be reported, and must not
	// be what silences the finding.
	if hasErr(errs, "other-job") {
		t.Fatalf("a correctly pinned sibling job was reported: %v", errs)
	}
}

// The direct case: `go` in the step's own run block. `go install` belongs here
// too — it builds a binary from source, so it is exactly as version-sensitive as
// `go build` even though nothing of the product is compiled.
func TestJobThatRunsGoInlineMustDeclareAToolchain(t *testing.T) {
	workflowFixture(t, `name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: go test -count=1 ./...
`, nil)
	errs := errorsFrom(func(rp *report) { checkGoToolchainPerJob(rp) })
	if !hasErr(errs, `job "test" runs a go subcommand`) {
		t.Fatalf("an inline go invocation slipped past: %v", errs)
	}

	workflowFixture(t, `name: release
on: push
jobs:
  notices:
    runs-on: ubuntu-latest
    steps:
      - run: go install github.com/google/go-licenses/v2@v2.0.1
`, nil)
	errs = errorsFrom(func(rp *report) { checkGoToolchainPerJob(rp) })
	if !hasErr(errs, `job "notices" runs a go subcommand`) {
		t.Fatalf("an unversioned `go install` slipped past: %v", errs)
	}
}

// A pinned literal is a declaration too: checkWorkflowGoVersions is what
// compares it to go.mod, and this check only has to know a choice was made.
func TestJobPinningALiteralGoVersionCountsAsDeclared(t *testing.T) {
	workflowFixture(t, `name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.1'
      - run: go build ./...
`, nil)
	if errs := errorsFrom(func(rp *report) { checkGoToolchainPerJob(rp) }); len(errs) != 0 {
		t.Fatalf("a job with an explicit Go version was reported: %v", errs)
	}
}

// The near-misses. A gate that fires on any of these is a gate that gets
// switched off, so each one is pinned down.
//
// `go env` is the interesting one: it proves the toolchain is on PATH and
// compiles nothing, so requiring a pinned version for it would be insisting on
// ceremony. `go-licenses` is the other: the hyphen means the tool's name, not a
// `go` subcommand.
func TestGoToolchainCheckIgnoresJobsThatDoNotCompile(t *testing.T) {
	workflowFixture(t, `name: release
on: push
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: actionlint .github/workflows/*.yml
  query:
    runs-on: ubuntu-latest
    steps:
      - run: echo "GOPATH=$(go env GOPATH)"
  publish:
    runs-on: ubuntu-latest
    steps:
      - run: docker build -t loopworker:smoke .
      - run: bash .release/scripts/docker-smoke.sh loopworker:smoke 19527
`, map[string]string{
		".release/scripts/boot-smoke.sh":   bootSmokeGo,
		".release/scripts/docker-smoke.sh": "#!/usr/bin/env bash\ndocker run --rm \"$1\" true\n",
	})
	if errs := errorsFrom(func(rp *report) { checkGoToolchainPerJob(rp) }); len(errs) != 0 {
		t.Fatalf("jobs that do not compile Go were reported: %v", errs)
	}
}

// A script that invokes go makes the *calling* job a Go job; one that does not,
// does not. Proving the negative is what keeps the second hop from flagging
// every job that runs a release script.
func TestGoToolchainCheckOnlyFollowsScriptsThatActuallyUseGo(t *testing.T) {
	workflowFixture(t, `name: release
on: push
jobs:
  container:
    runs-on: ubuntu-latest
    steps:
      - run: bash .release/scripts/docker-smoke.sh img:tag 19527
`, map[string]string{
		".release/scripts/docker-smoke.sh": "#!/usr/bin/env bash\ndocker run --rm \"$1\" true\n",
	})
	if errs := errorsFrom(func(rp *report) { checkGoToolchainPerJob(rp) }); len(errs) != 0 {
		t.Fatalf("a script with no go in it was treated as a Go user: %v", errs)
	}
}

// ------------------------------------------------- smoke script env vars --

// smokeEnvFixture lays down a product source that reads the two names under
// test, plus a smoke script, so the check has both halves of the comparison.
func smokeEnvFixture(t *testing.T, script, rel string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module loopworker\n\ngo 1.26.1\n")
	mustWrite(t, filepath.Join(dir, "internal", "config", "spec.go"),
		"package config\n\nvar specs = []spec{\n\t{path: \"server.port\", env: []string{\"LOOPWORKER_SERVER_PORT\", \"LOOPWORKER_PORT\"}},\n\t{path: \"server.admin_port\", env: []string{\"LOOPWORKER_API_ADMIN_PORT\"}},\n\t{path: \"data.dir\", env: []string{\"LOOPWORKER_DATA_DIR\"}},\n}\n")
	mustWrite(t, filepath.Join(dir, rel), script)
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

// The silent-failure case: a name the product never reads. The server keeps its
// default port, and the smoke reports a health timeout that is not about the
// artifact at all.
func TestSmokeScriptEnvVarTheProductNeverReadsIsRejected(t *testing.T) {
	smokeEnvFixture(t, "$env:LOOPWORKER_ADMIN_LISTENER_PORT = \"12345\"\n", ".release/smoke.ps1")
	errs := errorsFrom(func(rp *report) { checkSmokeScriptEnvVars(rp) })
	if !hasErr(errs, "sets LOOPWORKER_ADMIN_LISTENER_PORT") {
		t.Fatalf("an env var no Go source reads slipped past: %v", errs)
	}
	if !hasErr(errs, "smoke.ps1:1") {
		t.Fatalf("the finding lost its line number: %v", errs)
	}
}

// The positive case, and the one that keeps the gate from being switched off.
func TestSmokeScriptEnvVarsTheProductReadsPass(t *testing.T) {
	smokeEnvFixture(t, `$env:LOOPWORKER_PORT       = "$Port"
$env:LOOPWORKER_DATA_DIR   = $dataDir
$env:LOOPWORKER_API_ADMIN_PORT = "$AdminPort"
`, ".release/smoke.ps1")
	if errs := errorsFrom(func(rp *report) { checkSmokeScriptEnvVars(rp) }); len(errs) != 0 {
		t.Fatalf("env vars the product reads were reported: %v", errs)
	}
}

// The distinction the rule turns on. A script reading its own input is not the
// script handing the server a variable, and demanding a registry entry for a
// test-harness knob would make this gate permanently red.
func TestSmokeScriptReadingItsOwnInputIsNotAnAssignment(t *testing.T) {
	smokeEnvFixture(t, `SMOKE_KEY="${LOOPWORKER_SMOKE_API_KEY:-default}"
if [ -z "${LOOPWORKER_SMOKE_API_KEY:-}" ]; then echo "unset"; fi
hdr=(-H "Authorization: Bearer ${LOOPWORKER_SMOKE_TOKEN}")
export LOOPWORKER_PORT="$PORT"
`, ".release/scripts/boot-smoke.sh")
	errs := errorsFrom(func(rp *report) { checkSmokeScriptEnvVars(rp) })
	if len(errs) != 0 {
		t.Fatalf("variables the script only reads were treated as assignments: %v", errs)
	}
}

// The registry is derived from non-test product sources. A name that appears
// only in a test is not something the shipped binary reads, and accepting it
// would make the gate agree with a name the product cannot honour.
func TestSmokeEnvRegistryExcludesTestOnlyNames(t *testing.T) {
	smokeEnvFixture(t, "$env:LOOPWORKER_PORT = \"12345\"\n", ".release/smoke.ps1")
	dir := *root
	mustWrite(t, filepath.Join(dir, "internal", "config", "spec_test.go"),
		"package config\n\nconst s = \"LOOPWORKER_TESTONLY_NAME\"\n")
	errs := errorsFrom(func(rp *report) { checkSmokeScriptEnvVars(rp) })
	if hasErr(errs, "LOOPWORKER_TESTONLY_NAME") {
		t.Fatalf("a test-only name entered the registry: %v", errs)
	}
}

func TestDockerfileCommentProseIsNotConfiguration(t *testing.T) {
	fixture(t, goodDockerfile, "")
	if errs := errorsFrom(func(rp *report) { checkDockerfile(rp, "1.26.1") }); len(errs) != 0 {
		t.Fatalf("correct Dockerfile reported %v", errs)
	}
}

func TestDockerfileRealCGOEnabledIsRejected(t *testing.T) {
	fixture(t, "FROM golang:1.26.1-alpine AS builder\nENV CGO_ENABLED=1\nHEALTHCHECK CMD true\nUSER root\n", "")
	errs := errorsFrom(func(rp *report) { checkDockerfile(rp, "1.26.1") })
	if !hasErr(errs, "CGO_ENABLED=1") {
		t.Fatalf("a real CGO_ENABLED=1 slipped past: %v", errs)
	}
}

func TestDockerfileVersionDriftIsRejected(t *testing.T) {
	fixture(t, "FROM golang:1.21-alpine AS builder\nENV CGO_ENABLED=0\nHEALTHCHECK CMD true\nUSER 10001\n", "")
	errs := errorsFrom(func(rp *report) { checkDockerfile(rp, "1.26.1") })
	if !hasErr(errs, "image build would fail") {
		t.Fatalf("a stale builder tag slipped past: %v", errs)
	}
}

func TestDockerfileUnsetArgWarnsInsteadOfFailing(t *testing.T) {
	// A tag that really is a variable cannot be compared to go.mod, so it is a
	// warning. Erroring here is what made the gate unusable.
	fixture(t, "FROM golang:${GO_VERSION}-alpine AS builder\nENV CGO_ENABLED=0\nHEALTHCHECK CMD true\nUSER 10001\n", "")
	var rp report
	checkDockerfile(&rp, "1.26.1")
	if len(rp.errs) != 0 {
		t.Fatalf("unresolved ARG failed the gate: %v", rp.errs)
	}
	if len(rp.warn) == 0 {
		t.Fatal("unresolved ARG produced no warning either — it was silently skipped")
	}
}

func TestDockerIgnoreNegationReIncludesEmbedTarget(t *testing.T) {
	fixture(t, goodDockerfile, "dist/\n!pkg/api/dist\n!pkg/api/dist/**\n")
	dir := *root
	mustWrite(t, filepath.Join(dir, "pkg", "api", "api.go"), "package api\n\n//go:embed all:dist\nvar dist embed.FS\n")
	if errs := errorsFrom(func(rp *report) { checkDockerIgnoreEmbeds(rp) }); len(errs) != 0 {
		t.Fatalf("a re-included embed target was reported: %v", errs)
	}
}

func TestDockerIgnoreRealExclusionOfEmbedTargetIsRejected(t *testing.T) {
	fixture(t, goodDockerfile, "pkg/api/dist/\n")
	dir := *root
	mustWrite(t, filepath.Join(dir, "pkg", "api", "api.go"), "package api\n\n//go:embed all:dist\nvar dist embed.FS\n")
	// The assets exist locally; the image would not get them. That is the bug.
	mustWrite(t, filepath.Join(dir, "pkg", "api", "dist", "index.html"), "<html></html>")
	errs := errorsFrom(func(rp *report) { checkDockerIgnoreEmbeds(rp) })
	if !hasErr(errs, "cannot contain assets the binary embeds") {
		t.Fatalf("an embed target hidden from the image slipped past: %v", errs)
	}
}

// ------------------------------------------------------- shipped doc links --

// archivesFixture is a .goreleaser.yaml with one archive carrying a files list,
// which is the only place relcheck can learn what a customer actually receives.
func archivesFixture(t *testing.T, files map[string]string) {
	t.Helper()
	goreleaserFixture(t, `
archives:
  - id: loopworker
    ids:
      - loopworker
    files:
      - LICENSE
      - README.md
      - docs/QUICKSTART.md
      - docs/USAGE.md
`, files)
}

// TestShippedDocLinkToUnshippedFileIsRejected is the defect that shipped twice in
// README.md: it linked to AGENT-COLLABORATION-SPEC.md and docs/USAGE.md, both of
// which exist in the repository and neither of which was in archives[].files.
// The link resolves on GitHub and does not resolve in the tarball.
func TestShippedDocLinkToUnshippedFileIsRejected(t *testing.T) {
	archivesFixture(t, map[string]string{
		"LICENSE":                     "MIT",
		"README.md":                   "See [spec](AGENT-COLLABORATION-SPEC.md).\n",
		"docs/QUICKSTART.md":          "# Quick start\n",
		"AGENT-COLLABORATION-SPEC.md": "internal\n",
	})
	errs := errorsFrom(func(rp *report) { checkShippedDocLinks(rp) })
	if !hasErr(errs, "not in archives[].files") {
		t.Fatalf("a link to a document the customer does not receive slipped past: %v", errs)
	}
}

// A link can also be missing outright, which is a different mistake and needs
// its own wording so the reader knows which one they are looking at.
func TestShippedDocLinkToMissingFileIsRejected(t *testing.T) {
	archivesFixture(t, map[string]string{
		"LICENSE":            "MIT",
		"README.md":          "See [architecture](docs/architecture.md).\n",
		"docs/QUICKSTART.md": "# Quick start\n",
	})
	errs := errorsFrom(func(rp *report) { checkShippedDocLinks(rp) })
	if !hasErr(errs, "does not exist in this repository at all") {
		t.Fatalf("a link to a file that does not exist slipped past: %v", errs)
	}
}

// Findings must name a real line, or the fix cannot be located.
func TestShippedDocLinkFindingCarriesRealLineNumber(t *testing.T) {
	archivesFixture(t, map[string]string{
		"LICENSE":            "MIT",
		"README.md":          "# Title\n\nintro\n\nSee [gone](nope.md).\n",
		"docs/QUICKSTART.md": "# Quick start\n",
	})
	errs := errorsFrom(func(rp *report) { checkShippedDocLinks(rp) })
	if !hasErr(errs, "README.md:5") {
		t.Fatalf("finding does not carry the real line number: %v", errs)
	}
}

// The positive case, and the one that keeps the gate from being switched off:
// everything the archive ships, links correctly.
func TestShippedDocLinksThatAllShipPass(t *testing.T) {
	archivesFixture(t, map[string]string{
		"LICENSE":            "MIT",
		"README.md":          "See [quickstart](docs/QUICKSTART.md) and the [guide](docs/USAGE.md).\n",
		"docs/QUICKSTART.md": "Back to [readme](../README.md).\n",
		"docs/USAGE.md":      "# Usage\n",
	})
	if errs := errorsFrom(func(rp *report) { checkShippedDocLinks(rp) }); len(errs) != 0 {
		t.Fatalf("correct links were reported as broken: %v", errs)
	}
}

// Everything the shipped docs legitimately contain that is NOT a link to
// another shipped file. A gate that flags these is a gate that gets deleted.
func TestShippedDocLinkCheckIgnoresURLsAnchorsAndCodeFences(t *testing.T) {
	archivesFixture(t, map[string]string{
		"LICENSE": "MIT",
		"README.md": `# Title

Anchor: [jump](#installation)
Web: [site](https://example.invalid/x) and [mail](mailto:a@example.invalid)
Titled: [api](./docs/QUICKSTART.md "the quickstart")

` + "```bash" + `
# not a link, this is shell: [see](docs/nope.md)
curl https://example.invalid
` + "```" + `

Still fine: [usage](docs/USAGE.md#sdk).
`,
		"docs/QUICKSTART.md": "# Quick start\n",
		"docs/USAGE.md":      "# Usage\n",
	})
	if errs := errorsFrom(func(rp *report) { checkShippedDocLinks(rp) }); len(errs) != 0 {
		t.Fatalf("URLs, anchors, titles or fenced code were read as broken links: %v", errs)
	}
}

// --------------------------------------------------- documented API routes --

// routeFixture builds a repository whose archives ship two documents, with
// pkg/api/openapi.json standing in for the registered route table.
func routeFixture(t *testing.T, spec string, files map[string]string) {
	t.Helper()
	files["pkg/api/openapi.json"] = spec
	goreleaserFixture(t, `
archives:
  - id: loopworker
    files:
      - README.md
      - CHANGELOG.md
      - docs/USAGE.md
`, files)
}

const smallSpec = `{"paths":{
  "/api/v1/health": {},
  "/api/v1/tasks": {},
  "/api/v1/tasks/{taskID}": {},
  "/api/v1/workflow/{workflowID}": {}
}}`

// TestDocumentedRouteParamNameDriftIsRejected is the defect that was about to
// ship: docs/api/api-reference.md used {taskId} and {id} where the server
// registers {taskID}. openapi.json never drifted because a Go test compares it
// to the router on every run; the hand-written reference beside it had no guard,
// so two shipped documents disagreed.
func TestDocumentedRouteParamNameDriftIsRejected(t *testing.T) {
	routeFixture(t, smallSpec, map[string]string{
		"README.md":     "See `/api/v1/tasks/{taskId}`.\n",
		"docs/USAGE.md": "# Usage\n",
	})
	errs := errorsFrom(func(rp *report) { checkDocumentedRoutes(rp) })
	if !hasErr(errs, "registers that parameter as /api/v1/tasks/{taskID}") {
		t.Fatalf("a parameter-name drift between shipped docs and the spec slipped past: %v", errs)
	}
}

func TestDocumentedRouteThatDoesNotExistIsRejected(t *testing.T) {
	routeFixture(t, smallSpec, map[string]string{
		"README.md":     "Call `/api/v1/tasks/{taskID}/retry`.\n",
		"docs/USAGE.md": "# Usage\n",
	})
	errs := errorsFrom(func(rp *report) { checkDocumentedRoutes(rp) })
	if !hasErr(errs, "which the server does not register") {
		t.Fatalf("a documented 404 slipped past: %v", errs)
	}
}

// The positive case. It deliberately includes the three shapes that could
// easily produce a false positive: a bare prefix, a prefix of several routes,
// and a concrete value substituted into a path parameter.
func TestDocumentedRoutesThatAllExistPass(t *testing.T) {
	routeFixture(t, smallSpec, map[string]string{
		"README.md": "The API lives under /api/v1.\n\n" +
			"Health: `/api/v1/health`, list: `/api/v1/tasks`, " +
			"one: `/api/v1/tasks/{taskID}`.\n\n" +
			"Workflows are grouped under `/api/v1/workflow`, and " +
			"`/api/v1/workflow/builtin.anomaly-review` is one of them.\n",
		"docs/USAGE.md": "# Usage\n",
	})
	if errs := errorsFrom(func(rp *report) { checkDocumentedRoutes(rp) }); len(errs) != 0 {
		t.Fatalf("valid route references were reported as broken: %v", errs)
	}
}

// CHANGELOG.md exists to name routes that do not exist. Reading it as a
// specification would make the gate permanently red on a correct file, which is
// how gates get switched off.
func TestChangelogIsExemptFromRouteChecking(t *testing.T) {
	routeFixture(t, smallSpec, map[string]string{
		"README.md":     "# Readme\n",
		"CHANGELOG.md":  "`/api/v1/metrics` and `/api/v1/logs` are not routes.\n",
		"docs/USAGE.md": "# Usage\n",
	})
	if errs := errorsFrom(func(rp *report) { checkDocumentedRoutes(rp) }); len(errs) != 0 {
		t.Fatalf("CHANGELOG.md was read as a specification: %v", errs)
	}
}

// Findings must name a real line so the drift can be located and fixed.
func TestDocumentedRouteFindingCarriesRealLineNumber(t *testing.T) {
	routeFixture(t, smallSpec, map[string]string{
		"README.md":     "# Title\n\nintro\n\nmore\n\nCall `/api/v1/nope`.\n",
		"docs/USAGE.md": "# Usage\n",
	})
	errs := errorsFrom(func(rp *report) { checkDocumentedRoutes(rp) })
	if !hasErr(errs, "README.md:7") {
		t.Fatalf("finding does not carry the real line number: %v", errs)
	}
}

// ------------------------------------------------------ workflow file paths --

// workflowFixture writes a single workflow and points the tool at a repository
// containing it, so path resolution has something real to resolve against.
func workflowFixture(t *testing.T, workflow string, repo map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range repo {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(name)), body)
	}
	mustWrite(t, filepath.Join(dir, ".github", "workflows", "ci.yml"), workflow)
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

// TestWorkflowPathWrittenForTheRootButRunFromASubdirectory is the canvas bug:
// the job runs from web/canvas and the step still spells repository-relative
// paths, so both sides resolve to directories that do not exist.
func TestWorkflowPathWrittenForTheRootButRunFromASubdirectory(t *testing.T) {
	workflowFixture(t, `name: ci
on: push
jobs:
  canvas:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: web/canvas
    steps:
      - uses: actions/checkout@v4
      - name: Committed embed matches this build
        run: |
          diff -r web/canvas/dist pkg/api/dist
`, map[string]string{
		"web/canvas/package.json":    "{}\n",
		"web/canvas/dist/index.html": "<html></html>\n",
		"pkg/api/dist/index.html":    "<html></html>\n",
	})
	errs := errorsFrom(func(rp *report) { checkWorkflowPaths(rp) })
	if !hasErr(errs, "does not exist at web/canvas/web/canvas/dist") {
		t.Fatalf("a repository-root path used from a subdirectory slipped past: %v", errs)
	}
}

// The same shape as a per-step override rather than a job default.
func TestWorkflowPathUnderAStepWorkingDirectoryIsChecked(t *testing.T) {
	workflowFixture(t, `name: ci
on: push
jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: run relcheck
        working-directory: .release/tools
        run: go run ./relcheck -root ../..
      - name: enforce the coverage threshold
        working-directory: .release/tools
        run: go run ./covergate -file ../../coverage.out -min "${COVERAGE_MIN}"
`, map[string]string{
		".release/tools/go.mod":    "module x\n",
		".release/tools/relcheck":  "x\n",
		".release/tools/covergate": "x\n",
	})
	if errs := errorsFrom(func(rp *report) { checkWorkflowPaths(rp) }); len(errs) != 0 {
		t.Fatalf("correct paths under a step working-directory were reported: %v", errs)
	}

	workflowFixture(t, `name: ci
on: push
jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - name: run relcheck
        working-directory: .release/tools
        run: go run ./relcheck -root .release/tools
`, map[string]string{
		".release/tools/go.mod": "module x\n",
	})
	errs := errorsFrom(func(rp *report) { checkWorkflowPaths(rp) })
	if !hasErr(errs, "does not exist at .release/tools/.release/tools") {
		t.Fatalf("a root-relative path under a step working-directory slipped past: %v", errs)
	}
}

// The near-misses. Each of these is something a correct workflow contains; a
// check that flags any of them is a check that gets switched off.
func TestWorkflowPathCheckIgnoresThingsThatAreNotFileReferences(t *testing.T) {
	workflowFixture(t, `name: ci
on: push
jobs:
  canvas:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: web/canvas
    steps:
      - name: build and compare
        run: |
          npm ci
          npm run build
          npm run lint
          go test ./...
          diff -r dist ../../pkg/api/dist \
            || (echo "::error::pkg/api/dist is stale — run 'npm run build' in web/canvas and sync the output into pkg/api/dist" && exit 1)
          curl -fsS https://example.com/api/v1/health
`, map[string]string{
		"web/canvas/package.json": "{}\n",
		"web/canvas/dist/i.js":    "x\n",
		"pkg/api/dist/i.js":       "x\n",
	})
	if errs := errorsFrom(func(rp *report) { checkWorkflowPaths(rp) }); len(errs) != 0 {
		t.Fatalf("something that is not a file reference was reported: %v", errs)
	}
}

// At the repository root a repository-relative path is correct by definition,
// so nothing is checked there. This keeps the signal narrow on purpose.
func TestWorkflowPathsAtTheRootAreNotChecked(t *testing.T) {
	workflowFixture(t, `name: ci
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: |
          cat .release/tools/go.mod
          ls docs
`, map[string]string{
		".release/tools/go.mod": "module x\n",
	})
	if errs := errorsFrom(func(rp *report) { checkWorkflowPaths(rp) }); len(errs) != 0 {
		t.Fatalf("root-relative paths were checked even though they are correct there: %v", errs)
	}
}

// ------------------------------------------------------------ release gate --

// The whole matrix, because the point of -strict is exactly one difference
// between the PR gate and the release gate.
func TestReleaseGateMatrix(t *testing.T) {
	cases := []struct {
		name        string
		errs, warns int
		strict      bool
		want        int
	}{
		{"clean PR gate", 0, 0, false, 0},
		{"clean release gate", 0, 0, true, 0},
		{"warnings pass on the PR gate", 0, 3, false, 0},
		{"warnings fail the release gate", 0, 3, true, 1},
		{"errors fail the PR gate", 1, 0, false, 1},
		{"errors fail the release gate", 1, 3, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := report{errs: make([]string, tc.errs), warn: make([]string, tc.warns)}
			if got := r.gate(tc.strict); got != tc.want {
				t.Errorf("gate(errors=%d warnings=%d strict=%v) = %d, want %d",
					tc.errs, tc.warns, tc.strict, got, tc.want)
			}
		})
	}
}

// TestPlaceholdersBlockTheReleaseGateAndNotThePRGate is the end-to-end version,
// on a fixture that really contains the placeholder this product still has: an
// unnamed copyright holder. A unit test over a hand-built report would pass
// even if placeholder detection stopped matching anything at all.
func TestPlaceholdersBlockTheReleaseGateAndNotThePRGate(t *testing.T) {
	licenseFixture(t, "MIT License\n\nCopyright (c) 2026 TODO(owner)\n")

	rp := report{}
	checkPlaceholders(&rp)
	if len(rp.warn) == 0 {
		t.Fatal("a LICENSE naming TODO(owner) produced no warning, so nothing can block a release on it")
	}
	if got := rp.gate(false); got != 0 {
		t.Errorf("PR gate = %d, want 0: placeholders are a release concern, not a PR one", got)
	}
	if got := rp.gate(true); got != 1 {
		t.Errorf("release gate = %d, want 1: this is the check that stops an unnamed copyright holder shipping", got)
	}
}

// Once the owner is named the same tree must publish. A gate that cannot be
// opened is not a gate, it is a wall.
func TestResolvedPlaceholdersLetTheReleaseThrough(t *testing.T) {
	licenseFixture(t, "MIT License\n\nCopyright (c) 2026 Example Corp\n")

	rp := report{}
	checkPlaceholders(&rp)
	if len(rp.warn) != 0 {
		t.Fatalf("a fully resolved tree still warns, which would make the gate impossible to open: %v", rp.warn)
	}
	if got := rp.gate(true); got != 0 {
		t.Errorf("release gate = %d, want 0 once every placeholder is named", got)
	}
}

// licenseFixture is a repository with every file checkPlaceholders reads, none
// of them carrying a placeholder except the LICENSE text it is given.
func licenseFixture(t *testing.T, license string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module loopworker\n\ngo 1.26.1\n")
	mustWrite(t, filepath.Join(dir, "LICENSE"), license)
	mustWrite(t, filepath.Join(dir, "SECURITY.md"), "Report to security@example.invalid\n")
	mustWrite(t, filepath.Join(dir, "SUPPORT.md"), "No support channel by design.\n")
	mustWrite(t, filepath.Join(dir, "NOTICE"), "Third-party notices.\n")
	mustWrite(t, filepath.Join(dir, "CODE_OF_CONDUCT.md"), "Enforced by conduct@example.invalid\n")
	mustWrite(t, filepath.Join(dir, "CONTRIBUTING.md"), "Open an issue.\n")
	mustWrite(t, filepath.Join(dir, ".goreleaser.yaml"), "project_name: loopworker\n")
	mustWrite(t, filepath.Join(dir, ".github", "CODEOWNERS"), "* @example\n")
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

// The runbook promises preflight blocks on seven placeholder categories, and two
// of them lived in files the check never opened. A published CODE OF CONDUCT
// naming no enforcement contact, or a CONTRIBUTING pointing at `TODO(owner)`, is
// a ticket this product cannot answer — so each is pinned here rather than left
// to the file list staying as it happens to be.
func TestPlaceholderInCoCOrContributingIsCaught(t *testing.T) {
	for _, tc := range []struct{ file, body string }{
		{"CODE_OF_CONDUCT.md", "Enforcement: TODO(owner)\n"},
		{"CONTRIBUTING.md", "Contact TODO(owner) before sending a patch.\n"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			licenseFixture(t, "MIT License\n\nCopyright (c) 2026 Example Corp\n")
			mustWrite(t, filepath.Join(*root, tc.file), tc.body)
			var rp report
			checkPlaceholders(&rp)
			found := false
			for _, w := range rp.warn {
				if strings.Contains(w, tc.file) {
					found = true
				}
			}
			if !found {
				t.Fatalf("a placeholder in %s was not reported: %v", tc.file, rp.warn)
			}
			if got := rp.gate(true); got != 1 {
				t.Errorf("release gate = %d, want 1: %s would ship with a placeholder in it", got, tc.file)
			}
		})
	}
}

// noticeFixture is a repository whose NOTICE is exactly the given text and
// whose go.mod is exactly the given text.
func noticeFixture(t *testing.T, gomod, notice string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), gomod)
	mustWrite(t, filepath.Join(dir, "NOTICE"), notice)
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

const noticeGoMod = `module loopworker

go 1.26.6

require (
	github.com/go-chi/chi/v5 v5.3.0
	github.com/spf13/pflag v1.0.10
	go.uber.org/zap v1.28.0
)

require golang.org/x/sys v0.47.0 // indirect
`

const goodNotice = `Third-party notices.

Direct dependencies
-------------------

github.com/go-chi/chi/v5 v5.3.0
  License: MIT

github.com/spf13/pflag v1.0.10
  License: BSD-3-Clause

go.uber.org/zap v1.28.0
  License: MIT
`

func TestNoticeDirectDependenciesThatMatchPass(t *testing.T) {
	noticeFixture(t, noticeGoMod, goodNotice)
	if errs := errorsFrom(checkNoticeDirectDependencies); len(errs) != 0 {
		t.Errorf("a NOTICE that matches go.mod must pass, got: %v", errs)
	}
}

// The first time this drifted: a dependency was bumped and the old version
// number stayed in the attribution file. Every build stayed green.
func TestNoticeVersionDriftIsRejected(t *testing.T) {
	noticeFixture(t, noticeGoMod, strings.Replace(goodNotice, "pflag v1.0.10", "pflag v1.0.9", 1))
	if !hasErr(errorsFrom(checkNoticeDirectDependencies), "version drift") {
		t.Error("a version in NOTICE that go.mod contradicts is exactly the drift this gate exists for")
	}
}

// The second time: a module became a direct import and was never added.
func TestNoticeMissingDirectDependencyIsRejected(t *testing.T) {
	noticeFixture(t, noticeGoMod, strings.Replace(goodNotice, "github.com/spf13/pflag v1.0.10\n  License: BSD-3-Clause\n\n", "", 1))
	if !hasErr(errorsFrom(checkNoticeDirectDependencies), "github.com/spf13/pflag v1.0.10 is not listed") {
		t.Error("a direct requirement missing from NOTICE is an incomplete legal document")
	}
}

// An indirect dependency has no entry of its own, so one in NOTICE is either a
// stale list or a wrong one.
func TestNoticeListingAnIndirectDependencyIsRejected(t *testing.T) {
	noticeFixture(t, noticeGoMod, goodNotice+"\ngolang.org/x/sys v0.47.0\n  License: BSD-3-Clause\n")
	if !hasErr(errorsFrom(checkNoticeDirectDependencies), "not a direct requirement") {
		t.Error("NOTICE must not list a module go.mod marks indirect")
	}
}

// NOTICE explains how to regenerate the list, and names modules while doing it.
// A gate that read those as entries would be reporting on its own instructions.
func TestNoticeProseNamingAModuleIsNotAnEntry(t *testing.T) {
	noticeFixture(t, noticeGoMod, goodNotice+
		"\n- Regenerate with: go list -m -f '{{if not .Indirect}}{{.Path}}{{end}}' all\n"+
		"- go.yaml.in/yaml/v3 v3.0.4 ships in the binary\n")
	if errs := errorsFrom(checkNoticeDirectDependencies); len(errs) != 0 {
		t.Errorf("prose that names a module must not be read as an entry, got: %v", errs)
	}
}

// go.mod accepts the one-line form as well as the block form. A gate that only
// understood one of them would silently stop firing the day go.mod is
// reformatted, which is the quiet way a gate dies.
func TestNoticeCheckHandlesTheSingleLineRequireForm(t *testing.T) {
	noticeFixture(t, "module loopworker\n\ngo 1.26.6\n\nrequire github.com/spf13/pflag v1.0.10\n",
		"Third-party notices.\n\ngithub.com/spf13/pflag v1.0.10\n  License: BSD-3-Clause\n")
	if errs := errorsFrom(checkNoticeDirectDependencies); len(errs) != 0 {
		t.Errorf("the single-line require form must be read, got: %v", errs)
	}
}

// A finding must name the module, or fixing it means re-deriving the whole
// list by hand.
func TestNoticeFindingNamesTheModuleAndVersion(t *testing.T) {
	noticeFixture(t, noticeGoMod, goodNotice+"\ngithub.com/extra/dep v1.0.0\n  License: MIT\n")
	errs := errorsFrom(checkNoticeDirectDependencies)
	if !hasErr(errs, "github.com/extra/dep v1.0.0") {
		t.Errorf("the finding must name the module and version, got: %v", errs)
	}
}

// attributionFixture writes the only two files this gate reads.
func attributionFixture(t *testing.T, goreleaser, dockerfile string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".goreleaser.yaml"), goreleaser)
	mustWrite(t, filepath.Join(dir, "Dockerfile"), dockerfile)
	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

// The shape the repository actually ships, trimmed to the parts under test.
const goodAttributionGoreleaser = `version: 2
project_name: loopworker
archives:
  - id: loopworker
    files:
      - LICENSE
      - NOTICE
      - src: config/config.example.yaml
nfpms:
  - id: loopworker
    contents:
      - src: config/config.example.yaml
        dst: /etc/loopworker/config.yaml
      - src: LICENSE
        dst: /usr/share/doc/loopworker/copyright
      - src: NOTICE
        dst: /usr/share/doc/loopworker/NOTICE
brews:
  - repository:
      owner: example
`

const goodAttributionDockerfile = `FROM golang:1.26.6-alpine AS builder
COPY . /src
RUN go build -o /out/loopworker ./cmd/loopworker

FROM alpine:3.22
COPY --from=builder /out/loopworker /usr/bin/loopworker
COPY LICENSE NOTICE README.md /licenses/
USER 10001
`

func TestAttributionCarriedByEveryChannelPasses(t *testing.T) {
	attributionFixture(t, goodAttributionGoreleaser, goodAttributionDockerfile)
	if errs := errorsFrom(checkAttributionChannels); len(errs) != 0 {
		t.Errorf("a packaging config that ships both documents everywhere must pass, got: %v", errs)
	}
}

// The defect that shipped: the deb and the rpm together carried a binary, a
// config file and a data directory, and not one licence text. Debian Policy
// 12.5 requires the copyright file, and both packages were redistributing
// without attribution.
func TestAttributionMissingFromTheDebAndRpmIsRejected(t *testing.T) {
	attributionFixture(t,
		strings.Replace(goodAttributionGoreleaser, "      - src: LICENSE\n        dst: /usr/share/doc/loopworker/copyright\n", "", 1),
		goodAttributionDockerfile)
	if !hasErr(errorsFrom(checkAttributionChannels), "nfpms contents do not ship LICENSE") {
		t.Error("a package-manager install with no licence text is the exact drift this gate exists for")
	}
}

// The image had the same gap: the runtime stage copied the binary and nothing
// else, so anyone who pulled it from a registry held the same unattributed
// redistributable.
func TestAttributionMissingFromTheContainerImageIsRejected(t *testing.T) {
	attributionFixture(t, goodAttributionGoreleaser,
		strings.Replace(goodAttributionDockerfile, "COPY LICENSE NOTICE README.md /licenses/\n", "", 1))
	if !hasErr(errorsFrom(checkAttributionChannels), "runtime stage does not copy LICENSE") {
		t.Error("an image published without its licence carries no attribution at all")
	}
}

// The tarball is the channel most likely to keep working by accident, because
// `verify-artifacts` boots the archive and would catch it. The check still
// needs to stand on its own: the archive is what a developer unpacks and builds
// from.
func TestAttributionMissingFromTheArchiveIsRejected(t *testing.T) {
	attributionFixture(t,
		strings.Replace(goodAttributionGoreleaser, "      - LICENSE\n", "", 1),
		goodAttributionDockerfile)
	if !hasErr(errorsFrom(checkAttributionChannels), "archives do not ship LICENSE") {
		t.Error("a tarball without the licence must be reported even though verify-artifacts would also catch it")
	}
}

// A COPY in the builder stage never reaches the published image. Treating it
// as coverage is how a gate reports a pass that the artefact does not have.
func TestAttributionCopiedOnlyIntoTheBuilderStageIsRejected(t *testing.T) {
	moved := strings.Replace(goodAttributionDockerfile, "COPY LICENSE NOTICE README.md /licenses/\n", "",
		1)
	moved = strings.Replace(moved, "FROM golang:1.26.6-alpine AS builder\n",
		"FROM golang:1.26.6-alpine AS builder\nCOPY LICENSE NOTICE README.md /licenses/\n", 1)
	attributionFixture(t, goodAttributionGoreleaser, moved)
	if errs := errorsFrom(checkAttributionChannels); !hasErr(errs, "runtime stage does not copy LICENSE") {
		t.Errorf("documents copied only into the builder are discarded before the image is published, got: %v", errs)
	}
}

// A destination path is not a source. The nfpms block puts NOTICE in a
// `/usr/share/doc/.../NOTICE` destination, and a substring match on the bare
// name would call that a shipped source.
func TestAttributionDestinationPathIsNotAChannel(t *testing.T) {
	attributionFixture(t,
		strings.Replace(goodAttributionGoreleaser, "      - src: LICENSE\n        dst: /usr/share/doc/loopworker/copyright\n",
			"      - src: config/config.example.yaml\n        dst: /usr/share/doc/loopworker/LICENSE\n", 1),
		goodAttributionDockerfile)
	if !hasErr(errorsFrom(checkAttributionChannels), "nfpms contents do not ship LICENSE") {
		t.Error("only the src of an nfpm entry puts a file in the package")
	}
}

// A section name that merely contains the document is not an entry, and neither
// is a longer filename.
func TestAttributionNearMissEntriesAreNotAccepted(t *testing.T) {
	attributionFixture(t,
		strings.Replace(goodAttributionGoreleaser, "      - LICENSE\n", "      - LICENSE.md\n", 1),
		goodAttributionDockerfile)
	if !hasErr(errorsFrom(checkAttributionChannels), "archives do not ship LICENSE") {
		t.Error("LICENSE.md is a different file from LICENSE")
	}
}

// After nfpms the document appears again under brews. A section tracker that
// never closes leaves it attributed to the wrong channel.
func TestAttributionIsNotCountedFromALaterSection(t *testing.T) {
	attributionFixture(t,
		strings.Replace(goodAttributionGoreleaser, "      - LICENSE\n", "", 1)+
			`brews_tap:
      - src: LICENSE
`, goodAttributionDockerfile)
	if !hasErr(errorsFrom(checkAttributionChannels), "archives do not ship LICENSE") {
		t.Error("a LICENSE under a later top-level section must not count for archives")
	}
}

// ---------------------------------------------------- error-message routes --

// errorRouteFixture writes a .go file holding user-facing error strings beside
// the same spec the document check uses.
func errorRouteFixture(t *testing.T, spec, goSource string) {
	t.Helper()
	routeFixture(t, spec, map[string]string{
		"docs/USAGE.md":     "# Usage\n",
		"pkg/api/errors.go": goSource,
	})
}

func errorRouteErrs(t *testing.T, spec, goSource string) []string {
	t.Helper()
	errorRouteFixture(t, spec, goSource)
	return errorsFrom(func(rp *report) { checkErrorRouteReferences(rp) })
}

// The drift that survived: the shipped documents were corrected to {taskID}
// and the ErrTaskInvalid message, which quotes the same route at the moment an
// operator is stuck, was not. Nothing looked at it because the route check only
// read .md files.
func TestErrorRouteParamNameDriftIsRejected(t *testing.T) {
	errs := errorRouteErrs(t, smallSpec, `package api

func msg() string {
	return "The task exists but its state forbids this. Fix: GET /api/v1/tasks/{id} and read state."
}
`)
	if !hasErr(errs, "registers that parameter as /api/v1/tasks/{taskID}") {
		t.Fatalf("an error message quoting the wrong parameter name is the drift this gate exists for, got: %v", errs)
	}
}

func TestErrorRouteThatDoesNotExistIsRejected(t *testing.T) {
	errs := errorRouteErrs(t, smallSpec, `package api

func msg() string {
	return "No metrics here. Fix: GET /api/v1/metrics"
}
`)
	if !hasErr(errs, "which the server does not register") {
		t.Errorf("a message sending a caller to a 404 is the defect this gate exists for, got: %v", errs)
	}
}

func TestErrorRoutesThatAllExistPass(t *testing.T) {
	errs := errorRouteErrs(t, smallSpec, `package api

func msg() string {
	return "Retry after GET /api/v1/health, or GET /api/v1/tasks/{taskID}, or GET /api/v1/tasks/t1."
}
`)
	if len(errs) != 0 {
		t.Errorf("routes the spec registers must pass, got: %v", errs)
	}
}

// The narrowing that keeps this check honest. A comment stating that a route
// does not exist quotes it precisely in order to deny it; the Deprecated notes
// on GetMetrics/GetLogs are exactly that. Rewriting a correct comment to satisfy
// a checker is how a repository stops being honest, so comments are out of scope
// and a comment may say whatever is true.
func TestCommentsAreNotScannedForErrorRouteReferences(t *testing.T) {
	errs := errorRouteErrs(t, smallSpec, `package api

// Deprecated: this cannot succeed. There is no /api/v1/metrics route, and
// there never was one; see /api/v1/health for what this server does serve.
func GetMetrics() error { return nil }
`)
	if len(errs) != 0 {
		t.Errorf("a comment must not be judged as a route reference, got: %v", errs)
	}
}

// A bare prefix is a sentence, not a route -- same rule the document check uses.
func TestBarePrefixInASentenceIsNotARouteReference(t *testing.T) {
	errs := errorRouteErrs(t, smallSpec, `package api

func msg() string {
	return "Everything under /api/v1 needs a credential."
}
`)
	if len(errs) != 0 {
		t.Errorf("the bare prefix must be accepted, got: %v", errs)
	}
}

// The property that makes the two checks trustworthy together: one reference,
// one verdict. If these could disagree, fixing the documents would again leave
// the error message wrong, which is exactly how this defect stayed open.
func TestDocumentAndErrorRouteChecksAgreeOnTheSameReference(t *testing.T) {
	const ref = "/api/v1/tasks/{id}"
	goSource := "package api\n\nfunc msg() string { return `Fix: GET " + ref + "` }\n"
	routeFixture(t, smallSpec, map[string]string{
		"README.md":         "See `" + ref + "`.\n",
		"docs/USAGE.md":     "# Usage\n",
		"pkg/api/errors.go": goSource,
	})
	fromDoc := errorsFrom(func(rp *report) { checkDocumentedRoutes(rp) })
	fromErr := errorsFrom(func(rp *report) { checkErrorRouteReferences(rp) })
	if len(fromDoc) == 0 || len(fromErr) == 0 {
		t.Fatalf("both checks must reject the drift; doc=%v err=%v", fromDoc, fromErr)
	}
	if strings.Contains(fromDoc[0], "{taskID}") != strings.Contains(fromErr[0], "{taskID}") {
		t.Errorf("the two checks disagree about the same reference:\n doc: %v\n err: %v", fromDoc, fromErr)
	}
}

// ------------------------------------------------------------ api.* settings

const apiConfigFixture = `package api

type Config struct {
	AnonRate            int
	MaxBodyBytes        int64
	MaxInputBytes       int64
	MaxStreamsPerCaller int
	MaxStreamsTotal     int
}

func DefaultConfig() *Config { return &Config{} }
`

func apiNameFixture(t *testing.T, configGo, otherGo string) {
	t.Helper()
	docFixture(t, map[string]string{"README.md": "# r\n"}, map[string]string{
		"pkg/api/config.go":     configGo,
		"pkg/api/handlers_x.go": otherGo,
	})
}

func apiNameErrs(t *testing.T, configGo, otherGo string) []string {
	t.Helper()
	apiNameFixture(t, configGo, otherGo)
	return errorsFrom(func(rp *report) { checkNoAPINamesInCode(rp) })
}

func TestAPISettingNamePresentedAsConfigIsRejected(t *testing.T) {
	errs := apiNameErrs(t, apiConfigFixture, `package api

func msg() string {
	return "Body too large. Fix: reduce it, or raise api.max_body_bytes in the config file."
}
`)
	if !hasErr(errs, "api.max_body_bytes") {
		t.Fatalf("a message sending the operator to a key the loader rejects is the defect this gate exists for, got: %v", errs)
	}
	if !hasErr(errs, "no such configuration key") {
		t.Errorf("the finding should say the key does not exist, got: %v", errs)
	}
}

// The false-positive guard, and the reason the name set is derived from the
// struct instead of assumed. The first version of this rule matched any
// `api.<lowercase>` and reported `api.openai` in two files, which is the host
// in https://api.openai.com. A gate that cannot tell a URL from a setting is a
// gate that gets switched off.
func TestAPINameThatIsNotAConfigFieldIsNotFlagged(t *testing.T) {
	errs := apiNameErrs(t, apiConfigFixture, `package api

const endpoint = "https://api.openai.com/v1/chat/completions"

func msg() string { return "upstream said " + endpoint }
`)
	if len(errs) != 0 {
		t.Errorf("a URL host must not be read as a setting name, got: %v", errs)
	}
}

// Upper-case is Go field access in a source file, not an instruction in a
// message. Only the dotted lower-case form is prose.
func TestUpperCaseFieldAccessIsNotAUserFacingSetting(t *testing.T) {
	errs := apiNameErrs(t, apiConfigFixture, `package api

func f(c *Config) int64 { return api.MaxBodyBytes }
`)
	if len(errs) != 0 {
		t.Errorf("Go field access must not be read as prose, got: %v", errs)
	}
}

// A struct that stopped parsing would make every name acceptable.
func TestEmptyAPINameSetIsAnErrorNotAPass(t *testing.T) {
	errs := apiNameErrs(t, "package api\n\n// the Config struct was refactored away\n",
		"package api\n\nfunc msg() string { return \"raise api.max_body_bytes\" }\n")
	if !hasErr(errs, "would pass on anything") {
		t.Error("a check that cannot fail is not a check")
	}
}

// ------------------------------------------------------------ documented ports

// docFixture writes a set of documents plus a .goreleaser.yaml whose
// archives[].files is generated from the documents actually given, because a
// fixture that advertises a document it never wrote would fail on the missing
// file rather than on the thing under test.
func docFixture(t *testing.T, files map[string]string, extra map[string]string) {
	t.Helper()
	dir := t.TempDir()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var list strings.Builder
	for _, name := range names {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(name)), files[name])
		if strings.HasSuffix(name, ".md") {
			list.WriteString("      - " + name + "\n")
		}
	}
	for name, body := range extra {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(name)), body)
	}
	mustWrite(t, filepath.Join(dir, ".goreleaser.yaml"),
		"version: 2\narchives:\n  - id: loopworker\n    files:\n"+list.String())

	old := *root
	*root = dir
	t.Cleanup(func() { *root = old })
}

// portFixture writes the example config that states the default ports plus the
// documents that quote addresses.
func portFixture(t *testing.T, exampleYAML string, files map[string]string) {
	t.Helper()
	docFixture(t, files, map[string]string{"config/config.example.yaml": exampleYAML})
}

const goodExampleYAML = `server:
  host: "127.0.0.1"
  port: 19527
  admin_port: 19528
`

func TestDocumentedPortsThatMatchTheDefaultPass(t *testing.T) {
	portFixture(t, goodExampleYAML, map[string]string{
		"README.md":     "API on 127.0.0.1:19527, metrics on 127.0.0.1:19528.\n",
		"docs/USAGE.md": "curl http://localhost:19527/api/v1/health\n",
	})
	if errs := errorsFrom(checkDocumentedPorts); len(errs) != 0 {
		t.Errorf("addresses using the default ports must pass, got: %v", errs)
	}
}

// The drift that shipped: QUICKSTART quoted the bind-refusal message with
// 0.0.0.0:19700, a port that exists nowhere in the repository. Everything else
// about the line was accurate, so it read like a real transcript.
func TestDocumentedPortTheServerNeverBindsIsRejected(t *testing.T) {
	portFixture(t, goodExampleYAML, map[string]string{
		"README.md": "```\nloopworker: bind_address: the API would listen on 0.0.0.0:19700 using only an ephemeral bootstrap key\n```\n",
	})
	if !hasErr(errorsFrom(checkDocumentedPorts), "0.0.0.0:19700") {
		t.Error("an address a reader could copy, that the server never binds, is the drift this gate exists for")
	}
}

// The finding must name the file and line, or it cannot be located.
func TestDocumentedPortFindingCarriesRealLineNumber(t *testing.T) {
	portFixture(t, goodExampleYAML, map[string]string{
		"README.md": "# Title\n\nintro\n\nmore\n\nBind 0.0.0.0:8080 to expose it.\n",
	})
	if !hasErr(errorsFrom(checkDocumentedPorts), "README.md:7") {
		t.Error("the finding must carry a real line number")
	}
}

// localhost and IPv6 are the same claim in different notation. A check that
// only understood dotted quads would leave the loopback examples unchecked.
func TestDocumentedPortOnEveryHostSpellingIsRejected(t *testing.T) {
	portFixture(t, goodExampleYAML, map[string]string{
		"README.md": "a localhost:19529\nb [::1]:19530\nc 10.0.0.5:19531\n",
	})
	errs := errorsFrom(checkDocumentedPorts)
	for _, want := range []string{"localhost:19529", "[::1]:19530", "10.0.0.5:19531"} {
		if !hasErr(errs, want) {
			t.Errorf("%s should be rejected the same way a dotted quad is", want)
		}
	}
}

// The false-positive guard, and the reason the pattern is narrow. Documents are
// full of numbers that are not ports; flagging those is how a gate gets deleted.
func TestNumbersThatAreNotListenAddressesAreNotFlagged(t *testing.T) {
	portFixture(t, goodExampleYAML, map[string]string{
		"README.md": "The health body is 235 bytes; start-period is 25s, retries 3, " +
			"interval 15s, timeout 5s. Coverage is 84.2% over 22 packages; the module is v1.0.10.\n" +
			"See line 19527 of the changelog and section 19528 for details.\n" +
			"A response looks like `{\"retries\": 3, \"limit\": 10, \"code\": 429}` and the\n" +
			"backup ran at 09:30 for 1:30, and step 2: 3 of the 4 workers were busy.\n" +
			"MAD0: 1 row, 0 rows; sha256 is 64 hex characters; ratio 16:9.\n",
	})
	if errs := errorsFrom(checkDocumentedPorts); len(errs) != 0 {
		t.Errorf("only address:port shapes may be checked, got: %v", errs)
	}
}

// The allowed set is read, not baked in. If the defaults move, the documents
// that were updated move with them and the stale ones turn red.
func TestDocumentedPortsFollowTheExampleConfig(t *testing.T) {
	portFixture(t, "server:\n  port: 8080\n  admin_port: 9090\n", map[string]string{
		"README.md": "API on 127.0.0.1:19527 and metrics on 127.0.0.1:19528.\n",
	})
	errs := errorsFrom(checkDocumentedPorts)
	if !hasErr(errs, "8080 and 9090") {
		t.Errorf("after the defaults move, the stale documents must be reported against the new set, got: %v", errs)
	}
}

// A gate whose allowed set is empty passes everything. Say so instead.
func TestAnEmptyAllowedPortSetIsAnErrorNotAPass(t *testing.T) {
	portFixture(t, "server:\n  host: \"127.0.0.1\"\n", map[string]string{
		"README.md": "API on 127.0.0.1:19527.\n",
	})
	if !hasErr(errorsFrom(checkDocumentedPorts), "allowed set is empty") {
		t.Error("a check that cannot fail is not a check")
	}
}

// Found by the first real run of this check: the CHANGELOG entry describing
// this defect quotes the wrong port, which is the entire purpose of that entry.
// A historical record that cannot record what used to be wrong is not a record.
func TestChangelogIsExemptFromPortChecking(t *testing.T) {
	portFixture(t, goodExampleYAML, map[string]string{
		"README.md":    "API on 127.0.0.1:19527.\n",
		"CHANGELOG.md": "* Fixed: the bind refusal said `0.0.0.0:19700`; it now says 19527.\n",
	})
	if errs := errorsFrom(checkDocumentedPorts); len(errs) != 0 {
		t.Errorf("a changelog must be able to quote the value it fixed, got: %v", errs)
	}
}

// ----------------------------------------------------- documented config keys

const goodSpecGo = `package config

var specs = []struct{ path string }{
	{path: "server.host"},
	{path: "server.port"},
	{path: "server.admin_port"},
	{path: "plugins.dir"},
	{path: "data.dir"},
	{path: "workers.count"},
	{path: "logging.level"},
}
`

func configKeyFixture(t *testing.T, spec string, files map[string]string) {
	t.Helper()
	docFixture(t, files, map[string]string{"internal/config/spec.go": spec})
}

func TestDocumentedConfigKeysThatAreAcceptedPass(t *testing.T) {
	configKeyFixture(t, goodSpecGo, map[string]string{
		"README.md": "| Key | Environment variable | Default |\n|---|---|---|\n" +
			"| `server.host` | `LOOPWORKER_SERVER_HOST` | `127.0.0.1` |\n" +
			"| `logging.level` | `LOOPWORKER_LOG_LEVEL` | `info` |\n",
	})
	if errs := errorsFrom(checkDocumentedConfigKeys); len(errs) != 0 {
		t.Errorf("keys the loader accepts must pass, got: %v", errs)
	}
}

// The drift that shipped. The environment-variable column in that row was
// correct, so the table read as entirely trustworthy, and the loader rejects
// the key outright — pointing the operator back at the table that caused it.
func TestDocumentedConfigKeyTheLoaderRejectsIsRejected(t *testing.T) {
	configKeyFixture(t, goodSpecGo, map[string]string{
		"README.md": "| Key | Environment variable | Default |\n|---|---|---|\n" +
			"| `log.level` | `LOOPWORKER_LOG_LEVEL` | `info` |\n",
	})
	if !hasErr(errorsFrom(checkDocumentedConfigKeys), `documents config key "log.level"`) {
		t.Error("a key that makes the server refuse to start is exactly what a customer cannot answer alone")
	}
}

// The finding must name the environment variable beside it, because that is
// the part of the row that is still correct and the reader's next question.
func TestDocumentedConfigKeyFindingNamesTheEnvVar(t *testing.T) {
	configKeyFixture(t, goodSpecGo, map[string]string{
		"README.md": "| `log.level` | `LOOPWORKER_LOG_LEVEL` | `info` |\n",
	})
	if !hasErr(errorsFrom(checkDocumentedConfigKeys), "LOOPWORKER_LOG_LEVEL") {
		t.Error("the finding should point at the env var that does work")
	}
}

func TestDocumentedConfigKeyFindingCarriesRealLineNumber(t *testing.T) {
	configKeyFixture(t, goodSpecGo, map[string]string{
		"README.md": "# Title\n\nintro\n\n| `log.level` | `LOOPWORKER_LOG_LEVEL` | `info` |\n",
	})
	if !hasErr(errorsFrom(checkDocumentedConfigKeys), "README.md:5") {
		t.Error("the finding must carry a real line number")
	}
}

// The false-positive guard, and the reason the row shape is narrow. Documents
// name many dotted things that are not configuration keys, and a check that
// flagged those would be switched off on its first day.
func TestDottedThingsThatAreNotConfigKeysAreNotFlagged(t *testing.T) {
	configKeyFixture(t, goodSpecGo, map[string]string{
		"README.md": "Events are `task.created`, `task.failed` and `plugin.loaded`; " +
			"the state field is `data.task` and the error field `details.allowed`.\n" +
			"Files: `plugin.json`, `go.mod`, `config.example.yaml`, `event.go`, `server.go`.\n" +
			"Prose mentions `server.host` and `workers.count` without a table row.\n" +
			"Prose mentions `admin_host` on its own, and `nope.gone` in backticks.\n" +
			"| Path | What |\n|---|---|\n| `/usr/bin/loopworker` | the server |\n",
	})
	if errs := errorsFrom(checkDocumentedConfigKeys); len(errs) != 0 {
		t.Errorf("only configuration-table rows may be checked, got: %v", errs)
	}
}

// A registry that stopped parsing would make every key acceptable.
func TestAnEmptyConfigKeyRegistryIsAnErrorNotAPass(t *testing.T) {
	configKeyFixture(t, "package config\n\n// the registry was refactored\n", map[string]string{
		"README.md": "| `log.level` | `LOOPWORKER_LOG_LEVEL` | `info` |\n",
	})
	if !hasErr(errorsFrom(checkDocumentedConfigKeys), "accepted set is empty") {
		t.Error("a check that cannot fail is not a check")
	}
}

// Same reasoning as the port check: the entry recording this defect has to
// quote the key it fixed.
func TestChangelogIsExemptFromConfigKeyChecking(t *testing.T) {
	configKeyFixture(t, goodSpecGo, map[string]string{
		"README.md":    "| `server.host` | `LOOPWORKER_SERVER_HOST` | `127.0.0.1` |\n",
		"CHANGELOG.md": "* Fixed: the table said `log.level`; the key is `logging.level`.\n",
	})
	if errs := errorsFrom(checkDocumentedConfigKeys); len(errs) != 0 {
		t.Errorf("a changelog must be able to quote the key it fixed, got: %v", errs)
	}
}

// ------------------------------------------------- error code reachability --

// errorCodeFixture builds a repository whose archive ships one document, with a
// small ErrorCode catalogue standing in for pkg/api/errors.go. The catalogue is
// gofmt-aligned on purpose: an earlier version of the check recognised a
// declaration by a single-space prefix, matched nothing, and so counted every
// code as reachable through its own declaration — a gate that reported 0
// unreachable on a tree that had three.
func errorCodeFixture(t *testing.T, catalog string, files map[string]string) {
	t.Helper()
	files["pkg/api/errors.go"] = catalog
	files["pkg/errors/errors.go"] = "package errors\n\nimport \"errors\"\n\nvar (\n\tErrQueueFull = errors.New(\"task queue is full\")\n)\n"
	goreleaserFixture(t, `
archives:
  - id: loopworker
    files:
      - docs/USAGE.md
`, files)
}

const alignedCatalog = `package api

const (
	CodeTaskNotFound  ErrorCode = "TASK_NOT_FOUND"
	CodeQueueFull     ErrorCode = "QUEUE_FULL"
	CodeRateLimited   ErrorCode = "RATE_LIMITED"
)

func register() {}

var _ = register
`

// The defect, unchanged: the reference told clients to handle TASK_ALREADY_EXISTS,
// which no path can emit because the create endpoint allocates the id itself.
func TestUnreachableErrorCodeInDocTableIsRejected(t *testing.T) {
	errorCodeFixture(t, `package api

const (
	CodeTaskAlreadyExists ErrorCode = "TASK_ALREADY_EXISTS"
	CodeTaskNotFound      ErrorCode = "TASK_NOT_FOUND"
)

func register() {}

var _ = register
`, map[string]string{
		"docs/USAGE.md": "| TASK_ALREADY_EXISTS | 409 | a task with that identity already exists |\n| TASK_NOT_FOUND | 404 | no such task |\n",
		"pkg/api/tasks.go": `package api

import "net/http"

func emit(w http.ResponseWriter) {
	_ = w
	_ = CodeTaskNotFound // TASK_NOT_FOUND is reachable; TASK_ALREADY_EXISTS is not
}
`,
	})
	errs := errorsFrom(checkErrorCodeReachability)
	if !hasErr(errs, "documents error code TASK_ALREADY_EXISTS") {
		t.Fatalf("a code no path can emit was documented as receivable: %v", errs)
	}
}

// RATE_LIMITED is emitted by writing an ErrorBody directly, never by returning
// its registered sentinel. A sentinel-only reading calls that code dead, which
// would have produced a false positive on a live error.
func TestDirectlyEmittedErrorCodeCountsAsReachable(t *testing.T) {
	errorCodeFixture(t, alignedCatalog, map[string]string{
		"docs/USAGE.md":    "| RATE_LIMITED | 429 | too many requests |\n",
		"pkg/api/mw.go":    "package api\n\nfunc rateLimit() { _ = CodeRateLimited }\n",
		"pkg/api/tasks.go": "package api\n\nfunc emit() { _ = CodeTaskNotFound }\n",
	})
	if errs := errorsFrom(checkErrorCodeReachability); len(errs) != 0 {
		t.Fatalf("a code emitted directly was called unreachable: %v", errs)
	}
}

// The positive case, and the one that has to keep working: a code that reaches
// clients through classify() rather than through an ErrorBody literal.
func TestClassifiedErrorCodeCountsAsReachable(t *testing.T) {
	errorCodeFixture(t, `package api

import (
	"net/http"

	lwerrors "loopworker/pkg/errors"
)

const (
	CodeQueueFull ErrorCode = "QUEUE_FULL"
)

func register(err error, status int, code ErrorCode, msg string) {}

func init() {
	register(lwerrors.ErrQueueFull, http.StatusServiceUnavailable, CodeQueueFull, "full")
}
`, map[string]string{
		"docs/USAGE.md":    "| QUEUE_FULL | 503 | the queue is full |\n",
		"pkg/api/queue.go": "package api\n\nimport lwerrors \"loopworker/pkg/errors\"\n\nfunc submit() error { return lwerrors.ErrQueueFull }\n",
	})
	if errs := errorsFrom(checkErrorCodeReachability); len(errs) != 0 {
		t.Fatalf("a code reachable through classify() was called unreachable: %v", errs)
	}
}

// An unreachable code that no shipped document mentions is a warning, not an
// error: it cannot mislead a client, but the next reader of errors.go will
// assume it is live.
func TestUndocumentedUnreachableErrorCodeWarns(t *testing.T) {
	errorCodeFixture(t, alignedCatalog, map[string]string{
		"docs/USAGE.md":    "| TASK_NOT_FOUND | 404 | no such task |\n",
		"pkg/api/tasks.go": "package api\n\nfunc emit() { _ = CodeTaskNotFound }\n",
	})
	var rp report
	checkErrorCodeReachability(&rp)
	found := false
	for _, w := range rp.warn {
		if strings.Contains(w, "QUEUE_FULL is unreachable and no shipped document mentions it") {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unreachable code nobody documents produced no warning: %v", rp.warn)
	}
}

// Naming the code in prose is how the reference says "this exists but will never
// arrive" — that must silence the warning, or the honest sentence would be
// forced out of the document.
func TestDocumentedUnreachableErrorCodeDoesNotWarn(t *testing.T) {
	errorCodeFixture(t, alignedCatalog, map[string]string{
		"docs/USAGE.md":    "| TASK_NOT_FOUND | 404 | no such task |\n\nQUEUE_FULL exists but is never emitted.\n",
		"pkg/api/tasks.go": "package api\n\nfunc emit() { _ = CodeTaskNotFound }\n",
	})
	var rp report
	checkErrorCodeReachability(&rp)
	for _, w := range rp.warn {
		if strings.Contains(w, "QUEUE_FULL") {
			t.Fatalf("prose that names the unreachable code still warned: %v", rp.warn)
		}
	}
}

// ------------------------------------------------- admin listener documents --

// adminDocsFixture builds a repository whose archive ships the two documents
// that carry the admin route tables, with pkg/api/admin.go standing in for the
// real registrations. Both documents are in archives[].files, which is what
// makes them a customer-visible disagreement rather than a private note.
func adminDocsFixture(t *testing.T, adminGo, contract, usage string) {
	t.Helper()
	goreleaserFixture(t, `
archives:
  - id: loopworker
    files:
      - docs/USAGE.md
      - docs/api/api-reference.md
`, map[string]string{
		"pkg/api/admin.go":          adminGo,
		"docs/USAGE.md":             usage,
		"docs/api/api-reference.md": contract,
	})
}

const adminGoTwo = `package api

import "net/http"

func (s *APIServer) AdminHandler() http.Handler {
	router := chi.NewRouter()
	router.Get("/healthz", s.livenessProbe)
	router.Group(func(authed chi.Router) {
		authed.Get("/logs", s.adminLogs)
		authed.Method(http.MethodPost, "/shutdown", http.HandlerFunc(s.shutdown))
	})
	return router
}
`

func usageTable(rows string) string {
	return "# Usage\n\n| Route | Method | Purpose |\n|---|---|---|\n" + rows
}

func contractTable(rows string) string {
	return "# API\n\n| 方法 | 路径 | 说明 | 格式 |\n|---|---|---|---|\n" + rows
}

// The defect, unchanged: USAGE.md told operators /shutdown existed, and the
// contract document — the one that says what it answers with — did not mention
// it. A weaker rule ("some shipped document names it") passes on exactly this
// tree, because USAGE.md is still there saying the same thing. That is why the
// rule is one-directional.
func TestAdminRouteMissingFromTheContractDocumentIsRejected(t *testing.T) {
	adminDocsFixture(t, adminGoTwo,
		contractTable("| GET | /healthz | 存活探针 | 文本 |\n| GET | /logs | 日志 | JSON |\n"),
		usageTable("| `/logs` | GET | observer ring |\n| `/shutdown` | POST | graceful stop |\n"))
	errs := errorsFrom(checkAdminListenerDocs)
	if !hasErr(errs, "no documented contract for it") {
		t.Fatalf("a route the orientation guide advertises but the contract document omits slipped past: %v", errs)
	}
}

// A method the server does not serve is a client that gets 405 after reading
// the document. Both column orders occur in the shipped documents, so the
// method may sit before or after the path.
func TestAdminRouteMethodMismatchIsRejected(t *testing.T) {
	adminDocsFixture(t, adminGoTwo,
		contractTable("| GET | /healthz | probe | text |\n| GET | /logs | logs | JSON |\n| GET | /shutdown | graceful stop | JSON |\n"),
		usageTable("| `/logs` | GET | observer ring |\n"))
	errs := errorsFrom(checkAdminListenerDocs)
	if !hasErr(errs, "would call a verb that answers 405") {
		t.Fatalf("a documented verb the server does not serve slipped past: %v", errs)
	}
}

// The positive case, and the one that must not become noisy: both documents
// cover every route, the methods agree, and the reference documents one route
// the guide does not — which is legitimate, because /healthz is on both
// listeners and only the contract document lists it on the admin one.
func TestAdminRoutesFullyDocumentedPass(t *testing.T) {
	adminDocsFixture(t, adminGoTwo,
		contractTable("| GET | /healthz | 存活探针 | 文本 |\n| GET | /logs | observer 环形快照 | JSON |\n| POST | /shutdown | 优雅停机 | JSON |\n"),
		usageTable("| `/logs` | GET | observer ring |\n| `/shutdown` | POST | graceful stop |\n"))
	if errs := errorsFrom(checkAdminListenerDocs); len(errs) != 0 {
		t.Fatalf("a fully documented admin surface was rejected: %v", errs)
	}
}

// An empty parse must fail rather than pass. A check that reads nothing and
// finds nothing wrong is indistinguishable from a correct one until the thing
// it watches is renamed.
func TestAdminRouteTableParsingIsChecked(t *testing.T) {
	adminDocsFixture(t, "package api\n\nfunc (s *APIServer) AdminHandler() http.Handler { return nil }\n",
		contractTable("| GET | /healthz | probe | text |\n"),
		usageTable("| `/logs` | GET | observer ring |\n"))
	errs := errorsFrom(checkAdminListenerDocs)
	if !hasErr(errs, "would pass on an empty table") {
		t.Fatalf("an unparseable route table passed silently: %v", errs)
	}
}

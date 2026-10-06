package main

import (
	"os"
	"path/filepath"
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

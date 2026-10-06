package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildOneProfile produces a real coverage profile with the go tool, so the
// tests exercise the same path CI does instead of a hand-written fixture whose
// shape could drift from what `go test -coverprofile` actually emits.
func buildOneProfile(t *testing.T) string {
	t.Helper()
	goTool := goBinary(t)
	dir := t.TempDir()
	pkg := filepath.Join(dir, "cov")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Match the toolchain's own language version: a go.mod that names an older
	// release makes the go command demand a toolchain download it cannot do
	// offline, and the fixture fails for a reason that has nothing to do with
	// coverage.
	goVersion := strings.TrimPrefix(strings.TrimSpace(goEnv(t, "GOVERSION")), "go")
	write("go.mod", "module covfixture\n\ngo "+goVersion+"\n")
	// A function with two blocks, only one of which the test reaches.
	write("a.go", "package cov\n\nfunc Used() int {\n\tx := 1\n\tif x > 0 {\n\t\treturn x\n\t}\n\treturn 0\n}\n\nfunc Unused() int {\n\treturn 42\n}\n")
	write("a_test.go", "package cov\n\nimport \"testing\"\n\nfunc TestUsed(t *testing.T) {\n\tif Used() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")

	// The profile lives inside the module, exactly as a real one does: the
	// package paths it records are resolved against the module root.
	out := filepath.Join(pkg, "cover.out")
	cmd := exec.Command(goTool, "test", "-coverprofile="+out, "./...")
	cmd.Dir = pkg
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -coverprofile: %v\n%s", err, b)
	}
	return out
}

func goBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go tool on PATH: %v", err)
	}
	return p
}

func goEnv(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command(goBinary(t), "env", name).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", name, err)
	}
	return string(out)
}

// TestMeasureAgreesWithGoTool is the whole point of the rewrite: the gate must
// report exactly what `go tool cover -func` reports. It used to report 7.51% for
// a tree the go tool called 80%, and after a first fix still reported 77.64%.
func TestMeasureAgreesWithGoTool(t *testing.T) {
	profile := buildOneProfile(t)
	got, err := measure(goBinary(t), profile)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}

	// Ask the go tool directly, from the module that owns the profile, and
	// require covergate's number to equal it. What is under test is covergate's
	// parsing and its "never silently green" behaviour - not the go tool.
	cmd := exec.Command(goBinary(t), "tool", "cover", "-func="+profile)
	cmd.Dir = moduleDirFor(profile)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go tool cover: %v", err)
	}
	want := parseTotalLine(t, string(out))

	if got != want {
		t.Errorf("covergate measured %.1f%% but `go tool cover -func` says %.1f%% — the gate and the tool must never disagree", got, want)
	}
	if want <= 0 || want >= 100 {
		t.Errorf("fixture should be partially covered, got %.1f%%", want)
	}
}

// TestMeasureRejectsAnUnusableProfile keeps the "never silently green" rule: a
// profile the go tool cannot read must be a harness error (exit 2), not a 0%
// that fails the gate for the wrong reason.
func TestMeasureRejectsAnUnusableProfile(t *testing.T) {
	goTool := goBinary(t)
	empty := filepath.Join(t.TempDir(), "empty.out")
	if err := os.WriteFile(empty, []byte("mode: atomic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := measure(goTool, empty); err == nil {
		t.Error("a profile with no blocks must be an error, not 0%")
	}
	if _, err := measure(goTool, filepath.Join(t.TempDir(), "missing.out")); err == nil {
		t.Error("a missing profile must be an error, not 0%")
	}
}

// TestCoverOnePackageMatchesTheGoTool guards the per-package floor. The floor is
// the reason this tool exists in its second mode: without it, "cmd layer >= 60%"
// in the acceptance criteria had no machine check at all, so it sat red for
// weeks while every build passed.
func TestCoverOnePackageMatchesTheGoTool(t *testing.T) {
	profile := buildOneProfile(t)

	// The fixture module holds a single package at its root, so the package path
	// is "." - which also exercises resolving a relative path against cmd.Dir.
	got, err := coverOnePackage(goBinary(t), ".", profile)
	if err != nil {
		t.Fatalf("coverOnePackage: %v", err)
	}

	// Same package, same toolchain, asked directly.
	cmd := exec.Command(goBinary(t), "test", "-covermode=atomic", "-count=1", ".")
	cmd.Dir = moduleDirFor(profile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test -cover: %v\n%s", err, out)
	}
	want := percentAfterCoverage(t, lastLine(string(out)))

	if got != want {
		t.Errorf("covergate measured %.1f%% but `go test -cover` says %.1f%%", got, want)
	}
	// The fixture has one used and one unused function, so it must land between.
	if got <= 0 || got >= 100 {
		t.Errorf("fixture should be partially covered, got %.1f%%", got)
	}
}

// TestCoverOnePackageTreatsUntestedPackagesAsZero is the rule that stops a real
// gap hiding in a green build: a package with no test files measures 0%, not
// "skip". An untested package listed in -pkg is a gate failure, by design.
func TestCoverOnePackageTreatsUntestedPackagesAsZero(t *testing.T) {
	profile := buildOneProfile(t)

	untested := filepath.Join(moduleDirFor(profile), "untested")
	if err := os.MkdirAll(untested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(untested, "b.go"),
		[]byte("package untested\n\nfunc Never() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := coverOnePackage(goBinary(t), "./untested", profile)
	if err != nil {
		t.Fatalf("coverOnePackage: %v", err)
	}
	if got != 0 {
		t.Errorf("a package with no test files measured %.1f%%; it must be 0 so the floor fails", got)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// percentAfterCoverage reads the number out of a `go test -cover` status line.
// The existing parsePercent takes the last whitespace-separated field, which is
// right for `go tool cover -func` ("file:line:\tfunc\t60.0%") and wrong here
// ("ok\tcovfixture\t0.166s\tcoverage: 60.0% of statements" ends in "statements").
func percentAfterCoverage(t *testing.T, line string) float64 {
	t.Helper()
	at := strings.LastIndex(line, "coverage:")
	if at < 0 {
		t.Fatalf("no coverage in %q", line)
	}
	rest := line[at+len("coverage:"):]
	text := strings.TrimSpace(strings.SplitN(rest, "of", 2)[0])
	v, err := strconv.ParseFloat(strings.TrimSuffix(text, "%"), 64)
	if err != nil {
		t.Fatalf("cannot read the coverage out of %q: %v", line, err)
	}
	return v
}

func parseTotalLine(t *testing.T, out string) float64 {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "total:") {
			return parsePercent(t, line)
		}
	}
	t.Fatalf("no total line in:\n%s", out)
	return 0
}

func parsePercent(t *testing.T, line string) float64 {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) == 0 {
		t.Fatalf("cannot parse %q", line)
	}
	text := strings.TrimSuffix(fields[len(fields)-1], "%")
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		t.Fatalf("cannot parse %q: %v", line, err)
	}
	return v
}

// Command covergate enforces a minimum total test-coverage percentage.
//
// The old `make test-coverage` wrote an HTML report and asserted nothing, so
// coverage could silently fall to zero. This tool reads a coverage profile and
// fails (exit 1) when total coverage < -min, printing the number it measured.
//
// The number comes from `go tool cover -func`, not from a hand-rolled parse of
// the profile. That is deliberate. A profile written by
// `go test -coverpkg=./...` repeats every package's blocks once per test binary,
// so whoever reads it has to merge those repeats - and this tool got that wrong
// twice in a row: summing the blocks reported 7.51% for a tree the go tool calls
// 80%, and merging by source location while keeping the highest hit count
// reported 77.64%. Both were "reasonable" implementations of a rule nobody had
// written down. Asking the go tool removes the guess: the gate and the tool a
// developer runs by hand can no longer disagree.
//
// It also enforces a per-package floor (-pkg / -pkg-min) for the packages that
// ship. That floor is measured by asking `go test -cover` for each package
// rather than by summing profile blocks, for the reason in the paragraph above:
// there is no profile arithmetic that reliably reproduces a per-package number,
// and a gate built on arithmetic that only usually agrees is a gate that
// sometimes waves through a regression.
//
// A package with no test files measures 0%, not "skip". A shipped binary with no
// tests is zero percent covered; treating it as "not applicable" is how a real
// gap hides inside a green build.
//
// Usage:
//
//	go run ./covergate -file ../../coverage.out -min 80
//	go run ./covergate -pkg ./cmd/loopworker,./cmd/loopctl -pkg-min 60
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	file := flag.String("file", "coverage.out", "coverage profile produced by `go test -coverprofile`")
	min := flag.Float64("min", 80, "minimum allowed total coverage, percent")
	goTool := flag.String("go", "go", "the go command to measure with (must be the same toolchain that wrote the profile)")
	pkgs := flag.String("pkg", "", "comma-separated packages that must each meet -pkg-min (e.g. ./cmd/loopworker,./cmd/loopctl)")
	pkgMin := flag.Float64("pkg-min", 60, "minimum allowed coverage per -pkg, percent")
	flag.Parse()

	if strings.TrimSpace(*pkgs) == "" {
		total, err := measure(*goTool, *file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "covergate: %v\n", err)
			os.Exit(2) // harness error, not a gate failure — never silently green
		}

		fmt.Printf("covergate: total coverage = %.1f%% (threshold %.1f%%, profile %s)\n", total, *min, *file)
		if total < *min {
			fmt.Printf("covergate: FAIL — %.1f%% < required %.1f%%\n", total, *min)
			fmt.Println("covergate: add tests; do not lower -min in CI to get green.")
			fmt.Println("covergate: per-package detail: go tool cover -html=" + *file)
			os.Exit(1)
		}
		fmt.Printf("covergate: PASS — %.1f%% >= %.1f%%\n", total, *min)
		return
	}

	failed := false
	for _, pkg := range strings.Split(*pkgs, ",") {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		pct, err := coverOnePackage(*goTool, pkg, *file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "covergate: %v\n", err)
			os.Exit(2)
		}
		verdict := "PASS"
		if pct < *pkgMin {
			verdict = "FAIL"
			failed = true
		}
		fmt.Printf("covergate: %-28s %6.1f%%  (floor %.1f%%)  %s\n", pkg, pct, *pkgMin, verdict)
	}
	if failed {
		fmt.Printf("covergate: FAIL — at least one shipped package is below %.1f%%\n", *pkgMin)
		fmt.Println("covergate: add tests; do not lower -pkg-min, and do not drop a package")
		fmt.Println("covergate: from the list to make this gate pass.")
		os.Exit(1)
	}
	fmt.Printf("covergate: PASS — every listed package >= %.1f%%\n", *pkgMin)
}

// coverOnePackage asks the go tool what it thinks of one package.
//
// It runs `go test -cover` rather than reading the profile, so the number is
// the same one CI prints and there is no per-package aggregation rule for this
// tool to get wrong. The profile path is accepted only to locate the module root.
func coverOnePackage(goTool, pkg, profile string) (float64, error) {
	dir := moduleDirFor(profile)
	cmd := exec.Command(goTool, "test", "-covermode=atomic", "-count=1", pkg)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		// A build failure is a harness error: the gate cannot judge coverage of
		// code that does not compile, and must not report it as 0%.
		return 0, fmt.Errorf("go test -cover %s: %v\n%s", pkg, err, strings.TrimSpace(text))
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "coverage:") {
			continue
		}
		at := strings.LastIndex(line, "coverage:")
		rest := line[at+len("coverage:"):]
		pct := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.SplitN(rest, "of", 2)[0]), "%"))
		v, perr := strconv.ParseFloat(pct, 64)
		if perr != nil {
			return 0, fmt.Errorf("cannot read the coverage out of %q: %w", line, perr)
		}
		return v, nil
	}
	// `go test -cover` prints "?  pkg  [no test files]" with no coverage line.
	// That is 0% covered, and for a shipped package that is the honest answer.
	return 0, nil
}

// moduleDirFor walks up from the profile to the directory holding go.mod.
// `go tool cover` resolves the package paths recorded in a profile against the
// working directory, so it has to run from inside the module that produced the
// profile - not from wherever covergate happens to be invoked, and not from this
// process's own module when a test profile comes from a fixture module.
func moduleDirFor(profile string) string {
	dir := filepath.Dir(profile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(profile)
		}
		dir = parent
	}
}

// measure asks the go tool for the total and returns it as a percentage.
func measure(goTool, profile string) (float64, error) {
	if _, err := os.Stat(profile); err != nil {
		return 0, fmt.Errorf("open profile: %w", err)
	}
	abs, err := filepath.Abs(profile)
	if err != nil {
		return 0, fmt.Errorf("resolve profile path: %w", err)
	}
	cmd := exec.Command(goTool, "tool", "cover", "-func="+abs)
	cmd.Dir = moduleDirFor(abs)
	out, err := cmd.Output()
	if err != nil {
		// Surface the go tool's own message: "profile is empty" and "no such
		// file" are both far more useful than this wrapper's guess at the cause.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return 0, fmt.Errorf("go tool cover: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return 0, fmt.Errorf("go tool cover: %w", err)
	}
	// `go tool cover` answers "total: 0.0%" for a profile with no instrumented
	// blocks at all, which would fail the gate while looking exactly like
	// "nobody wrote any tests". That is a broken harness, not a coverage
	// regression, and the two deserve different exit codes. A real report lists
	// at least one "<file>.go:<line>.<col>,<line>.<col>" row.
	if !strings.Contains(string(out), ".go:") {
		return 0, fmt.Errorf("%q contains no instrumented blocks — did `go test -coverprofile` actually run?", profile)
	}
	for _, line := range strings.Split(string(out), "\n") {
		// "total:\t(statements)\t80.3%"
		if !strings.HasPrefix(line, "total:") {
			continue
		}
		pct := strings.TrimSpace(line[strings.LastIndexByte(line, '\t')+1:])
		pct = strings.TrimSuffix(pct, "%")
		v, err := strconv.ParseFloat(pct, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot read the total out of %q: %w", line, err)
		}
		return v, nil
	}
	return 0, fmt.Errorf("%q reported no total line — was it produced by `go test -coverprofile`?", profile)
}

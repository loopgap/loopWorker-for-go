// Command relcheck verifies that the release/delivery plumbing describes the
// repository as it actually is. Every check here exists because a mismatch was
// previously shipped (or was about to be): the Dockerfile pinned golang:1.21
// while go.mod demanded 1.26.1; CI hardcoded a Go matrix that had drifted;
// .dockerignore excluded the UI assets the server embeds; a CGO-only SQLite
// driver was combined with CGO_ENABLED=0 (compiles, then panics at DB open);
// and LICENSE named a licensor that matches no identity in git history.
//
// Stdlib only, so it runs wherever Go runs — including Windows, where there is
// neither bash nor make.
//
// Usage:
//
//	go run ./relcheck -root ../..            # PR gate: placeholders are warnings
//	go run ./relcheck -root ../.. -strict    # release gate: placeholders fail
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

var (
	strict    = flag.Bool("strict", false, "treat TODO(owner)/placeholder findings as failures (release workflow)")
	root      = flag.String("root", ".", "repository root")
	skipGoVer = flag.Bool("skip-go-version", false, "do not assert the local Go toolchain satisfies go.mod")
)

type report struct {
	errs []string
	warn []string
}

func (r *report) errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }
func (r *report) warnf(f string, a ...any)  { r.warn = append(r.warn, fmt.Sprintf(f, a...)) }

func main() {
	flag.Parse()
	var rp report

	module, goDirective := readGoMod(filepath.Join(*root, "go.mod"))
	if module == "" {
		rp.errorf("go.mod: no `module` line — ldflags and install paths cannot be validated")
	}
	if goDirective == "" {
		rp.errorf("go.mod: no `go` line — CI cannot derive the toolchain version")
	}
	fmt.Printf("relcheck: module=%q go=%q (local toolchain %s)\n", module, goDirective, runtime.Version())

	checkGoreleaserLDFlags(&rp, module)
	checkDockerfile(&rp, goDirective)
	checkWorkflowGoVersions(&rp, goDirective)
	checkDockerIgnoreEmbeds(&rp)
	checkCgoClaims(&rp)
	checkCommandDirs(&rp)
	checkHealthRoute(&rp)
	checkPlaceholders(&rp)
	checkLicensor(&rp)
	checkToolchain(&rp, goDirective)

	for _, w := range rp.warn {
		fmt.Printf("  WARN  %s\n", w)
	}
	for _, e := range rp.errs {
		fmt.Printf("  ERROR %s\n", e)
	}
	fmt.Printf("relcheck: %d error(s), %d warning(s)\n", len(rp.errs), len(rp.warn))
	if len(rp.errs) > 0 {
		os.Exit(1)
	}
	if *strict && len(rp.warn) > 0 {
		fmt.Println("relcheck: -strict given and placeholders/TODOs remain — refusing to publish")
		os.Exit(1)
	}
	fmt.Println("relcheck: PASS")
}

// ---------------------------------------------------------------- utilities --

func lines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, sc.Err()
}

func readGoMod(path string) (module, goDirective string) {
	ls, err := lines(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relcheck: cannot read go.mod: %v\n", err)
		return "", ""
	}
	for _, l := range ls {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "module "):
			module = strings.TrimSpace(strings.TrimPrefix(l, "module"))
		case strings.HasPrefix(l, "go "):
			goDirective = strings.TrimSpace(strings.TrimPrefix(l, "go "))
		}
	}
	return module, goDirective
}

// majorMinor reduces "1.26.1" to "1.26".
func majorMinor(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

func goFiles(roots ...string) []string {
	var out []string
	for _, r := range roots {
		filepath.Walk(r, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if strings.HasSuffix(p, ".go") {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

// ------------------------------------------------------------------ checks --

// checkGoreleaserLDFlags asserts every `-X` in .goreleaser.yaml targets
// <module>/version.<Var> and that <Var> exists in the version package. A typo
// here is invisible until a customer's `loopworker version` prints "dev".
func checkGoreleaserLDFlags(rp *report, module string) {
	path := filepath.Join(*root, ".goreleaser.yaml")
	ls, err := lines(path)
	if err != nil {
		rp.errorf("%s: %v", path, err)
		return
	}
	re := regexp.MustCompile(`-X\s+([A-Za-z0-9_./\-]+)/version\.([A-Za-z]+)=`)
	var found int
	for _, l := range ls {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		found++
		if module != "" && m[1] != module {
			rp.errorf(".goreleaser.yaml ldflags use import prefix %q but go.mod declares module %q", m[1], module)
		}
		if !versionVarExists(filepath.Join(*root, "version"), m[2]) {
			rp.errorf(".goreleaser.yaml injects version.%s, which does not exist in the version package", m[2])
		}
	}
	if found == 0 {
		rp.errorf(".goreleaser.yaml injects no version ldflags — released binaries would report Version=dev")
	}
	fmt.Printf("relcheck: .goreleaser.yaml ldflags checked (%d)\n", found)
}

func versionVarExists(dir, name string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	re := regexp.MustCompile(`\b` + name + `\b\s*(=|\[\])`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		ls, err := lines(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, l := range ls {
			if re.MatchString(l) {
				return true
			}
		}
	}
	return false
}

// checkDockerfile: builder tag must satisfy go.mod, no CGO without a compiler,
// and a HEALTHCHECK must exist. These were the four reasons the old image
// could not build or could not be trusted by an orchestrator.
func checkDockerfile(rp *report, goDirective string) {
	path := filepath.Join(*root, "Dockerfile")
	ls, err := lines(path)
	if err != nil {
		rp.errorf("%s: %v", path, err)
		return
	}
	var builderTag string
	var runtimeTags []string
	var cgo1, healthcheck, userSet bool
	reFrom := regexp.MustCompile(`^FROM\s+(\S+)(\s+AS\s+(\w+))?`)
	reArg := regexp.MustCompile(`^ARG\s+(\w+)(?:=(\S*))?$`)
	reVar := regexp.MustCompile(`\$\{(\w+)\}`)
	// ARG defaults declared before the first FROM are in scope inside FROM
	// lines. Without this the builder tag reads as the literal string
	// "golang:${GO_VERSION}-alpine" and every pin looks like a drift.
	argVals := map[string]string{}
	for _, l := range ls {
		if m := reArg.FindStringSubmatch(strings.TrimSpace(l)); m != nil && m[2] != "" {
			argVals[m[1]] = m[2]
		}
	}
	for _, l := range ls {
		// Comment lines describe the old bugs this tool exists to catch ("the
		// builder was golang:1.21-alpine", "CGO_ENABLED=1 with no gcc"). Read
		// them as prose, not as configuration.
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if m := reFrom.FindStringSubmatch(t); m != nil {
			img := reVar.ReplaceAllStringFunc(m[1], func(v string) string {
				if s, ok := argVals[strings.Trim(v, "${}")]; ok {
					return s
				}
				return v
			})
			if strings.HasSuffix(img, "-builder") || strings.Contains(img, "golang:") || m[3] == "builder" {
				builderTag = img
			} else {
				runtimeTags = append(runtimeTags, img)
			}
		}
		if strings.Contains(t, "CGO_ENABLED=1") {
			cgo1 = true
		}
		if strings.HasPrefix(t, "HEALTHCHECK") {
			healthcheck = true
		}
		if strings.HasPrefix(t, "USER ") && !strings.HasSuffix(t, "root") {
			userSet = true
		}
	}
	if builderTag == "" {
		rp.errorf("Dockerfile: no golang:<tag> builder stage found — Go version is not pinned/derivable")
		return
	}
	if goDirective != "" {
		want := majorMinor(goDirective)
		tagVer := builderTag[strings.Index(builderTag, ":")+1:]
		switch {
		case strings.Contains(tagVer, "${"):
			// Sourced from an env var the Dockerfile never defaults. Cannot be
			// checked here; the image build itself is the check.
			rp.warnf("Dockerfile builder %s pins its Go version from an unset variable — pass ARG GO_VERSION=<go %s> or the build will fail", builderTag, goDirective)
		case !strings.HasPrefix(tagVer, want) && !strings.HasPrefix(tagVer, goDirective):
			rp.errorf("Dockerfile builder is %s but go.mod requires go %s — image build would fail", builderTag, goDirective)
		default:
			fmt.Printf("relcheck: Dockerfile builder %s satisfies go.mod go %s\n", builderTag, goDirective)
		}
	}
	if cgo1 {
		rp.errorf("Dockerfile sets CGO_ENABLED=1 — needs gcc/build-base AND breaks the static binary promise; use CGO_ENABLED=0")
	}
	if !healthcheck {
		rp.errorf("Dockerfile has no HEALTHCHECK — a crash-looping container looks healthy to Docker/K8s")
	}
	if !userSet {
		rp.errorf("Dockerfile has no non-root USER")
	}
	eol := map[string]string{"alpine:3.19": "EOL", "alpine:3.18": "EOL", "alpine:3.17": "EOL"}
	for _, t := range runtimeTags {
		if why, bad := eol[t]; bad {
			rp.errorf("Dockerfile runtime base %s is %s — pin a supported base", t, why)
		}
	}
	fmt.Printf("relcheck: Dockerfile runtime base(s) %v\n", runtimeTags)
}

// checkWorkflowGoVersions forbids hardcoded Go versions in CI.
func checkWorkflowGoVersions(rp *report, goDirective string) {
	dir := filepath.Join(*root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		rp.errorf("%s: %v", dir, err)
		return
	}
	reHard := regexp.MustCompile(`go-version:\s*['"]?(\d+\.\d+)['"]?`)
	reMatrix := regexp.MustCompile(`go-version:\s*\[([^\]]+)\]`)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		ls, err := lines(p)
		if err != nil {
			rp.errorf("read %s: %v", e.Name(), err)
			continue
		}
		joined := strings.Join(ls, "\n")
		for i, l := range ls {
			if m := reMatrix.FindStringSubmatch(l); m != nil {
				rp.errorf("%s:%d hardcodes a Go version matrix (%s) — drift from go.mod is guaranteed; derive it from go.mod", e.Name(), i+1, m[1])
			}
			if m := reHard.FindStringSubmatch(l); m != nil && goDirective != "" {
				if majorMinor(m[1]) != majorMinor(goDirective) {
					rp.errorf("%s:%d pins go %q while go.mod declares %q", e.Name(), i+1, m[1], goDirective)
				}
			}
		}
		if !strings.Contains(joined, "go-version-file") && !strings.Contains(joined, "go.mod") {
			rp.warnf("%s does not source its Go version from go.mod (use setup-go `go-version-file: go.mod`)", e.Name())
		}
	}
}

// checkDockerIgnoreEmbeds: .dockerignore must not hide go:embed inputs.
func checkDockerIgnoreEmbeds(rp *report) {
	ls, err := lines(filepath.Join(*root, ".dockerignore"))
	if err != nil {
		rp.errorf(".dockerignore: %v", err)
		return
	}
	// Docker's rule is last-match-wins, and a leading "!" means "include".
	// Collapsing that to "does any pattern match" made every negated entry read
	// as an exclusion, so a correct .dockerignore (exclude dist/, then
	// !pkg/api/dist back in) failed the gate.
	type pattern struct {
		text   string // slash form, trailing "/" and "/**" stripped
		negate bool
	}
	var pats []pattern
	for _, l := range ls {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		p := pattern{}
		if strings.HasPrefix(l, "!") {
			p.negate, l = true, l[1:]
		}
		l = strings.TrimSuffix(l, "/")
		p.text = strings.TrimSuffix(l, "/**")
		pats = append(pats, p)
	}
	// excludedBy reports the last pattern that decides p's fate, or "" when
	// p is in the build context.
	excludedBy := func(p string) string {
		segs := strings.Split(p, "/")
		decision, decided := pattern{}, false
		for _, pat := range pats {
			if strings.Contains(pat.text, "*") {
				continue // globs like *.md stay a review item, not a gate
			}
			hit := pat.text == p || strings.HasPrefix(p, pat.text+"/")
			if !hit {
				for _, s := range segs {
					if s == pat.text {
						hit = true
						break
					}
				}
			}
			if hit {
				decision, decided = pat, true
			}
		}
		if decided && !decision.negate {
			return decision.text
		}
		return ""
	}

	// Collect every go:embed target in the repo.
	rootSlash := filepath.ToSlash(*root)
	type embedTarget struct {
		importingFile string
		path          string // repo-relative, slash form
		exists        bool
	}
	var targets []embedTarget
	reEmbed := regexp.MustCompile(`go:embed\s+(?:all:)?([^\s"]+)`)
	for _, f := range goFiles(filepath.Join(*root, "pkg"), filepath.Join(*root, "internal"), filepath.Join(*root, "cmd")) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range reEmbed.FindAllStringSubmatch(string(b), -1) {
			for _, one := range strings.Split(m[1], ",") {
				rel := filepath.ToSlash(filepath.Join(filepath.Dir(f), one))
				rel = strings.TrimPrefix(rel, rootSlash+"/")
				_, statErr := os.Stat(filepath.ToSlash(filepath.Join(*root, rel)))
				targets = append(targets, embedTarget{filepath.Base(f), rel, statErr == nil})
			}
		}
	}

	for _, t := range targets {
		if pat := excludedBy(t.path); pat != "" {
			if !t.exists {
				rp.errorf("go:embed target %q (from %s) does not exist on disk AND is excluded by .dockerignore pattern %q",
					t.path, t.importingFile, pat)
			} else {
				rp.errorf(".dockerignore excludes %q, which contains the go:embed target %q — the container image cannot contain assets the binary embeds at build time",
					pat, t.path)
			}
		}
	}
	fmt.Printf("relcheck: .dockerignore patterns=%d, go:embed targets=%d checked\n", len(pats), len(targets))
}

// checkCgoClaims: CGO_ENABLED=0 everywhere is only honest if nothing in the
// module graph needs cgo. gorm.io/driver/sqlite (mattn/go-sqlite3) does: such a
// binary builds fine and then panics the first time it opens the database.
func checkCgoClaims(rp *report) {
	ls, err := lines(filepath.Join(*root, "go.mod"))
	if err != nil {
		rp.errorf("go.mod: %v", err)
		return
	}
	cgoOnly := []string{"github.com/mattn/go-sqlite3", "gorm.io/driver/sqlite"}
	var hit []string
	for _, l := range ls {
		if strings.HasPrefix(l, "require") || strings.Contains(l, "indirect") {
			// still fine to match; kept for clarity
		}
		for _, d := range cgoOnly {
			if strings.Contains(l, d+" ") {
				hit = append(hit, d)
			}
		}
	}
	if len(hit) > 0 {
		msg := fmt.Sprintf("cgo-only driver(s) still in go.mod: %s — a CGO_ENABLED=0 build panics when opening the DB. Land the pure-Go driver (modernc.org/sqlite) before publishing static artifacts",
			strings.Join(unique(hit), ", "))
		if *strict {
			rp.errorf("%s", msg)
		} else {
			rp.warnf("%s", msg)
		}
		return
	}
	fmt.Println("relcheck: no cgo-only sqlite driver in go.mod — CGO_ENABLED=0 promise holds")
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// checkCommandDirs: Makefile / build.ps1 / CI enumerate cmd/* binaries; a stale
// name there breaks the release for a reason that looks unrelated.
func checkCommandDirs(rp *report) {
	entries, err := os.ReadDir(filepath.Join(*root, "cmd"))
	if err != nil {
		rp.errorf("cmd/: %v", err)
		return
	}
	present := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			present[e.Name()] = true
		}
	}
	declared := map[string]bool{}
	for _, f := range []string{"Makefile", filepath.Join(".release", "build.ps1")} {
		ls, err := lines(filepath.Join(*root, f))
		if err != nil {
			rp.warnf("%s not readable: %v", f, err)
			continue
		}
		re := regexp.MustCompile(`\b(loop[a-z]+)\b`)
		for _, l := range ls {
			for _, m := range re.FindAllStringSubmatch(l, -1) {
				declared[m[1]] = true
			}
		}
	}
	delete(declared, "loopworker-ci") // commit author name in .goreleaser.yaml, not a command
	delete(declared, "loopworker")
	if !present["loopworker"] {
		rp.errorf("cmd/loopworker is missing — that is the product binary")
	}
	for d := range declared {
		if !present[d] {
			rp.errorf("Makefile/.release/build.ps1 reference cmd/%s which does not exist", d)
		}
	}
	fmt.Printf("relcheck: cmd/* present=%v, referenced-by-build-scripts=%v\n", keys(present), keys(declared))
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkHealthRoute: HEALTHCHECK and both smoke tests target
// GET /api/v1/health. If the route moves, fail here, not in a timeout.
func checkHealthRoute(rp *report) {
	ls, err := lines(filepath.Join(*root, "pkg", "api", "api.go"))
	if err != nil {
		rp.warnf("pkg/api/api.go not readable: %v", err)
		return
	}
	inV1, found := false, false
	for _, l := range ls {
		if strings.Contains(l, `"/api/v1"`) {
			inV1 = true
		}
		if inV1 && strings.Contains(l, `Get("/health"`) {
			found = true
		}
	}
	if !found {
		rp.errorf(`no Get("/health") inside the /api/v1 route group, but Dockerfile HEALTHCHECK and the smoke gates require GET /api/v1/health`)
	} else {
		fmt.Println("relcheck: GET /api/v1/health exists — HEALTHCHECK/smoke target verified")
	}
}

// checkPlaceholders: never publish a release page full of TODO(owner).
func checkPlaceholders(rp *report) {
	files := []string{".goreleaser.yaml", "LICENSE", "SECURITY.md", "SUPPORT.md", "NOTICE",
		filepath.Join(".github", "CODEOWNERS")}
	re := regexp.MustCompile(`TODO\(owner\)|<owner>|todo-owner@example\.invalid|TODO-owner`)
	for _, f := range files {
		ls, err := lines(filepath.Join(*root, f))
		if err != nil {
			rp.warnf("%s not readable: %v", f, err)
			continue
		}
		var hits int
		for _, l := range ls {
			if re.MatchString(l) {
				hits++
			}
		}
		if hits > 0 {
			rp.warnf("%s still has %d TODO(owner)/<owner> placeholder(s) — the human owner must confirm them before the first publish", f, hits)
		}
	}
}

// checkLicensor guards the "MIT © loopgap / author is loopgad" defect class.
func checkLicensor(rp *report) {
	ls, err := lines(filepath.Join(*root, "LICENSE"))
	if err != nil {
		rp.warnf("LICENSE not readable: %v", err)
		return
	}
	for _, l := range ls {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "Copyright (c)") {
			if regexp.MustCompile(`loopgap|loopgad`).MatchString(t) {
				rp.errorf("LICENSE names a git handle as licensor (%q) — that is not a legal entity", t)
			}
			if strings.Contains(t, "TODO(owner)") {
				rp.warnf("LICENSE copyright holder is still an unconfirmed placeholder: %q", t)
			}
			return
		}
	}
	rp.errorf("LICENSE has no `Copyright (c)` line — the MIT text is incomplete")
}

// checkToolchain asserts the local Go satisfies go.mod.
func checkToolchain(rp *report, goDirective string) {
	if *skipGoVer || goDirective == "" {
		return
	}
	ver := strings.TrimPrefix(runtime.Version(), "go")
	req, err1 := strconv.ParseFloat(majorMinor(goDirective), 64)
	got, err2 := strconv.ParseFloat(majorMinor(ver), 64)
	if err1 != nil || err2 != nil {
		rp.warnf("cannot compare local go %q with go.mod %q", ver, goDirective)
		return
	}
	if got < req {
		rp.errorf("local Go toolchain is %s but go.mod requires %s", ver, goDirective)
	} else {
		fmt.Printf("relcheck: local toolchain go%s satisfies go.mod go %s\n", ver, goDirective)
	}
}

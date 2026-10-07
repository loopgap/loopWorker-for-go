// Command relcheck verifies that the release/delivery plumbing describes the
// repository as it actually is. Every check here exists because a mismatch was
// previously shipped (or was about to be): the Dockerfile pinned golang:1.21
// while go.mod demanded 1.26.1; CI hardcoded a Go matrix that had drifted;
// .dockerignore excluded the UI assets the server embeds; a CGO-only SQLite
// driver was combined with CGO_ENABLED=0 (compiles, then panics at DB open);
// and LICENSE named a licensor that matches no identity in git history.
//
// The newest three are about what a customer receives rather than what CI
// builds. The release archive shipped documents that linked to files the
// archive did not contain, and documented API paths that no longer matched the
// router. Both resolved perfectly on GitHub, which is exactly why nothing
// noticed. The third is the same shape again: workflows asserting things about
// the filesystem — a path written for one working directory and run from
// another, a checkout list that excluded what a smoke script compiles — which
// is how two jobs came to be permanently red for reasons nobody would have
// guessed from the failure message.
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
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
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

// mdLinks matches an inline markdown link or image: the capture group is the
// target, with any title stripped. Deliberately not a CommonMark parser — the
// documents it reads are shipped product docs, and a regex that understands
// "[text](target)" and "[text](target \"title\")" is enough to catch the class
// of breakage that actually shipped.
var mdLinks = regexp.MustCompile(`\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)`)

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
	checkGoreleaserSources(&rp)
	checkShippedDocLinks(&rp)
	checkDocumentedRoutes(&rp)
	checkWorkflowPaths(&rp)
	checkReleaseScriptAgreement(&rp)
	checkDockerfile(&rp, goDirective)
	checkWorkflowGoVersions(&rp, goDirective)
	checkGoToolchainPerJob(&rp)
	checkDockerIgnoreEmbeds(&rp)
	checkAttributionChannels(&rp)
	checkDocumentedPorts(&rp)
	checkDocumentedConfigKeys(&rp)
	checkErrorRouteReferences(&rp)
	checkErrorCodeReachability(&rp)
	checkAdminListenerDocs(&rp)
	checkNoAPINamesInCode(&rp)
	checkCgoClaims(&rp)
	checkCommandDirs(&rp)
	checkHealthRoute(&rp)
	checkPlaceholders(&rp)
	checkSmokeScriptEnvVars(&rp)
	checkLicensor(&rp)
	checkNoticeDirectDependencies(&rp)
	checkToolchain(&rp, goDirective)

	for _, w := range rp.warn {
		fmt.Printf("  WARN  %s\n", w)
	}
	for _, e := range rp.errs {
		fmt.Printf("  ERROR %s\n", e)
	}
	fmt.Printf("relcheck: %d error(s), %d warning(s)\n", len(rp.errs), len(rp.warn))
	switch code := rp.gate(*strict); code {
	case 0:
		fmt.Println("relcheck: PASS")
	default:
		if len(rp.errs) == 0 {
			fmt.Println("relcheck: -strict given and placeholders/TODOs remain — refusing to publish")
		}
		os.Exit(code)
	}
}

// gate is the release decision, separated from main so it can be tested.
//
// This is the one line standing between a tree whose LICENSE names nobody and a
// published release carrying that name. An untested release gate is a gate that
// eventually stops firing: a refactor that moved this into main() would look
// correct in review and would ship a release with the placeholder still in it.
// The distinction that matters is that warnings alone pass on the PR gate and
// fail on the release gate — that is the whole reason the flag exists.
func (r *report) gate(strict bool) int {
	if len(r.errs) > 0 {
		return 1
	}
	if strict && len(r.warn) > 0 {
		return 1
	}
	return 0
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

// checkGoreleaserSources asserts that every source path .goreleaser.yaml names
// exists in this repository.
//
// `nfpms[].contents` and `archives[].files` both take a path or glob that nfpm
// and goreleaser expand against the *build machine's* filesystem. A path that
// does not exist here does not exist on any release runner either, so the
// release dies inside the packager — after the binaries and every archive are
// already built, leaving 0-byte .deb/.rpm files in dist/ that look exactly like
// real artifacts. `goreleaser check` does not catch it: that validates the
// schema, and `/usr/bin/loopworker` is a perfectly well-formed string.
//
// That is the defect this exists for. `src: /usr/bin/loopworker` was added
// because the binary has to land in /usr/bin — but goreleaser already installs
// every matched binary into `bindir` on its own, so the entry was both
// unnecessary and fatal.
//
// `type: symlink` is the one legitimate absolute source: there the value is a
// link target *inside* the package, not a file to read.
func checkGoreleaserSources(rp *report) {
	var checked int
	for _, section := range []string{"nfpms", "archives"} {
		ls, err := lines(filepath.Join(*root, ".goreleaser.yaml"))
		if err != nil {
			rp.errorf(".goreleaser.yaml: %v", err)
			return
		}
		sub, base := goreleaserSection(ls, section)
		for _, it := range goreleaserItems(sub, base) {
			if it.keys["type"] == "symlink" {
				continue
			}
			src, ok := it.keys["src"]
			if !ok && it.parent == "files" {
				src, ok = it.keys[""] // a bare path, as archives[].files writes them
			}
			if !ok || src == "" {
				continue
			}
			checked++
			if strings.Contains(src, "{{") {
				continue // the release runner expands the template
			}
			if filepath.IsAbs(src) || strings.HasPrefix(src, "/") {
				rp.errorf(".goreleaser.yaml:%d (nfpms) lists src: %s — an absolute source is globbed against the build machine, where it exists on no runner. Drop the entry (goreleaser installs binaries into bindir itself), or use type: symlink if you meant a link target", it.line, src)
				continue
			}
			matches, gerr := filepath.Glob(filepath.Join(*root, filepath.FromSlash(src)))
			if gerr != nil || len(matches) == 0 {
				rp.errorf(".goreleaser.yaml:%d (%s) lists src: %s, which matches nothing in this repository — the release fails in the packager after the archives are built, leaving 0-byte packages behind", it.line, section, src)
			}
		}
	}
	fmt.Printf("relcheck: .goreleaser.yaml source paths checked (%d)\n", checked)
}

// goreleaserItem is one `- ` entry, the keys nested under it, and the key that
// owns the list it sits in. The owner is what tells `- loopworker` under
// `ids:` (a build id) apart from `- LICENSE` under `files:` (a real path).
type goreleaserItem struct {
	line   int // 1-based line of the dash
	indent int // column of the dash
	parent string
	keys   map[string]string
}

// goreleaserSection returns the lines under one top-level key, stopping at the
// next top-level key, plus the 0-based file index of the first line returned so
// that findings can still be reported as real file line numbers. Scanning only
// the sections that need it keeps the rest of the file out of the parse
// entirely: the release-notes template embeds a bash block whose lines start
// with "--", and the whole file is wrapped in comments explaining the very
// mistake this check looks for. Prose must never be read as configuration — a
// permanently red gate gets switched off, which is worse than no gate.
func goreleaserSection(ls []string, key string) (sub []string, base int) {
	start := -1
	for i, raw := range ls {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if len(raw)-len(strings.TrimLeft(raw, " ")) != 0 {
			continue
		}
		if t == key+":" {
			start = i
		} else if start >= 0 {
			return ls[start+1 : i], start + 1
		}
	}
	if start < 0 {
		return nil, 0
	}
	return ls[start+1:], start + 1
}

// goreleaserItems lists the `- ` entries of one section with their nested keys.
// Indentation, not a YAML parser: relcheck is stdlib-only, and `goreleaser
// check` already validates the schema — all that is needed here is "does this
// path exist", which indentation answers.
func goreleaserItems(ls []string, base int) []goreleaserItem {
	type frame struct {
		indent int
		key    string
	}
	var (
		out     []goreleaserItem
		stack   []frame
		lastIdx = -1
	)
	indentOf := func(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }
	pop := func(to int) {
		for len(stack) > 0 && stack[len(stack)-1].indent >= to {
			stack = stack[:len(stack)-1]
		}
	}

	for i, raw := range ls {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		indent := indentOf(raw)

		if t == "-" || strings.HasPrefix(t, "- ") {
			pop(indent)
			it := goreleaserItem{line: base + i + 1, indent: indent, keys: map[string]string{}}
			if len(stack) > 0 {
				it.parent = stack[len(stack)-1].key
			}
			if body := strings.TrimSpace(strings.TrimPrefix(t, "-")); body != "" {
				if k, v, ok := yamlKV(body); ok {
					it.keys[k] = v
				} else {
					it.keys[""] = body // a bare scalar, e.g. `- LICENSE`
				}
			}
			out = append(out, it)
			lastIdx = len(out) - 1
			continue
		}

		k, v, ok := yamlKV(t)
		if !ok {
			continue
		}
		pop(indent)
		stack = append(stack, frame{indent: indent, key: k})
		if lastIdx >= 0 && out[lastIdx].indent < indent {
			out[lastIdx].keys[k] = v
		}
	}
	return out
}

// yamlKV splits `key: value`. ok is false for a bare scalar, which is how a
// path written without a key is recognised.
func yamlKV(s string) (key, val string, ok bool) {
	i := strings.Index(s, ":")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), unquote(strings.TrimSpace(s[i+1:])), true
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// checkReleaseScriptAgreement guards the two release conventions that live as
// plain text in three places each and had no check at all.
//
// The first is tool pinning. Every workflow pins goreleaser and syft itself, so
// bumping one and forgetting the other makes CI validate the pipeline with a
// different tool than the one that publishes it, and every local snapshot
// stop meaning what CI will do.
//
// The second is the snapshot command. build.ps1, release.yml and the header of
// .goreleaser.yaml each state it independently, and they had already drifted:
// build.ps1 skipped only "publish" while the other two skipped
// "publish,sbom,sign", so `-Task release` failed on every machine without syft
// and cosign installed, with "executable file not found in %PATH%" as the only
// clue. A comment in a YAML file agreeing with a PowerShell script is a
// coincidence, not a constraint.
// ------------------------------------------------------ workflow file paths --

// quotedSpans blanks the contents of single- and double-quoted spans. Without
// it a step's own error message counts as a reference: the canvas job says
// "pkg/api/dist is stale — run 'npm run build' in web/canvas", and both of
// those are named for a human, not for the filesystem.
var quotedSpans = regexp.MustCompile(`'[^']*'|"[^"]*"`)

// stepGeneratedFiles are paths a job writes earlier and reads later, so they
// cannot exist in a clean checkout. Each entry says which step produces it, so
// the list cannot grow by accident.
var stepGeneratedFiles = map[string]string{
	"coverage.out":  "written by the cross-package coverage step",
	"coverage.html": "written by the cover HTML step",
}

// checkWorkflowPaths asserts that a path named in a workflow step exists where
// that step will actually run.
//
// It exists because of two release-blocking bugs found in one sitting, both the
// same mistake: a workflow asserted something about the filesystem that nothing
// checked. The canvas job wrote `diff -r web/canvas/dist pkg/api/dist` while the
// job's working-directory was web/canvas, so both sides resolved to directories
// that do not exist and every pull request would have gone red with a message
// blaming the one thing that was fine. The verify-artifacts job checked out
// only `.release` and `docs` and then ran a smoke script that compiles
// examples/hello-plugin, which was not among them.
//
// Only steps whose effective working directory is inside the repository are
// checked: at the root, a repository-relative path is correct by definition,
// and the signal this check exists for is a path written for one base and run
// from another.
func checkWorkflowPaths(rp *report) {
	dir := filepath.Join(*root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		rp.errorf("%s: %v", dir, err)
		return
	}

	var checked int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		ls, err := lines(filepath.Join(dir, e.Name()))
		if err != nil {
			rp.errorf("read %s: %v", e.Name(), err)
			continue
		}
		checked += checkWorkflowFilePaths(rp, e.Name(), ls)
	}
	fmt.Printf("relcheck: workflow run-block paths checked (%d)\n", checked)
}

// checkWorkflowFilePaths walks one workflow and returns how many path references
// it resolved.
func checkWorkflowFilePaths(rp *report, name string, ls []string) int {
	job, jobWD := "", "" // current job and its defaults.run.working-directory
	stepWD := ""         // per-step override
	inRun, runIndent := false, 0
	checked := 0

	reset := func() { jobWD, stepWD, inRun = "", "", false }

	for i, raw := range ls {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if inRun {
			if trimmed == "" || indent <= runIndent {
				inRun = false
			} else {
				wd := stepWD
				if wd == "" {
					wd = jobWD
				}
				if wd != "" {
					checked += resolveRunPathRefs(rp, name, job, i+1, wd, trimmed)
				}
				continue
			}
		}

		switch {
		case indent == 2 && strings.HasSuffix(trimmed, ":"):
			job = strings.TrimSuffix(trimmed, ":")
			reset()
			if job == "" {
				job = "<root>"
			}
		case indent == 4 && trimmed == "defaults:":
			// the working-directory that follows belongs to the job
			jobWD, stepWD = "", ""
			for j := i + 1; j < len(ls); j++ {
				next := strings.TrimSpace(ls[j])
				nextIndent := len(ls[j]) - len(strings.TrimLeft(ls[j], " "))
				if next != "" && nextIndent <= 4 {
					break
				}
				if v, ok := strings.CutPrefix(next, "working-directory:"); ok {
					jobWD = strings.TrimSpace(v)
					break
				}
			}
		case indent == 6 && strings.HasPrefix(trimmed, "- "):
			// A new step. Its working-directory, if any, is the step's own.
			if stepWD != "" {
				stepWD = ""
			}
		case indent == 8 && strings.HasPrefix(trimmed, "working-directory:"):
			stepWD = strings.TrimSpace(strings.TrimPrefix(trimmed, "working-directory:"))
		case indent == 8 && isRunBlock(trimmed):
			inRun, runIndent = true, indent
		case indent == 8 && strings.HasPrefix(trimmed, "run: "):
			// A single-line `run:` is as common as a block in these workflows,
			// and skipping them is how the canvas job's own defect went unnoticed
			// through this check's first draft.
			wd := stepWD
			if wd == "" {
				wd = jobWD
			}
			if wd != "" {
				checked += resolveRunPathRefs(rp, name, job, i+1, wd, strings.TrimPrefix(trimmed, "run: "))
			}
		}
	}
	return checked
}

// isRunBlock reports whether a step's `run:` opens a block scalar, whose body
// follows on more-indented lines rather than on the same line.
func isRunBlock(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "run:") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "run:"))
	return rest == "|" || rest == ">" ||
		rest == "|-" || rest == ">-" || rest == "|+" || rest == ">+"
}

// pathRefTokens returns the tokens in one run-block line that could name a file
// or directory in the repository. Everything else is skipped deliberately:
// flags, variables, globs, URLs, and quoted text.
func pathRefTokens(line string) []string {
	stripped := quotedSpans.ReplaceAllString(line, " ")
	var out []string
	for _, tok := range strings.Fields(stripped) {
		if !strings.Contains(tok, "/") || strings.HasPrefix(tok, "-") ||
			strings.HasPrefix(tok, "$") || strings.ContainsAny(tok, "*?:\\\"'") {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// resolveRunPathRefs reports every path reference in one run-block line that
// does not exist under the step's working directory, and returns how many
// references the line carried.
func resolveRunPathRefs(rp *report, file, job string, lineNo int, wd, line string) int {
	tokens := pathRefTokens(line)
	for _, tok := range tokens {
		resolved := path.Clean(path.Join(wd, filepath.ToSlash(tok)))
		if resolved == "." || resolved == ".." {
			continue
		}
		if _, ok := stepGeneratedFiles[path.Base(resolved)]; ok {
			continue // produced earlier in the same job; the map documents which
		}
		if _, serr := os.Stat(filepath.Join(*root, filepath.FromSlash(resolved))); serr == nil {
			continue
		}
		rp.errorf("%s:%d (job %s) names %q, which does not exist at %s (the step's working-directory) — the step runs from there, so this resolves to a path that is not in the repository. A repository-relative path only works from the repository root.", file, lineNo, job, tok, resolved)
	}
	return len(tokens)
}

func checkReleaseScriptAgreement(rp *report) {
	// --- tool versions must agree across workflows -------------------------
	type pin struct {
		version string
		where   []string
	}
	pins := map[string]*pin{}
	dir := filepath.Join(*root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		rp.warnf("%s: %v", dir, err)
		return
	}
	// The trailing `(?:#.*)?` is not decoration. Without it a pin written as
	// `SYFT_VERSION: v1.9.0 # pinned by security review` does not match at all,
	// and a pin relcheck cannot see is a pin it can no longer keep consistent
	// across workflows — the drift check below would skip it in silence.
	rePin := regexp.MustCompile(`^\s*([A-Z][A-Z0-9_]*_VERSION)\s*:\s*(\S+)\s*(?:#.*)?$`)
	type decl struct{ file, name string }
	var decls []decl
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		ls, err := lines(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, l := range ls {
			m := rePin.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			decls = append(decls, decl{e.Name(), m[1]})
			p, ok := pins[m[1]]
			if !ok {
				p = &pin{version: m[2]}
				pins[m[1]] = p
			}
			p.where = append(p.where, e.Name())
			if p.version != m[2] {
				rp.errorf("tool %s is pinned as %s in %s but %s in %s — CI would validate the pipeline with a different tool than the one that publishes it",
					m[1], p.version, strings.Join(p.where, ", "), m[2], e.Name())
			}
		}
	}

	// A version pin that nothing reads is a claim about which tool CI uses, and
	// when the claim is false nothing finds out until the tool is invoked — at
	// which point the run has usually already spent minutes building.
	//
	// ci.yml carried `SYFT_VERSION: v1.9.0` for its whole life with no consumer
	// anywhere in the file: CI never installed syft at all. Meanwhile
	// .goreleaser.yaml handed syft a value its own PostLoad hook rejects, so every
	// real release would have died at "cataloging artifacts". The pin is what made
	// that invisible — reading the workflow said the tool version was under
	// control.
	//
	// The search is per file on purpose. Workflow `env:` is scoped to the file
	// that declares it, so a reference in the sibling workflow is not a consumer:
	// ci.yml can never see release.yml's block, and treating it as one is how this
	// rule would start reporting correct workflows.
	for _, d := range decls {
		ls, err := lines(filepath.Join(dir, d.file))
		if err != nil {
			continue
		}
		declRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(d.name) + `\s*:`)
		// \b is enough to keep SYFT_VERSION from being "found" inside
		// SYFT_VERSION_SHA; `_` is a word character, so a longer name built on
		// this one does not produce a boundary before `S`.
		refRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(d.name) + `\b`)
		refs := 0
		for _, l := range ls {
			if declRe.MatchString(l) {
				continue
			}
			refs += len(refRe.FindAllString(l, -1))
		}
		if refs == 0 {
			rp.errorf("%s pins %s but never reads it — a version nothing installs is a claim about CI that is not true; wire it into the job that uses the tool, or delete the pin",
				d.file, d.name)
		}
	}

	// --- the snapshot command must be stated identically -------------------
	reFlag := regexp.MustCompile(`--[a-z0-9][a-z0-9-]*(?:=[^\s"'|]+)?`)
	snapshotFlags := func(label, path string, wantLine func(string) bool) []string {
		ls, err := lines(filepath.Join(*root, filepath.FromSlash(path)))
		if err != nil {
			rp.warnf("%s not readable: %v", path, err)
			return nil
		}
		for _, l := range ls {
			if !wantLine(l) {
				continue
			}
			var out []string
			for _, f := range reFlag.FindAllString(l, -1) {
				if f == "--config" { // the path to the config is not the contract
					continue
				}
				out = append(out, f)
			}
			sort.Strings(out)
			return out
		}
		rp.errorf("%s: no `goreleaser release --snapshot` line found, so the local snapshot command and CI's dry run cannot be compared", path)
		return nil
	}

	local := snapshotFlags("build.ps1", ".release/build.ps1", func(l string) bool {
		return strings.Contains(l, "$gr release") && strings.Contains(l, "--snapshot")
	})
	ciDryRun := snapshotFlags("release.yml", ".github/workflows/release.yml", func(l string) bool {
		return strings.Contains(l, "args=release") && strings.Contains(l, "--snapshot")
	})
	documented := snapshotFlags(".goreleaser.yaml", ".goreleaser.yaml", func(l string) bool {
		t := strings.TrimSpace(l)
		return strings.HasPrefix(t, "#") && strings.Contains(t, "goreleaser release") && strings.Contains(t, "--snapshot")
	})

	baseline := local
	label := "build.ps1"
	if len(ciDryRun) > 0 {
		baseline, label = ciDryRun, "release.yml"
	}
	if len(baseline) > 0 {
		for _, other := range []struct {
			name  string
			flags []string
		}{{"build.ps1", local}, {".goreleaser.yaml docs", documented}} {
			if len(other.flags) == 0 {
				continue
			}
			if strings.Join(other.flags, " ") != strings.Join(baseline, " ") {
				rp.errorf("the snapshot command differs: %s uses [%s] but %s uses [%s] — a local `release` run then tests something the release pipeline never does",
					other.name, strings.Join(other.flags, " "), label, strings.Join(baseline, " "))
			}
		}
	}

	var seen []string
	for name := range pins {
		seen = append(seen, name)
	}
	sort.Strings(seen)
	fmt.Printf("relcheck: workflow tool pins checked (%d: %s)\n", len(seen), strings.Join(seen, ", "))
	join := func(f []string) string {
		if len(f) == 0 {
			return "(absent)"
		}
		return strings.Join(f, " ")
	}
	fmt.Printf("relcheck: snapshot command checked: build.ps1=[%s] release.yml=[%s] .goreleaser.yaml=[%s]\n",
		join(local), join(ciDryRun), join(documented))
}

// ------------------------------------------------------- shipped doc links --

// shippedDocs returns the set of paths in archives[].files, which is the only
// reliable statement of what a customer actually receives. A single list, so
// every shipped-document check agrees on the answer.
func shippedDocs() (map[string]bool, error) {
	ls, err := lines(filepath.Join(*root, ".goreleaser.yaml"))
	if err != nil {
		return nil, err
	}
	sub, _ := goreleaserSection(ls, "archives")
	shipped := map[string]bool{}
	for _, it := range goreleaserItems(sub, 0) {
		if it.parent != "files" {
			continue
		}
		if p, ok := it.keys[""]; ok && p != "" && !strings.Contains(p, "{{") {
			shipped[path.Clean(filepath.ToSlash(p))] = true
		}
	}
	if len(shipped) == 0 {
		return nil, fmt.Errorf("no archives[].files entries found")
	}
	return shipped, nil
}

// checkShippedDocLinks asserts that every relative link inside a document the
// release archive ships also resolves inside the archive.
//
// This exists because it shipped. README.md linked to AGENT-COLLABORATION-SPEC.md
// and docs/USAGE.md; both exist in the repository and neither was in
// archives[].files, so anyone who unpacked a release tarball and followed either
// link got a file that was not there. On GitHub the links resolve, which is
// exactly why CI never noticed. A product with no support desk cannot afford a
// dead link to its own user guide.
//
// Anchors, absolute URLs and anything inside a fenced code block are skipped:
// a link example in a shell snippet is text, not a reference.
func checkShippedDocLinks(rp *report) {
	shipped, err := shippedDocs()
	if err != nil {
		rp.errorf(".goreleaser.yaml: %v — cannot tell which documents ship, so document links cannot be checked", err)
		return
	}

	var docs, links int
	for doc := range shipped {
		if !strings.HasSuffix(doc, ".md") {
			continue
		}
		docs++
		body, rerr := os.ReadFile(filepath.Join(*root, filepath.FromSlash(doc)))
		if rerr != nil {
			rp.errorf("%s: %v", doc, rerr)
			continue
		}
		base := path.Dir(doc)
		inFence := false
		for i, raw := range strings.Split(string(body), "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			for _, m := range mdLinks.FindAllStringSubmatch(raw, -1) {
				target := strings.TrimSpace(m[1])
				if target == "" || strings.HasPrefix(target, "#") ||
					strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				if idx := strings.IndexAny(target, "#?"); idx >= 0 {
					target = target[:idx]
				}
				if target == "" {
					continue
				}
				links++
				resolved := path.Clean(path.Join(base, filepath.ToSlash(target)))
				if !shipped[resolved] {
					kind := "is not in archives[].files"
					if _, serr := os.Stat(filepath.Join(*root, filepath.FromSlash(resolved))); serr != nil {
						kind = "does not exist in this repository at all"
					}
					rp.errorf("%s:%d links to %s, which %s — a customer who only unpacked the release archive gets a dead link (it resolves on GitHub, which is why this went unnoticed)", doc, i+1, resolved, kind)
				}
			}
		}
	}
	fmt.Printf("relcheck: shipped document links checked (%d links across %d shipped .md)\n", links, docs)
}

// ------------------------------------------------- documented config keys --

// specPath pulls the key registry out of internal/config/spec.go. The format it
// matches is a literal in a file this repository owns, so a reformat is a
// visible event: the empty-set guard below turns "the regex stopped matching"
// into an error instead of a silent pass.
var specPath = regexp.MustCompile(`\{path:\s*"([a-z0-9_.]+)"`)

// configKeyRow matches one row of a shipped document's configuration table: a
// backticked dotted key in the first cell, an environment variable in the
// second. Scoped to that shape deliberately. Documents name plenty of dotted
// things that are not configuration keys — event types (`task.created`),
// response fields (`data.task`), filenames (`plugin.json`, `go.mod`) — and a
// check that flagged those would be deleted on its first day. Requiring the
// environment-variable cell is what distinguishes "the key you put in your
// config file" from every other dotted token in prose.
var configKeyRow = regexp.MustCompile("^\\|\\s*`([a-z][a-z0-9_]*(?:\\.[a-z0-9_]+)+)`\\s*\\|\\s*`?([A-Z][A-Z0-9_]*)")

// acceptedConfigKeys returns every path the loader registers.
func acceptedConfigKeys() ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(*root, "internal", "config", "spec.go"))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range specPath.FindAllStringSubmatch(string(raw), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out, nil
}

// checkDocumentedConfigKeys asserts that every key a shipped document's
// configuration table tells a customer to put in their file is one the loader
// accepts.
//
// This exists because it shipped. QUICKSTART's table said `log.level`; the
// registry entry is `logging.level` (its flat alias is `log_level`). The
// environment-variable column in that same row was correct, so the table looked
// entirely trustworthy. But `internal/config/load.go` rejects an unknown key
// outright, with a did-you-mean hint and the full accepted list — so a customer
// who copied the file-key column got a hard startup failure pointing at the
// very documentation they were reading. In a product with no support desk,
// that is the whole ticket.
func checkDocumentedConfigKeys(rp *report) {
	accepted, err := acceptedConfigKeys()
	if err != nil {
		rp.errorf("internal/config/spec.go: %v — cannot read the key registry, so documented keys cannot be checked", err)
		return
	}
	if len(accepted) == 0 {
		rp.errorf("internal/config/spec.go yielded no {path: \"...\" entries — the accepted set is empty, so this check would pass on any key at all")
		return
	}
	ok := map[string]bool{}
	for _, k := range accepted {
		ok[k] = true
	}

	shipped, err := shippedDocs()
	if err != nil {
		rp.errorf(".goreleaser.yaml: %v", err)
		return
	}

	var rows int
	for doc := range shipped {
		if !strings.HasSuffix(doc, ".md") || path.Base(doc) == "CHANGELOG.md" {
			continue
		}
		body, rerr := os.ReadFile(filepath.Join(*root, filepath.FromSlash(doc)))
		if rerr != nil {
			rp.errorf("%s: %v", doc, rerr)
			continue
		}
		for i, raw := range strings.Split(string(body), "\n") {
			m := configKeyRow.FindStringSubmatch(raw)
			if m == nil {
				continue
			}
			rows++
			if !ok[m[1]] {
				rp.errorf("%s:%d documents config key %q, which the loader does not accept (next to %s) — writing it makes the server refuse to start, and the error points back at this table", doc, i+1, m[1], m[2])
			}
		}
	}
	fmt.Printf("relcheck: documented config keys checked (%d table rows, %d accepted keys)\n", rows, len(accepted))
}

// checkNoAPINamesInCode: no string the server can send may present an `api.*`
// name as something the operator can set.
//
// The namespace is closed and unambiguous, which is why this check is a rule and
// not a heuristic. `api.max_body_bytes` and friends are field names on
// pkg/api.Config; the file the operator edits is read by internal/config, whose
// registry has no `api.` section at all — so a message saying "raise
// api.max_body_bytes in the config file" sends the reader to a key that, if they
// try it, makes the server refuse to start. The knob that really moves most of
// these is an environment variable, which is invisible in a config file and was
// documented nowhere until recently.
//
// Two of these shipped. The STREAM_LIMIT_REACHED message sent an operator to
// `api.max_streams_total`; the request-too-large message named
// `api.max_body_bytes` *in the config file*; and the input-ceiling message told
// them to raise `api.max_input_bytes`, which has no environment variable either
// and is a compile-time constant. The only correct move is to name the
// environment variable, or to say plainly that the value is fixed.
//
// Comments are out of scope, for the same reason as the route check: a comment
// may say whatever is true.
// apiConfigFields returns the lower-cased field names of pkg/api.Config, which
// is what makes this check exact rather than a guess about a namespace.
//
// The first version of this rule assumed `api.` was reserved for pkg/api knobs
// and matched `\bapi\.[a-z_]+\b` directly. Its first real run reported
// `api.openai` in two files, which is the host in https://api.openai.com. The
// prefix is not reserved. Deriving the names from the struct removes the whole
// question: a match is by construction a real field, so nothing else in the
// repository can trip it.
func apiConfigFields() (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(*root, "pkg", "api", "config.go"))
	if err != nil {
		return nil, err
	}
	src := string(raw)
	start := strings.Index(src, "type Config struct {")
	if start < 0 {
		return nil, fmt.Errorf("no `type Config struct` in pkg/api/config.go")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		return nil, fmt.Errorf("unterminated `type Config struct` in pkg/api/config.go")
	}
	fields := map[string]bool{}
	for _, line := range strings.Split(src[start:start+end], "\n")[1:] {
		m := apiConfigField.FindStringSubmatch(line)
		if m != nil {
			fields[camelToSnake(m[1])] = true
		}
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("pkg/api.Config has no exported fields; the name set would be empty")
	}
	return fields, nil
}

var apiConfigField = regexp.MustCompile(`^\t([A-Z][A-Za-z0-9]*)\s`)

// camelToSnake turns MaxBodyBytes into max_body_bytes, which is the form the
// name takes in prose. A plain lower-case is not enough: Go field names have no
// underscores, so "MaxBodyBytes" and "max_body_bytes" are different strings and
// the set silently matched nothing. Acronyms would need a dictionary
// (MaxURLLength -> max_url_length, not max_u_r_l_length); pkg/api.Config has
// none today, and a new one would show up as a gate that stops firing rather
// than as a wrong verdict.
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func checkNoAPINamesInCode(rp *report) {
	fields, err := apiConfigFields()
	if err != nil {
		rp.errorf("pkg/api/config.go: %v — the api.* name set is empty, so this check would pass on anything", err)
		return
	}
	var files, hits int
	for _, dir := range []string{"pkg", "cmd", "internal"} {
		base := filepath.Join(*root, dir)
		_ = filepath.WalkDir(base, func(p string, d os.DirEntry, werr error) error {
			if werr != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil //nolint:nilerr // reported by the checks that need the file
			}
			body, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			files++
			rel, _ := filepath.Rel(*root, p)
			for i, line := range strings.Split(string(body), "\n") {
				if k := strings.Index(line, "//"); k >= 0 && (k == 0 || line[k-1] != ':') {
					line = line[:k]
				}
				for _, m := range apiSettingName.FindAllStringSubmatch(line, -1) {
					if !fields[m[1]] {
						continue
					}
					hits++
					rp.errorf("%s:%d tells the operator to change %s, but there is no such configuration key — the api.* names are pkg/api fields, not file settings. Name the environment variable that moves it, or say the value is fixed", filepath.ToSlash(rel), i+1, m[0])
				}
			}
			return nil
		})
	}
	fmt.Printf("relcheck: api.* setting names checked (%d occurrences across %d .go files, %d known fields)\n", hits, files, len(fields))
}

// apiSettingName captures the dotted form as it appears in prose. Upper-case
// field access (api.MaxBodyBytes) is Go code, not an instruction to an operator.
var apiSettingName = regexp.MustCompile(`\bapi\.([a-z][a-z0-9_]*)\b`)

// --------------------------------------------------- documented listen ports --

// addrPort matches the address shapes a shipped document can show: a dotted
// quad, `localhost`, or `[::1]`, each with a port. Deliberately narrow. A bare
// `:1234` is not matched, because documents legitimately contain numbers that
// are not network ports — byte counts, timeouts, task counts, line numbers —
// and a check that flags those is a check that gets switched off.
var addrPort = regexp.MustCompile(`(?:(?:[0-9]{1,3}\.){3}[0-9]{1,3}|localhost|\[::1\]):([0-9]{2,5})`)

// documentedPorts returns the ports the product binds by default, read from
// config/config.example.yaml rather than hardcoded. That file is the one a
// customer is told to copy, so if the defaults move, the allowed set moves with
// them and this check keeps meaning the same thing.
func documentedPorts() ([]string, error) {
	ls, err := lines(filepath.Join(*root, "config", "config.example.yaml"))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, raw := range ls {
		l := strings.TrimSpace(raw)
		// HasPrefix, not Contains: `admin_port:` must not satisfy a `port:`
		// lookup by accident, and a commented-out default must not count.
		for _, key := range []string{"port:", "admin_port:"} {
			if !strings.HasPrefix(l, key) {
				continue
			}
			v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, key)), `"'`)
			if v != "" {
				seen[v] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// checkDocumentedPorts asserts that every listen address a shipped document
// prints uses a port the server actually binds by default.
//
// This exists because it shipped. QUICKSTART quoted the bind-refusal message
// with `0.0.0.0:19700` — a port that appears nowhere in this repository, while
// the default is 19527. Everything else about that line was right: `main.go`
// prints `loopworker: %v` and `FieldError.Error()` renders `field: reason`, so
// it read exactly like a real terminal transcript. A customer who bound
// 0.0.0.0 on the default port saw 19527 and could not match it to the error
// they had just been shown.
//
// Fenced code blocks are NOT skipped here, unlike the link check above. The
// line that shipped with the wrong port was a quoted transcript inside a fence,
// which is the shape a reader trusts most.
//
// CHANGELOG.md is exempt, for the same reason the route check exempts it: this
// file has to be able to quote the wrong value in order to record that it was
// fixed. The first real run of this check found exactly that — the entry
// describing this defect quotes `0.0.0.0:19700`, which is the whole point of
// the entry.
func checkDocumentedPorts(rp *report) {
	allowed, err := documentedPorts()
	if err != nil {
		rp.errorf("config/config.example.yaml: %v — cannot read the default ports, so documented addresses cannot be checked", err)
		return
	}
	if len(allowed) == 0 {
		rp.errorf("config/config.example.yaml declares no port/admin_port — the allowed set is empty, so this check would pass on any address at all")
		return
	}
	shipped, err := shippedDocs()
	if err != nil {
		rp.errorf(".goreleaser.yaml: %v", err)
		return
	}
	ok := map[string]bool{}
	for _, p := range allowed {
		ok[p] = true
	}

	var docs, addrs int
	for doc := range shipped {
		if !strings.HasSuffix(doc, ".md") || path.Base(doc) == "CHANGELOG.md" {
			continue
		}
		docs++
		body, rerr := os.ReadFile(filepath.Join(*root, filepath.FromSlash(doc)))
		if rerr != nil {
			rp.errorf("%s: %v", doc, rerr)
			continue
		}
		for i, raw := range strings.Split(string(body), "\n") {
			for _, m := range addrPort.FindAllStringSubmatch(raw, -1) {
				addrs++
				if !ok[m[1]] {
					rp.errorf("%s:%d shows %s, but the server binds %s by default — an address copied from here does not match what the operator sees", doc, i+1, m[0], strings.Join(allowed, " and "))
				}
			}
		}
	}
	fmt.Printf("relcheck: documented listen addresses checked (%d addresses across %d shipped .md, allowed ports %s)\n", addrs, docs, strings.Join(allowed, ", "))
}

// ---------------------------------------------------- documented API routes --

// apiPathRef finds /api/v1/... references. Scoped to that prefix on purpose:
// it is the only shape in the repository that unambiguously means "a route",
// so the check cannot drift into prose, file paths or flags.
var apiPathRef = regexp.MustCompile(`/api/v1[A-Za-z0-9_{}./-]*`)

// routeParam matches a {param} placeholder in a path template.
var routeParam = regexp.MustCompile(`\{[^}]*\}`)

// checkDocumentedRoutes asserts that every /api/v1 path in a shipped document is
// a route the server actually registers.
//
// It exists because docs/api/api-reference.md — itself in archives[].files —
// documented `{taskId}` and `{id}` where the server registers `{taskID}`.
// openapi.json never drifted, because TestOpenAPISpecMatchesRegisteredRoutes
// compares it to the router on every run. The hand-written reference next to it
// had no such guard, so the two shipped documents disagreed with each other.
// "Your docs say taskId, your spec says taskID" is a ticket nobody can answer.
//
// CHANGELOG.md is exempt: naming a route that does not exist is exactly what
// that file is for. Reading it as a specification is the same mistake as reading
// a comment as configuration.
//
// What this cannot do: decide whether a concrete ID substituted into a {param}
// actually exists. "/api/v1/tasks/typo" has the shape of a registered route and
// is accepted, because statically it is indistinguishable from
// "/api/v1/workflow/builtin.anomaly-review", which is a real documented value.
// It verifies route *shape*, and says nothing about whether a given ID resolves.
func checkDocumentedRoutes(rp *report) {
	raw, err := os.ReadFile(filepath.Join(*root, "pkg", "api", "openapi.json"))
	if err != nil {
		rp.errorf("pkg/api/openapi.json: %v — documented routes cannot be verified without the spec", err)
		return
	}
	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		rp.errorf("pkg/api/openapi.json: %v", err)
		return
	}
	shipped, err := shippedDocs()
	if err != nil {
		rp.errorf(".goreleaser.yaml: %v — cannot tell which documents ship", err)
		return
	}

	registered := map[string]bool{}
	byShape := map[string]string{}
	for p := range spec.Paths {
		registered[p] = true
		byShape[routeShape(p)] = p
	}

	var docs, refs int
	for doc := range shipped {
		if !strings.HasSuffix(doc, ".md") || path.Base(doc) == "CHANGELOG.md" {
			continue
		}
		docs++
		body, rerr := os.ReadFile(filepath.Join(*root, filepath.FromSlash(doc)))
		if rerr != nil {
			rp.errorf("%s: %v", doc, rerr)
			continue
		}
		inFence := false
		for i, raw := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(raw), "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			for _, m := range apiPathRef.FindAllString(raw, -1) {
				p := strings.TrimRight(m, "./")
				refs++
				if finding := routeVerdict(p, registered, byShape); finding != "" {
					rp.errorf("%s:%d documents %s", doc, i+1, finding)
				}
			}
		}
	}
	fmt.Printf("relcheck: documented API routes checked (%d references across %d shipped .md)\n", refs, docs)
}

// routeShape reduces a path template to its shape by blanking every parameter,
// so "/tasks/{taskID}" and "/tasks/{id}" compare equal.
func routeShape(p string) string { return routeParam.ReplaceAllString(p, "{}") }

// substituteLast blanks only the final path segment, which is how a
// concrete value written in place of a parameter is recognised.
func substituteLast(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i] + "/{}"
	}
	return p
}

// routeVerdict judges one /api/v1... reference found in prose. It returns "" when
// the reference is acceptable and the finding text when it is not.
//
// Shared by the document check and the error-message check below, so the two
// cannot come to disagree about what counts as valid. That disagreement is not
// hypothetical: this exact drift existed in both places at once -- the shipped
// documents said `{taskId}` and `{id}` where the router registers `{taskID}` --
// and only the documents were ever checked, so fixing them left the error
// message still wrong.
func routeVerdict(p string, registered map[string]bool, byShape map[string]string) string {
	if p == "/api/v1" {
		return "" // the bare prefix is a sentence, not a route
	}
	if registered[p] {
		return ""
	}
	if want, ok := byShape[routeShape(p)]; ok {
		return fmt.Sprintf("references %s, but the server registers that parameter as %s — copy the name from the spec so the router and everything that quotes it agree", p, want)
	}
	if _, ok := byShape[routeShape(substituteLast(p))]; ok {
		return "" // a concrete value substituted into a path parameter
	}
	for r := range registered {
		if strings.HasPrefix(r, p+"/") {
			return "" // a parent whose child is the one being referenced
		}
	}
	return fmt.Sprintf("references %s, which the server does not register — a caller following this text gets a 404", p)
}

// checkErrorRouteReferences applies the same judgement to the routes quoted in
// the server's own user-facing strings: the registered error catalogue, and the
// `fix`/`reason` text a FieldError carries.
//
// This exists because that text is read by an operator at the moment they are
// stuck, and it had drifted from the router without anything noticing: the
// `ErrTaskInvalid` message told the reader to GET /api/v1/tasks/{id} while the
// router registers {taskID}. The documents were fixed for exactly this reason
// earlier; the error message was not, because nothing looked at it.
//
// Comments are deliberately out of scope, and that is a narrowing rather than a
// concession. What a caller follows is a string the server sends them, not a
// comment. It also removes the one false positive the check has: the
// `Deprecated:` notes on `pkg/client.GetMetrics`/`GetLogs` say "there is no
// /api/v1/metrics route", which is a statement of absence — a correct sentence
// that quotes a route precisely in order to deny it. Rewriting correct comments
// to satisfy a checker is how a repository stops being honest.
func checkErrorRouteReferences(rp *report) {
	raw, err := os.ReadFile(filepath.Join(*root, "pkg", "api", "openapi.json"))
	if err != nil {
		rp.errorf("pkg/api/openapi.json: %v", err)
		return
	}
	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		rp.errorf("pkg/api/openapi.json: %v", err)
		return
	}
	registered := map[string]bool{}
	byShape := map[string]string{}
	for p := range spec.Paths {
		registered[p] = true
		byShape[routeShape(p)] = p
	}

	var files, refs int
	for _, dir := range []string{"pkg", "cmd", "internal"} {
		base := filepath.Join(*root, dir)
		_ = filepath.WalkDir(base, func(p string, d os.DirEntry, werr error) error {
			if werr != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil //nolint:nilerr // an unreadable dir is reported by the checks that need it
			}
			body, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			files++
			rel, _ := filepath.Rel(*root, p)
			for i, line := range strings.Split(string(body), "\n") {
				// Drop line comments before matching. `://` is left alone so
				// that a route inside a string literal (which is where the
				// drift this checks for actually lives) is still seen.
				if k := strings.Index(line, "//"); k >= 0 && (k == 0 || line[k-1] != ':') {
					line = line[:k]
				}
				for _, m := range apiPathRef.FindAllString(line, -1) {
					ref := strings.TrimRight(m, "./")
					if ref == "/api/v1" {
						continue
					}
					refs++
					if finding := routeVerdict(ref, registered, byShape); finding != "" {
						rp.errorf("%s:%d %s", filepath.ToSlash(rel), i+1, finding)
					}
				}
			}
			return nil
		})
	}
	fmt.Printf("relcheck: error-message API routes checked (%d references across %d .go files)\n", refs, files)
}

// ------------------------------------------------------- error code reachability --

// errorCodeConst captures one entry of the ErrorCode catalogue.
var errorCodeConst = regexp.MustCompile(`\bCode([A-Za-z0-9]+)\s+ErrorCode\s*=\s*"([A-Z_]+)"`)

// errorCodeRegister captures the classification table entry that maps a sentinel
// onto a code: register(lwerrors.ErrX, http.Status..., CodeY, "...").
var errorCodeRegister = regexp.MustCompile(`register\(\s*(?:lwerrors\.)?(Err[A-Za-z0-9]+)\s*,\s*http\.Status[A-Za-z]+\s*,\s*(Code[A-Za-z0-9]+)\b`)

// errorCodeDocRow captures a row of the reference's error-code table:
// "| SOME_CODE | 400 | ...". The status column is what distinguishes a table row
// from prose that merely mentions a code in backticks.
var errorCodeDocRow = regexp.MustCompile(`^\|\s*([A-Z][A-Z0-9_]{3,})\s*\|\s*\d{3}\b`)

// checkErrorCodeReachability asserts that every code the shipped reference
// promises a client can receive is one some code path can actually emit.
//
// This exists because that promise had already broken twice, in both directions.
// The reference told clients to handle TASK_ALREADY_EXISTS — a code no path can
// emit, because POST /api/v1/tasks takes no id and the server allocates one, so
// two submissions can never collide. It also folded QUEUE_FULL into the
// SERVICE_UNAVAILABLE row, implying a 503 variant that is never produced. A
// client writing `if code == "QUEUE_FULL" { backoff }` waits forever, and one
// writing an idempotent retry around task creation gets a second task rather
// than the 409 it was promised.
//
// Reachability is judged two ways, because errors reach clients two ways:
// directly, by building an ErrorBody with the code (middleware.go does this for
// RATE_LIMITED, which is why a sentinel-only reading calls it dead), and through
// classify(), which maps a returned lwerrors sentinel onto its registered code.
// The sentinel's own declaration is not a producer, and neither is the
// registration line, so both are excluded — including them is what made a first
// attempt report RATE_LIMITED as reachable only "via" a producer that did not
// exist.
func checkErrorCodeReachability(rp *report) {
	apiErrorsPath := filepath.Join(*root, "pkg", "api", "errors.go")
	src, err := os.ReadFile(apiErrorsPath)
	if err != nil {
		rp.errorf("pkg/api/errors.go: %v — the error-code catalogue cannot be read", err)
		return
	}
	body := string(src)
	matches := errorCodeConst.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		rp.errorf("pkg/api/errors.go: no `CodeXxx ErrorCode = \"...\"` constants found — this check would pass on an empty catalogue")
		return
	}

	// Parsed line by line rather than over the whole file: the patterns use \s*,
	// which in Go's regexp crosses newlines, so a whole-body match can glue the
	// tail of one line to the head of the next and invent a registration.
	declared := map[string]string{}  // code value -> Go constant name
	sentinels := map[string]string{} // code constant -> sentinel it is registered against
	catalogLines := strings.Split(body, "\n")
	for _, l := range catalogLines {
		if m := errorCodeConst.FindStringSubmatch(l); m != nil {
			declared[m[2]] = "Code" + m[1]
		}
		if m := errorCodeRegister.FindStringSubmatch(l); m != nil {
			sentinels[m[2]] = m[1]
		}
	}

	// Producer set: non-test code, minus the catalogue and the sentinel
	// declarations. Everything else counts as a place that can name the code.
	type sourceFile struct{ rel, body string }
	var sources []sourceFile
	for _, dir := range []string{"pkg", "cmd", "internal"} {
		base := filepath.Join(*root, dir)
		_ = filepath.WalkDir(base, func(p string, d os.DirEntry, werr error) error {
			if werr != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil //nolint:nilerr // reported by the checks that need the file
			}
			raw, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			rel, _ := filepath.Rel(*root, p)
			rel = filepath.ToSlash(rel)
			if rel == "pkg/api/errors.go" || rel == "pkg/errors/errors.go" {
				return nil
			}
			sources = append(sources, sourceFile{rel, string(raw)})
			return nil
		})
	}

	unreachable := map[string]string{} // code value -> why
	for value, constName := range declared {
		pat := regexp.MustCompile(`\b` + constName + `\b`)
		reachable := false
		for _, s := range sources {
			if pat.MatchString(s.body) {
				reachable = true
				break
			}
		}
		if !reachable {
			// A use inside the catalogue that is neither the declaration nor the
			// registration is a real emission site (an ErrorBody literal).
			// The two are recognised by shape, not by a space-prefixed string:
			// gofmt aligns the declarations, so "CodeX ErrorCode" with a single
			// space matches nothing and every code then counts as reachable
			// through its own declaration.
			for _, l := range strings.Split(body, "\n") {
				t := strings.TrimSpace(l)
				if t == "" {
					continue
				}
				if f := strings.Fields(t); len(f) >= 2 && f[0] == constName && f[1] == "ErrorCode" {
					continue // the declaration itself
				}
				if strings.HasPrefix(t, "register(") {
					continue // the classification table
				}
				if pat.MatchString(l) {
					reachable = true
					break
				}
			}
		}
		if !reachable {
			if sent, ok := sentinels[constName]; ok {
				spat := regexp.MustCompile(`\b` + sent + `\b`)
				for _, s := range sources {
					if spat.MatchString(s.body) {
						reachable = true
						break
					}
				}
			}
		}
		if !reachable {
			why := "no code path names " + constName
			if sent, ok := sentinels[constName]; ok {
				why += " and its registered sentinel " + sent + " is never returned"
			}
			unreachable[value] = why
		}
	}

	// The reference's table: every row must name a declared, reachable code.
	shipped, err := shippedDocs()
	if err != nil {
		rp.errorf("%v", err)
		return
	}
	docNames := make([]string, 0, len(shipped))
	for p := range shipped {
		docNames = append(docNames, p)
	}
	sort.Strings(docNames)

	var rows int
	for _, rel := range docNames {
		if !strings.HasSuffix(rel, ".md") {
			continue
		}
		ls, lerr := lines(filepath.Join(*root, rel))
		if lerr != nil {
			continue
		}
		for i, l := range ls {
			m := errorCodeDocRow.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			rows++
			value := m[1]
			if _, ok := declared[value]; !ok {
				rp.errorf("%s:%d documents error code %s, which pkg/api/errors.go does not declare — a client branching on it is branching on nothing", rel, i+1, value)
				continue
			}
			if why, ok := unreachable[value]; ok {
				rp.errorf("%s:%d documents error code %s as something a client can receive, but %s — move it out of the table and say so in prose, or give it a producer", rel, i+1, value, why)
			}
		}
	}

	// The other direction, as a warning: a code nothing can emit and that no
	// shipped document mentions is a constant waiting to be mistaken for a live
	// code by the next reader.
	var orphans []string
	for value := range unreachable {
		mentioned := false
		for _, rel := range docNames {
			if !strings.HasSuffix(rel, ".md") {
				continue
			}
			raw, rerr := os.ReadFile(filepath.Join(*root, rel))
			if rerr == nil && strings.Contains(string(raw), value) {
				mentioned = true
				break
			}
		}
		if !mentioned {
			orphans = append(orphans, value)
		}
	}
	sort.Strings(orphans)
	for _, value := range orphans {
		rp.warnf("error code %s is unreachable and no shipped document mentions it (%s) — a client reading pkg/api/errors.go will wait for a response that cannot arrive", value, unreachable[value])
	}

	fmt.Printf("relcheck: error-code reachability checked (%d codes, %d table rows, %d unreachable)\n", len(declared), rows, len(unreachable))
}

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

// listEntry reports whether a trimmed YAML list line names want as the whole
// entry. The exactness is the point: `- LICENSE.md` must not satisfy a check
// for LICENSE, and `- dst: /usr/share/doc/loopworker/NOTICE` must not satisfy a
// check for a NOTICE source. A prefix match would pass both.
func listEntry(line, want string) bool {
	rest, ok := strings.CutPrefix(line, "- ")
	return ok && (rest == want || strings.HasPrefix(rest, want+" "))
}

// checkGoToolchainPerJob requires every job that compiles Go to declare which
// toolchain it uses — per job, not per file.
//
// checkWorkflowGoVersions already warns when a workflow never mentions go.mod,
// but it reaches that verdict once for the whole file, so a single job declaring
// `go-version-file: go.mod` silences it for every other job alongside.
// release.yml's verify-artifacts job is exactly what that bought: boot-smoke.sh
// builds examples/hello-plugin for wasip1/wasm, and the job ran on whatever Go
// the runner image happened to ship — an unpinned toolchain on the one job whose
// entire stated purpose is to prove a customer can install the artifact.
//
// "Compiles Go" is decided in two hops, and the second one is not a nicety: the
// `go build` lives in .release/scripts/boot-smoke.sh, not in the YAML, so a scan
// of the workflow alone sees a job that runs bash and nothing more.
func checkGoToolchainPerJob(rp *report) {
	// A job satisfies the rule by naming a toolchain explicitly. Matching the
	// substring is deliberate: setup-go's two inputs are `go-version` and
	// `go-version-file`, and a job may legitimately pin a literal that
	// checkWorkflowGoVersions has already compared against go.mod.
	declares := func(jobLines []string) bool {
		for _, l := range jobLines {
			if strings.Contains(l, "actions/setup-go") ||
				strings.Contains(l, "go-version-file") ||
				strings.Contains(l, "go-version:") {
				return true
			}
		}
		return false
	}

	scripts := goUsingScripts()
	reGo := regexp.MustCompile(`\bgo\s+(build|test|run|vet|generate|install|mod)\b`)
	reJob := regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)

	dir := filepath.Join(*root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		rp.errorf("%s: %v", dir, err)
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		ls, err := lines(filepath.Join(dir, e.Name()))
		if err != nil {
			rp.errorf("read %s: %v", e.Name(), err)
			continue
		}
		inJobs, job := false, ""
		jobLines, order := map[string][]string{}, []string{}
		for _, l := range ls {
			if l == "jobs:" {
				inJobs = true
				continue
			}
			if !inJobs {
				continue
			}
			if m := reJob.FindStringSubmatch(l); m != nil {
				job = m[1]
				order = append(order, job)
				jobLines[job] = nil
				continue
			}
			if job != "" {
				jobLines[job] = append(jobLines[job], l)
			}
		}
		for _, name := range order {
			body := jobLines[name]
			if declares(body) {
				continue
			}
			why := ""
			for _, l := range body {
				if reGo.MatchString(l) {
					why = "runs a go subcommand"
					break
				}
				for script, usesGo := range scripts {
					if usesGo && strings.Contains(l, script) {
						why = "runs " + script + ", which invokes go"
						break
					}
				}
				if why != "" {
					break
				}
			}
			if why == "" {
				continue
			}
			rp.errorf("%s: job %q %s but never declares a Go toolchain — it would compile with whatever the runner image ships; add actions/setup-go with `go-version-file: go.mod`", e.Name(), name, why)
		}
	}
}

// goUsingScripts maps each tracked release script to whether it invokes a go
// subcommand. The map is keyed by the repository-relative path, which is how a
// workflow spells it.
func goUsingScripts() map[string]bool {
	out := map[string]bool{}
	reGo := regexp.MustCompile(`\bgo\s+(build|test|run|vet|generate|install|mod)\b`)
	base := filepath.Join(*root, ".release")
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is walked past, not fatal
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".sh" && ext != ".ps1" && ext != ".bash" {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(*root, p)
		if err != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = reGo.Match(body)
		return nil
	})
	return out
}

// checkSmokeScriptEnvVars: every LOOPWORKER_* name a shipped smoke script hands
// to the server must be one the product actually reads.
//
// This fails quietly, which is the reason it needs a gate. A name that drifts
// out of the config registry is not an error at startup: the server simply keeps
// its default. The smoke script then polls the ephemeral port it chose while the
// server listens on 19527, and the report is "GET /api/v1/health did not return
// 200" — a conclusion about the artifact that has nothing to do with the
// artifact. Establishing these four names by hand is what prompted the rule; the
// fifth (LOOPWORKER_API_ADMIN_PORT, which boot-smoke.sh has always set and
// smoke.ps1 did not) is the divergence that motivated it.
//
// Only assignments count. `${LOOPWORKER_SMOKE_API_KEY:-default}` is the script
// reading its own input, not a variable it is handing the server, and treating
// the two alike would demand a registry entry for a test harness knob.
func checkSmokeScriptEnvVars(rp *report) {
	known := productEnvVars()
	if len(known) == 0 {
		rp.errorf("no LOOPWORKER_* names found in the product's Go sources — the registry this check compares against is empty, so it would pass on anything")
		return
	}
	reName := regexp.MustCompile(`LOOPWORKER_[A-Z0-9_]+`)
	reAssign := regexp.MustCompile(`LOOPWORKER_[A-Z0-9_]+\s*=`)

	base := filepath.Join(*root, ".release")
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is walked past, not fatal
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".sh" && ext != ".ps1" && ext != ".bash" {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(*root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for i, l := range strings.Split(string(raw), "\n") {
			for _, name := range reName.FindAllString(l, -1) {
				if !reAssign.MatchString(l) {
					continue // read, not assigned to the child
				}
				if !known[name] {
					rp.errorf("%s:%d sets %s, which no non-test Go source reads — the server would silently keep its default and this smoke would report a health failure that is not about the artifact",
						rel, i+1, name)
				}
			}
		}
		return nil
	})
	fmt.Printf("relcheck: smoke-script env vars checked (%d names known to the product)\n", len(known))
}

// productEnvVars is every LOOPWORKER_* name that appears as a string literal in
// the product's non-test Go sources. Deriving the registry from the code rather
// than restating it here is deliberate: a hand-kept list is one more thing to
// forget when a variable is added, and a false positive in a release gate is
// worse than no gate.
func productEnvVars() map[string]bool {
	known := map[string]bool{}
	re := regexp.MustCompile(`"((?:LOOPWORKER|OPENAI|ANTHROPIC)_[A-Z0-9_]+)"`)
	_ = filepath.WalkDir(*root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is walked past, not fatal
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(*root, p)
		if err != nil || strings.HasPrefix(filepath.ToSlash(rel), ".release/tools/") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			known[m[1]] = true
		}
		return nil
	})
	return known
}

// checkAttributionChannels: every artefact a customer can obtain has to carry
// the licence and the notice.
//
// Three channels exist and they drifted independently, which is the argument for
// checking all three rather than trusting one. The tarball was already covered
// by the `verify-artifacts` release job. The deb and the rpm were not: between
// them they shipped the binary, a config file and a data directory, with no
// licence, no notice and no documentation — a deb with no copyright file is a
// Debian Policy 12.5 violation, and a package-manager install handed someone a
// redistributable with no attribution. The container image had the same gap: the
// runtime stage copied the binary and nothing else, so anyone who pulled it from
// a registry held exactly the same thing.
//
// This checks that each channel *names* the files. It does not check that the
// build actually put them in — that is what booting the artefact is for, and
// `verify-artifacts` already does it for the archive. The signal here is a
// reference in a config file, which is cheap, exact, and catches the realistic
// regression: somebody edits a packaging file and drops the entry.
func checkAttributionChannels(rp *report) {
	const channels = 3
	for _, doc := range []string{"LICENSE", "NOTICE"} {
		found := 0

		if ls, err := lines(filepath.Join(*root, ".goreleaser.yaml")); err != nil {
			rp.errorf(".goreleaser.yaml: %v", err)
		} else {
			inArchives, inNfpms := false, false
			section := ""
			for _, raw := range ls {
				l := strings.TrimSpace(raw)
				// A top-level key closes the previous section. Without this the
				// last section stayed open for the rest of the file, so a
				// `src:` in brews or signs would read as an nfpms entry.
				if raw == l && strings.HasSuffix(l, ":") {
					section = strings.TrimSuffix(l, ":")
					continue
				}
				if section == "" || !strings.Contains(l, doc) {
					continue
				}
				switch {
				case section == "archives" && listEntry(l, doc):
					inArchives = true
				// nfpm entries are `- src: LICENSE`, so the list dash is part of
				// the line. Matching `src: ` rather than the bare name keeps a
				// `dst: .../NOTICE` from reading as the source.
				case section == "nfpms" && listEntry(l, "src: "+doc):
					inNfpms = true
				}
			}
			if !inArchives {
				rp.errorf(".goreleaser.yaml: archives do not ship %s — a customer who keeps only the tarball gets no %s", doc, doc)
			}
			if !inNfpms {
				rp.errorf(".goreleaser.yaml: nfpms contents do not ship %s — a package-manager install has no %s", doc, doc)
			}
			if inArchives {
				found++
			}
			if inNfpms {
				found++
			}
		}

		if ls, err := lines(filepath.Join(*root, "Dockerfile")); err != nil {
			rp.errorf("Dockerfile: %v", err)
		} else {
			// Only the final stage reaches the published image, and a COPY in
			// the builder never gets there. A line belongs to the final stage
			// exactly when it comes after the last FROM, because a new stage
			// can only begin with one.
			lastFrom := -1
			for i, raw := range ls {
				if strings.HasPrefix(strings.TrimSpace(raw), "FROM ") {
					lastFrom = i
				}
			}
			copies := false
			if lastFrom >= 0 {
				for _, raw := range ls[lastFrom+1:] {
					l := strings.TrimSpace(raw)
					if strings.HasPrefix(l, "COPY ") && strings.Contains(l, doc) {
						copies = true
					}
				}
			}
			if !copies {
				rp.errorf("Dockerfile: the runtime stage does not copy %s — an image pulled from a registry would carry no %s", doc, doc)
			} else {
				found++
			}
		}

		if found == channels {
			fmt.Printf("relcheck: %s carried by all %d distribution channels (archive, package, image)\n", doc, channels)
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
//
// The file list is the runbook's contract, and it used to be shorter than the
// contract. .release/RELEASE-PROCESS.md §1 promises that preflight's
// `relcheck -strict` fails while any of seven placeholder categories remain, and
// two of those seven lived in files this never opened: CODE_OF_CONDUCT.md (which
// carried a `TODO(owner)` enforcement address) and CONTRIBUTING.md (which names
// `TODO(owner)` as its contact). Both ship in the repository a customer is told
// to open an issue against, and a published CoC that names no one is exactly the
// ticket this repository cannot answer. A maintainer could follow the runbook to
// the letter, satisfy everything the gate actually looked at, tag, and publish
// both files still holding a placeholder.
func checkPlaceholders(rp *report) {
	files := []string{".goreleaser.yaml", "LICENSE", "SECURITY.md", "SUPPORT.md", "NOTICE",
		"CODE_OF_CONDUCT.md", "CONTRIBUTING.md",
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

// checkNoticeDirectDependencies keeps the attribution list in NOTICE honest.
//
// NOTICE ships inside the binary archive and is the first legal document a
// buyer's counsel reads. Its direct-dependency list had already drifted twice
// before this check existed: a version bump left the old number in place, and a
// module that became a direct import was never added at all. Neither is
// visible anywhere else — the build stays green, every test passes and the
// archive is byte-identical — so the drift is only ever discovered by a
// customer, which is the most expensive place to discover it.
//
// The signal is a comparison, not a judgement: go.mod marks every module that is
// not a direct requirement with `// indirect`, and NOTICE has to list the same
// set at the same versions. That exactness is what makes it safe to make a
// build gate. It deliberately says nothing about the licence names or copyright
// lines above each entry — those are a human's reading of each LICENSE file, and
// a check that second-guessed them would be wrong more often than right.
func checkNoticeDirectDependencies(rp *report) {
	gomod, err := lines(filepath.Join(*root, "go.mod"))
	if err != nil {
		rp.errorf("go.mod: %v", err)
		return
	}
	notice, err := lines(filepath.Join(*root, "NOTICE"))
	if err != nil {
		rp.errorf("NOTICE: %v", err)
		return
	}

	direct := directRequirements(gomod)
	if len(direct) == 0 {
		rp.errorf("go.mod: no direct requirement found — NOTICE's dependency list cannot be verified")
		return
	}
	listed := noticeDirectEntries(notice)

	var missing, mismatched []string
	for _, mod := range sortedKeys(direct) {
		want := direct[mod]
		got, ok := listed[mod]
		switch {
		case !ok:
			missing = append(missing, fmt.Sprintf("%s %s", mod, want))
		case got != want:
			mismatched = append(mismatched, fmt.Sprintf("%s is v%s in NOTICE but %s in go.mod", mod, got, want))
		}
	}
	for _, mod := range sortedKeys(listed) {
		if _, ok := direct[mod]; !ok {
			missing = append(missing, fmt.Sprintf("%s %s (not a direct requirement in go.mod)", mod, listed[mod]))
		}
	}
	for _, m := range missing {
		rp.errorf("NOTICE: direct dependency %s is not listed — a buyer's counsel reads this file, not go.mod", m)
	}
	for _, m := range mismatched {
		rp.errorf("NOTICE: version drift — %s", m)
	}
	if len(missing) == 0 && len(mismatched) == 0 {
		fmt.Printf("relcheck: NOTICE direct dependencies checked (%d)\n", len(direct))
	}
}

// directRequirements returns the module path and version of every requirement in
// go.mod that is not marked `// indirect`. Single-line and block forms are both
// handled because go.mod uses whichever the author last wrote, and a gate that
// only understood one of them would stop firing the day someone reformats.
func directRequirements(ls []string) map[string]string {
	out := map[string]string{}
	inBlock := false
	for _, raw := range ls {
		l := strings.TrimSpace(raw)
		switch {
		case l == "" || strings.HasPrefix(l, "//"):
			continue
		case inBlock && l == ")":
			inBlock = false
			continue
		case strings.HasPrefix(l, "require ("):
			inBlock = true
			continue
		case strings.HasPrefix(l, "require "):
			l = strings.TrimSpace(strings.TrimPrefix(l, "require "))
		case inBlock:
		default:
			continue // not a require statement
		}
		fields := strings.Fields(l)
		if len(fields) < 2 {
			continue
		}
		if strings.Contains(l, "// indirect") {
			continue
		}
		out[fields[0]] = fields[1]
	}
	return out
}

// noticeDirectEntries returns every "module vX.Y.Z" entry in NOTICE that is
// followed by an indented `License:` line, which is what distinguishes the
// dependency list from prose that happens to mention a module and a version.
func noticeDirectEntries(ls []string) map[string]string {
	out := map[string]string{}
	for i, raw := range ls {
		if raw == "" || raw[0] == ' ' || raw[0] == '\t' {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) != 2 || !strings.Contains(fields[0], "/") {
			continue
		}
		if !strings.HasPrefix(fields[1], "v") {
			continue
		}
		if i+1 >= len(ls) || !strings.HasPrefix(strings.TrimSpace(ls[i+1]), "License:") {
			continue
		}
		out[fields[0]] = fields[1]
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// ------------------------------------------------- admin listener documents --

// adminRouteCall captures a route registration on the admin router. Both the
// plain form and the Method form appear in pkg/api/admin.go, and only the
// second pins a verb, so they are recognised separately rather than with one
// loose pattern.
var adminRouteCall = regexp.MustCompile(`(?:authed\.)?(?:Get|Handle|Method)\(\s*(?:http\.Method([A-Za-z]+)\s*,\s*)?"(/[^"]*)"`)

// parseRouteRow pulls a path and a method out of a markdown table row.
//
// Cell-by-cell rather than one pattern, because the two shipped documents
// order those columns differently — api-reference.md is
// "| GET | /logs | ... |" and USAGE.md is "| `/logs` | GET | ... |". A single
// regex that expects the method first silently matches nothing in the second
// document, which is how this check first shipped reading only half the tree
// while reporting that it had read all of it.
func parseRouteRow(line string) (path, method string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") {
		return "", "", false
	}
	for _, cell := range strings.Split(trimmed, "|") {
		v := strings.TrimSpace(strings.Trim(strings.TrimSpace(cell), "`"))
		if v == "" {
			continue
		}
		switch upper := strings.ToUpper(v); upper {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
			if method == "" {
				method = upper
			}
			continue
		default:
			_ = upper
		}
		if path == "" && strings.HasPrefix(v, "/") {
			path = v
		}
	}
	return path, method, path != ""
}

// contractDoc is the shipped document that defines response contracts. It is
// the one an integrator codes against, which is why an endpoint it does not
// mention is worse than one no document mentions at all: the guide will have
// told the reader the route exists, and the reference they turn to next will
// have nothing about it.
const contractDoc = "docs/api/api-reference.md"

// checkAdminListenerDocs asserts that every route the admin listener registers
// is named in a shipped document, and that the documents agree with the code
// about its method.
//
// This exists because the two shipped documents had drifted apart in opposite
// directions and neither noticed. docs/USAGE.md listed /statusz and /shutdown
// on the admin listener; docs/api/api-reference.md, which is the document that
// defines response contracts, listed neither — so a customer reading the
// reference had no way to learn that the endpoint answering them has no
// documented body, and in this case no documented shape at all: GET /statusz
// returns a bare object rather than the {"success","data"} envelope everything
// else on this server uses.
//
// The rule is deliberately one-directional. The reference is allowed to
// document more than the orientation guide does — it does, and legitimately:
// /healthz is on both listeners and only the reference lists it on the admin
// one. What is not allowed is a document telling an operator to use a route the
// contract document never mentions, because that is the state this was found
// in.
// plural keeps a diagnostic grammatical when the count is not one. Findings
// are read in CI output, and "docs/USAGE.md name it" is the kind of slip that
// makes a reader doubt the rest of the sentence.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func checkAdminListenerDocs(rp *report) {
	raw, err := os.ReadFile(filepath.Join(*root, "pkg", "api", "admin.go"))
	if err != nil {
		rp.errorf("pkg/api/admin.go: %v — the admin route table cannot be read", err)
		return
	}

	type route struct{ path, method string }
	registered := map[string]string{} // path -> method
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "authed.") && !strings.HasPrefix(t, "router.") {
			continue
		}
		m := adminRouteCall.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		method := strings.ToUpper(m[1])
		if method == "" {
			// router.Get / authed.Get: a GET. Handle registers every verb, which
			// the file's own comments say it must not do here.
			method = "GET"
		}
		registered[m[2]] = method
	}
	if len(registered) == 0 {
		rp.errorf("pkg/api/admin.go: no admin routes parsed — this check would pass on an empty table")
		return
	}

	shipped, err := shippedDocs()
	if err != nil {
		rp.errorf("%v", err)
		return
	}
	docNames := make([]string, 0, len(shipped))
	for p := range shipped {
		docNames = append(docNames, p)
	}
	sort.Strings(docNames)

	namedIn := map[string][]string{} // path -> documents that mention it
	inContract := map[string]bool{}  // path -> named in the contract document
	mismatched := 0
	rows := 0
	for _, rel := range docNames {
		if !strings.HasSuffix(rel, ".md") {
			continue
		}
		ls, lerr := lines(filepath.Join(*root, rel))
		if lerr != nil {
			continue
		}
		for i, l := range ls {
			if !strings.HasPrefix(strings.TrimSpace(l), "|") {
				continue
			}
			path, method, isRow := parseRouteRow(l)
			if !isRow {
				continue
			}
			want, isAdmin := registered[path]
			if !isAdmin {
				continue
			}
			rows++
			namedIn[path] = append(namedIn[path], rel)
			if rel == contractDoc {
				inContract[path] = true
			}
			if method != "" && method != want {
				mismatched++
				rp.errorf("%s:%d documents %s %s, but pkg/api/admin.go registers it as %s — a client built from the document would call a verb that answers 405",
					rel, i+1, method, path, want)
			}
		}
	}

	for _, p := range sortedKeys(registered) {
		if !inContract[p] {
			// The precise failure this was written for: the orientation guide
			// tells an operator the route exists, and the contract document —
			// the one that says what it answers with — never mentions it. A
			// weaker "some document names it" rule passed on exactly this tree,
			// because the other document was still there saying the same thing.
			where := "no shipped document names it"
			if others := namedIn[p]; len(others) > 0 {
				where = fmt.Sprintf("%s %s it, %s does not", strings.Join(others, " and "), plural(len(others), "names", "name"), contractDoc)
			}
			rp.errorf("the admin listener serves %s, but %s: a customer reading the archive has no documented contract for it",
				p, where)
		}
	}

	fmt.Printf("relcheck: admin listener documents checked (%d routes, %d table rows naming them, %d method mismatches)\n", len(registered), rows, mismatched)
}

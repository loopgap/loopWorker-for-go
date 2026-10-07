package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"
)

// DefaultMaxArtifactMB bounds how large a .wasm artifact may be.
const DefaultMaxArtifactMB = 128

// wasmMagic is the 4-byte preamble of every WebAssembly binary module,
// followed by a 4-byte version.
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// known host imports accepted at load time.
var (
	knownEnvFunctions     = map[string]bool{"host_http_request": true, "host_log": true}
	knownWASIModulePrefix = "wasi_snapshot_preview1"
)

// ImportRef is one import declaration of a wasm module.
type ImportRef struct {
	Module string `json:"module"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // func, table, memory, global, tag
}

func (r ImportRef) String() string { return r.Module + ":" + r.Name }

// AuditOptions configures AuditWasmFile / AuditWasmBytes.
type AuditOptions struct {
	// MaxArtifactBytes rejects a file before it is read into memory. Zero uses
	// DefaultMaxArtifactMB.
	MaxArtifactBytes int64
	// MemoryMB, MaxCPUSeconds and MaxOutputMB are the budget the probe runs
	// under. Zero values use the sandbox defaults.
	MemoryMB      int
	MaxCPUSeconds int
	MaxOutputMB   int
	// AllowedHosts is the egress allowlist the plugin is audited against.
	AllowedHosts []string
	// RunProbe instantiates the module under the budget with a stubbed
	// host_http_request that records the URLs it asks for and performs no
	// network I/O at all. Static checks only when false.
	RunProbe bool
	// Input is handed to the probe on stdin.
	Input []byte
	// ExtraAllowedImportModules adds host module names this deployment provides.
	ExtraAllowedImportModules []string
}

// DefaultAuditOptions returns the audit budget used when a caller supplies the
// zero value.
func DefaultAuditOptions() AuditOptions {
	return AuditOptions{
		MaxArtifactBytes: DefaultMaxArtifactMB * 1024 * 1024,
		MemoryMB:         DefaultMaxMemoryMB,
		MaxCPUSeconds:    DefaultMaxCPUSeconds,
		MaxOutputMB:      DefaultMaxOutputMB,
		RunProbe:         true,
	}
}

func (o AuditOptions) withDefaults() AuditOptions {
	if o.MaxArtifactBytes <= 0 {
		o.MaxArtifactBytes = DefaultMaxArtifactMB * 1024 * 1024
	}
	if o.MemoryMB <= 0 {
		o.MemoryMB = DefaultMaxMemoryMB
	}
	if o.MaxCPUSeconds <= 0 {
		o.MaxCPUSeconds = DefaultMaxCPUSeconds
	}
	if o.MaxOutputMB <= 0 {
		o.MaxOutputMB = DefaultMaxOutputMB
	}
	return o
}

// AuditReport is the conformance verdict for one artifact. It is JSON-friendly
// so a buyer can gate it in CI.
type AuditReport struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	MagicOK       bool   `json:"magic_ok"`
	ModuleVersion uint32 `json:"wasm_version"`

	// Memory footprint declared by the module.
	MemoryMinPages      uint32 `json:"memory_min_pages"`
	MemoryMaxPages      uint32 `json:"memory_max_pages"` // 0 when unbounded
	MemoryBounded       bool   `json:"memory_bounded"`
	MemoryBytesRequired int64  `json:"memory_bytes_required"`

	Imports        []ImportRef `json:"imports"`
	UnknownImports []ImportRef `json:"unknown_imports"`
	Exports        []string    `json:"exports"`
	HasEntry       bool        `json:"has_entry"`
	UnsupportedABI bool        `json:"unsupported_abi"`

	// Bounded-budget probe results.
	ProbeRan       bool   `json:"probe_ran"`
	BudgetExceeded bool   `json:"budget_exceeded"`
	ProbeMillis    int64  `json:"probe_millis"`
	ProbeExitCode  uint32 `json:"probe_exit_code"`
	ProbeError     string `json:"probe_error,omitempty"`

	// Egress findings from the stubbed host function.
	AttemptedURLs  []string `json:"attempted_urls"`
	DisallowedURLs []string `json:"disallowed_urls"`

	// Findings is the human-readable summary, one entry per problem.
	Findings []string `json:"findings"`
}

// OK reports whether the artifact is acceptable under the audit options.
func (r *AuditReport) OK() bool {
	return r.MagicOK && !r.UnsupportedABI && !r.BudgetExceeded && len(r.DisallowedURLs) == 0 && len(r.Findings) == 0
}

// AuditWasmFile reads path (bounded by opts.MaxArtifactBytes), verifies it is a
// wasm module and audits it. The returned error is non-nil only for I/O and
// format failures (ErrArtifactMissing, ErrArtifactTooLarge, ErrArtifactBadMagic);
// a well-formed but non-conforming module returns a report with Findings.
func AuditWasmFile(path string, opts AuditOptions) (*AuditReport, error) {
	data, err := readArtifact(path, opts.withDefaults().MaxArtifactBytes)
	if err != nil {
		return nil, err
	}
	return AuditWasmBytes(path, data, opts)
}

// AuditWasmBytes audits an in-memory artifact. See AuditWasmFile.
func AuditWasmBytes(path string, data []byte, opts AuditOptions) (*AuditReport, error) {
	opts = opts.withDefaults()

	report := &AuditReport{
		Path:           path,
		SizeBytes:      int64(len(data)),
		SHA256:         hexSHA256(data),
		AttemptedURLs:  []string{},
		DisallowedURLs: []string{},
		UnknownImports: []ImportRef{},
		Imports:        []ImportRef{},
		Exports:        []string{},
		MemoryMaxPages: 0,
	}

	sum, err := inspect(data)
	if err != nil {
		return nil, err
	}
	report.MagicOK = sum.magicOK
	report.ModuleVersion = sum.version
	if !sum.magicOK {
		report.Findings = append(report.Findings, "file does not start with the wasm magic preamble")
		return report, fmt.Errorf("%w: %s", ErrArtifactBadMagic, path)
	}
	report.MemoryMinPages = sum.minPages
	report.MemoryMaxPages = sum.maxPages
	report.MemoryBounded = sum.bounded
	report.MemoryBytesRequired = int64(sum.minPages) * wasmPageSize
	report.Imports = sum.imports
	report.Exports = sum.exports
	report.HasEntry = sum.hasEntry

	known := map[string]bool{"env": true, knownWASIModulePrefix: true}
	for _, m := range opts.ExtraAllowedImportModules {
		known[m] = true
	}

	for _, imp := range sum.imports {
		switch {
		case imp.Module == "env" && imp.Kind == "func" && !knownEnvFunctions[imp.Name]:
			report.UnknownImports = append(report.UnknownImports, imp)
		case !known[imp.Module]:
			report.UnknownImports = append(report.UnknownImports, imp)
		}
	}
	report.UnsupportedABI = len(report.UnknownImports) > 0 || !sum.hasEntry

	for _, imp := range report.UnknownImports {
		report.Findings = append(report.Findings, fmt.Sprintf("imports unknown host function %s", imp))
	}
	if !sum.hasEntry {
		report.Findings = append(report.Findings, "module exports no _start entrypoint and has no start section")
	}
	if int64(sum.minPages)*wasmPageSize > int64(opts.MemoryMB)*1024*1024 {
		report.Findings = append(report.Findings, fmt.Sprintf(
			"declared minimum memory %d pages (%d MiB) exceeds the %d MiB budget",
			sum.minPages, int64(sum.minPages)*wasmPageSize/(1<<20), opts.MemoryMB))
	}

	if !opts.RunProbe {
		return report, nil
	}

	attempted, elapsed, exitCode, probeErr := probeWasm(context.Background(), data, sum.exports, opts)
	report.ProbeRan = true
	report.ProbeMillis = elapsed.Milliseconds()
	report.ProbeExitCode = exitCode
	report.AttemptedURLs = attempted
	if probeErr != nil {
		report.ProbeError = probeErr.Error()
	}

	for _, u := range attempted {
		if !hostAllowed(u, opts.AllowedHosts) {
			report.DisallowedURLs = append(report.DisallowedURLs, u)
		}
	}
	for _, u := range report.DisallowedURLs {
		report.Findings = append(report.Findings, fmt.Sprintf("requests egress to %s which is not in the allowlist", u))
	}

	if errors.Is(probeErr, errProbeBudget) {
		report.BudgetExceeded = true
		report.Findings = append(report.Findings, fmt.Sprintf("did not finish within the %d second CPU budget", opts.MaxCPUSeconds))
	} else if probeErr != nil && !errors.Is(probeErr, errProbeCleanExit) {
		report.Findings = append(report.Findings, fmt.Sprintf("probe run failed: %v", probeErr))
	}

	return report, nil
}

// readArtifact enforces the size cap before reading, so an oversized artifact
// cannot be buffered.
func readArtifact(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxArtifactMB * 1024 * 1024
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrArtifactMissing, path)
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: %s is a directory", ErrArtifactMissing, path)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes, limit %d", ErrArtifactTooLarge, path, info.Size(), maxBytes)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrArtifactTooLarge, path, maxBytes)
	}
	return data, nil
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// matchesSHA256 compares a hex digest without leaking its length through timing.
func matchesSHA256(want string, got []byte) bool {
	wantBytes, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(want)))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(wantBytes, got) == 1
}

// hostAllowed applies the same rule the runtime uses: exact host match. An empty
// allowlist denies everything.
func hostAllowed(rawURL string, allowed []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	for _, h := range allowed {
		if u.Host == h {
			return true
		}
	}
	return false
}

// errProbeBudget and errProbeCleanExit are internal sentinels that classify the
// probe outcome.
var (
	errProbeBudget    = errors.New("cpu budget exhausted")
	errProbeCleanExit = errors.New("clean exit")
)

// probeWasm runs the artifact under a bounded budget with no egress: the host
// functions it calls are stubs that record requests and fail. It reports the
// URLs the module asked for.
func probeWasm(ctx context.Context, data []byte, exports []string, opts AuditOptions) (attempted []string, elapsed time.Duration, exitCode uint32, err error) {
	pages, limitErr := memoryPages(opts.MemoryMB)
	if limitErr != nil {
		return nil, 0, 0, limitErr
	}

	// Host functions run on the goroutine that invoked the module, so a plain
	// slice is sufficient and needs no lock.
	recorder := func(requestURL string, _ []byte) {
		attempted = append(attempted, requestURL)
	}

	rt, err := newWasmRuntime(ctx, pages, wasmEnvConfig{
		AllowedHosts: []string{"*"},
		AllowAnyHost: true,
		EgressProbe:  recorder,
	})
	if err != nil {
		return nil, 0, 0, err
	}
	//nolint:contextcheck // the probe's own context may already be spent, and the runtime has to be closed either way
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = rt.Close(closeCtx)
	}()

	compiled, err := rt.CompileModule(ctx, data)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("compile: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(opts.MaxCPUSeconds)*time.Second)
	defer cancel()

	start := time.Now()
	stdout := newCappedWriter(int64(opts.MaxOutputMB)*1024*1024, cancel)
	stderr := newCappedWriter(maxStderrBytes, nil)

	mod, instErr := rt.InstantiateModule(callCtx, compiled, wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(opts.Input)).
		WithStdout(stdout).
		WithStderr(stderr).
		WithName("loopworker-audit"))
	elapsed = time.Since(start)

	if mod != nil {
		closeCtx, closeCloseCancel := context.WithTimeout(context.Background(), shutdownGrace)
		//nolint:contextcheck // same reason as the runtime close above
		_ = mod.Close(closeCtx)
		closeCloseCancel()
	}

	if stdout.didOverflow() {
		return attempted, elapsed, 0, fmt.Errorf("%w: max %d MB", errProbeBudget, opts.MaxOutputMB)
	}

	var exitErr *sys.ExitError
	if errors.As(instErr, &exitErr) {
		exitCode = exitErr.ExitCode()
		switch exitCode {
		case sys.ExitCodeDeadlineExceeded:
			return attempted, elapsed, exitCode, fmt.Errorf("%w: after %d seconds", errProbeBudget, opts.MaxCPUSeconds)
		case sys.ExitCodeContextCanceled:
			if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
				return attempted, elapsed, exitCode, fmt.Errorf("%w: after %d seconds", errProbeBudget, opts.MaxCPUSeconds)
			}
			return attempted, elapsed, exitCode, context.Canceled
		case 0:
			return attempted, elapsed, 0, errProbeCleanExit
		}
		return attempted, elapsed, exitCode, fmt.Errorf("wasm exited with code %d: %w", exitCode, instErr)
	}
	if instErr != nil {
		return attempted, elapsed, exitCode, instErr
	}

	return attempted, elapsed, 0, nil
}

// staticSummary is what the pure decoder learns about an artifact.
type staticSummary struct {
	magicOK  bool
	version  uint32
	imports  []ImportRef
	exports  []string
	minPages uint32
	maxPages uint32
	bounded  bool
	hasEntry bool
}

// inspect decodes the wasm sections needed for auditing. It is a bounded parse
// over the artifact's own bytes: no execution, no host access.
func inspect(data []byte) (*staticSummary, error) {
	if len(data) < 8 {
		return &staticSummary{}, nil
	}
	sum := &staticSummary{magicOK: bytes.Equal(data[:4], wasmMagic), imports: []ImportRef{}, exports: []string{}}
	if !sum.magicOK {
		return sum, nil
	}
	sum.version = uint32(data[4]) | uint32(data[5])<<8 | uint32(data[6])<<16 | uint32(data[7])<<24

	r := &sectionReader{buf: data[8:]}
	for r.offset < len(r.buf) {
		sectionID, err := r.readSectionHeader()
		if err != nil {
			return sum, fmt.Errorf("%w: malformed wasm section: %w", ErrArtifactBadMagic, err)
		}
		body := r.sectionBody()

		switch sectionID {
		case sectionImport:
			imports, err := decodeImports(body)
			if err != nil {
				return sum, fmt.Errorf("%w: import section: %w", ErrArtifactBadMagic, err)
			}
			sum.imports = append(sum.imports, imports...)
		case sectionMemory:
			minPages, maxPages, bounded, err := decodeMemorySection(body)
			if err != nil {
				return sum, fmt.Errorf("%w: memory section: %w", ErrArtifactBadMagic, err)
			}
			if minPages > sum.minPages {
				sum.minPages = minPages
			}
			if bounded {
				sum.bounded = true
				if maxPages > sum.maxPages {
					sum.maxPages = maxPages
				}
			}
		case sectionExport:
			names, err := decodeExports(body)
			if err != nil {
				return sum, fmt.Errorf("%w: export section: %w", ErrArtifactBadMagic, err)
			}
			sum.exports = append(sum.exports, names...)
		case sectionStart:
			sum.hasEntry = true
		}
	}

	for _, name := range sum.exports {
		if name == "_start" || name == "main" || name == "run" {
			sum.hasEntry = true
			break
		}
	}
	sort.Strings(sum.exports)
	return sum, nil
}

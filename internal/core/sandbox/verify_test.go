package sandbox

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAuditReportMemoryFootprint(t *testing.T) {
	path := writeFixtureFile(t, "minmem.wasm", minMemoryWasm(4))

	report, err := AuditWasmFile(path, AuditOptions{RunProbe: false})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if report.MemoryMinPages != 4 {
		t.Errorf("expected 4 pages, got %d", report.MemoryMinPages)
	}
	if report.MemoryBytesRequired != 4*wasmPageSize {
		t.Errorf("expected %d bytes, got %d", 4*wasmPageSize, report.MemoryBytesRequired)
	}
	if report.SizeBytes != int64(len(minMemoryWasm(4))) {
		t.Errorf("unexpected size %d", report.SizeBytes)
	}
	if report.SHA256 == "" {
		t.Error("expected a digest")
	}
	if !report.HasEntry || report.UnsupportedABI {
		t.Errorf("expected a conforming module, got %+v", report)
	}
	if !report.OK() {
		t.Errorf("expected OK, findings=%v", report.Findings)
	}
}

func TestAuditReportUnknownImports(t *testing.T) {
	path := writeFixtureFile(t, "evil.wasm", unknownImportWasm("evil", "pwn"))

	report, err := AuditWasmFile(path, AuditOptions{RunProbe: false})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !report.UnsupportedABI {
		t.Fatal("expected UnsupportedABI for an unknown host module")
	}
	if len(report.UnknownImports) != 1 || report.UnknownImports[0].String() != "evil:pwn" {
		t.Errorf("expected [evil:pwn], got %v", report.UnknownImports)
	}
	if report.OK() {
		t.Error("an unknown import must fail the audit")
	}
	if len(report.Findings) == 0 || !strings.Contains(report.Findings[0], "evil:pwn") {
		t.Errorf("expected a finding naming the import, got %v", report.Findings)
	}
}

func TestAuditReportUnknownEnvFunction(t *testing.T) {
	path := writeFixtureFile(t, "env-evil.wasm", unknownImportWasm("env", "read_credentials"))

	report, err := AuditWasmFile(path, AuditOptions{RunProbe: false})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !report.UnsupportedABI {
		t.Errorf("expected an unknown env function to be flagged, got %+v", report)
	}
}

func TestAuditReportEgressAllowlist(t *testing.T) {
	path := writeFixtureFile(t, "egress.wasm", egressWasm("http://internal-metadata.example/latest/meta-data"))

	report, err := AuditWasmFile(path, AuditOptions{
		AllowedHosts:  []string{"api.trusted.example"},
		MaxCPUSeconds: 10,
		MemoryMB:      16,
		RunProbe:      true,
	})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !report.ProbeRan {
		t.Fatal("expected the probe to run")
	}
	if len(report.AttemptedURLs) != 1 || !strings.Contains(report.AttemptedURLs[0], "internal-metadata.example") {
		t.Fatalf("expected the attempted URL to be recorded, got %v", report.AttemptedURLs)
	}
	if len(report.DisallowedURLs) != 1 {
		t.Errorf("expected the egress violation, got %v", report.DisallowedURLs)
	}
	if report.OK() {
		t.Error("an allowlist violation must fail the audit")
	}

	allowed, err := AuditWasmFile(path, AuditOptions{
		AllowedHosts:  []string{"internal-metadata.example"},
		MaxCPUSeconds: 10,
		MemoryMB:      16,
		RunProbe:      true,
	})
	if err != nil {
		t.Fatalf("audit allowlisted: %v", err)
	}
	if len(allowed.DisallowedURLs) != 0 {
		t.Errorf("an allowlisted host must not be reported, got %v", allowed.DisallowedURLs)
	}
}

func TestAuditReportExhaustsBudget(t *testing.T) {
	path := writeFixtureFile(t, "spin.wasm", spinWasm())

	report, err := AuditWasmFile(path, AuditOptions{MaxCPUSeconds: 1, RunProbe: true})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !report.BudgetExceeded {
		t.Errorf("expected the bounded budget to stop the module, got %+v", report)
	}
	if report.ProbeMillis > 5000 {
		t.Errorf("expected the budget to be enforced near 1s, took %dms", report.ProbeMillis)
	}
	if report.OK() {
		t.Error("a module that outlives its budget must fail the audit")
	}
	if len(report.Findings) == 0 {
		t.Error("expected a finding about the budget")
	}
}

func TestAuditRejectsNonArtifacts(t *testing.T) {
	dir := t.TempDir()
	notWasm := dir + "/junk.wasm"
	if err := writeFixtureBytes(notWasm, []byte("pretend this is a module")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := AuditWasmFile(notWasm, AuditOptions{}); !errors.Is(err, ErrArtifactBadMagic) {
		t.Errorf("expected ErrArtifactBadMagic, got %v", err)
	}
	if _, err := AuditWasmFile(dir+"/absent.wasm", AuditOptions{}); !errors.Is(err, ErrArtifactMissing) {
		t.Errorf("expected ErrArtifactMissing, got %v", err)
	}

	path := writeFixtureFile(t, "big.wasm", minMemoryWasm(1))
	if _, err := AuditWasmFile(path, AuditOptions{MaxArtifactBytes: 8}); !errors.Is(err, ErrArtifactTooLarge) {
		t.Errorf("expected ErrArtifactTooLarge, got %v", err)
	}
}

func TestAuditMalformedSectionIsRejected(t *testing.T) {
	// A truncated section header must produce a typed error, never a panic.
	truncated := append([]byte{}, wasmMagic...)
	truncated = append(truncated, 0x01, 0x00, 0x00, 0x00)
	truncated = append(truncated, 0x02, 0x40, 0x01, 0x02) // import section claiming 64 bytes

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("audit panicked on a malformed artifact: %v", r)
		}
	}()

	path := writeFixtureFile(t, "truncated.wasm", truncated)
	if _, err := AuditWasmFile(path, AuditOptions{RunProbe: false}); err == nil {
		t.Error("expected a malformed section to be rejected")
	}
}

func TestAuditRealToolchainArtifact(t *testing.T) {
	artifact := realWasmArtifact(t, "go-wasi-echo.wasm")
	if artifact == nil {
		t.Skip("testdata/go-wasi-echo.wasm missing; build it with GOOS=wasip1 GOARCH=wasm")
	}

	path := writeFixtureFile(t, "go-echo.wasm", artifact)

	report, err := AuditWasmFile(path, AuditOptions{
		MemoryMB:      512,
		MaxCPUSeconds: 20,
		MaxOutputMB:   8,
		RunProbe:      true,
		Input:         []byte("audited input\n"),
	})
	if err != nil {
		t.Fatalf("audit real artifact: %v", err)
	}
	if report.MemoryMinPages < 30 {
		t.Errorf("expected a real Go module to demand tens of pages, got %d", report.MemoryMinPages)
	}
	if len(report.UnknownImports) != 0 {
		t.Errorf("WASI imports must be recognised, got %v", report.UnknownImports)
	}
	if !report.HasEntry {
		t.Error("expected a _start entrypoint")
	}
	if report.BudgetExceeded {
		t.Errorf("an echoing module should finish inside its budget: %+v", report)
	}
	if !report.OK() {
		t.Errorf("expected a conforming artifact, findings=%v", report.Findings)
	}
}

func writeFixtureBytes(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

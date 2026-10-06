package sandbox

import (
	"context"
	"os"
	"testing"
)

func TestFixturesAreValidWasm(t *testing.T) {
	ctx := context.Background()
	sb := NewSandbox(SandboxConfig{MaxMemoryMB: 64, MaxCPUSeconds: 2})
	defer sb.Close(ctx)

	fixtures := map[string][]byte{
		"fetch":    fetchWasm("http://127.0.0.1:1/x"),
		"grow":     growWasm(8),
		"minmem":   minMemoryWasm(4),
		"spin":     spinWasm(),
		"noisy":    noisyWasm([]byte("0123456789abcdef0123456789abcdef")),
		"badabi":   unknownImportWasm("evil", "danger"),
		"badmagic": []byte("not wasm at all, longer than 8 bytes"),
	}
	for name, data := range fixtures {
		path := writeFixtureFile(t, name+".wasm", data)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(data) {
			t.Fatalf("fixture %s roundtrip: %v", name, err)
		}
		report, err := AuditWasmFile(path, AuditOptions{RunProbe: false, MemoryMB: 64})
		t.Logf("%s: err=%v findings=%v imports=%v minPages=%d hasEntry=%v", name, err, report.Findings, report.Imports, report.MemoryMinPages, report.HasEntry)
	}
}

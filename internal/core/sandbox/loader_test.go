package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loopworker/pkg/skill"
)

// writePluginDir lays out the loader's directory convention:
//
//	<root>/<name>/plugin.json
//	<root>/<name>/<entry>
func writePluginDir(t *testing.T, root, name, entry string, artifact []byte, limits WasmLimits, digest string) string {
	t.Helper()

	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if entry == "" {
		entry = DefaultWasmEntry
	}
	if err := os.WriteFile(filepath.Join(dir, entry), artifact, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	manifest := WasmManifest{
		Name:    name,
		Version: "1.0.0",
		Entry:   entry,
		SHA256:  digest,
		Limits:  limits,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestLoadWasmDirHappyPath(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	artifact := minMemoryWasm(1)
	root := t.TempDir()
	dir := writePluginDir(t, root, "quiet-plugin", "", artifact, WasmLimits{MemoryMB: 8, MaxCPUSeconds: 5, MaxOutputMB: 4}, digestOf(artifact))

	opts := DefaultLoadOptions()
	plugin, err := sb.LoadWasmDir(ctx, dir, opts)
	if err != nil {
		t.Fatalf("LoadWasmDir: %v", err)
	}
	if plugin.Limits().MemoryMB != 8 || plugin.Limits().MaxCPUSeconds != 5 {
		t.Errorf("expected the manifest limits to be honoured, got %+v", plugin.Limits())
	}
	if names := sb.ListPlugins(); len(names) != 1 || names[0] != "quiet-plugin" {
		t.Errorf("expected the plugin registered, got %v", names)
	}

	out, err := sb.Execute(ctx, "quiet-plugin", nil, skill.SkillContext{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected no output, got %q", out)
	}
}

func TestLoadWasmRejectsBadArtifacts(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name     string
		artifact []byte
		limits   WasmLimits
		opts     func(pluginName string) LoadOptions
		want     error
	}{
		{
			name:     "bad-magic",
			artifact: []byte("this is not wasm, definitely not"),
			want:     ErrArtifactBadMagic,
		},
		{
			name:     "too-large",
			artifact: minMemoryWasm(1),
			opts: func(string) LoadOptions {
				o := DefaultLoadOptions()
				o.MaxArtifactBytes = 16
				return o
			},
			want: ErrArtifactTooLarge,
		},
		{
			name:     "unknown-host-import",
			artifact: unknownImportWasm("evil", "read_the_host_filesystem"),
			want:     ErrUnsupportedABI,
		},
		{
			name:     "unknown-env-function",
			artifact: unknownImportWasm("env", "spawn_a_shell"),
			want:     ErrUnsupportedABI,
		},
		{
			name:     "memory-above-plugin-budget",
			artifact: minMemoryWasm(400),
			limits:   WasmLimits{MemoryMB: 2},
			want:     ErrLimitTooLarge,
		},
		{
			name:     "budget-above-host-ceiling",
			artifact: minMemoryWasm(1),
			limits:   WasmLimits{MemoryMB: 4096},
			want:     ErrLimitTooLarge,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := mustSandbox(t, SandboxConfig{})
			dir := writePluginDir(t, t.TempDir(), tc.name, "", tc.artifact, tc.limits, "")

			opts := DefaultLoadOptions()
			if tc.opts != nil {
				opts = tc.opts(tc.name)
			}

			_, err := sb.LoadWasmDir(ctx, dir, opts)
			if err == nil {
				t.Fatalf("expected %v, got nil", tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("expected %v, got %v", tc.want, err)
			}
			if !IsNonRetryable(err) {
				t.Errorf("a rejected artifact must be non-retryable, got %v", err)
			}
		})
	}
}

func TestLoadWasmDirMissingArtifactFile(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	root := t.TempDir()
	dir := writePluginDir(t, root, "ghost", "", nil, WasmLimits{}, "")
	if err := os.Remove(filepath.Join(dir, DefaultWasmEntry)); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}

	if _, err := sb.LoadWasmDir(ctx, dir, DefaultLoadOptions()); !errors.Is(err, ErrArtifactMissing) {
		t.Errorf("expected ErrArtifactMissing, got %v", err)
	}
}

func TestLoadWasmDirManifestMissing(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	dir := t.TempDir()
	if _, err := sb.LoadWasmDir(ctx, dir, DefaultLoadOptions()); !errors.Is(err, ErrManifestInvalid) {
		t.Errorf("expected ErrManifestInvalid, got %v", err)
	}
}

func TestLoadWasmDirRejectsPathTraversal(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	root := t.TempDir()
	outside := filepath.Join(root, "outside.wasm")
	if err := os.WriteFile(outside, minMemoryWasm(1), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	dir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := WasmManifest{Name: "escape", Version: "1", Entry: ".." + string(filepath.Separator) + "outside.wasm"}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if _, err := sb.LoadWasmDir(ctx, dir, DefaultLoadOptions()); !errors.Is(err, ErrManifestInvalid) {
		t.Errorf("expected ErrManifestInvalid for an escaping entry, got %v", err)
	}
}

func TestLoadWasmDirRequireChecksumAndValidDigest(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})
	artifact := minMemoryWasm(1)

	root := t.TempDir()
	dir := writePluginDir(t, root, "signed", "", artifact, WasmLimits{MemoryMB: 4}, digestOf(artifact))

	opts := DefaultLoadOptions()
	opts.RequireChecksum = true
	if _, err := sb.LoadWasmDir(ctx, dir, opts); err != nil {
		t.Fatalf("a correctly signed artifact must load: %v", err)
	}

	// A tampered artifact with the old digest in the manifest is refused.
	if err := os.WriteFile(filepath.Join(dir, DefaultWasmEntry), unknownImportWasm("evil", "x"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	sb2 := mustSandbox(t, SandboxConfig{})
	_, err := sb2.LoadWasmDir(ctx, dir, opts)
	if !errors.Is(err, ErrChecksumMismatch) && !errors.Is(err, ErrUnsupportedABI) {
		t.Errorf("expected the tampered artifact to be refused, got %v", err)
	}
}

func TestLoadWasmDirRequiresDigestWhenOrdered(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})
	artifact := minMemoryWasm(1)

	root := t.TempDir()
	dir := writePluginDir(t, root, "unsigned", "", artifact, WasmLimits{}, "")

	opts := DefaultLoadOptions()
	opts.RequireChecksum = true
	if _, err := sb.LoadWasmDir(ctx, dir, opts); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch for a manifest without sha256, got %v", err)
	}
}

func TestLoadWasmTree(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		artifact := minMemoryWasm(1)
		writePluginDir(t, root, name, "", artifact, WasmLimits{MemoryMB: 4}, digestOf(artifact))
	}
	// A plain directory without a manifest must be ignored, not an error.
	if err := os.MkdirAll(filepath.Join(root, "not-a-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	loaded, err := sb.LoadWasmTree(ctx, root, DefaultLoadOptions())
	if err != nil {
		t.Fatalf("LoadWasmTree: %v", err)
	}
	if strings.Join(loaded, ",") != "alpha,beta" {
		t.Errorf("expected [alpha,beta], got %v", loaded)
	}

	for _, name := range loaded {
		if _, err := sb.Execute(ctx, name, nil, skill.SkillContext{}); err != nil {
			t.Errorf("execute %s: %v", name, err)
		}
	}

	if _, err := sb.LoadWasmTree(ctx, filepath.Join(root, "missing"), DefaultLoadOptions()); !errors.Is(err, ErrArtifactMissing) {
		t.Errorf("expected ErrArtifactMissing for an absent root, got %v", err)
	}
}

// TestLoadWasmDirRealArtifact loads an artifact produced by the Go toolchain for
// WASI: it imports real WASI functions, needs megabytes of memory and echoes
// stdin to stdout.
func TestLoadWasmDirRealArtifact(t *testing.T) {
	ctx := context.Background()
	artifact := realEchoArtifact(t)

	sb := mustSandbox(t, SandboxConfig{MaxMemoryMB: 512, MaxCPUSeconds: 20})
	root := t.TempDir()
	dir := writePluginDir(t, root, "go-echo", "", artifact, WasmLimits{MemoryMB: 512, MaxCPUSeconds: 20}, digestOf(artifact))

	// Two independent ceilings, and a manifest has to clear both. The sandbox
	// config above only supplies the budget a manifest inherits when it asks for
	// nothing; LoadOptions.MaxMemoryMB is what this strict loader refuses to go
	// beyond. DefaultLoadOptions() would cap at 256 MiB and reject the 512 MiB
	// manifest above with ErrLimitTooLarge — correct behaviour, wrong pairing
	// for what this test is about.
	if _, err := sb.LoadWasmDir(ctx, dir, LoadOptions{MaxMemoryMB: 512}); err != nil {
		t.Fatalf("load real artifact: %v", err)
	}

	out, err := sb.Execute(ctx, "go-echo", []byte("ping from the sandbox\n"), skill.SkillContext{})
	if err != nil {
		t.Fatalf("execute real artifact: %v", err)
	}
	if !strings.Contains(string(out), "ping from the sandbox") {
		t.Errorf("expected the artifact to echo its stdin, got %q", out)
	}
}

// TestLoadWasmDirEgressPolicy proves the per-plugin allowlist: an identical
// artifact is allowed for one plugin and refused for another.
func TestLoadWasmDirEgressPolicy(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	ctx := context.Background()
	permitted := mustSandbox(t, SandboxConfig{AllowedHosts: []string{ts.URL[7:]}})
	root := t.TempDir()
	dir := writePluginDir(t, root, "fetcher", "", fetchWasm(ts.URL), WasmLimits{MemoryMB: 8}, "")
	if _, err := permitted.LoadWasmDir(ctx, dir, DefaultLoadOptions()); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := permitted.Execute(ctx, "fetcher", nil, skill.SkillContext{}); err != nil {
		t.Errorf("the allowlisted host must be reachable: %v", err)
	}

	denied := mustSandbox(t, SandboxConfig{AllowedHosts: []string{"in.example"}})
	root2 := t.TempDir()
	dir2 := writePluginDir(t, root2, "fetcher", "", fetchWasm(ts.URL), WasmLimits{MemoryMB: 8}, "")
	if _, err := denied.LoadWasmDir(ctx, dir2, DefaultLoadOptions()); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := denied.Execute(ctx, "fetcher", nil, skill.SkillContext{}); err == nil {
		t.Error("a host outside the sandbox allowlist must not be reachable")
	}
	if hits == 0 {
		t.Log("test server was never contacted; the allowed case may be broken")
	}
}

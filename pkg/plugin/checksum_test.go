package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loopworker/internal/core/sandbox"
	"loopworker/pkg/event"
)

// artifactDigest is what a correctly published manifest declares: the sha256 of
// the bytes actually shipped. It is computed from the fixture rather than pasted
// as a constant so the "matching digest" case cannot rot when testdata changes.
func artifactDigest(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hello.wasm"))
	if err != nil {
		t.Fatalf("read testdata/hello.wasm: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writePluginWithDigest writes a loadable plugin whose manifest declares declared.
// Pass a wrong digest to describe a plugin whose bytes contradict its manifest.
func writePluginWithDigest(t *testing.T, dir, name, declared string) {
	t.Helper()
	if err := writeWasmPlugin(dir, PluginInfo{Name: name, Version: "1.0", SHA256: declared}); err != nil {
		t.Fatalf("write plugin %s: %v", name, err)
	}
}

func newManagerFor(t *testing.T, pluginsDir string, opts ...PluginManagerOption) (*PluginManager, *sandbox.Sandbox) {
	t.Helper()
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	t.Cleanup(func() { bus.Close() })

	mgr, err := NewPluginManager(sb, bus, pluginsDir, opts...)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return mgr, sb
}

// TestMismatchedDigestLoadsWhenVerificationIsOff records the gap this flag
// exists to close, and pins the default: plugins.verify_checksum is off, so a
// plugin whose bytes contradict its own manifest still loads today.
//
// If this test ever fails because the plugin was refused, the default became
// stricter than documented - that is a behaviour change, not a fix.
func TestMismatchedDigestLoadsWhenVerificationIsOff(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "undeclared")
	const wrongDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	writePluginWithDigest(t, pluginDir, "undeclared", wrongDigest)

	mgr, _ := newManagerFor(t, filepath.Join(tmp, "plugins"))
	if err := mgr.LoadPlugin(context.Background(), pluginDir); err != nil {
		t.Fatalf("with verification off, a mismatched digest must not block loading: %v", err)
	}
	if _, ok := mgr.GetPlugin("undeclared"); !ok {
		t.Error("plugin should be listed as loaded with verification off")
	}
}

func TestMismatchedDigestIsRefusedWhenVerificationIsOn(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "tampered")
	const wrongDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	writePluginWithDigest(t, pluginDir, "tampered", wrongDigest)

	mgr, _ := newManagerFor(t, filepath.Join(tmp, "plugins"), WithVerifyChecksum(true))
	err := mgr.LoadPlugin(context.Background(), pluginDir)
	if err == nil {
		t.Fatal("a plugin whose bytes contradict its manifest must be refused when verify_checksum is on")
	}
	if !errors.Is(err, sandbox.ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch so the sandbox retry policy can classify it, got %v", err)
	}
	if _, ok := mgr.GetPlugin("tampered"); ok {
		t.Error("a refused plugin must not be listed as loaded")
	}

	// The message has to be actionable without reading this repository.
	msg := err.Error()
	for _, want := range []string{
		"plugins.verify_checksum", // which setting did this
		wrongDigest,               // what the manifest expected
		filepath.Join(pluginDir, "plugin.json"),
		"cause:", "fix:", "docs:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message must mention %q, got:\n%s", want, msg)
		}
	}
}

func TestMissingDigestIsRefusedWhenVerificationIsOn(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "unsigned")
	writePluginWithDigest(t, pluginDir, "unsigned", "")

	mgr, _ := newManagerFor(t, filepath.Join(tmp, "plugins"), WithVerifyChecksum(true))
	err := mgr.LoadPlugin(context.Background(), pluginDir)
	if err == nil {
		t.Fatal("verification on must refuse a manifest that declares no sha256, or the switch verifies nothing")
	}
	if !errors.Is(err, sandbox.ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch, got %v", err)
	}
	if _, ok := mgr.GetPlugin("unsigned"); ok {
		t.Error("a refused plugin must not be listed as loaded")
	}
}

func TestMatchingDigestStillLoadsWhenVerificationIsOn(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "signed")
	writePluginWithDigest(t, pluginDir, "signed", artifactDigest(t))

	mgr, _ := newManagerFor(t, filepath.Join(tmp, "plugins"), WithVerifyChecksum(true))
	if err := mgr.LoadPlugin(context.Background(), pluginDir); err != nil {
		t.Fatalf("a plugin whose manifest matches its bytes must load with verification on: %v", err)
	}
	if _, ok := mgr.GetPlugin("signed"); !ok {
		t.Error("plugin should be listed as loaded")
	}
}

// One bad plugin must not take the process down: the refusal is per plugin and
// the rest of the directory still loads.
func TestOneRefusedPluginDoesNotStopTheOthers(t *testing.T) {
	tmp := t.TempDir()
	pluginsDir := filepath.Join(tmp, "plugins")
	writePluginWithDigest(t, filepath.Join(pluginsDir, "good"), "good", artifactDigest(t))
	writePluginWithDigest(t, filepath.Join(pluginsDir, "bad"), "bad",
		"1111111111111111111111111111111111111111111111111111111111111111")

	mgr, _ := newManagerFor(t, pluginsDir, WithVerifyChecksum(true))
	err := mgr.LoadAllPlugins(context.Background())
	if err == nil {
		t.Fatal("LoadAllPlugins must report the refused plugin")
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("error should name how many failed, got: %v", err)
	}
	if _, ok := mgr.GetPlugin("good"); !ok {
		t.Error("the plugin that passed verification must still be loaded")
	}
	if _, ok := mgr.GetPlugin("bad"); ok {
		t.Error("the refused plugin must not be loaded")
	}
}

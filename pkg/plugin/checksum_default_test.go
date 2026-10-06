package plugin

import (
	"context"
	"path/filepath"
	"testing"
)

// TestLoadAllPluginsToleratesMismatchedDigestByDefault is the honest default,
// asserted through the batch path an operator actually uses.
//
// Recorded here because it used to be the whole story: LoadPlugin parsed
// plugin.json, ignored the sha256 field entirely, and any .wasm in the plugins
// directory executed whatever its manifest claimed. plugins.verify_checksum
// closes that; this test exists so the default is never widened silently.
func TestLoadAllPluginsToleratesMismatchedDigestByDefault(t *testing.T) {
	tmp := t.TempDir()
	pluginsDir := filepath.Join(tmp, "plugins")
	writePluginWithDigest(t, filepath.Join(pluginsDir, "wrong-digest"), "wrong-digest",
		"0000000000000000000000000000000000000000000000000000000000000000")

	mgr, _ := newManagerFor(t, pluginsDir) // no WithVerifyChecksum: the default
	if err := mgr.LoadAllPlugins(context.Background()); err != nil {
		t.Fatalf("with verification off the load must succeed, got: %v", err)
	}
	if _, ok := mgr.GetPlugin("wrong-digest"); !ok {
		t.Error("plugin with a wrong digest is loaded when verification is off - that is the documented default")
	}
}

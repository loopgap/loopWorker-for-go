package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyChecksumIsOffByDefaultAndConfigurable pins the operator switch.
//
// The default is deliberately false: enabling checksum verification unannounced
// would refuse plugins that load fine today, which is not a change this setting
// may make on its own.
func TestVerifyChecksumIsOffByDefaultAndConfigurable(t *testing.T) {
	if Defaults().Plugins.VerifyChecksum {
		t.Error("plugins.verify_checksum must default to false - turning it on would refuse plugins that load today")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write(t, path, "plugins:\n  verify_checksum: true\n")

	cfg, err := Load(Options{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Plugins.VerifyChecksum {
		t.Error("file value ignored")
	}

	t.Setenv("LOOPWORKER_PLUGINS_VERIFY_CHECKSUM", "false")
	cfg, err = Load(Options{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}
	if cfg.Plugins.VerifyChecksum {
		t.Error("env must beat file, like every other key")
	}

	write(t, path, "plugins:\n  verify_checksum: maybe\n")
	if _, err := Load(Options{ConfigFile: path}); err == nil {
		t.Error("a non-boolean verify_checksum must be rejected instead of ignored")
	}

	// The shape error has to name the right type, or the operator is told to fix
	// a key that was never wrong.
	write(t, path, "plugins:\n  verify_checksum: [yes, no]\n")
	_, err = Load(Options{ConfigFile: path})
	if err == nil || !strings.Contains(err.Error(), "true or false") {
		t.Errorf("a list must be reported as a boolean key, got: %v", err)
	}
}

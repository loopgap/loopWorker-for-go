package config

import "testing"

// TestAdminPortIsConfigurableFromFileAndEnv guards a real gap found by running
// the shipped binary: the admin listener's port was a compile-time constant, so
// two LoopWorker servers on one host collided on it and the second one's
// /metrics silently disappeared behind a warning.
func TestAdminPortIsConfigurableFromFileAndEnv(t *testing.T) {
	def := Defaults()
	if def.Server.AdminPort != 19528 {
		t.Errorf("default admin port: got %d, want 19528", def.Server.AdminPort)
	}

	dir := t.TempDir()
	path := dir + "/config.yaml"
	write(t, path, "server:\n  admin_port: 20222\n")
	cfg, err := Load(Options{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.AdminPort != 20222 {
		t.Errorf("file value ignored: got %d, want 20222", cfg.Server.AdminPort)
	}

	t.Setenv("LOOPWORKER_API_ADMIN_PORT", "20333")
	cfg, err = Load(Options{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}
	if cfg.Server.AdminPort != 20333 {
		t.Errorf("env must beat file: got %d, want 20333", cfg.Server.AdminPort)
	}
}

func TestAdminPortOutOfRangeIsRejected(t *testing.T) {
	for _, v := range []string{"0", "70000", "-1"} {
		dir := t.TempDir()
		path := dir + "/config.yaml"
		write(t, path, "server:\n  admin_port: "+v+"\n")
		if _, err := Load(Options{ConfigFile: path}); err == nil {
			t.Errorf("admin_port %s was accepted", v)
		}
	}
}

// A bare `loopworker` on a fresh machine must be able to start. It used to exit
// 1: the default listener was 0.0.0.0 and the server refuses to serve a
// writable API on a public interface without configured credentials. Safe, but
// the first thing a new user sees is a refusal to boot.
func TestDefaultListenerIsLoopback(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("Load with no configuration at all: %v", err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("default listener = %q, want 127.0.0.1: a public default cannot start "+
			"without credentials, so the out-of-box run would refuse to boot", cfg.Server.Host)
	}
}

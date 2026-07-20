package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Port != 19527 {
		t.Errorf("expected port 19527, got %d", cfg.Port)
	}
	if cfg.PluginsDir != "./plugins" {
		t.Errorf("expected plugins dir './plugins', got '%s'", cfg.PluginsDir)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected log level 'info', got '%s'", cfg.LogLevel)
	}
}

func TestConfigJSON(t *testing.T) {
	cfg := DefaultConfig()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	if loaded.Port != cfg.Port {
		t.Errorf("port mismatch: expected %d, got %d", cfg.Port, loaded.Port)
	}
}

func TestValidateValidConfig(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config should not return error: %v", err)
	}
}

func TestValidateInvalidPort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Port = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid port")
	}

	cfg.Port = 99999
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid port")
	}
}

func TestValidateInvalidLogLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogLevel = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid log level")
	}
}

func TestValidateInvalidSandbox(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.MaxMemoryMB = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid sandbox config")
	}
}

func TestValidateInvalidWorkers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workers.Count = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid workers count")
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	cfg.Port = 9090
	data, _ := json.Marshal(cfg)
	os.WriteFile(configPath, data, 0644)

	loaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if loaded.Port != 9090 {
		t.Errorf("expected port 9090, got %d", loaded.Port)
	}
}

func TestLoadConfigDefault(t *testing.T) {
	loaded, err := LoadConfig("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if loaded.Port != 19527 {
		t.Errorf("expected default port 19527, got %d", loaded.Port)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	os.Setenv("LOOPWORKER_PORT", "7777")
	os.Setenv("LOOPWORKER_LOG_LEVEL", "debug")
	defer os.Unsetenv("LOOPWORKER_PORT")
	defer os.Unsetenv("LOOPWORKER_LOG_LEVEL")

	loaded, err := LoadConfig("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if loaded.Port != 7777 {
		t.Errorf("expected port 7777 from env, got %d", loaded.Port)
	}
	if loaded.LogLevel != "debug" {
		t.Errorf("expected log level 'debug' from env, got '%s'", loaded.LogLevel)
	}
}

func TestSaveConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	loaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if loaded.Port != cfg.Port {
		t.Errorf("port mismatch after save/load")
	}
}

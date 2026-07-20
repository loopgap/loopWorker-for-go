package main

import (
	"encoding/json"
	"testing"

	"loopworker/internal/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Port != 19527 {
		t.Errorf("expected port 19527, got %d", cfg.Port)
	}
	if cfg.PluginsDir != "./plugins" {
		t.Errorf("expected plugins dir './plugins', got '%s'", cfg.PluginsDir)
	}
}

func TestConfigJSON(t *testing.T) {
	cfg := config.DefaultConfig()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var loaded config.Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	if loaded.Port != cfg.Port {
		t.Errorf("port mismatch: expected %d, got %d", cfg.Port, loaded.Port)
	}
}

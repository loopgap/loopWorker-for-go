package config

import (
	"encoding/json"
	"fmt"
	"loopworker/pkg/errors"
	"os"
	"strconv"
)

type Config struct {
	Port       int           `json:"port"`
	PluginsDir string        `json:"plugins_dir"`
	DataDir    string        `json:"data_dir"`
	LogLevel   string        `json:"log_level"`
	Sandbox    SandboxConfig `json:"sandbox"`
	Workers    WorkersConfig `json:"workers"`
}

type SandboxConfig struct {
	MaxMemoryMB   int `json:"max_memory_mb"`
	MaxCPUSeconds int `json:"max_cpu_seconds"`
	MaxOutputMB   int `json:"max_output_mb"`
	MaxConcurrent int `json:"max_concurrent"`
}

type WorkersConfig struct {
	Count int `json:"count"`
}

func DefaultConfig() *Config {
	return &Config{
		Port:       19527,
		PluginsDir: "./plugins",
		DataDir:    "./data",
		LogLevel:   "info",
		Sandbox: SandboxConfig{
			MaxMemoryMB:   256,
			MaxCPUSeconds: 30,
			MaxOutputMB:   64,
			MaxConcurrent: 10,
		},
		Workers: WorkersConfig{
			Count: 4,
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	config := DefaultConfig()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return config, nil
			}
			return nil, fmt.Errorf("read config file: %w", err)
		}

		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("parse config file: %w", err)
		}
	}

	config.applyEnvironmentOverrides()

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return config, nil
}

func (c *Config) applyEnvironmentOverrides() {
	if v := os.Getenv("LOOPWORKER_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			c.Port = port
		}
	}

	if v := os.Getenv("LOOPWORKER_PLUGINS_DIR"); v != "" {
		c.PluginsDir = v
	}

	if v := os.Getenv("LOOPWORKER_DATA_DIR"); v != "" {
		c.DataDir = v
	}

	if v := os.Getenv("LOOPWORKER_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}

	if v := os.Getenv("LOOPWORKER_WORKERS"); v != "" {
		if count, err := strconv.Atoi(v); err == nil {
			c.Workers.Count = count
		}
	}

	if v := os.Getenv("LOOPWORKER_SANDBOX_MAX_MEMORY"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil {
			c.Sandbox.MaxMemoryMB = mb
		}
	}

	if v := os.Getenv("LOOPWORKER_SANDBOX_MAX_CPU_SECONDS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			c.Sandbox.MaxCPUSeconds = secs
		}
	}

	if v := os.Getenv("LOOPWORKER_SANDBOX_MAX_CONCURRENT"); v != "" {
		if conc, err := strconv.Atoi(v); err == nil {
			c.Sandbox.MaxConcurrent = conc
		}
	}
}

func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
	}

	if c.PluginsDir == "" {
		return errors.ErrConfigInvalid
	}

	if c.DataDir == "" {
		return errors.ErrConfigInvalid
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("log_level must be one of: debug, info, warn, error, got %s", c.LogLevel)
	}

	if c.Sandbox.MaxMemoryMB < 1 {
		return fmt.Errorf("sandbox.max_memory_mb must be positive, got %d", c.Sandbox.MaxMemoryMB)
	}

	if c.Sandbox.MaxCPUSeconds < 1 {
		return fmt.Errorf("sandbox.max_cpu_seconds must be positive, got %d", c.Sandbox.MaxCPUSeconds)
	}

	if c.Sandbox.MaxOutputMB < 1 {
		return fmt.Errorf("sandbox.max_output_mb must be positive, got %d", c.Sandbox.MaxOutputMB)
	}

	if c.Sandbox.MaxConcurrent < 1 {
		return fmt.Errorf("sandbox.max_concurrent must be positive, got %d", c.Sandbox.MaxConcurrent)
	}

	if c.Workers.Count < 1 {
		return fmt.Errorf("workers.count must be positive, got %d", c.Workers.Count)
	}

	return nil
}

func SaveConfig(path string, config *Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

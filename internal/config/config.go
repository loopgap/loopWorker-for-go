// Package config is the single configuration system for LoopWorker.
//
// Layer precedence is fixed: defaults < config file < environment < flags.
// Every accepted key is declared once in specs (spec.go), which is where the
// file key, the environment variable name and the resolved-value report all come
// from. Unknown keys, unparsable values and invalid combinations are hard errors
// that name the key, the file it came from and the accepted values, so a
// misconfigured install fails at startup instead of limping along silently.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loopworker/pkg/security"
)

// Config is the complete resolved configuration of a LoopWorker server.
type Config struct {
	WorkDir  string         `yaml:"work_dir" json:"work_dir"`
	Server   ServerConfig   `yaml:"server" json:"server"`
	Data     DataConfig     `yaml:"data" json:"data"`
	Plugins  PluginsConfig  `yaml:"plugins" json:"plugins"`
	Logging  LoggingConfig  `yaml:"logging" json:"logging"`
	Security SecurityConfig `yaml:"security" json:"security"`
	Sandbox  SandboxConfig  `yaml:"sandbox" json:"sandbox"`
	Workers  WorkersConfig  `yaml:"workers" json:"workers"`
	SelfHeal SelfHealConfig `yaml:"selfheal" json:"selfheal"`
	Workflow WorkflowConfig `yaml:"workflow" json:"workflow"`
	LLM      LLMConfig      `yaml:"llm" json:"llm"`

	sources       map[string]string
	configFile    string
	searchedPaths []string
}

type ServerConfig struct {
	Host            string        `yaml:"host" json:"host"`
	Port            int           `yaml:"port" json:"port"`
	ReadTimeout     time.Duration `yaml:"read_timeout" json:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout" json:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" json:"shutdown_timeout"`
	// AdminPort is the port of the loopback-only observability listener
	// (/metrics, /runtime/stats, /logs). Its host is deliberately not
	// configurable: exposing the admin surface on a public interface would
	// hand out metrics to anyone who can reach the port.
	AdminPort int `yaml:"admin_port" json:"admin_port"`
}

type DataConfig struct {
	Dir    string `yaml:"dir" json:"dir"`
	DBFile string `yaml:"db_file" json:"db_file"`
}

type PluginsConfig struct {
	Dir      string `yaml:"dir" json:"dir"`
	AutoLoad bool   `yaml:"auto_load" json:"auto_load"`
	// VerifyChecksum makes loading enforce the sha256 each manifest declares.
	// It is off by default: switching it on refuses plugins that load fine
	// today (most manifests declare no digest at all), and that is an operator's
	// decision to make once their plugins carry digests, not a default change.
	VerifyChecksum bool `yaml:"verify_checksum" json:"verify_checksum"`
}

type LoggingConfig struct {
	Level  string `yaml:"level" json:"level"`
	Format string `yaml:"format" json:"format"`
	Output string `yaml:"output" json:"output"`
}

type SecurityConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	AuthRequired bool   `yaml:"auth_required" json:"auth_required"`
	APIKey       string `yaml:"api_key" json:"api_key"`
}

// Posture states the effective security stance in one line, for logs, health
// output and the doctor dump.
func (s SecurityConfig) Posture() string {
	switch {
	case !s.Enabled:
		return "security disabled (security.enabled=false)"
	case s.AuthRequired:
		return "API key required on every endpoint except /healthz"
	default:
		return "no authentication required"
	}
}

type SandboxConfig struct {
	MaxMemoryMB   int      `yaml:"max_memory_mb" json:"max_memory_mb"`
	MaxCPUSeconds int      `yaml:"max_cpu_seconds" json:"max_cpu_seconds"`
	MaxOutputMB   int      `yaml:"max_output_mb" json:"max_output_mb"`
	MaxConcurrent int      `yaml:"max_concurrent" json:"max_concurrent"`
	AllowedHosts  []string `yaml:"allowed_hosts" json:"allowed_hosts"`
}

type WorkersConfig struct {
	Count       int           `yaml:"count" json:"count"`
	TaskTimeout time.Duration `yaml:"task_timeout" json:"task_timeout"`
}

type SelfHealConfig struct {
	Enabled        bool                 `yaml:"enabled" json:"enabled"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker" json:"circuit_breaker"`
	Retry          SelfHealRetryConfig  `yaml:"retry" json:"retry"`
}

type CircuitBreakerConfig struct {
	Threshold int           `yaml:"threshold" json:"threshold"`
	Timeout   time.Duration `yaml:"timeout" json:"timeout"`
}

type SelfHealRetryConfig struct {
	MaxAttempts int           `yaml:"max_attempts" json:"max_attempts"`
	Backoff     time.Duration `yaml:"backoff" json:"backoff"`
}

type WorkflowConfig struct {
	MaxConcurrent int           `yaml:"max_concurrent" json:"max_concurrent"`
	Timeout       time.Duration `yaml:"timeout" json:"timeout"`
}

type LLMConfig struct {
	BaseURL string `yaml:"base_url" json:"base_url"`
	APIKey  string `yaml:"api_key" json:"api_key"`
	Model   string `yaml:"model" json:"model"`
}

// Accepted enum values, referenced by Validate and the startup report.
const (
	AcceptedLogLevels  = "debug, info, warn, error"
	AcceptedLogFormats = "json, text"
	AcceptedLogOutputs = "stdout, stderr"
)

// Defaults returns the configuration used when nothing is set. Data lives under
// a real per-user directory so a first run never litters the working directory.
func Defaults() *Config {
	c := &Config{sources: map[string]string{}}
	applyDefaults(c)
	return c
}

func applyDefaults(c *Config) {
	home := defaultWorkDir()
	c.WorkDir = home
	// Loopback by default. The server refuses to serve a writable API on a
	// public interface without configured credentials, so a 0.0.0.0 default
	// meant a bare `loopworker` exited 1 on a fresh install - safe, but not
	// usable. Containers that need to be reachable set this explicitly; the
	// Dockerfile does.
	c.Server.Host = "127.0.0.1"
	c.Server.Port = 19527
	c.Server.ReadTimeout = 30 * time.Second
	// Zero keeps the HTTP write deadline off, which is what makes the streaming
	// endpoint (/api/v1/events/live) survive on a fresh install.
	c.Server.WriteTimeout = 0
	c.Server.ShutdownTimeout = 10 * time.Second
	c.Server.AdminPort = 19528
	c.Data.Dir = filepath.Join(home, "data")
	c.Data.DBFile = "loopworker_tasks.db"
	c.Plugins.Dir = filepath.Join(home, "plugins")
	c.Plugins.AutoLoad = true
	c.Plugins.VerifyChecksum = false
	c.Logging.Level = "info"
	c.Logging.Format = "text"
	c.Logging.Output = "stdout"
	c.Security.Enabled = true
	c.Security.AuthRequired = false
	c.Security.APIKey = ""
	c.Sandbox.MaxMemoryMB = 256
	c.Sandbox.MaxCPUSeconds = 30
	c.Sandbox.MaxOutputMB = 64
	c.Sandbox.MaxConcurrent = 10
	c.Sandbox.AllowedHosts = nil
	c.Workers.Count = 4
	c.Workers.TaskTimeout = 30 * time.Minute
	c.SelfHeal.Enabled = true
	c.SelfHeal.CircuitBreaker.Threshold = 5
	c.SelfHeal.CircuitBreaker.Timeout = 30 * time.Second
	c.SelfHeal.Retry.MaxAttempts = 3
	c.SelfHeal.Retry.Backoff = time.Second
	c.Workflow.MaxConcurrent = 10
	c.Workflow.Timeout = 5 * time.Minute
	c.LLM.BaseURL = "https://api.openai.com/v1"
	c.LLM.Model = "gpt-4o"
}

func defaultWorkDir() string {
	if dir := os.Getenv(EnvWorkDir); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".loopworker")
}

// ConfigFile is the file the values were read from, empty when none was found.
func (c *Config) ConfigFile() string { return c.configFile }

// SearchedPaths are the directories scanned for a config file when none was
// found. Empty when an explicit --config path was given, or when a file was
// located, because in both cases there was no search to report.
func (c *Config) SearchedPaths() []string { return c.searchedPaths }

// DBPath is the absolute path of the scheduler database.
func (c *Config) DBPath() string {
	if filepath.IsAbs(c.Data.DBFile) {
		return c.Data.DBFile
	}
	return filepath.Join(c.Data.Dir, c.Data.DBFile)
}

// Addr is the TCP address the server listens on.
func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

// Validate reports every problem it finds, each naming the key and where the
// value came from.
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...interface{}) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	c.addChecks(add)

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func (c *Config) addChecks(add func(string, ...interface{})) {
	check := func(path string, format string, args ...interface{}) {
		add("%s: %s (source: %s)", path, fmt.Sprintf(format, args...), c.SourceOf(path))
	}

	if c.WorkDir == "" {
		check("work_dir", "cannot be empty and could not be derived from the user home directory; set it explicitly")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		check("server.port", "must be between 1 and 65535, got %d", c.Server.Port)
	}
	if c.Server.Host == "" {
		check("server.host", "cannot be empty")
	}
	if c.Server.ReadTimeout <= 0 {
		check("server.read_timeout", "must be positive, got %s", c.Server.ReadTimeout)
	}
	if c.Server.WriteTimeout < 0 {
		check("server.write_timeout", "must be 0 (no deadline) or positive, got %s", c.Server.WriteTimeout)
	}
	if c.Server.ShutdownTimeout <= 0 {
		check("server.shutdown_timeout", "must be positive, got %s", c.Server.ShutdownTimeout)
	}
	if c.Server.AdminPort < 1 || c.Server.AdminPort > 65535 {
		check("server.admin_port", "must be between 1 and 65535, got %d", c.Server.AdminPort)
	}
	if c.Workers.TaskTimeout <= 0 {
		check("workers.task_timeout", "must be positive, got %s", c.Workers.TaskTimeout)
	}
	if c.Data.Dir == "" {
		check("data.dir", "cannot be empty")
	}
	if c.Data.DBFile == "" {
		check("data.db_file", "cannot be empty")
	}
	if c.Plugins.Dir == "" {
		check("plugins.dir", "cannot be empty")
	}
	if !oneOf(c.Logging.Level, "debug", "info", "warn", "error") {
		check("logging.level", "must be one of: %s, got %q", AcceptedLogLevels, c.Logging.Level)
	}
	if !oneOf(c.Logging.Format, "json", "text") {
		check("logging.format", "must be one of: %s, got %q", AcceptedLogFormats, c.Logging.Format)
	}
	if !oneOf(c.Logging.Output, "stdout", "stderr") {
		check("logging.output", "must be one of: %s, got %q", AcceptedLogOutputs, c.Logging.Output)
	}
	// A credential can arrive two ways: security.api_key in the config file, or
	// LOOPWORKER_API_KEYS in the environment (pkg/server reads both). Validating
	// only the first made the documented combination - auth_required: true plus
	// LOOPWORKER_API_KEYS - fail to start, so an operator following README or
	// QUICKSTART was told to set a config key they did not need.
	envKey := strings.TrimSpace(os.Getenv(security.EnvAPIKeys))
	hasCredential := strings.TrimSpace(c.Security.APIKey) != "" || envKey != ""
	if c.Security.AuthRequired && !hasCredential {
		check("security.api_key", "must be set when security.auth_required is true; set it in the config file or export %s", security.EnvAPIKeys)
	}
	if c.Security.Enabled && c.Security.AuthRequired && strings.TrimSpace(c.Security.APIKey) != "" && len(c.Security.APIKey) < 16 {
		check("security.api_key", "must be at least 16 characters when auth is required, got %d", len(c.Security.APIKey))
	}
	if c.Sandbox.MaxMemoryMB < 1 {
		check("sandbox.max_memory_mb", "must be positive, got %d", c.Sandbox.MaxMemoryMB)
	}
	if c.Sandbox.MaxCPUSeconds < 1 {
		check("sandbox.max_cpu_seconds", "must be positive, got %d", c.Sandbox.MaxCPUSeconds)
	}
	if c.Sandbox.MaxOutputMB < 1 {
		check("sandbox.max_output_mb", "must be positive, got %d", c.Sandbox.MaxOutputMB)
	}
	if c.Sandbox.MaxConcurrent < 1 {
		check("sandbox.max_concurrent", "must be positive, got %d", c.Sandbox.MaxConcurrent)
	}
	if c.Workers.Count < 1 {
		check("workers.count", "must be positive, got %d", c.Workers.Count)
	}
	if c.SelfHeal.Enabled {
		if c.SelfHeal.CircuitBreaker.Threshold < 1 {
			check("selfheal.circuit_breaker.threshold", "must be positive, got %d", c.SelfHeal.CircuitBreaker.Threshold)
		}
		if c.SelfHeal.CircuitBreaker.Timeout <= 0 {
			check("selfheal.circuit_breaker.timeout", "must be positive, got %s", c.SelfHeal.CircuitBreaker.Timeout)
		}
		if c.SelfHeal.Retry.MaxAttempts < 1 {
			check("selfheal.retry.max_attempts", "must be positive, got %d", c.SelfHeal.Retry.MaxAttempts)
		}
		if c.SelfHeal.Retry.Backoff <= 0 {
			check("selfheal.retry.backoff", "must be positive, got %s", c.SelfHeal.Retry.Backoff)
		}
	}
	if c.Workflow.MaxConcurrent < 1 {
		check("workflow.max_concurrent", "must be positive, got %d", c.Workflow.MaxConcurrent)
	}
	if c.Workflow.Timeout <= 0 {
		check("workflow.timeout", "must be positive, got %s", c.Workflow.Timeout)
	}
	if c.LLM.BaseURL != "" && !strings.HasPrefix(c.LLM.BaseURL, "http://") && !strings.HasPrefix(c.LLM.BaseURL, "https://") {
		check("llm.base_url", "must start with http:// or https://, got %q", c.LLM.BaseURL)
	}
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// SourceOf reports which layer produced the current value of a key.
func (c *Config) SourceOf(path string) string {
	if c.sources == nil {
		return "default"
	}
	if s, ok := c.sources[path]; ok {
		return s
	}
	return "default"
}

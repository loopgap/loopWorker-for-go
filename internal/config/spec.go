package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// spec declares one accepted configuration key. This table is the single source
// of truth for file keys, key aliases, environment names, the resolved-value
// report and the unknown-key error message.
type spec struct {
	path    string
	aliases []string
	env     []string
	secret  bool
	set     func(*Config, string) error
	get     func(*Config) string
}

func parseInt(dst *int, v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("expected an integer, got %q", v)
	}
	*dst = n
	return nil
}

func parseBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("expected true or false, got %q", v)
	}
	*dst = b
	return nil
}

func parseDur(dst *time.Duration, v string) error {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("expected a duration such as 30s, 5m or 1h30m, got %q", v)
	}
	*dst = d
	return nil
}

func parseList(dst *[]string, v string) error {
	out := []string{}
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	*dst = out
	return nil
}

func specs() []spec {
	return []spec{
		{path: "work_dir", env: []string{EnvWorkDir},
			set: func(c *Config, v string) error { c.WorkDir = v; return nil },
			get: func(c *Config) string { return c.WorkDir }},

		{path: "server.host", env: []string{"LOOPWORKER_SERVER_HOST"},
			set: func(c *Config, v string) error { c.Server.Host = v; return nil },
			get: func(c *Config) string { return c.Server.Host }},
		{path: "server.port", aliases: []string{"port"}, env: []string{"LOOPWORKER_SERVER_PORT", "LOOPWORKER_PORT"},
			set: func(c *Config, v string) error { return parseInt(&c.Server.Port, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Server.Port) }},
		{path: "server.read_timeout", env: []string{"LOOPWORKER_SERVER_READ_TIMEOUT"},
			set: func(c *Config, v string) error { return parseDur(&c.Server.ReadTimeout, v) },
			get: func(c *Config) string { return c.Server.ReadTimeout.String() }},
		{path: "server.write_timeout", env: []string{"LOOPWORKER_SERVER_WRITE_TIMEOUT"},
			set: func(c *Config, v string) error { return parseDur(&c.Server.WriteTimeout, v) },
			get: func(c *Config) string { return c.Server.WriteTimeout.String() }},
		{path: "server.shutdown_timeout", env: []string{"LOOPWORKER_SHUTDOWN_TIMEOUT"},
			set: func(c *Config, v string) error { return parseDur(&c.Server.ShutdownTimeout, v) },
			get: func(c *Config) string { return c.Server.ShutdownTimeout.String() }},
		{path: "server.admin_port", env: []string{"LOOPWORKER_API_ADMIN_PORT"},
			set: func(c *Config, v string) error { return parseInt(&c.Server.AdminPort, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Server.AdminPort) }},

		{path: "data.dir", aliases: []string{"data_dir"}, env: []string{"LOOPWORKER_DATA_DIR"},
			set: func(c *Config, v string) error { c.Data.Dir = v; return nil },
			get: func(c *Config) string { return c.Data.Dir }},
		{path: "data.db_file", env: []string{"LOOPWORKER_DB_FILE"},
			set: func(c *Config, v string) error { c.Data.DBFile = v; return nil },
			get: func(c *Config) string { return c.Data.DBFile }},

		{path: "plugins.dir", aliases: []string{"plugins_dir"}, env: []string{"LOOPWORKER_PLUGINS_DIR"},
			set: func(c *Config, v string) error { c.Plugins.Dir = v; return nil },
			get: func(c *Config) string { return c.Plugins.Dir }},
		{path: "plugins.auto_load", env: []string{"LOOPWORKER_PLUGINS_AUTO_LOAD"},
			set: func(c *Config, v string) error { return parseBool(&c.Plugins.AutoLoad, v) },
			get: func(c *Config) string { return strconv.FormatBool(c.Plugins.AutoLoad) }},

		{path: "logging.level", aliases: []string{"log_level"}, env: []string{"LOOPWORKER_LOG_LEVEL"},
			set: func(c *Config, v string) error { c.Logging.Level = strings.ToLower(strings.TrimSpace(v)); return nil },
			get: func(c *Config) string { return c.Logging.Level }},
		{path: "logging.format", env: []string{"LOOPWORKER_LOG_FORMAT"},
			set: func(c *Config, v string) error { c.Logging.Format = strings.ToLower(strings.TrimSpace(v)); return nil },
			get: func(c *Config) string { return c.Logging.Format }},
		{path: "logging.output", env: []string{"LOOPWORKER_LOG_OUTPUT"},
			set: func(c *Config, v string) error { c.Logging.Output = strings.ToLower(strings.TrimSpace(v)); return nil },
			get: func(c *Config) string { return c.Logging.Output }},

		{path: "security.enabled", env: []string{"LOOPWORKER_SECURITY_ENABLED"},
			set: func(c *Config, v string) error { return parseBool(&c.Security.Enabled, v) },
			get: func(c *Config) string { return strconv.FormatBool(c.Security.Enabled) }},
		{path: "security.auth_required", env: []string{"LOOPWORKER_AUTH_REQUIRED"},
			set: func(c *Config, v string) error { return parseBool(&c.Security.AuthRequired, v) },
			get: func(c *Config) string { return strconv.FormatBool(c.Security.AuthRequired) }},
		{path: "security.api_key", env: []string{"LOOPWORKER_API_KEY"}, secret: true,
			set: func(c *Config, v string) error { c.Security.APIKey = v; return nil },
			get: func(c *Config) string { return c.Security.APIKey }},

		{path: "sandbox.max_memory_mb", env: []string{"LOOPWORKER_SANDBOX_MAX_MEMORY", "LOOPWORKER_SANDBOX_MAX_MEMORY_MB"},
			set: func(c *Config, v string) error { return parseInt(&c.Sandbox.MaxMemoryMB, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Sandbox.MaxMemoryMB) }},
		{path: "sandbox.max_cpu_seconds", env: []string{"LOOPWORKER_SANDBOX_MAX_CPU_SECONDS"},
			set: func(c *Config, v string) error { return parseInt(&c.Sandbox.MaxCPUSeconds, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Sandbox.MaxCPUSeconds) }},
		{path: "sandbox.max_output_mb", env: []string{"LOOPWORKER_SANDBOX_MAX_OUTPUT_MB"},
			set: func(c *Config, v string) error { return parseInt(&c.Sandbox.MaxOutputMB, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Sandbox.MaxOutputMB) }},
		{path: "sandbox.max_concurrent", env: []string{"LOOPWORKER_SANDBOX_MAX_CONCURRENT"},
			set: func(c *Config, v string) error { return parseInt(&c.Sandbox.MaxConcurrent, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Sandbox.MaxConcurrent) }},
		{path: "sandbox.allowed_hosts", env: []string{"LOOPWORKER_SANDBOX_ALLOWED_HOSTS"},
			set: func(c *Config, v string) error { return parseList(&c.Sandbox.AllowedHosts, v) },
			get: func(c *Config) string { return strings.Join(c.Sandbox.AllowedHosts, ",") }},

		{path: "workers.count", aliases: []string{"workers"}, env: []string{"LOOPWORKER_WORKERS", "LOOPWORKER_WORKERS_COUNT"},
			set: func(c *Config, v string) error { return parseInt(&c.Workers.Count, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Workers.Count) }},
		{path: "workers.task_timeout", env: []string{"LOOPWORKER_TASK_TIMEOUT"},
			set: func(c *Config, v string) error { return parseDur(&c.Workers.TaskTimeout, v) },
			get: func(c *Config) string { return c.Workers.TaskTimeout.String() }},

		{path: "selfheal.enabled", env: []string{"LOOPWORKER_SELFHEAL_ENABLED"},
			set: func(c *Config, v string) error { return parseBool(&c.SelfHeal.Enabled, v) },
			get: func(c *Config) string { return strconv.FormatBool(c.SelfHeal.Enabled) }},
		{path: "selfheal.circuit_breaker.threshold", env: []string{"LOOPWORKER_CIRCUIT_THRESHOLD"},
			set: func(c *Config, v string) error { return parseInt(&c.SelfHeal.CircuitBreaker.Threshold, v) },
			get: func(c *Config) string { return strconv.Itoa(c.SelfHeal.CircuitBreaker.Threshold) }},
		{path: "selfheal.circuit_breaker.timeout", env: []string{"LOOPWORKER_CIRCUIT_TIMEOUT"},
			set: func(c *Config, v string) error { return parseDur(&c.SelfHeal.CircuitBreaker.Timeout, v) },
			get: func(c *Config) string { return c.SelfHeal.CircuitBreaker.Timeout.String() }},
		{path: "selfheal.retry.max_attempts", env: []string{"LOOPWORKER_RETRY_MAX_ATTEMPTS"},
			set: func(c *Config, v string) error { return parseInt(&c.SelfHeal.Retry.MaxAttempts, v) },
			get: func(c *Config) string { return strconv.Itoa(c.SelfHeal.Retry.MaxAttempts) }},
		{path: "selfheal.retry.backoff", env: []string{"LOOPWORKER_RETRY_BACKOFF"},
			set: func(c *Config, v string) error { return parseDur(&c.SelfHeal.Retry.Backoff, v) },
			get: func(c *Config) string { return c.SelfHeal.Retry.Backoff.String() }},

		{path: "workflow.max_concurrent", env: []string{"LOOPWORKER_WORKFLOW_MAX_CONCURRENT"},
			set: func(c *Config, v string) error { return parseInt(&c.Workflow.MaxConcurrent, v) },
			get: func(c *Config) string { return strconv.Itoa(c.Workflow.MaxConcurrent) }},
		{path: "workflow.timeout", env: []string{"LOOPWORKER_WORKFLOW_TIMEOUT"},
			set: func(c *Config, v string) error { return parseDur(&c.Workflow.Timeout, v) },
			get: func(c *Config) string { return c.Workflow.Timeout.String() }},

		{path: "llm.base_url", env: []string{"LOOPWORKER_LLM_BASE_URL"},
			set: func(c *Config, v string) error { c.LLM.BaseURL = v; return nil },
			get: func(c *Config) string { return c.LLM.BaseURL }},
		{path: "llm.api_key", env: []string{"LOOPWORKER_LLM_API_KEY", "OPENAI_API_KEY"}, secret: true,
			set: func(c *Config, v string) error { c.LLM.APIKey = v; return nil },
			get: func(c *Config) string { return c.LLM.APIKey }},
		{path: "llm.model", env: []string{"LOOPWORKER_LLM_MODEL"},
			set: func(c *Config, v string) error { c.LLM.Model = v; return nil },
			get: func(c *Config) string { return c.LLM.Model }},
	}
}

func specByPath(path string) (spec, bool) {
	for _, sp := range specs() {
		if sp.path == path {
			return sp, true
		}
	}
	return spec{}, false
}

func specByKey(key string) (spec, string, bool) {
	normalized := normalizeKey(key)
	all := specs()
	for _, sp := range all {
		if normalizeKey(sp.path) == normalized {
			return sp, sp.path, true
		}
	}
	for _, sp := range all {
		for _, alias := range sp.aliases {
			if normalizeKey(alias) == normalized {
				return sp, alias, true
			}
		}
	}
	return spec{}, "", false
}

// acceptedKeys lists every file key the loader understands, used in errors.
func acceptedKeys() []string {
	var keys []string
	for _, sp := range specs() {
		keys = append(keys, sp.path)
		keys = append(keys, sp.aliases...)
	}
	return keys
}

// normalizeKey makes a config key comparable: lowercase, _ for - and spaces.
func normalizeKey(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	k = strings.ReplaceAll(k, " ", "_")
	return k
}

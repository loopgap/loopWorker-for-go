package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func repoFile(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "config", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}
	return path
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"LOOPWORKER_CONFIG", "LOOPWORKER_WORK_DIR", "LOOPWORKER_PORT", "LOOPWORKER_SERVER_PORT",
		"LOOPWORKER_DATA_DIR", "LOOPWORKER_PLUGINS_DIR", "LOOPWORKER_LOG_LEVEL", "LOOPWORKER_LOG_FORMAT",
		"LOOPWORKER_WORKERS", "LOOPWORKER_SANDBOX_MAX_MEMORY", "LOOPWORKER_SANDBOX_MAX_CPU_SECONDS",
		"LOOPWORKER_SANDBOX_MAX_CONCURRENT", "LOOPWORKER_API_KEY", "LOOPWORKER_LLM_API_KEY",
		"OPENAI_API_KEY", "LOOPWORKER_AUTH_REQUIRED", "LOOPWORKER_SECURITY_ENABLED", "LOOPWORKER_TASK_TIMEOUT",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Server.Port != 19527 {
		t.Errorf("expected port 19527, got %d", cfg.Server.Port)
	}
	if cfg.Workers.Count != 4 {
		t.Errorf("expected 4 workers, got %d", cfg.Workers.Count)
	}
	if cfg.Sandbox.MaxMemoryMB != 256 || cfg.Sandbox.MaxCPUSeconds != 30 ||
		cfg.Sandbox.MaxOutputMB != 64 || cfg.Sandbox.MaxConcurrent != 10 {
		t.Errorf("unexpected sandbox defaults: %+v", cfg.Sandbox)
	}
	if cfg.Data.Dir != filepath.Join(cfg.WorkDir, "data") {
		t.Errorf("data dir %q not derived from work dir %q", cfg.Data.Dir, cfg.WorkDir)
	}
	if !filepath.IsAbs(cfg.DBPath()) {
		t.Errorf("DBPath must be absolute, got %s", cfg.DBPath())
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestWorkDirUnderHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := defaultWorkDir(); got != filepath.Join(home, ".loopworker") {
		t.Errorf("expected ~/.loopworker, got %s", got)
	}
}

func TestLoadMissingFileIsDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 19527 {
		t.Errorf("expected default port, got %d", cfg.Server.Port)
	}
	if cfg.ConfigFile() != "" && !strings.HasSuffix(cfg.ConfigFile(), "config.yaml") {
		t.Errorf("unexpected config file %s", cfg.ConfigFile())
	}
}

func TestExplicitMissingConfigFails(t *testing.T) {
	clearEnv(t)
	_, err := Load(Options{ConfigFile: filepath.Join(t.TempDir(), "nope.yaml")})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected actionable missing-file error, got %v", err)
	}
}

func TestShippedExampleLoads(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(Options{ConfigFile: repoFile(t, "config.example.yaml")})
	if err != nil {
		t.Fatalf("the shipped example must load: %v", err)
	}
	if cfg.Server.Port != 19527 || cfg.Server.Host != "127.0.0.1" {
		t.Errorf("example server section not applied: %+v", cfg.Server)
	}
	// The example ships write_timeout: 0, not 30s. Zero is the deliberate value:
	// a write deadline would cut the event stream on a fresh install. A non-zero
	// default here would quietly break /api/v1/events/live for every new user.
	if cfg.Server.ReadTimeout != 30*time.Second || cfg.Server.WriteTimeout != 0 {
		t.Errorf("example timeouts not applied: %+v", cfg.Server)
	}
	if cfg.Server.AdminPort != 19528 {
		t.Errorf("example admin_port not applied: %+v", cfg.Server)
	}
	if cfg.Workflow.MaxConcurrent != 10 || cfg.Workflow.Timeout != 5*time.Minute {
		t.Errorf("example workflow section not applied: %+v", cfg.Workflow)
	}
	if cfg.SelfHeal.CircuitBreaker.Threshold != 5 || cfg.SelfHeal.CircuitBreaker.Timeout != 30*time.Second {
		t.Errorf("example selfheal section not applied: %+v", cfg.SelfHeal)
	}
	if cfg.SelfHeal.Retry.MaxAttempts != 3 || cfg.SelfHeal.Retry.Backoff != time.Second {
		t.Errorf("example retry section not applied: %+v", cfg.SelfHeal.Retry)
	}
	if cfg.Logging.Format != "json" || cfg.Logging.Level != "info" {
		t.Errorf("example logging section not applied: %+v", cfg.Logging)
	}
	if !cfg.Plugins.AutoLoad {
		t.Error("example auto_load should be true")
	}
	if !strings.HasPrefix(cfg.Data.Dir, homeDir(t)) || !strings.HasPrefix(cfg.Plugins.Dir, homeDir(t)) {
		t.Errorf("example ~ paths must expand under home %s: data=%s plugins=%s", homeDir(t), cfg.Data.Dir, cfg.Plugins.Dir)
	}
	if filepath.Base(cfg.DBPath()) != "loopworker.db" {
		t.Errorf("example db_file not applied: %s", cfg.DBPath())
	}
}

func homeDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	return home
}

func TestFlatJSONFormLoads(t *testing.T) {
	clearEnv(t)
	base := t.TempDir()
	plugins, data := filepath.Join(base, "p"), filepath.Join(base, "d")
	path := filepath.Join(base, "config.json")
	write(t, path, `{"port":8123,"plugins_dir":"`+jsonPath(plugins)+`","data_dir":"`+jsonPath(data)+`","log_level":"warn",
	  "sandbox":{"max_memory_mb":512,"max_cpu_seconds":60,"max_output_mb":32,"max_concurrent":3},
	  "workers":{"count":7}}`)
	cfg, err := Load(Options{ConfigFile: path})
	if err != nil {
		t.Fatalf("flat JSON (documented in docs/USAGE.md) must load: %v", err)
	}
	if cfg.Server.Port != 8123 || cfg.Plugins.Dir != plugins || cfg.Data.Dir != data {
		t.Errorf("flat aliases not applied: %+v %+v %+v", cfg.Server, cfg.Plugins, cfg.Data)
	}
	if cfg.Logging.Level != "warn" || cfg.Workers.Count != 7 || cfg.Sandbox.MaxMemoryMB != 512 {
		t.Errorf("flat values not applied: %+v %+v", cfg.Logging, cfg.Sandbox)
	}
	if cfg.SourceOf("server.port") != "file:"+path {
		t.Errorf("source = %q", cfg.SourceOf("server.port"))
	}
}

func TestYAMLExtensionlessFileParsesAsJSON(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config")
	write(t, path, `{"server": {"port": 9099}}`)
	cfg, err := Load(Options{ConfigFile: path})
	if err != nil {
		t.Fatalf("content-sniffed JSON must load: %v", err)
	}
	if cfg.Server.Port != 9099 {
		t.Errorf("expected 9099, got %d", cfg.Server.Port)
	}
}

func TestPrecedenceDefaultsFileEnvFlag(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "server:\n  port: 8000\nlogging:\n  level: warn\nworkers:\n  count: 2\n")
	t.Setenv("LOOPWORKER_LOG_LEVEL", "debug")

	cfg, err := Load(Options{ConfigFile: path, Flags: map[string]string{"server.port": "9111"}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 9111 {
		t.Errorf("flag must win over file and env, got %d", cfg.Server.Port)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("env must win over file, got %s", cfg.Logging.Level)
	}
	if cfg.Workers.Count != 2 {
		t.Errorf("file must win over defaults, got %d", cfg.Workers.Count)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("default lost: %s", cfg.Server.ReadTimeout)
	}
	if src := cfg.SourceOf("server.read_timeout"); src != "default" {
		t.Errorf("source of untouched key = %q", src)
	}
	if src := cfg.SourceOf("server.port"); !strings.HasPrefix(src, "flag:") {
		t.Errorf("source of flag key = %q", src)
	}
}

func TestUnknownKeyNamesKeyFileAndAccepted(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "loging:\n  level: debug\n")
	_, err := Load(Options{ConfigFile: path})
	if err == nil {
		t.Fatal("unknown key must fail")
	}
	msg := err.Error()
	for _, want := range []string{"unknown configuration key", "loging.level", path, "logging.level", "accepted keys"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must contain %q, got:\n%s", want, msg)
		}
	}
}

func TestInvalidValueNamesKeyFileAndAccepted(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "logging:\n  level: verbose\n")
	_, err := Load(Options{ConfigFile: path})
	if err == nil {
		t.Fatal("invalid log level must fail")
	}
	for _, want := range []string{"logging.level", "debug, info, warn, error", "verbose", "file:" + path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must contain %q, got:\n%s", want, err)
		}
	}
}

func TestEnvironmentParseErrorIsNotSwallowed(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOOPWORKER_PORT", "notanumber")
	_, err := Load(Options{})
	if err == nil || !strings.Contains(err.Error(), "env:LOOPWORKER_PORT") {
		t.Fatalf("bad env must name the variable, got %v", err)
	}
}

func TestValidateReportsEveryProblemWithSource(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "server:\n  port: 70000\nsandbox:\n  max_memory_mb: 0\nworkers:\n  count: -1\n")
	_, err := Load(Options{ConfigFile: path})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	msg := err.Error()
	for _, want := range []string{"server.port", "sandbox.max_memory_mb", "workers.count", "source: file:" + path} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must name %q, got:\n%s", want, msg)
		}
	}
	if strings.Count(msg, " - ") < 3 {
		t.Errorf("all problems should be reported at once, got:\n%s", msg)
	}
}

func TestAuthRequiredNeedsAPIKey(t *testing.T) {
	clearEnv(t)
	cfg := Defaults()
	cfg.Security.AuthRequired = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("auth_required without api_key must fail")
	}
	cfg.Security.APIKey = "0123456789abcdef0123"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
}

func TestSecretsAreRedactedInReports(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOOPWORKER_API_KEY", "supersecretvalue123456")
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	summary := cfg.Summary()
	if strings.Contains(summary, "supersecretvalue123456") {
		t.Errorf("Summary leaked the API key:\n%s", summary)
	}
	var found bool
	for _, e := range cfg.Resolved() {
		if e.Key == "security.api_key" {
			found = true
			if !strings.Contains(e.Redacted(), "redacted") {
				t.Errorf("expected redaction, got %q", e.Redacted())
			}
		}
	}
	if !found {
		t.Error("security.api_key missing from Resolved()")
	}
}

func TestRelativePathsResolveAgainstOriginalDir(t *testing.T) {
	clearEnv(t)
	base := t.TempDir()
	path := filepath.Join(base, "config.yaml")
	write(t, path, "data:\n  dir: ./relative-data\nwork_dir: ./work\n")
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := filepath.Clean(filepath.Join(base, "relative-data"))
	if cfg.Data.Dir != want {
		t.Errorf("data dir = %s, want %s", cfg.Data.Dir, want)
	}
	if cfg.WorkDir != filepath.Clean(filepath.Join(base, "work")) {
		t.Errorf("work dir = %s", cfg.WorkDir)
	}
	if cfg.Plugins.Dir != filepath.Join(cfg.WorkDir, "plugins") {
		t.Errorf("plugins dir should derive from the resolved work_dir, got %s", cfg.Plugins.Dir)
	}
}

func TestEnvConfigFileIsHonored(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "env-config.yaml")
	write(t, path, "server:\n  port: 12345\n")
	t.Setenv(EnvConfig, path)
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 12345 {
		t.Errorf("LOOPWORKER_CONFIG ignored, port=%d", cfg.Server.Port)
	}
}

func TestTildeExpansion(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "data:\n  dir: \"~/loopworker-test-data\"\n")
	cfg, err := Load(Options{ConfigFile: path})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Data.Dir != filepath.Join(homeDir(t), "loopworker-test-data") {
		t.Errorf("~ not expanded: %s", cfg.Data.Dir)
	}
}

func TestDuplicateAliasAndCanonicalFails(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "port: 1\nserver:\n  port: 2\n")
	_, err := Load(Options{ConfigFile: path})
	if err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}

func TestJSONMarshalsAllSections(t *testing.T) {
	cfg := Defaults()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored Config
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	restored.sources = map[string]string{}
	if err := restored.Validate(); err != nil {
		t.Errorf("round-tripped config invalid: %v", err)
	}
	if restored.Server.Port != cfg.Server.Port || restored.WorkDir != cfg.WorkDir {
		t.Errorf("round trip lost values: %+v", restored.Server)
	}
}

func TestAddr(t *testing.T) {
	cfg := Defaults()
	// Loopback, not 0.0.0.0: the server refuses to serve a writable API on a
	// public interface without configured credentials, so a public default made
	// a bare `loopworker` exit 1. See TestDefaultListenerIsLoopback.
	if cfg.Addr() != "127.0.0.1:19527" {
		t.Errorf("Addr = %s", cfg.Addr())
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// jsonPath escapes a Windows path so it can be embedded in a JSON string.
func jsonPath(p string) string { return strings.ReplaceAll(p, `\`, `\\`) }

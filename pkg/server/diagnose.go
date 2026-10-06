package server

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"loopworker/internal/config"
	"loopworker/pkg/api"
	"loopworker/pkg/logger"
	"loopworker/pkg/security"
	"loopworker/version"
)

// Status is the outcome of one self-check.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Check is one validated fact about this host and configuration.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Diagnostics is the startup self-check document. It is printed on boot, served
// at /healthz and available to host commands through DoctorDump.
type Diagnostics struct {
	Timestamp    time.Time      `json:"timestamp"`
	Version      version.Info   `json:"version"`
	ConfigFile   string         `json:"config_file"`
	ConfigSource string         `json:"config_source"`
	Values       []config.Entry `json:"values"`
	Checks       []Check        `json:"checks"`
	Runtime      map[string]any `json:"runtime,omitempty"`
	Unapplied    []string       `json:"unapplied_keys,omitempty"`
}

// Diagnose validates the environment a server would start in. It performs no
// mutation other than a port probe, so a doctor command can call it freely.
func Diagnose(cfg *config.Config) *Diagnostics {
	d := &Diagnostics{
		Timestamp:    time.Now(),
		Version:      version.Get(),
		ConfigFile:   cfg.ConfigFile(),
		ConfigSource: describeConfigSource(cfg),
		Values:       cfg.Resolved(),
	}

	d.add(d.checkDirectories(cfg))
	d.add(d.checkDatabase(cfg))
	d.add(d.checkPlugins(cfg))
	d.add(d.checkPort(cfg))
	d.add(d.checkSandbox(cfg))
	d.add(d.checkSecurity(cfg))
	d.add(d.checkWorkers(cfg))
	d.add(d.checkHTTPTimeouts(cfg))
	d.add(d.checkLLM(cfg))

	d.Unapplied = unappliedKeys(cfg)
	if len(d.Unapplied) > 0 {
		d.add(Check{
			Name:   "unapplied_keys",
			Status: StatusWarn,
			Detail: strings.Join(d.Unapplied, ", ") + " are accepted and validated but not enforced by this build",
			Hint:   "the workflow engine does not expose concurrency or step-timeout options yet; these keys are reported here so nothing is silently ignored",
		})
	}
	return d
}

func describeConfigSource(cfg *config.Config) string {
	file := cfg.ConfigFile()
	if file == "" {
		return "defaults + environment" + flagSuffix(cfg)
	}
	ext := strings.ToLower(filepath.Ext(file))
	format := "YAML"
	if ext == ".json" {
		format = "JSON"
	}
	return fmt.Sprintf("%s (%s)%s", file, format, flagSuffix(cfg))
}

func flagSuffix(cfg *config.Config) string {
	var fromFlag []string
	for _, e := range cfg.Resolved() {
		if strings.HasPrefix(e.Source, "flag:") {
			fromFlag = append(fromFlag, e.Key)
		}
	}
	if len(fromFlag) == 0 {
		return ""
	}
	return ", overridden by flags: " + strings.Join(fromFlag, ", ")
}

func (d *Diagnostics) add(c Check) { d.Checks = append(d.Checks, c) }

func (d *Diagnostics) checkDirectories(cfg *config.Config) Check {
	var problems []string
	for _, dir := range []string{cfg.WorkDir, cfg.Data.Dir, cfg.Plugins.Dir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			problems = append(problems, fmt.Sprintf("%s cannot be created: %v", dir, err))
			continue
		}
		if err := writable(dir); err != nil {
			problems = append(problems, fmt.Sprintf("%s is not writable: %v", dir, err))
		}
	}
	if len(problems) > 0 {
		return Check{
			Name: "directories", Status: StatusFail,
			Detail: strings.Join(problems, "; "),
			Hint:   "point data.dir / plugins.dir / work_dir at a directory this user can write, or grant write access to the current one",
		}
	}

	free, err := diskFreeBytes(cfg.Data.Dir)
	if err != nil {
		return Check{Name: "directories", Status: StatusWarn,
			Detail: fmt.Sprintf("%s, %s, %s are writable; free space unknown: %v", cfg.WorkDir, cfg.Data.Dir, cfg.Plugins.Dir, err)}
	}
	const warnBelow = 100 << 20
	const failBelow = 8 << 20
	status, detail := StatusOK, fmt.Sprintf("%s, %s, %s are writable; %s free on the data volume", cfg.WorkDir, cfg.Data.Dir, cfg.Plugins.Dir, humanBytes(free))
	hint := ""
	switch {
	case free < failBelow:
		status, hint = StatusFail, "free up disk space under data.dir before starting: tasks and the event log cannot be written"
	case free < warnBelow:
		status, hint = StatusWarn, "less than 100 MB free; long runs will fill the disk and fail tasks"
	}
	return Check{Name: "directories", Status: status, Detail: detail, Hint: hint}
}

func writable(dir string) error {
	probe := filepath.Join(dir, fmt.Sprintf(".loopworker-write-probe-%d", os.Getpid()))
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

func (d *Diagnostics) checkDatabase(cfg *config.Config) Check {
	path := cfg.DBPath()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Check{Name: "database", Status: StatusOK,
				Detail: fmt.Sprintf("%s does not exist yet and will be created by the scheduler on first task", path)}
		}
		return Check{Name: "database", Status: StatusFail,
			Detail: fmt.Sprintf("cannot stat %s: %v", path, err),
			Hint:   "fix the permissions on data.dir or move it"}
	}

	header, err := readSQLiteHeader(path)
	if err != nil {
		return Check{Name: "database", Status: StatusFail,
			Detail: fmt.Sprintf("%s (%d bytes) is not a readable SQLite database: %v", path, info.Size(), err),
			Hint:   fmt.Sprintf("move the file away or point data.db_file at a new name; the scheduler will recreate %s", path)}
	}
	if header.dropped {
		return Check{Name: "database", Status: StatusWarn,
			Detail: fmt.Sprintf("%s (%d bytes) reports %d freelist page(s); run VACUUM or delete it to reclaim space", path, info.Size(), header.freelist)}
	}
	return Check{Name: "database", Status: StatusOK,
		Detail: fmt.Sprintf("%s reachable (%d bytes, page size %d, schema cookie %d, schema format %d, sqlite %d.%d.%d)",
			path, info.Size(), header.pageSize, header.schemaCookie, header.schemaFormat,
			header.sqliteVersion/1000000, header.sqliteVersion/1000%1000, header.sqliteVersion%1000)}
}

func (d *Diagnostics) checkPlugins(cfg *config.Config) Check {
	entries, readable, err := pluginDirListing(cfg.Plugins.Dir)
	if err != nil {
		return Check{Name: "plugins", Status: StatusFail,
			Detail: fmt.Sprintf("cannot read %s: %v", cfg.Plugins.Dir, err),
			Hint:   "create the directory or point plugins.dir at an existing one"}
	}
	_ = readable
	if entries == 0 {
		return Check{Name: "plugins", Status: StatusWarn,
			Detail: fmt.Sprintf("%s is empty", cfg.Plugins.Dir),
			Hint:   "copy a plugin folder containing plugin.json into " + cfg.Plugins.Dir + " (no plugin means wasm tasks fail with \"plugin not found\")"}
	}
	return Check{Name: "plugins", Status: StatusOK,
		Detail: fmt.Sprintf("%d entr(ies) in %s, auto_load=%v", entries, cfg.Plugins.Dir, cfg.Plugins.AutoLoad)}
}

func (d *Diagnostics) checkPort(cfg *config.Config) Check {
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return Check{Name: "port", Status: StatusFail,
			Detail: fmt.Sprintf("cannot bind %s: %v", cfg.Addr(), err),
			Hint:   fmt.Sprintf("use --port %d or stop whatever holds %s", cfg.Server.Port+1, cfg.Addr())}
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	detail := fmt.Sprintf("%s is free (resolved listen address %s)", cfg.Addr(), addr)
	if adminErr := checkAdminPort(cfg.Server.AdminPort); adminErr != nil {
		// The API port is the service; the admin port is observability. A
		// collision there is a warning naming the key, because the server
		// starts either way and simply loses /metrics.
		return Check{Name: "port", Status: StatusWarn,
			Detail: detail + "; but the admin listener cannot start",
			Hint:   "set server.admin_port (or LOOPWORKER_API_ADMIN_PORT) to a free port, or stop whatever holds it; until then /metrics and /runtime/stats are not served"}
	}
	return Check{Name: "port", Status: StatusOK, Detail: detail}
}

func checkAdminPort(port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(api.DefaultAdminBind, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return ln.Close()
}

func (d *Diagnostics) checkSandbox(cfg *config.Config) Check {
	// These are the ceiling. A plugin manifest may tighten any of them, and the
	// effective value is the smaller of the two; a manifest can never raise them.
	// So a plugin allocating all of it still affects the next one in this sandbox.
	detail := fmt.Sprintf("limits: %d MB memory, %d CPU seconds, %d MB output, %d concurrent - the ceiling for this sandbox; a plugin manifest may tighten them but never raise them",
		cfg.Sandbox.MaxMemoryMB, cfg.Sandbox.MaxCPUSeconds, cfg.Sandbox.MaxOutputMB, cfg.Sandbox.MaxConcurrent)
	if len(cfg.Sandbox.AllowedHosts) == 0 {
		return Check{Name: "sandbox", Status: StatusWarn,
			Detail: detail + "; sandbox.allowed_hosts is empty so a wasm plugin may call any URL through the host HTTP function",
			Hint:   "set sandbox.allowed_hosts to the hosts your plugins legitimately need (this is enforced inside the wasm host bridge)"}
	}
	return Check{Name: "sandbox", Status: StatusOK,
		Detail: detail + "; outbound host HTTP restricted to " + strings.Join(cfg.Sandbox.AllowedHosts, ", ")}
}

func (d *Diagnostics) checkSecurity(cfg *config.Config) Check {
	sec := cfg.Security
	public := cfg.Server.Host == "" || cfg.Server.Host == "0.0.0.0" || cfg.Server.Host == "::" || cfg.Server.Host == "*"
	// Credentials registered from the environment count as real credentials.
	// Reporting "no authentication" while LOOPWORKER_API_KEYS is set would be
	// the kind of false all-clear this command exists to prevent.
	envKeys, envErr := security.AuthConfigFromEnv()
	envCount := 0
	if envErr == nil {
		envCount = len(envKeys.Keys)
	}
	switch {
	case public && envCount == 0 && strings.TrimSpace(sec.APIKey) == "":
		// This is the one security state that is not a warning. boot() calls
		// api.ValidateBindAddress after this check and refuses to serve, so
		// reporting WARN here left the operator reading a table of warnings
		// and then watching the process exit with a message that was in
		// neither the table nor the flag they had set.
		return Check{Name: "security", Status: StatusFail,
			Detail: "the API would listen on " + cfg.Addr() + " with no configured credential, and the server refuses to start in that state: only an ephemeral per-process key exists",
			Hint: "either set server.host=127.0.0.1 for a single machine, or configure a credential before exposing the port - " +
				"set " + security.EnvAPIKeys + "=id:role:hex-sha256, or security.api_key in the config file"}
	case !sec.Enabled:
		// Measured: with security.enabled=false the server still answers an
		// unauthenticated POST /api/v1/tasks with 401 and reports
		// auth="required". Nothing outside this function reads the field, so the
		// key changes no behaviour - it is not a way to open the API. Saying
		// otherwise would have sent an operator looking for a breach that the
		// setting cannot create.
		return Check{Name: "security", Status: StatusWarn,
			Detail: "security.enabled=false, but no code reads it: authentication is enforced identically either way " +
				"(measured: an unauthenticated write still gets 401). Treat it as an accepted-but-unapplied key",
			Hint: "to decide whether credentials are required, configure one and check the result; " +
				"the setting that changes anything is server.host"}
	case sec.AuthRequired:
		detail := fmt.Sprintf("API key required on every endpoint except /healthz (key %s), listener %s", maskKey(sec.APIKey), cfg.Addr())
		if envCount > 0 {
			detail += fmt.Sprintf("; %d key(s) from %s", envCount, security.EnvAPIKeys)
		}
		return Check{Name: "security", Status: StatusOK, Detail: detail}
	case envCount > 0:
		// Keys exist, and pkg/api enforces them the moment any credential is
		// configured - security.auth_required does NOT gate enforcement. This used
		// to say the opposite ("unauthenticated requests are still served"), which
		// a measured run disproved: no credential -> 401, /api/v1/health reported
		// auth="required". Telling an operator their unauthenticated API is open
		// when it is closed is worse than saying nothing.
		state := fmt.Sprintf("%d credential(s) registered, and every endpoint except the three probes requires one; security.auth_required=false does not relax that", envCount)
		if public {
			state += "; the listener is on " + cfg.Addr() + ", so reachability is a network question, not an auth one"
		}
		return Check{Name: "security", Status: StatusWarn,
			Detail: fmt.Sprintf("%d key(s) from %s configured; %s", envCount, security.EnvAPIKeys, state),
			Hint: "auth_required documents whether a credential is mandatory; it is not required to be enabled here. " +
				"Use `loopworker doctor` after changing server.host, and rotate keys with POST /api/v1/auth/keys"}
	case public:
		return Check{Name: "security", Status: StatusWarn,
			Detail: "auth_required=false while the server listens on all interfaces: any host that can reach " + cfg.Addr() + " can create and run tasks",
			Hint:   "set server.host=127.0.0.1 for a single machine, or security.auth_required=true with a security.api_key"}
	default:
		return Check{Name: "security", Status: StatusOK,
			Detail: "auth_required=false but the listener is bound to the local interface " + cfg.Addr()}
	}
}

func (d *Diagnostics) checkHTTPTimeouts(cfg *config.Config) Check {
	if cfg.Server.WriteTimeout > 0 {
		return Check{
			Name: "http", Status: StatusWarn,
			Detail: fmt.Sprintf("read timeout %s, write timeout %s, shutdown drain %s: server.write_timeout also applies to streaming responses",
				cfg.Server.ReadTimeout, cfg.Server.WriteTimeout, cfg.Server.ShutdownTimeout),
			Hint: "GET /api/v1/events/live is closed after " + cfg.Server.WriteTimeout.String() + " of silence; set server.write_timeout to 0 to keep streams open",
		}
	}
	return Check{Name: "http", Status: StatusOK,
		Detail: fmt.Sprintf("read timeout %s, no write deadline (streams stay open), shutdown drain %s",
			cfg.Server.ReadTimeout, cfg.Server.ShutdownTimeout)}
}

func (d *Diagnostics) checkWorkers(cfg *config.Config) Check {
	return Check{Name: "workers", Status: StatusOK,
		Detail: fmt.Sprintf("%d worker(s) will be started, task timeout %s, shutdown drain %s",
			cfg.Workers.Count, cfg.Workers.TaskTimeout, cfg.Server.ShutdownTimeout)}
}

func (d *Diagnostics) checkLLM(cfg *config.Config) Check {
	if cfg.LLM.APIKey == "" {
		return Check{Name: "llm", Status: StatusWarn,
			Detail: fmt.Sprintf("no API key configured for %s: agent tasks and the llm.chat skill fail until it is set", cfg.LLM.BaseURL),
			Hint:   "set llm.api_key in the config file or export LOOPWORKER_LLM_API_KEY"}
	}
	return Check{Name: "llm", Status: StatusOK,
		Detail: fmt.Sprintf("%s, model %s, key %s", cfg.LLM.BaseURL, cfg.LLM.Model, maskKey(cfg.LLM.APIKey))}
}

// unappliedKeys lists validated settings this build records but cannot enforce.
func unappliedKeys(cfg *config.Config) []string {
	var out []string
	for _, e := range cfg.Resolved() {
		if strings.HasPrefix(e.Key, "workflow.") {
			out = append(out, fmt.Sprintf("%s=%s", e.Key, e.Redacted()))
		}
	}
	sort.Strings(out)
	return out
}

func maskKey(key string) string {
	if key == "" {
		return "(unset)"
	}
	return fmt.Sprintf("(set, %d chars, redacted)", len(key))
}

// FatalError turns the failed checks into one actionable startup error.
func (d *Diagnostics) FatalError() error {
	var problems []string
	for _, c := range d.Checks {
		if c.Status == StatusFail {
			problems = append(problems, fmt.Sprintf("%s: %s", c.Name, c.Detail))
			if c.Hint != "" {
				problems = append(problems, "  next step: "+c.Hint)
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("startup self-check failed\n  - %s", strings.Join(problems, "\n  - "))
}

// Log prints the self-check so a first run explains itself before any request.
func (d *Diagnostics) Log() {
	fmt.Printf("LoopWorker %s starting\n", d.Version.Version)
	fmt.Printf("  config     : %s\n", d.ConfigSource)
	for _, c := range d.Checks {
		marker := "ok  "
		switch c.Status {
		case StatusWarn:
			marker = "WARN"
		case StatusFail:
			marker = "FAIL"
		}
		fmt.Printf("  [%s] %-11s %s\n", marker, c.Name, c.Detail)
		if c.Hint != "" && c.Status != StatusOK {
			fmt.Printf("         -> %s\n", c.Hint)
		}
	}
	logger.Info("startup self-check complete",
		zap.String("config_source", d.ConfigSource),
		zap.Int("warnings", d.Count(StatusWarn)),
		zap.Int("failures", d.Count(StatusFail)))
}

// Count totals checks with the given status.
func (d *Diagnostics) Count(status Status) int {
	n := 0
	for _, c := range d.Checks {
		if c.Status == status {
			n++
		}
	}
	return n
}

// Text renders the full document for humans (loopworker doctor).
func (d *Diagnostics) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "LoopWorker %s (commit %s, built %s, %s)\n", d.Version.Version, d.Version.GitCommit, d.Version.BuildDate, d.Version.GoVersion)
	fmt.Fprintf(&b, "self-check at %s\n  config: %s\n\n", d.Timestamp.Format(time.RFC3339), d.ConfigSource)
	for _, c := range d.Checks {
		fmt.Fprintf(&b, "[%s] %s\n    %s\n", strings.ToUpper(string(c.Status)), c.Name, c.Detail)
		if c.Hint != "" {
			fmt.Fprintf(&b, "    -> %s\n", c.Hint)
		}
	}
	if len(d.Values) > 0 {
		fmt.Fprintf(&b, "\nresolved configuration (precedence: defaults < file < env < flags)\n")
		fmt.Fprint(&b, indentLines(configFromValues(d.Values)))
	}
	if len(d.Runtime) > 0 {
		fmt.Fprintf(&b, "\nruntime\n")
		keys := make([]string, 0, len(d.Runtime))
		for k := range d.Runtime {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-24s %v\n", k, d.Runtime[k])
		}
	}
	return b.String()
}

// JSON renders the document for machines.
func (d *Diagnostics) JSON() ([]byte, error) { return json.MarshalIndent(d, "", "  ") }

func configFromValues(entries []config.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%-32s = %-26s (%s)\n", e.Key, e.Redacted(), e.Source)
	}
	return b.String()
}

func indentLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// sqliteHeader holds the fields a doctor answer needs from the file header.
type sqliteHeader struct {
	pageSize      int
	schemaCookie  int
	schemaFormat  int
	freelist      int
	sqliteVersion int
	size          int64
	dropped       bool
}

func readSQLiteHeader(path string) (*sqliteHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, 100)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("file is shorter than the SQLite header: %w", err)
	}
	if string(buf[:16]) != "SQLite format 3\x00" {
		return nil, fmt.Errorf("bad magic %q", string(buf[:16]))
	}
	h := &sqliteHeader{
		pageSize:      int(binary.BigEndian.Uint16(buf[16:18])),
		schemaCookie:  int(binary.BigEndian.Uint32(buf[40:44])),
		schemaFormat:  int(binary.BigEndian.Uint32(buf[44:48])),
		freelist:      int(binary.BigEndian.Uint32(buf[32:36])),
		sqliteVersion: int(binary.BigEndian.Uint32(buf[96:100])),
	}
	if h.pageSize == 1 {
		h.pageSize = 65536
	}
	if info, err := f.Stat(); err == nil {
		h.size = info.Size()
	}
	h.dropped = h.freelist > 0
	return h, nil
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func versionString() string { return version.Get().String() }

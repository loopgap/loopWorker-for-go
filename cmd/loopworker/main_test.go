package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"loopworker/version"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// isolate points every environment variable and global flag at a private temp
// directory so tests never touch the developer's ~/.loopworker.
func isolate(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOOPWORKER_WORK_DIR", base)
	t.Setenv("LOOPWORKER_CONFIG", "")
	os.Unsetenv("LOOPWORKER_CONFIG")
	t.Setenv("LOOPWORKER_PORT", "")
	os.Unsetenv("LOOPWORKER_PORT")
	t.Setenv("LOOPWORKER_DATA_DIR", "")
	os.Unsetenv("LOOPWORKER_DATA_DIR")
	t.Setenv("LOOPWORKER_PLUGINS_DIR", "")
	os.Unsetenv("LOOPWORKER_PLUGINS_DIR")
	t.Setenv("LOOPWORKER_LOG_LEVEL", "")
	os.Unsetenv("LOOPWORKER_LOG_LEVEL")
	t.Cleanup(resetFlagState)
	resetFlagState()
	return base
}

func resetFlagState() {
	cfgFile = ""
	for name := range stringFlagVals {
		*stringFlagVals[name] = ""
	}
}

func TestVersionCommand(t *testing.T) {
	isolate(t)
	rootCmd.SetArgs([]string{"version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("version command: %v", err)
	}
	info := version.Get()
	if info.GoVersion == "" || !strings.Contains(info.String(), "loopworker") {
		t.Errorf("unexpected version output: %+v", info)
	}
}

func TestFlagsMapToConfigKeys(t *testing.T) {
	isolate(t)
	cmd := &cobra.Command{Use: "test"}
	for name := range stringFlags {
		stringFlagVals[name] = cmd.Flags().String(name, "", "")
	}
	cmd.Flags().IntP("port", "p", 0, "")
	cmd.Flags().Int("workers", 0, "")

	if err := cmd.Flags().Parse([]string{"--port", "18778", "--workers", "9", "--data-dir", "/tmp/d", "--log-level", "debug"}); err != nil {
		t.Fatal(err)
	}
	got := overrides(cmd)
	want := map[string]string{
		"server.port":   "18778",
		"workers.count": "9",
		"data.dir":      "/tmp/d",
		"logging.level": "debug",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("override %s = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unset flags must not produce overrides: %v", got)
	}
}

func TestLoadConfigFlagBeatsEnvBeatsFile(t *testing.T) {
	base := isolate(t)
	path := filepath.Join(base, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8000\nlogging:\n  level: warn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOPWORKER_LOG_LEVEL", "error")

	cmd := newRootForTest(t)
	if err := cmd.Flags().Parse([]string{"--config", path, "--port", "12345"}); err != nil {
		t.Fatal(err)
	}
	cfgFile = mustStringFlag(cmd, "config")

	cfg, err := loadConfig(cmd)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Server.Port != 12345 {
		t.Errorf("flag must win: port=%d", cfg.Server.Port)
	}
	if cfg.Logging.Level != "error" {
		t.Errorf("env must beat the file: level=%s", cfg.Logging.Level)
	}
	if cfg.Server.ReadTimeout <= 0 {
		t.Errorf("defaults lost: %+v", cfg.Server)
	}
	// 0 is the intended default, not a lost default: a write deadline would cut
	// long-lived streams such as the SSE endpoint. internal/config validates it
	// as "0 (no deadline) or positive", so pin it here too.
	if cfg.Server.WriteTimeout != 0 {
		t.Errorf("server.write_timeout must default to 0 (no deadline) so streams are not cut, got %s", cfg.Server.WriteTimeout)
	}
}

func TestRunServerStartsAndServesHealth(t *testing.T) {
	base := isolate(t)
	callerDir := t.TempDir()
	t.Chdir(callerDir)
	port := freePort(t)

	cmd := newRootForTest(t)
	cfgFile = ""
	if err := cmd.Flags().Parse([]string{"--port", fmt.Sprint(port), "--host", "127.0.0.1", "--workers", "2"}); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- runServer(cmd, nil) }()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(20 * time.Second)
	var health map[string]interface{}
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/v1/health")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				_ = json.Unmarshal(body, &health)
				break
			}
		}
		select {
		case err := <-errCh:
			t.Fatalf("server exited early: %v", err)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	if health == nil {
		t.Fatal("/api/v1/health never answered")
	}

	// Nothing may be dropped in the caller's directory (requirement: no CWD litter).
	if entries, err := os.ReadDir(callerDir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("running the server littered the working directory with %v", names)
	}

	db := filepath.Join(base, "data", "loopworker_tasks.db")
	if _, err := os.Stat(db); err != nil {
		t.Errorf("task database must live at the configured path %s: %v", db, err)
	}

	resp, err := http.Post(baseURL+"/shutdown", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runServer returned %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("server did not stop after /shutdown")
	}
}

func TestInvalidConfigExitsWithError(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	path := filepath.Join(base, "config.yaml")
	if err := os.WriteFile(path, []byte("loging:\n  level: debug\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootForTest(t)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cfgFile = path
	err := runServer(cmd, nil)
	if err == nil {
		t.Fatal("expected an actionable error for an unknown key")
	}
	if !strings.Contains(err.Error(), "unknown configuration key") || !strings.Contains(err.Error(), "logging.level") {
		t.Errorf("error must name the key and the suggestion: %v", err)
	}
}

// ---- doctor (AC-5) ----

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	os.Stdout = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

func doctorCmdForTest(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := newRootForTest(t)
	cmd.Flags().Bool("json", false, "")
	return cmd
}

// storageCommandForTest returns the `storage` subcommand as shipped.
//
// It deliberately does NOT re-register the command on a throwaway root. cobra's
// AddCommand sets the child's parent, so re-adding the package-level singleton to
// another root re-parents it away from the real rootCmd - which then fails a test
// that asserts the subcommand exists. Configuration for these tests arrives
// through the environment, so no re-registration is needed to make them hermetic.
func storageCommandForTest(t *testing.T) *cobra.Command {
	t.Helper()
	return storageCmd
}

// TestDoctorCoversEveryAcceptanceCheck is the AC-5 gate: a support answer must
// exist for a broken install without a human. It asserts the six required areas
// are each present, and that every failing check states a remedy - a diagnosis
// that names a problem but not a fix just moves the ticket to a human.
func TestDoctorCoversEveryAcceptanceCheck(t *testing.T) {
	base := isolate(t)
	cmd := doctorCmdForTest(t)
	if err := cmd.Flags().Parse([]string{
		"--data-dir", filepath.Join(base, "data"),
		"--plugins-dir", filepath.Join(base, "plugins"),
		"--work-dir", base,
		"--port", fmt.Sprint(freePort(t)),
	}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runDoctor(cmd, nil); err != nil {
			t.Fatalf("doctor: %v", err)
		}
	})

	for _, want := range []string{"directories", "database", "plugins", "port", "security"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output has no %q check; AC-5 requires it:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "resolved configuration") {
		t.Errorf("doctor must show the configuration it resolved, otherwise a silent default is invisible:\n%s", out)
	}
	// Every [WARN] and [FAIL] check has to state a remedy. The text form puts
	// the remedy on the indented "->" line that follows, so scan forward to the
	// next check header rather than the header line itself.
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "[WARN]") && !strings.HasPrefix(line, "[FAIL]") {
			continue
		}
		fixed := false
		for _, next := range lines[i+1:] {
			if strings.HasPrefix(next, "[") {
				break
			}
			if strings.Contains(next, "->") {
				fixed = true
				break
			}
		}
		if !fixed {
			t.Errorf("a diagnosis without a remedy is not self-service: %q", strings.TrimSpace(line))
		}
	}
}

func TestDoctorJSONIsMachineReadable(t *testing.T) {
	base := isolate(t)
	cmd := doctorCmdForTest(t)
	if err := cmd.Flags().Parse([]string{
		"--data-dir", filepath.Join(base, "data"),
		"--plugins-dir", filepath.Join(base, "plugins"),
		"--work-dir", base,
		"--port", fmt.Sprint(freePort(t)),
		"--json",
	}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runDoctor(cmd, nil); err != nil {
			t.Fatalf("doctor --json: %v", err)
		}
	})
	var report struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
			Fix    string `json:"fix"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor --json must emit valid JSON, got %v:\n%s", err, out)
	}
	if len(report.Checks) == 0 {
		t.Fatal("doctor --json reported no checks")
	}
	names := map[string]bool{}
	for _, c := range report.Checks {
		names[c.Name] = true
	}
	for _, want := range []string{"directories", "database", "plugins", "port", "security"} {
		if !names[want] {
			t.Errorf("doctor --json has no %q check; got %v", want, names)
		}
	}
}

func newRootForTest(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "loopworker-test"}
	for name := range stringFlags {
		stringFlagVals[name] = cmd.Flags().String(name, "", "")
	}
	cmd.Flags().StringVar(&cfgFile, "config", "", "")
	cmd.Flags().IntP("port", "p", 0, "")
	cmd.Flags().Int("workers", 0, "")
	t.Cleanup(func() {
		cfgFile = ""
	})
	return cmd
}

func mustStringFlag(cmd *cobra.Command, name string) string {
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		panic(err)
	}
	return v
}

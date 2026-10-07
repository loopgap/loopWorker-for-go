package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"loopworker/internal/config"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/api"
)

// pkgDir is this package's source directory, captured at init time because
// TestMain moves the working directory into a temp dir so stray writes from
// tests cannot land in the repository. Relative fixture paths break after that.
var pkgDir = func() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return dir
}()

// testConfig points every directory at a private temp dir so the suite never
// touches the developer's ~/.loopworker or the repository.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	base := t.TempDir()
	cfg := config.Defaults()
	cfg.WorkDir = base
	cfg.Data.Dir = filepath.Join(base, "data")
	cfg.Plugins.Dir = filepath.Join(base, "plugins")
	cfg.Data.DBFile = "loopworker_tasks.db"
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = freePort(t)
	cfg.Security.AuthRequired = false
	return cfg
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// newTestServer builds a server whose resources are released when the test
// ends. New opens the task database, the instance lock and the event store, so
// a test that never stops its server leaves Windows unable to delete the
// temp directory - which the next run reports as a failure in an unrelated
// place.
func newTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "loopworker-server-cwd")
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestNewRejectsNilConfig(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil configuration must be refused")
	}
}

func TestNewBuildsEveryComponent(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	c := srv.GetComponents()
	if c.Scheduler == nil || c.Dispatcher == nil || c.Executor == nil || c.Sandbox == nil ||
		c.PluginMgr == nil || c.Observer == nil || c.APIServer == nil || c.EventBus == nil ||
		c.SkillRegistry == nil || c.WorkflowEngine == nil || c.TaskBridge == nil {
		t.Fatalf("missing components: %+v", c)
	}
	if c.EventStore == nil {
		t.Error("event store must be configured when the data dir is writable")
	}
}

// TestSandboxLimitsComeFromConfig proves requirement 4: the hardcoded sandbox
// values are gone.
func TestSandboxLimitsComeFromConfig(t *testing.T) {
	cfg := testConfig(t)
	cfg.Sandbox.MaxMemoryMB = 111
	cfg.Sandbox.MaxCPUSeconds = 22
	cfg.Sandbox.MaxOutputMB = 33
	cfg.Sandbox.MaxConcurrent = 44

	srv := newTestServer(t, cfg)
	health := srv.Health()
	check := health.Checks["sandbox"]
	if !strings.Contains(check, "111MB") || !strings.Contains(check, "/22s") {
		t.Fatalf("sandbox health check does not report configured limits, got %q", check)
	}
}

// TestWorkersCountIsHonoured proves requirement 4 for workers.count.
func TestWorkersCountIsHonoured(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers.Count = 3
	srv := newTestServer(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := srv.loadPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.startWorkers(ctx, run); err != nil {
		t.Fatalf("startWorkers: %v", err)
	}
	defer srv.components.Executor.StopAllWorkers(context.Background())

	if got := srv.components.Executor.WorkerCount(); got != 3 {
		t.Errorf("workers started = %d, want workers.count = 3", got)
	}
	for _, w := range srv.components.Executor.ListWorkers() {
		if w.PluginID != noPluginID {
			t.Errorf("worker registered against %q; with no plugin installed it must be %q so failures are explainable", w.PluginID, noPluginID)
		}
	}
}

func TestEnsureDirsRefusesEmptyPath(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	err := EnsureDirs(data, "")
	if err == nil || !strings.Contains(err.Error(), "empty directory path") {
		t.Fatalf("empty directory must be a loud error, got %v", err)
	}
	// The refusal has to come before anything is created. This block used to
	// stat an unrelated fresh temp dir and asserted nothing at all, so a
	// half-configured server leaving an empty data directory behind went unseen.
	if _, statErr := os.Stat(data); !os.IsNotExist(statErr) {
		t.Errorf("a refused EnsureDirs must not create %s (stat error: %v)", data, statErr)
	}
}

func TestStartServesAndReportsRealHealth(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers.Count = 2
	srv := newTestServer(t, cfg)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()
	waitForListen(t, srv)
	defer srv.Stop()

	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)

	resp, err := http.Get(base + "/api/v1/health")
	if err != nil {
		t.Fatalf("api health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api health status = %d", resp.StatusCode)
	}

	healthResp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer healthResp.Body.Close()
	var health HealthResponse
	if err := json.NewDecoder(healthResp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"scheduler", "executor", "sandbox", "observer", "events", "selfheal", "plugins"} {
		if _, ok := health.Checks[key]; !ok {
			t.Errorf("health missing check %q (has %v)", key, keysOf(health.Checks))
		}
	}
	if !strings.Contains(health.Checks["executor"], "2/2 workers running") {
		t.Errorf("executor check should report configured workers, got %q", health.Checks["executor"])
	}
	if health.State != "running" {
		t.Errorf("state = %q", health.State)
	}

	// /statusz is no longer on the public listener: it moved to the admin one,
	// behind the admin credential. The public port must answer it with the SPA
	// shell, not the status document; the document itself is unchanged, so it
	// is asserted through the handler.
	publicStatus, err := http.Get(base + "/statusz")
	if err != nil {
		t.Fatal(err)
	}
	publicStatus.Body.Close()
	if strings.HasPrefix(publicStatus.Header.Get("Content-Type"), "application/json") {
		t.Errorf("public GET /statusz = %d %s, want the SPA catch-all: it moved to the admin listener",
			publicStatus.StatusCode, publicStatus.Header.Get("Content-Type"))
	}

	statusRec := httptest.NewRecorder()
	srv.StatusHandler()(statusRec, httptest.NewRequest(http.MethodGet, "/statusz", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("StatusHandler = %d", statusRec.Code)
	}
	var status StatusResponse
	if err := json.NewDecoder(statusRec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "running" || status.Components["sandbox"] == "" {
		t.Errorf("unexpected status document: %+v", status)
	}

	// Start blocks in Serve, so the only way to observe its return value is to
	// shut the server down first. Waiting on errCh before Stop deadlocks.
	if err := srv.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Errorf("Start returned %v while serving", err)
	}
}

func TestStartRefusesUnusableDataDir(t *testing.T) {
	cfg := testConfig(t)
	// A regular file where the data directory must be created is refused on every
	// platform, unlike chmod.
	blocked := filepath.Join(cfg.WorkDir, "blocked")
	writeFile(t, blocked, "not a directory")
	cfg.Data.Dir = filepath.Join(blocked, "data")

	_, err := New(cfg)
	if err == nil || !strings.Contains(err.Error(), blocked) {
		t.Fatalf("expected an actionable failure naming %s, got %v", blocked, err)
	}
}

func TestStartFailsOnBusyPort(t *testing.T) {
	cfg := testConfig(t)
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := newTestServer(t, cfg)
	err = srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected port conflict failure")
	}
	// The self-check probes the port before boot binds it, so the operator sees
	// the self-check wording, not the listener error.
	if !strings.Contains(err.Error(), "cannot bind") || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("port failure must be actionable, got %v", err)
	}
}

// TestQueuePumpRunsQueuedTasks proves the executor pump (Executor.Run) is really
// started by Server.Start: a task queued through the scheduler alone gets picked
// up, dispatched and failed by the missing plugin, instead of sitting queued.
func TestQueuePumpRunsQueuedTasks(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers.Count = 1
	srv := newTestServer(t, cfg)
	go srv.Start()
	waitForListen(t, srv)
	defer srv.Stop()

	ctx := context.Background()
	task, err := srv.Scheduler().CreateTask(ctx, "echo", nil, []byte(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Scheduler().QueueTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := srv.Scheduler().GetTask(task.ID)
		if !ok {
			t.Fatal("task disappeared from the scheduler")
		}
		// With no plugin installed every attempt fails; once the retry budget is
		// spent the task is dead-lettered, which is also a terminal state here.
		if current.State == scheduler.StateFailed || current.State == scheduler.StateDeadLetter {
			if !strings.Contains(current.Error, noPluginID) && !strings.Contains(current.Error, "plugin") {
				t.Errorf("task failed for an unexpected reason: %q", current.Error)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("queued task was never pumped by the executor (state %v)", srv.Scheduler().GetStats()["queued"])
}

func TestStopIsIdempotentAndSynchronised(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	go srv.Start()
	waitForListen(t, srv)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Stop(); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}()
	}
	wg.Wait()

	if srv.State() != "stopped" {
		t.Errorf("state after concurrent Stop = %q", srv.State())
	}
	if got := srv.components.Executor.WorkerCount(); got != 0 {
		t.Errorf("workers left running after Stop: %d", got)
	}
}

func TestStartTwiceFails(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	go srv.Start()
	waitForListen(t, srv)
	defer srv.Stop()

	if err := srv.Start(); err == nil || !strings.Contains(err.Error(), "cannot start from state") {
		t.Fatalf("second Start must be refused, got %v", err)
	}
}

func TestStopBeforeStart(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop on an idle server: %v", err)
	}
	if srv.State() != "stopped" {
		t.Errorf("state = %q", srv.State())
	}
}

// TestAuthMiddleware proves security.api_key reaches the one authenticator that
// guards the API. It used to pass against a second, private gate in pkg/server
// that read the config key on its own; that gate is gone, so this exercises
// /api/v1/workers (authenticated) rather than /api/v1/health, which is one of
// the three deliberately anonymous probes.
func TestAuthMiddleware(t *testing.T) {
	const key = "test-key-please-ignore-1234"
	cfg := testConfig(t)
	cfg.Security.AuthRequired = true
	cfg.Security.APIKey = key
	srv := newTestServer(t, cfg)
	handler := srv.handler()

	do := func(path, header, value string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if header != "" {
			req.Header.Set(header, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}

	if code := do("/api/v1/workers", "", ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated API call got %d, want 401", code)
	}
	// A raw API key is not a bearer token: Authorization carries a signed,
	// expiring token minted by POST /api/v1/auth/token. Handing it an API key
	// must fail even though the key is valid.
	if code := do("/api/v1/workers", "Authorization", "Bearer "+key); code != http.StatusUnauthorized {
		t.Errorf("API key presented as a bearer token got %d, want 401", code)
	}
	if code := do("/api/v1/workers", "Authorization", "Bearer lwt_not.a.token"); code != http.StatusUnauthorized {
		t.Errorf("forged bearer token got %d, want 401", code)
	}
	if code := do("/api/v1/workers", "X-API-Key", key); code != http.StatusOK {
		t.Errorf("X-API-Key got %d, want 200", code)
	}
	if code := do("/api/v1/workers", "X-API-Key", "wrong-key-1234567890"); code != http.StatusUnauthorized {
		t.Errorf("wrong API key got %d, want 401", code)
	}

	// The anonymous set is exactly the liveness/diagnostic surface: a server
	// that cannot be probed cannot be diagnosed.
	for _, probe := range []string{"/healthz", "/api/v1/health", "/api/v1/openapi.json"} {
		if code := do(probe, "", ""); code != http.StatusOK {
			t.Errorf("%s must stay reachable for diagnosis, got %d", probe, code)
		}
	}

	// A second server needs its own database: one database is served by one
	// process, by design, and New refuses to share it.
	//
	// security.auth_required=false on a loopback listener is the operator saying
	// "this is my own machine, do not make me configure anything", so the API is
	// served anonymously and a credential is optional rather than mandatory. The
	// bootstrap key is still minted, because the admin listener keeps demanding
	// one and nothing about a configured deployment changes.
	noKeyCfg := cfg
	noKeyCfg.Data.Dir = filepath.Join(t.TempDir(), "data")
	noKeyCfg.Security.AuthRequired = false
	noKeyCfg.Security.APIKey = ""
	noKey := newTestServer(t, noKeyCfg)
	bootKey, _ := noKey.components.APIServer.Authenticator().BootstrapKey()
	if bootKey == "" {
		t.Error("even in local trust mode the bootstrap key must exist for the admin listener")
	}
	if code := do2(noKey.handler(), "/api/v1/workers"); code != http.StatusOK {
		t.Errorf("local trust mode: anonymous /api/v1/workers got %d, want 200", code)
	}
	if code := doHeader(noKey.handler(), "/api/v1/workers", "X-API-Key", bootKey); code != http.StatusOK {
		t.Errorf("bootstrap key got %d, want 200", code)
	}

	// The other half of the contract, and the half that keeps this from becoming
	// an open API: local trust needs all three conditions - auth switched off, no
	// credential configured, loopback bind. Configuring a key must always make
	// that key take effect, even with auth_required=false.
	keyedCfg := cfg
	keyedCfg.Data.Dir = filepath.Join(t.TempDir(), "data")
	keyedCfg.Security.AuthRequired = false
	keyedCfg.Security.APIKey = "a-configured-key-1234567890"
	keyed := newTestServer(t, keyedCfg)
	if code := do2(keyed.handler(), "/api/v1/workers"); code == http.StatusOK {
		t.Error("with a configured API key an authenticated route must not answer 200 without presenting it")
	}
	if code := doHeader(keyed.handler(), "/api/v1/workers", "X-API-Key", "a-configured-key-1234567890"); code != http.StatusOK {
		t.Errorf("the configured key got %d, want 200", code)
	}
}

func do2(h http.Handler, path string) int {
	return doHeader(h, path, "", "")
}

func doHeader(h http.Handler, path, header, value string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if header != "" {
		req.Header.Set(header, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func TestHandleShutdownMethod(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	w := httptest.NewRecorder()
	srv.HandleShutdown(w, httptest.NewRequest(http.MethodGet, "/shutdown", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /shutdown = %d", w.Code)
	}
}

func TestHealthDegradedWhenTasksFail(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	ctx := context.Background()
	task, err := srv.components.Scheduler.CreateTask(ctx, "echo", nil, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	// A failed attempt does not make a task "failed": FailTask requeues while the
	// retry budget lasts, so the failure counter only moves once the budget is
	// spent. Drive the whole budget or this test would assert the wrong thing.
	for attempt := 0; attempt <= task.MaxRetry+1; attempt++ {
		if attempt > 0 {
			// FailTask either requeues the task (budget left) or dead-letters it
			// (budget spent). Both are correct; what matters is that neither is
			// silently reported as healthy.
			current, ok := srv.components.Scheduler.GetTask(task.ID)
			if !ok {
				t.Fatalf("attempt %d: task vanished", attempt)
			}
			if current.State == scheduler.StateDeadLetter {
				break
			}
			if current.State != scheduler.StateQueued {
				t.Fatalf("attempt %d: task should be requeued, is %s", attempt, current.State)
			}
		} else if err := srv.components.Scheduler.QueueTask(ctx, task.ID); err != nil {
			t.Fatalf("queue attempt %d: %v", attempt, err)
		}
		if err := srv.components.Scheduler.StartTask(ctx, task.ID, "worker-1"); err != nil {
			t.Fatalf("start attempt %d: %v", attempt, err)
		}
		if err := srv.components.Scheduler.FailTask(ctx, task.ID, "worker-1", "boom"); err != nil {
			t.Fatalf("fail attempt %d: %v", attempt, err)
		}
	}

	health := srv.Health()
	if health.Status != "degraded" {
		t.Errorf("a task that ran out of retries must degrade health, got %q (%v)", health.Status, health.Checks)
	}
	if !strings.Contains(health.Checks["scheduler"], "dead-lettered") {
		t.Errorf("scheduler check should name the dead-lettered tasks, got %q", health.Checks["scheduler"])
	}
}

func TestSelfHealDisabledIsVisible(t *testing.T) {
	cfg := testConfig(t)
	cfg.SelfHeal.Enabled = false
	srv := newTestServer(t, cfg)
	if srv.GetComponents().SelfHeal != nil {
		t.Error("selfheal.enabled=false must not build a healer")
	}
	if check := srv.Health().Checks["selfheal"]; !strings.Contains(check, "disabled") {
		t.Errorf("selfheal check = %q", check)
	}
}

func TestPluginDiscoveryAndLoad(t *testing.T) {
	cfg := testConfig(t)
	srv := newTestServer(t, cfg)
	if err := EnsureDirs(cfg.Plugins.Dir); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	run, err := srv.loadPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Discovered) != 0 {
		t.Errorf("empty plugin dir must discover nothing, got %v", run.Discovered)
	}
	if run.primaryPlugin() != noPluginID {
		t.Errorf("primary plugin = %q", run.primaryPlugin())
	}
	if !strings.Contains(run.Summary(), cfg.Plugins.Dir) {
		t.Errorf("summary should name the directory: %s", run.Summary())
	}

	writePlugin(t, cfg.Plugins.Dir, "alpha", "1.2.3")
	run, err = srv.loadPlugins(ctx)
	if err != nil {
		t.Fatalf("loadPlugins: %v", err)
	}
	if len(run.Loaded) != 1 || run.Loaded[0] != "alpha" {
		t.Fatalf("loaded = %v", run.Loaded)
	}
	if run.primaryPlugin() != "alpha" {
		t.Errorf("primary plugin = %q, want alpha", run.primaryPlugin())
	}
	if strings.Contains(run.Summary(), "placeholder") || strings.Contains(run.Summary(), "stub") {
		t.Errorf("summary must not claim a placeholder handler now that a real wasm loader is wired: %q", run.Summary())
	}
}

func TestBrokenPluginFailsStartup(t *testing.T) {
	cfg := testConfig(t)
	writePluginRaw(t, cfg.Plugins.Dir, "broken", "{ this is not json")
	srv := newTestServer(t, cfg)
	_, err := srv.loadPlugins(context.Background())
	if err == nil {
		t.Fatal("a discovered plugin with a broken plugin.json must abort startup")
	}
	for _, want := range []string{"auto-load failed", "next steps", "plugins.auto_load=false"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must be actionable, missing %q in %v", want, err)
		}
	}
}

func TestAutoLoadOffSkipsLoading(t *testing.T) {
	cfg := testConfig(t)
	cfg.Plugins.AutoLoad = false
	writePluginRaw(t, cfg.Plugins.Dir, "broken", "{ nope")
	srv := newTestServer(t, cfg)
	run, err := srv.loadPlugins(context.Background())
	if err != nil {
		t.Fatalf("auto_load=false must start anyway: %v", err)
	}
	if len(run.Loaded) != 0 || len(run.Discovered) != 1 {
		t.Errorf("run = %+v", run)
	}
	if !strings.Contains(run.Summary(), "auto_load is off") {
		t.Errorf("summary = %q", run.Summary())
	}
}

func TestSkillProvidersAreReal(t *testing.T) {
	cfg := testConfig(t)
	registry := newSkillRegistry(cfg)

	for _, name := range []string{"llm.chat", "research.anomaly"} {
		provider, ok := registry.Get(name)
		if !ok {
			t.Fatalf("skill %q registered without a provider", name)
		}
		if provider.Definition().Name != name {
			t.Errorf("provider definition mismatch for %q", name)
		}
	}

	anomaly, _ := registry.Get("research.anomaly")
	out, err := anomaly.Execute(context.Background(),
		[]byte(`{"values":[1,1,1,1,1,1,1,1,100,1],"threshold":2}`), nil)
	if err != nil {
		t.Fatalf("anomaly skill: %v", err)
	}
	if !strings.Contains(string(out), `"anomalies"`) || strings.Contains(string(out), `"anomalies":[]`) {
		t.Errorf("anomaly skill missed the outlier: %s", out)
	}

	chat, _ := registry.Get("llm.chat")
	if _, err := chat.Execute(context.Background(), []byte("hi"), nil); err == nil {
		t.Error("llm.chat without an API key must fail instead of pretending to work")
	} else if !strings.Contains(err.Error(), "LOOPWORKER_LLM_API_KEY") {
		t.Errorf("llm.chat error must name the setting, got %v", err)
	}
}

func TestSkillContextIsPopulated(t *testing.T) {
	cfg := testConfig(t)
	srv := newTestServer(t, cfg)
	if missing := srv.GetComponents().SkillRegistry.CheckDependencies([]string{"llm.chat", "research.anomaly"}); len(missing) != 0 {
		t.Errorf("built-in skills missing: %v", missing)
	}
	_ = srv
}

func TestSetupLoggingRejectsBadValues(t *testing.T) {
	if err := SetupLogging(config.LoggingConfig{Level: "trace", Format: "json", Output: "stdout"}); err == nil {
		t.Error("unknown log level must be refused")
	}
	if err := SetupLogging(config.LoggingConfig{Level: "info", Format: "xml", Output: "stdout"}); err == nil {
		t.Error("unknown log format must be refused")
	}
	if err := SetupLogging(config.LoggingConfig{Level: "warn", Format: "json", Output: "syslog"}); err == nil {
		t.Error("unknown log output must be refused")
	}
	if err := SetupLogging(config.LoggingConfig{Level: "debug", Format: "json", Output: "stderr"}); err != nil {
		t.Errorf("valid logging settings refused: %v", err)
	}
}

func TestDiagnoseAndDoctorDump(t *testing.T) {
	cfg := testConfig(t)
	diag := Diagnose(cfg)

	names := map[string]Status{}
	for _, c := range diag.Checks {
		names[c.Name] = c.Status
	}
	for _, want := range []string{"directories", "database", "plugins", "port", "sandbox", "security", "workers", "llm"} {
		if _, ok := names[want]; !ok {
			t.Errorf("self-check missing %q (have %v)", want, names)
		}
	}
	if err := diag.FatalError(); err != nil {
		t.Errorf("clean host should pass the self-check: %v", err)
	}

	text := diag.Text()
	for _, want := range []string{"config:", "server.port", "plugins.dir", "loopworker"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("doctor dump missing %q", want)
		}
	}

	// Secrets never reach the dump.
	cfg.Security.AuthRequired = true
	cfg.Security.APIKey = "super-secret-token-value"
	if strings.Contains(Diagnose(cfg).Text(), "super-secret-token-value") {
		t.Error("doctor dump leaked the API key")
	}

	srv := newTestServer(t, cfg)
	dump, err := srv.DoctorDump()
	if err != nil {
		t.Fatalf("DoctorDump: %v", err)
	}
	if !strings.Contains(dump, "security.api_key") {
		t.Errorf("dump must list the security key name:\n%s", dump)
	}

	cfg.Server.Host = "127.0.0.1"
	if status := Diagnose(cfg).namedCheck("security").Status; status == StatusFail {
		t.Errorf("loopback without auth should not be a hard failure: %+v", Diagnose(cfg).namedCheck("security"))
	}
}

func TestDiagnoseFailsOnUnwritableDataDir(t *testing.T) {
	cfg := testConfig(t)
	blocked := filepath.Join(cfg.WorkDir, "blocking-file")
	writeFile(t, blocked, "not a directory")
	cfg.Data.Dir = filepath.Join(blocked, "sub")

	diag := Diagnose(cfg)
	if err := diag.FatalError(); err == nil {
		t.Fatal("unwritable data dir must be a fatal self-check failure")
	} else if !strings.Contains(err.Error(), "next step") {
		t.Errorf("failure must say what to do: %v", err)
	}
}

func TestDiagnoseReportsCorruptDatabase(t *testing.T) {
	cfg := testConfig(t)
	if err := EnsureDirs(cfg.Data.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.DBPath(), []byte("this is definitely not a sqlite file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := Diagnose(cfg).namedCheck("database")
	if check.Status != StatusFail {
		t.Fatalf("corrupt db must fail, got %+v", check)
	}
	if !strings.Contains(check.Hint, "data.db_file") {
		t.Errorf("hint must name the setting: %+v", check)
	}
}

func TestDiagnoseWarnsOnOpenSandboxAndPublicBind(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.Host = "0.0.0.0"
	cfg.Security.AuthRequired = false
	// A credential, because without one the server refuses to start at all and
	// the security check is fatal rather than a warning - covered by
	// TestDoctorFailsWhenTheServerWouldRefuseToStart.
	cfg.Security.APIKey = "configured"
	diag := Diagnose(cfg)
	if diag.namedCheck("sandbox").Status != StatusWarn {
		t.Errorf("empty allowed_hosts should warn: %+v", diag.namedCheck("sandbox"))
	}
	if diag.namedCheck("security").Status != StatusWarn {
		t.Errorf("public bind without auth should warn: %+v", diag.namedCheck("security"))
	}
	cfg.Sandbox.AllowedHosts = []string{"api.example.com"}
	if diag := Diagnose(cfg); diag.namedCheck("sandbox").Status != StatusOK {
		t.Errorf("configured allowed_hosts should pass: %+v", diag.namedCheck("sandbox"))
	}
}

func TestUnappliedKeysAreReported(t *testing.T) {
	cfg := testConfig(t)
	diag := Diagnose(cfg)
	if len(diag.Unapplied) == 0 {
		t.Fatal("workflow.* keys must be reported as accepted-but-unapplied")
	}
	if diag.namedCheck("unapplied_keys").Status != StatusWarn {
		t.Errorf("unapplied keys must warn: %+v", diag.namedCheck("unapplied_keys"))
	}
}

func TestHealthHandlerHTTPIsHonest(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	w := httptest.NewRecorder()
	srv.HealthHandler()(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("idle-but-valid server should be healthy, got %d: %s", w.Code, w.Body)
	}
	var health HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(health.Checks["plugins"], "plugin") {
		t.Errorf("plugins check must explain the plugin situation: %q", health.Checks["plugins"])
	}
}

func TestVersionStringAndDiagnosticsRuntime(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	d := srv.Diagnostics()
	if d.Runtime == nil {
		t.Fatal("Diagnostics must carry runtime state")
	}
	if _, ok := d.Runtime["workers_configured"]; !ok {
		t.Errorf("runtime missing worker counts: %v", d.Runtime)
	}
	if d.Version.Version == "" {
		t.Error("diagnostics must include the build version")
	}
}

func (d *Diagnostics) namedCheck(name string) Check {
	for _, c := range d.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{Name: name, Status: StatusFail, Detail: "check not implemented"}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func waitForListen(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		ready := srv.listener != nil && srv.state == stateRunning
		srv.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never started listening (state %s)", srv.State())
}

func writePlugin(t *testing.T, dir, name, version string) {
	t.Helper()
	writePluginRaw(t, dir, name, fmt.Sprintf(`{"name":%q,"version":%q,"entry":"main.wasm","description":"test plugin"}`, name, version))
	// A manifest alone is not a loadable plugin: pkg/plugin compiles the entry
	// with wazero, so the module has to exist. Shared with pkg/plugin's tests
	// rather than duplicated; rebuild it with
	//   GOOS=wasip1 GOARCH=wasm go build -o hello.wasm ./examples/hello-plugin
	module, err := os.ReadFile(filepath.Join(pkgDir, "..", "plugin", "testdata", "hello.wasm"))
	if err != nil {
		t.Fatalf("read shared wasm fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "main.wasm"), module, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePluginRaw(t *testing.T, dir, name, manifest string) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPluginDirectoryNameMayDifferFromPluginName locks the directory -> plugin
// name mapping. Workers are registered against the name in plugin.json, so a
// folder called "smoke" holding a plugin called "hello" must still produce a
// worker bound to "hello". The previous code looked the name up by directory
// base name in a map keyed by plugin name, never found it, and bound the worker
// to "smoke" - every task then dead-lettered with "plugin not found: smoke"
// while the boot log cheerfully said the plugin was loaded.
func TestPluginDirectoryNameMayDifferFromPluginName(t *testing.T) {
	cfg := testConfig(t)
	// Deliberately mismatched: directory "smoke", manifest name "hello".
	dir := filepath.Join(cfg.Plugins.Dir, "smoke")
	writePlugin(t, cfg.Plugins.Dir, "smoke", "0.1.0-beta.1")
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"),
		bytes.Replace(manifest, []byte(`"name":"smoke"`), []byte(`"name":"hello"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, cfg)
	run, err := DiscoverAndLoad(context.Background(), srv.components.PluginMgr, cfg)
	if err != nil {
		t.Fatalf("DiscoverAndLoad: %v", err)
	}

	if got, want := run.Loaded, []string{"hello"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("loaded = %v, want %v (the manifest name, not the directory name)", got, want)
	}
	if got := run.primaryPlugin(); got != "hello" {
		t.Errorf("primaryPlugin() = %q, want %q - workers would be bound to a plugin that does not exist", got, "hello")
	}
	if _, ok := srv.components.PluginMgr.GetPlugin("hello"); !ok {
		t.Error("the plugin manager has no plugin named \"hello\": the name workers are given is not the name it was loaded under")
	}
	if len(run.Renamed) != 1 || run.Renamed[0].DirName != "smoke" || run.Renamed[0].Name != "hello" {
		t.Errorf("Renamed = %+v, want one entry naming smoke -> hello so the operator is told", run.Renamed)
	}
	if s := run.Summary(); !strings.Contains(s, "hello") {
		t.Errorf("Summary() = %q, does not mention the plugin name the operator has to use", s)
	}
}

// TestDoctorWarnsWhenTheAdminPortIsTaken locks the diagnostic half of the
// admin-port configuration. A busy admin port must not read as a fully healthy
// install: the server starts, but /metrics and /runtime/stats are gone, and the
// operator has no other way to find out why.
func TestDoctorWarnsWhenTheAdminPortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", net.JoinHostPort(api.DefaultAdminBind, "0"))
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer ln.Close()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split %s: %v", ln.Addr(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}

	cfg := testConfig(t)
	cfg.Server.AdminPort = port
	diag := Diagnose(cfg)
	if diag.FatalError() != nil {
		t.Fatalf("a busy admin port must not be fatal, got %v", diag.FatalError())
	}
	var portCheck *Check
	for i := range diag.Checks {
		if diag.Checks[i].Name == "port" {
			portCheck = &diag.Checks[i]
		}
	}
	if portCheck == nil {
		t.Fatal("no port check in the diagnosis")
	}
	if portCheck.Status != StatusWarn {
		t.Errorf("port check status: got %s (%s), want warn", portCheck.Status, portCheck.Detail)
	}
	if !strings.Contains(portCheck.Hint, "server.admin_port") {
		t.Errorf("hint must name the key, got %q", portCheck.Hint)
	}
}

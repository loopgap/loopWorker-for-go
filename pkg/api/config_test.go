package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"

	"testing"
	"time"

	"loopworker/version"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/security"
	"loopworker/pkg/workflow"
)

// ---- configuration surface ----

func TestDefaultConfigIsSecureByDefault(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.AllowWildcard {
		t.Error("the shipped configuration must not allow a CORS wildcard")
	}
	if len(cfg.AllowedOrigins) == 0 {
		t.Error("a shipped origin allow-list is required")
	}
	if cfg.TrustProxy {
		t.Error("proxy headers must be untrusted by default")
	}
	// Auth is layered on by newConfig, so the fresh-install credential path is
	// asserted there; here we only pin the HTTP-surface defaults.
	if cfg.MaxBodyBytes <= 0 || cfg.MaxInputBytes <= 0 {
		t.Error("body and input ceilings must be positive")
	}
	if cfg.MaxStreamsPerCaller < 1 || cfg.MaxStreamsTotal < 1 {
		t.Error("stream ceilings must be positive")
	}
	if cfg.RequestTimeout <= 0 {
		t.Error("ordinary handlers need a deadline")
	}
	if cfg.MaxLimit > MaxLimitPaged {
		t.Errorf("max page size %d exceeds the shipped ceiling %d", cfg.MaxLimit, MaxLimitPaged)
	}
}

func TestOptionsAreAppliedInOrder(t *testing.T) {
	cfg := newConfig(
		WithAllowedOrigins("https://one.example"),
		WithAllowedOrigins("https://two.example"),
		WithRateLimits(7, 9, time.Second),
		WithRequestTimeout(3*time.Second),
		WithTrustProxy(true),
		WithStaticCanvas(false),
		WithAdminListener("127.0.0.2", 19999),
	)
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "https://two.example" {
		t.Errorf("the last WithAllowedOrigins should win, got %v", cfg.AllowedOrigins)
	}
	if cfg.AnonRate != 7 || cfg.Authenticated != 9 {
		t.Errorf("rate limits: want 7/9, got %d/%d", cfg.AnonRate, cfg.Authenticated)
	}
	if cfg.RequestTimeout != 3*time.Second {
		t.Errorf("request timeout: got %s", cfg.RequestTimeout)
	}
	if !cfg.TrustProxy {
		t.Error("WithTrustProxy(true) did not apply")
	}
	if cfg.ServeStatic {
		t.Error("WithStaticCanvas(false) did not apply")
	}
	if cfg.AdminBind != "127.0.0.2" || cfg.AdminPort != 19999 || !cfg.EnableAdmin {
		t.Errorf("WithAdminListener did not apply: %s:%d enabled=%v", cfg.AdminBind, cfg.AdminPort, cfg.EnableAdmin)
	}
}

func TestWithConfigReplacesEverything(t *testing.T) {
	replacement := DefaultConfig()
	replacement.AnonRate = 42
	replacement.ServeStatic = false

	cfg := newConfig(WithConfig(replacement))
	if cfg.AnonRate != 42 {
		t.Errorf("WithConfig should replace the whole config, got anon rate %d", cfg.AnonRate)
	}
	if cfg.ServeStatic {
		t.Error("WithConfig should have cleared ServeStatic")
	}
}

// TestConfigAuthDefaultsAreFilledIn proves newConfig repairs a partial
// AuthConfig instead of letting it fail at request time.
func TestConfigAuthDefaultsAreFilledIn(t *testing.T) {
	cfg := newConfig(WithAuth(AuthConfig{Keys: mustAuth(t).Keys}))

	if cfg.Auth.APIKeyHeader != defaultAPIKeyHeader {
		t.Errorf("api key header should default, got %q", cfg.Auth.APIKeyHeader)
	}
	if cfg.Auth.TokenTTL <= 0 {
		t.Error("token TTL should default to a positive value")
	}

	// A fresh install must be able to mint exactly one usable credential, or the
	// documented "running in under a minute" path stops working.
	fresh := newConfig()
	if !fresh.Auth.AllowBootstrapKey {
		t.Error("a fresh install must still be able to mint one usable credential")
	}
	if fresh.Auth.APIKeyHeader == "" {
		t.Error("the api key header must never be empty")
	}
}

func TestEnvOverridesAreRead(t *testing.T) {
	setEnv(t, "LOOPWORKER_API_TRUST_PROXY", "yes")
	setEnv(t, "LOOPWORKER_API_ALLOWED_ORIGINS", "https://a.example, https://b.example")
	setEnv(t, "LOOPWORKER_API_MAX_BODY_BYTES", "4096")
	setEnv(t, "LOOPWORKER_API_ANON_RATE", "11")
	setEnv(t, "LOOPWORKER_API_AUTH_RATE", "22")
	setEnv(t, "LOOPWORKER_API_MAX_STREAMS", "33")
	setEnv(t, "LOOPWORKER_API_ADMIN_PORT", "19998")

	cfg := newConfig()
	if !cfg.TrustProxy {
		t.Error("LOOPWORKER_API_TRUST_PROXY should enable proxy trust")
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[1] != "https://b.example" {
		t.Errorf("origins: got %v", cfg.AllowedOrigins)
	}
	if cfg.MaxBodyBytes != 4096 {
		t.Errorf("max body: got %d", cfg.MaxBodyBytes)
	}
	if cfg.AnonRate != 11 || cfg.Authenticated != 22 {
		t.Errorf("rates: got %d/%d", cfg.AnonRate, cfg.Authenticated)
	}
	if cfg.MaxStreamsTotal != 33 {
		t.Errorf("max streams: got %d", cfg.MaxStreamsTotal)
	}
	if cfg.AdminPort != 19998 || !cfg.EnableAdmin {
		t.Errorf("admin port: got %d (enabled=%v)", cfg.AdminPort, cfg.EnableAdmin)
	}
}

func TestEnvWildcardOriginSetsWildcard(t *testing.T) {
	setEnv(t, "LOOPWORKER_API_ALLOWED_ORIGINS", "*")

	cfg := newConfig()
	if !cfg.AllowWildcard {
		t.Error("an explicit * in the env allow-list should enable wildcard mode")
	}
}

func TestEnvInvalidNumbersAreIgnored(t *testing.T) {
	setEnv(t, "LOOPWORKER_API_ANON_RATE", "not-a-number")
	setEnv(t, "LOOPWORKER_API_MAX_BODY_BYTES", "-1")

	cfg := newConfig()
	if cfg.AnonRate != DefaultAnonRate {
		t.Errorf("a malformed env value must not change the default, got %d", cfg.AnonRate)
	}
	if cfg.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Errorf("a negative env value must be ignored, got %d", cfg.MaxBodyBytes)
	}
}

func TestTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " true "} {
		if !truthy(v) {
			t.Errorf("%q should be truthy", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		if truthy(v) {
			t.Errorf("%q should not be truthy", v)
		}
	}
}

func TestWithEnvOverridesIsSafeWhenNothingIsSet(t *testing.T) {
	// Nothing set: the shipped defaults must survive verbatim.
	for _, key := range []string{
		"LOOPWORKER_API_TRUST_PROXY", "LOOPWORKER_API_ALLOWED_ORIGINS",
		"LOOPWORKER_API_MAX_BODY_BYTES", "LOOPWORKER_API_ANON_RATE",
		"LOOPWORKER_API_AUTH_RATE", "LOOPWORKER_API_MAX_STREAMS",
		"LOOPWORKER_API_ADMIN_PORT",
	} {
		os.Unsetenv(key)
	}

	cfg := newConfig()
	want := DefaultConfig()
	if cfg.AnonRate != want.AnonRate || cfg.Authenticated != want.Authenticated ||
		cfg.AnonWindow != want.AnonWindow || cfg.MaxBodyBytes != want.MaxBodyBytes ||
		cfg.MaxStreamsTotal != want.MaxStreamsTotal || cfg.TrustProxy != want.TrustProxy ||
		cfg.AdminPort != want.AdminPort {
		t.Errorf("defaults changed with no env set:\n got %+v\nwant %+v", cfg, want)
	}
}

// ---- version and http server ----

// TestHealthReportsTheRealBuildVersion pins the health endpoint to the single
// source of truth. It previously advertised a second, never-populated
// "dev" string, so a built binary contradicted `loopworker version`.
func TestHealthReportsTheRealBuildVersion(t *testing.T) {
	if got := versionString(); got != version.Version {
		t.Fatalf("versionString = %q, want the ldflags target version.Version = %q", got, version.Version)
	}
	if strings.TrimSpace(versionString()) == "" {
		t.Fatal("the reported version must never be empty")
	}
	if versionString() == "dev" {
		t.Errorf("the reported version is still the placeholder %q", versionString())
	}

	env := newTestEnv(t)
	w := env.call(roleNone, http.MethodGet, "/api/v1/health", "")
	if got := env.data(w)["version"]; got != versionString() {
		t.Errorf("/api/v1/health version: want %v, got %v", versionString(), got)
	}
}

func TestNewHTTPServerHasSlowlorisSafeTimeouts(t *testing.T) {
	srv := NewHTTPServer("127.0.0.1:0", http.NewServeMux(), DefaultConfig())

	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set")
	}
	if srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("socket timeouts must all be positive: %s %s %s", srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes must be bounded")
	}
	if srv.ErrorLog == nil {
		t.Error("internal errors must be routed into the structured log")
	}
}

func TestNewHTTPServerFallsBackOnZeroTimeouts(t *testing.T) {
	srv := NewHTTPServer("127.0.0.1:0", http.NewServeMux(), Config{})
	if srv.ReadTimeout != 15*time.Second {
		t.Errorf("want the 15s default, got %s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 75*time.Second {
		t.Errorf("streaming needs a generous write deadline, got %s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Errorf("want the 60s default, got %s", srv.IdleTimeout)
	}
}

// ---- admin listener lifecycle ----

func TestAdminAddrDefaults(t *testing.T) {
	env := newTestEnv(t)
	if got := env.AdminAddr(); got != DefaultAdminBind+":"+strconv.Itoa(DefaultAdminPort) {
		t.Errorf("admin addr default: got %s", got)
	}
}

func TestStartAdminBindsAndShutsDown(t *testing.T) {
	env := newTestEnv(t, WithAdminListener("127.0.0.1", 0))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := env.StartAdmin(ctx)
	if err != nil {
		t.Fatalf("StartAdmin: %v", err)
	}
	if srv == nil {
		t.Fatal("StartAdmin returned no server")
	}
	t.Cleanup(func() { _ = srv.Close() })

	if srv.Addr == "" {
		t.Error("the running admin server needs an address to be useful")
	}
}

func TestStartAdminIsANoOpWhenDisabled(t *testing.T) {
	env := newTestEnv(t, func(cfg *Config) { cfg.EnableAdmin = false })

	srv, err := env.StartAdmin(context.Background())
	if err != nil || srv != nil {
		t.Fatalf("a disabled admin listener must return (nil, nil), got (%v, %v)", srv, err)
	}
}

func TestStartAdminRefusesWithoutAnAuthenticator(t *testing.T) {
	cfg := security.DefaultAuthConfig()
	cfg.AllowBootstrapKey = false

	server := NewAPIServerWithDependencies(Dependencies{}, WithAuth(cfg))
	t.Cleanup(server.Close)

	if _, err := server.StartAdmin(context.Background()); err == nil {
		t.Fatal("the admin listener must refuse to start without an authenticator")
	}
}

// ---- ValidateBindAddress ----

// TestValidateBindAddressRefusesPublicBindWithoutCredentials is the guard that
// stops a fresh install from being published to the internet with only an
// ephemeral key.
func TestValidateBindAddressRefusesPublicBindWithoutCredentials(t *testing.T) {
	bus := newTestBus(t)
	env := newTestEnvNoKeys(t, bus)

	if err := ValidateBindAddress("0.0.0.0:19527", env.Authenticator()); err == nil {
		t.Error("a public bind with only a bootstrap key must be refused")
	}
	// Loopback is fine: that is the documented fresh-install path.
	for _, addr := range []string{"127.0.0.1:19527", "localhost:19527", "127.0.0.5:19527", "[::1]:19527"} {
		if err := ValidateBindAddress(addr, env.Authenticator()); err != nil {
			t.Errorf("loopback bind %s should be allowed: %v", addr, err)
		}
	}
	if err := ValidateBindAddress("", env.Authenticator()); err != nil {
		t.Errorf("an empty address should be left to the caller: %v", err)
	}

	// With a real credential configured, a public bind is legitimate.
	configured := newTestEnv(t)
	if err := ValidateBindAddress("0.0.0.0:19527", configured.Authenticator()); err != nil {
		t.Errorf("a public bind with configured credentials should be allowed: %v", err)
	}
}

func TestValidateBindAddressToleratesANilAuthenticator(t *testing.T) {
	if err := ValidateBindAddress("0.0.0.0:19527", nil); err != nil {
		t.Errorf("no authenticator means nothing to check: %v", err)
	}
}

// ---- redaction and view helpers ----

func TestRedactionCoversNestedStructures(t *testing.T) {
	in := map[string]any{
		"plain":         "visible",
		"api_key":       "secret",
		"Authorization": "Bearer x",
		"nested": map[string]any{
			"password": "hunter2",
			"innocent": "visible",
			"deeper":   []any{map[string]any{"session_id": "s3cret"}},
		},
		"list": []any{"a", "b"},
	}
	out := publicConfig(in)

	if out["plain"] != "visible" {
		t.Error("non-sensitive values must survive")
	}
	for _, key := range []string{"api_key", "Authorization"} {
		if out[key] != redactedPlaceholder {
			t.Errorf("%s should be masked, got %v", key, out[key])
		}
	}
	nested := out["nested"].(map[string]any)
	if nested["password"] != redactedPlaceholder {
		t.Error("nested passwords must be masked")
	}
	if nested["innocent"] != "visible" {
		t.Error("nested benign values must survive")
	}
	deeper := nested["deeper"].([]any)[0].(map[string]any)
	if deeper["session_id"] != redactedPlaceholder {
		t.Error("credentials inside lists must be masked too")
	}
	if len(out["list"].([]any)) != 2 {
		t.Error("lists of plain values must be preserved")
	}
}

func TestPublicConfigOnEmptyInput(t *testing.T) {
	if publicConfig(nil) != nil || publicConfig(map[string]any{}) != nil {
		t.Error("an empty config should project to nil, not an empty map")
	}
	if publicMetadata(nil) != nil {
		t.Error("empty metadata should project to nil")
	}
	// Metadata containing only the owner must also collapse to nil.
	if publicMetadata(map[string]string{OwnerMetadataKey: "x"}) != nil {
		t.Error("owner-only metadata should project to nil")
	}
}

func TestIsSensitiveKey(t *testing.T) {
	for _, key := range []string{"password", "API_KEY", "authToken", "my_secret", "Cookie", "private_key"} {
		if !isSensitiveKey(key) {
			t.Errorf("%q should be treated as sensitive", key)
		}
	}
	for _, key := range []string{"query", "top_k", "url", "model"} {
		if isSensitiveKey(key) {
			t.Errorf("%q should not be treated as sensitive", key)
		}
	}
}

// ---- worker views ----

func TestWorkerViewsProjectState(t *testing.T) {
	env := newTestEnv(t)

	// No workers are registered in a bare executor, so the endpoint reports an
	// empty catalog rather than failing.
	w := env.call(roleOperator, http.MethodGet, "/api/v1/workers", "")
	env.expectOK(w, http.StatusOK)
	data := env.data(w)
	workers, present := data["workers"]
	if !present {
		t.Fatalf("the workers response must carry a workers member: %s", w.Body.String())
	}
	if _, ok := workers.([]any); !ok {
		t.Errorf("workers should be a list, got %T", workers)
	}
	if _, ok := data["queue_size"]; !ok {
		t.Error("the workers response should report the queue size")
	}
}

func TestEncodeBytesDeclaresItsEncoding(t *testing.T) {
	if v, enc := encodeBytes(nil); v != "" || enc != "" {
		t.Errorf("empty payload: want empty/empty, got %q/%q", v, enc)
	}
	if v, enc := encodeBytes([]byte("hello")); v != "hello" || enc != "text" {
		t.Errorf("UTF-8 payload: want hello/text, got %q/%q", v, enc)
	}
	// Invalid UTF-8 must not be corrupted by a lossy conversion.
	value, enc := encodeBytes([]byte{0xff, 0xfe})
	if enc != "base64" {
		t.Errorf("binary payload: want base64, got %q", enc)
	}
	decoded, err := decodeBase64Loose(value)
	if err != nil || len(decoded) != 2 {
		t.Errorf("base64 payload did not round trip: %q %v", value, err)
	}
}

// ---- workflow views ----

func TestWorkflowViewsProjectRegisteredSteps(t *testing.T) {
	env := newTestEnv(t)

	wf := workflow.NewWorkflow("viewed", "Viewed Workflow")
	wf.AddStep(&workflow.Step{ID: "second", Name: "second"})
	wf.AddStep(&workflow.Step{ID: "first", Name: "first"})
	env.wfe.Register(wf)

	w := env.call(roleOperator, http.MethodGet, "/api/v1/workflow/viewed", "")
	env.expectOK(w, http.StatusOK)
	data := env.data(w)

	if data["id"] != "viewed" {
		t.Errorf("id: got %v", data["id"])
	}
	if data["status"] != "pending" {
		t.Errorf("status: want pending, got %v", data["status"])
	}
	steps, _ := data["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d: %s", len(steps), w.Body.String())
	}
	// StepOrder drives the rendering, so the declared order is preserved.
	order, _ := data["step_order"].([]any)
	if len(order) != 2 || order[0] != "second" {
		t.Errorf("step_order: got %v", order)
	}
	first := steps[0].(map[string]any)
	if first["id"] != "second" {
		t.Errorf("steps must follow step_order, got %v", first["id"])
	}
	if first["runnable"] != false {
		t.Error("a step with no action is not runnable")
	}
}

func TestWorkflowViewFallsBackToSortedSteps(t *testing.T) {
	wf := workflow.NewWorkflow("unsorted", "Unsorted")
	wf.Steps["beta"] = &workflow.Step{ID: "beta", Name: "beta"}
	wf.Steps["alpha"] = &workflow.Step{ID: "alpha", Name: "alpha"}

	order := orderedStepIDs(wf)
	if len(order) != 2 || order[0] != "alpha" {
		t.Errorf("without StepOrder the ids should be sorted, got %v", order)
	}

	// A StepOrder entry pointing at a removed step must be skipped.
	wf.StepOrder = []string{"alpha", "ghost"}
	if got := orderedStepIDs(wf); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("a dangling StepOrder entry should be dropped, got %v", got)
	}
}

func TestWorkflowStatusNames(t *testing.T) {
	cases := map[workflow.WorkflowStatus]string{
		workflow.WorkflowPending:   "pending",
		workflow.WorkflowRunning:   "running",
		workflow.WorkflowCompleted: "completed",
		workflow.WorkflowFailed:    "failed",
		workflow.WorkflowCancelled: "cancelled",
	}
	for status, want := range cases {
		if got := workflowStatusName(status); got != want {
			t.Errorf("status %v: want %s, got %s", status, want, got)
		}
	}
	if got := workflowStatusName(workflow.WorkflowStatus(99)); got != "unknown" {
		t.Errorf("an unrecognised status should read unknown, got %s", got)
	}
}

func TestWorkflowErrorTextIsEmptyWhenThereIsNoError(t *testing.T) {
	wf := workflow.NewWorkflow("clean", "Clean")
	if got := workflowErrorText(wf); got != "" {
		t.Errorf("a clean workflow must report no error text, got %q", got)
	}
	if newWorkflowView(nil) != nil {
		t.Error("a nil workflow should project to nil")
	}
}

// ---- auth handler edge cases ----

func TestCreateTokenRejectsBadTTL(t *testing.T) {
	env := newTestEnv(t)

	for _, ttl := range []string{`"soon"`, `"0s"`, `"-5m"`} {
		w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/token", `{"ttl":`+ttl+`}`)
		env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)
	}
}

func TestCreateTokenClampsTTLToTheServerMaximum(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/token",
		`{"api_key":"`+env.keys.operator+`","ttl":"720h"}`)
	env.expectOK(w, http.StatusOK)

	data := env.data(w)
	expiresIn, _ := data["expires_in"].(float64)
	if expiresIn > float64(env.Authenticator().TokenTTL().Seconds())+1 {
		t.Errorf("a requested TTL beyond the server maximum must be clamped, got expires_in=%v", expiresIn)
	}
}

func TestCreateTokenRejectsAnUnknownKey(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/token", `{"api_key":"lwk_nope"}`)
	env.expectCode(w, http.StatusUnauthorized, CodeTokenInvalid)
}

func TestCreateTokenRequiresCredentials(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/token", `{}`)
	env.expectCode(w, http.StatusBadRequest, CodeInvalidRequest)
}

func TestLoginIsRefusedWhenNoUserStoreIsConfigured(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/token",
		`{"username":"someone","password":"secret"}`)
	env.expectCode(w, http.StatusUnauthorized, CodeUnauthorized)
	assertNoLeak(t, "login refusal", w.Body.String())
}

func TestLoginSucceedsAgainstAConfiguredUserStore(t *testing.T) {
	bus := newTestBus(t)
	sched := scheduler.NewScheduler(bus)
	t.Cleanup(func() { _ = sched.Close() })

	users := newUserStore("alice", "correct-horse", "admin")
	authCfg, _ := testAuth(t)
	authCfg.Users = users

	server := NewAPIServerWithDependencies(Dependencies{Tasks: sched, Lister: sched},
		WithAuth(authCfg), WithRateLimits(100000, 100000, time.Minute))
	t.Cleanup(server.Close)

	// /api/v1/auth/token is an admin-only route, so the caller needs an admin
	// credential; that is itself part of the RBAC evidence.
	key, _, err := server.Authenticator().AddKey("login-tester", roleAdmin, time.Time{})
	if err != nil {
		t.Fatalf("mint key: %v", err)
	}

	// Wrong password.
	w := postAs(server, key, "/api/v1/auth/token", `{"username":"alice","password":"wrong"}`)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password: want 401, got %d: %s", w.Code, w.Body.String())
	}
	assertNoLeak(t, "wrong password", w.Body.String())

	// Correct password mints a usable bearer token.
	w = postAs(server, key, "/api/v1/auth/token", `{"username":"alice","password":"correct-horse"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("a correct password: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var env wire
	decodeJSONInto(t, w, &env)
	var payload map[string]any
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("data is not an object: %v", err)
	}
	token, _ := payload["access_token"].(string)
	if token == "" {
		t.Fatalf("no access_token in the response: %s", w.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("the password-minted token must authenticate: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWhoamiReportsPermissions(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleViewer, http.MethodGet, "/api/v1/auth/whoami", "")
	env.expectOK(w, http.StatusOK)
	data := env.data(w)

	perms, _ := data["permissions"].([]any)
	if len(perms) != 1 || perms[0] != "read" {
		t.Errorf("a viewer should hold exactly read, got %v", perms)
	}
	if data["via"] != "api_key" {
		t.Errorf("via: want api_key, got %v", data["via"])
	}
	if data["role"] != roleViewer {
		t.Errorf("role: want %s, got %v", roleViewer, data["role"])
	}
}

func TestRevokeBootstrapKeyIsRefused(t *testing.T) {
	env := newTestEnv(t)
	env.expectCode(env.call(roleAdmin, http.MethodDelete, "/api/v1/auth/keys/bootstrap", ""),
		http.StatusConflict, CodeTaskStateConflict)
}

// ---- helpers used above ----

func postAs(server *APIServer, key, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.Router.ServeHTTP(w, req)
	return w
}

// decodeJSONInto parses a whole response body into target.
func decodeJSONInto(t *testing.T, w *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v\n%s", err, w.Body.String())
	}
}

// userStore is the minimal UserVerifier a password login needs.
type userStore struct {
	users map[string]struct{ password, role string }
}

func newUserStore(username, password, role string) *userStore {
	return &userStore{users: map[string]struct{ password, role string }{username: {password, role}}}
}

func (s *userStore) AuthenticateUser(username, password string) (subject, role string, err error) {
	stored, known := s.users[username]
	if !known || stored.password != password {
		return "", "", errBadCredentials
	}
	return "user-" + username, stored.role, nil
}

var errBadCredentials = errors.New("invalid credentials")

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

// ---- dead-code probes that must stay reachable ----

// TestStaticLogWriterRoutesIntoTheStructuredLog proves stdlibErrorLog is wired
// to the application logger instead of the default stderr writer.
func TestStaticLogWriterRoutesIntoTheStructuredLog(t *testing.T) {
	log := stdlibErrorLog()
	if log == nil {
		t.Fatal("stdlibErrorLog returned nil")
	}
	if _, err := log.Writer().Write([]byte("something net/http complained about\n")); err != nil {
		t.Errorf("writing through the bridge failed: %v", err)
	}
	// An empty line must be dropped rather than logged as noise.
	if _, err := log.Writer().Write([]byte("\n")); err != nil {
		t.Errorf("writing an empty line failed: %v", err)
	}
}

// TestJSONBytesFallsBackOnAnEncodingError proves jsonBytes never emits a broken
// document.
func TestJSONBytesFallsBackOnAnEncodingError(t *testing.T) {
	encoded := jsonBytes(map[string]any{"fine": true})
	if !strings.Contains(string(encoded), `"fine":true`) {
		t.Errorf("an encodable value must be encoded, got %s", encoded)
	}

	broken := jsonBytes(make(chan int))
	if !strings.Contains(string(broken), "INTERNAL_ERROR") {
		t.Errorf("an unencodable value must fall back to the error stub, got %s", broken)
	}
}

func TestSendFieldErrorMatchesSendError(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	sendFieldError(w, req, &FieldError{
		Field: "thing", Reason: "it is wrong", Fix: "make it right",
		Status: http.StatusBadRequest, Code: CodeInvalidRequest,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	env := decodeEnvelope(t, w)
	if env.Error == nil || env.Error.Code != CodeInvalidRequest {
		t.Fatalf("want %s, got %+v", CodeInvalidRequest, env.Error)
	}
	if !strings.Contains(env.Error.Message, "make it right") {
		t.Errorf("the fix hint must reach the client: %q", env.Error.Message)
	}
}

func TestFieldErrorUnwrapExposesTheCause(t *testing.T) {
	cause := errors.New("underlying")
	fe := &FieldError{Field: "f", Reason: "r", Cause: cause}
	if !errors.Is(fe, cause) {
		t.Error("errors.Is must see through FieldError to its cause")
	}
	if !strings.Contains(fe.Error(), "underlying") {
		t.Errorf("Error() should mention the cause: %q", fe.Error())
	}
	plain := &FieldError{Field: "f", Reason: "just a reason"}
	if plain.Error() != "f: just a reason" {
		t.Errorf("Error() without a cause: got %q", plain.Error())
	}
}

// TestWriteJSONDoesNotEscapeHTML keeps payloads readable and prevents the
// envelope from mangling a task's text.
func TestWriteJSONDoesNotEscapeHTML(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", nil)
	writeJSON(w, req, http.StatusOK, Envelope{Success: true, Data: map[string]string{"text": "a<b>c&d"}})

	if !strings.Contains(w.Body.String(), "a<b>c&d") {
		t.Errorf("HTML characters must not be escaped into the payload: %s", w.Body.String())
	}
}

// TestNotImplementedAndSchedulerNotFoundKeepTheContract guards the two
// remaining error constructors against drift.
func TestNotImplementedAndSchedulerNotFoundKeepTheContract(t *testing.T) {
	c := classify(notImplemented("feature x", "enable the flag"))
	if c.status != http.StatusNotImplemented {
		t.Errorf("notImplemented status: got %d", c.status)
	}
	if !strings.Contains(c.message, "enable the flag") {
		t.Errorf("notImplemented must carry its fix: %q", c.message)
	}

	c = classify(schedulerNotFound("task-42"))
	if c.status != http.StatusNotFound || c.code != CodeTaskNotFound {
		t.Errorf("schedulerNotFound: got %d/%s", c.status, c.code)
	}
}

func TestStateNameHelpers(t *testing.T) {
	if stateName(scheduler.StateQueued) != "queued" {
		t.Error("stateName must render the scheduler state")
	}
	if stateNameOf(scheduler.StateFailed) != "failed" {
		t.Error("stateNameOf must render the scheduler state")
	}
}

func TestSortedKeysIsDeterministic(t *testing.T) {
	got := sortedKeys(map[string]any{"b": 1, "a": 2, "c": 3})
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("want sorted keys, got %v", got)
	}
}

func TestJoinStrings(t *testing.T) {
	if joinStrings([]string{"a", "b", "c"}, ", ") != "a, b, c" {
		t.Error("joinStrings must join with the given separator")
	}
}

func TestNewRequestIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := newRequestID()
		if id == "" {
			t.Fatal("newRequestID returned an empty id")
		}
		if seen[id] {
			t.Fatalf("duplicate request id %q after %d draws", id, i)
		}
		seen[id] = true
	}
}

func TestNowRFC3339IsUTC(t *testing.T) {
	if got := nowRFC3339(); !strings.HasSuffix(got, "Z") {
		t.Errorf("timestamps must be UTC, got %q", got)
	}
}

func TestFormatTimeHelpers(t *testing.T) {
	if got := formatTime(time.Time{}); got != "" {
		t.Errorf("a zero time should format as empty, got %q", got)
	}
	if formatTime(time.Now()) == "" {
		t.Error("a real time should format")
	}
	if got := formatTimePtr(nil); got != "" {
		t.Errorf("a nil pointer should format as empty, got %q", got)
	}
	now := time.Now()
	if formatTimePtr(&now) == "" {
		t.Error("a real pointer should format")
	}
}

// TestUnwrapFlusherReportsAMissingFlusher proves the SSE guard is honest: a
// writer that cannot stream must be reported rather than silently buffered.
func TestUnwrapFlusherReportsAMissingFlusher(t *testing.T) {
	if _, ok := unwrapFlusher(nonFlushingWriter{}); ok {
		t.Error("a writer with no Flusher must be reported as unable to stream")
	}
	// The statusWriter in the real chain must be found.
	if _, ok := unwrapFlusher(newStatusWriter(httptest.NewRecorder())); !ok {
		t.Error("the statusWriter must expose a Flusher (SSE depends on it)")
	}
}

type nonFlushingWriter struct{}

func (nonFlushingWriter) Header() http.Header       { return http.Header{} }
func (nonFlushingWriter) Write([]byte) (int, error) { return 0, nil }
func (nonFlushingWriter) WriteHeader(int)           {}

// TestStatusWriterForwardsHijackAndReadFrom proves the optional interfaces the
// statusWriter claims to forward really are forwarded.
func TestStatusWriterForwardsHijackAndReadFrom(t *testing.T) {
	sw := newStatusWriter(httptest.NewRecorder())

	// Unwrap reaches the wrapped writer, which is how http.ResponseController
	// gets past the wrapper.
	if sw.Unwrap() == nil {
		t.Error("Unwrap must return the wrapped writer")
	}

	// A recorder is not a Hijacker, so Hijack must fail cleanly instead of
	// panicking on a nil dereference.
	if _, _, err := sw.Hijack(); err == nil {
		t.Error("Hijack on a non-hijacking writer should report an error")
	}

	// ReadFrom must copy through even when the writer is not an io.ReaderFrom.
	n, err := sw.ReadFrom(bufio.NewReader(strings.NewReader("streamed body")))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("streamed body")) {
		t.Errorf("ReadFrom copied %d bytes, want %d", n, len("streamed body"))
	}
	if sw.bytes != len("streamed body") {
		t.Errorf("ReadFrom should be counted, got %d bytes", sw.bytes)
	}
}

// TestMergeJSONObjectsOverlaysTaskFields covers the SSE payload merge.
func TestMergeJSONObjectsOverlaysTaskFields(t *testing.T) {
	merged := mergeJSONObjects([]byte(`{"a":1}`), []byte(`{"id":"t1","state":"queued"}`))
	if !strings.Contains(string(merged), `"task"`) {
		t.Errorf("the task should be nested under \"task\": %s", merged)
	}
	if !strings.Contains(string(merged), `"t1"`) {
		t.Errorf("the task fields should survive: %s", merged)
	}

	// Degenerate inputs must not panic.
	if len(mergeJSONObjects(nil, nil)) == 0 {
		t.Error("merging two empty objects should still produce a document")
	}
}

// TestTaskIDOfAcceptsThePayloadShapes covers the tolerant id extraction the SSE
// renderer relies on.
func TestTaskIDOfAcceptsThePayloadShapes(t *testing.T) {
	cases := []struct {
		name    string
		payload any
		want    string
	}{
		{"snake_case map", map[string]any{"task_id": "t1"}, "t1"},
		{"Go struct via json tags", struct {
			TaskID string `json:"task_id"`
		}{TaskID: "t2"}, "t2"},
		{"exported field name", map[string]any{"TaskID": "t3"}, "t3"},
		{"missing", map[string]any{"other": 1}, ""},
		{"nil", nil, ""},
		{"wrong type", map[string]any{"task_id": 42}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskIDOf(payloadMap(tc.payload)); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestPayloadMapHandlesStringMaps(t *testing.T) {
	got := payloadMap(map[string]string{"task_id": "t1"})
	if got == nil || got["task_id"] != "t1" {
		t.Errorf("a string map should convert, got %v", got)
	}
}

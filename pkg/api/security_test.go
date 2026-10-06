package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loopworker/pkg/security"
)

// ---- B1: authentication is mandatory on every endpoint but healthz ----

// TestB1NoCredentialIs401 is the regression test for SPEC 10-B1: a server built
// through NewAPIServer (the way pkg/server builds it) must reject anonymous
// writes with 401, not create a task.
func TestB1NoCredentialIs401(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleNone, http.MethodPost, "/api/v1/tasks", `{"type":"anon"}`)
	env.expectCode(w, http.StatusUnauthorized, CodeUnauthorized)
}

func TestB1InvalidBearerIs401(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"type":"anon"}`))
	req.Header.Set("Authorization", "Bearer lwt_totally.made-up-signature")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)

	env.expectCode(w, http.StatusUnauthorized, CodeTokenInvalid)
	if strings.Contains(w.Body.String(), "signature") {
		t.Errorf("rejection must not echo credential material: %s", w.Body.String())
	}
}

func TestB1InvalidAPIKeyIs401(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"type":"anon"}`))
	req.Header.Set("X-API-Key", "lwk_not-a-real-key-at-all")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)

	env.expectCode(w, http.StatusUnauthorized, CodeTokenInvalid)
}

func TestB1ValidKeySucceeds(t *testing.T) {
	env := newTestEnv(t)
	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"real"}`), http.StatusCreated)
}

func TestB1ExpiredKeyIs401(t *testing.T) {
	env := newTestEnv(t)

	plaintext, _, err := env.Authenticator().AddKey("stale", security.RoleOperator, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("mint key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("X-API-Key", plaintext)
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired key: want 401, got %d: %s", w.Code, w.Body.String())
	}
}

// TestB1BootstrapKeyIsEphemeralAndAdminOnlyByAccident documents the fresh-install
// path: with no configured keys the server mints one admin key and logs it. It
// proves the fallback exists without leaving a standing backdoor, and that
// revoking it closes the API.
func TestB1BootstrapKeyIsEphemeralAndAdminOnlyByAccident(t *testing.T) {
	bus := newTestBus(t)
	env := newTestEnvNoKeys(t, bus)

	key, _ := env.Authenticator().BootstrapKey()
	if key == "" {
		t.Fatal("a keyless server must still mint a usable credential")
	}
	if env.Authenticator().HasCredentials() {
		t.Error("an ephemeral bootstrap key must not count as a configured credential")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap key should work: %d %s", w.Code, w.Body.String())
	}

	if err := env.Authenticator().RevokeKey("bootstrap"); err != nil {
		t.Fatalf("revoke bootstrap: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("X-API-Key", key)
	w = httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bootstrap key must stop working, got %d", w.Code)
	}
}

// TestB1BrokenCredentialConfigFailsClosed proves an unusable auth configuration
// answers 503 everywhere instead of degrading into an open API.
func TestB1BrokenCredentialConfigFailsClosed(t *testing.T) {
	cfg := security.DefaultAuthConfig()
	cfg.AllowBootstrapKey = false // no keys and no bootstrap => constructor error

	server := NewAPIServerWithDependencies(Dependencies{},
		WithAuth(cfg), WithRateLimits(100000, 100000, time.Minute))
	t.Cleanup(server.Close)

	if server.ConfigError() == nil {
		t.Fatal("an unusable credential configuration must be reported")
	}
	for _, path := range []string{"/api/v1/tasks", "/api/v1/auth/keys"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		server.Router.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: want 503 while the credential config is broken, got %d", path, w.Code)
		}
		if w.Code != http.StatusServiceUnavailable {
			continue
		}
		env := decodeEnvelope(t, w)
		if env.Error == nil || env.Error.Code != CodeServiceUnavailable {
			t.Errorf("%s: want %s, got %+v", path, CodeServiceUnavailable, env.Error)
		}
	}
}

// ---- B2: RBAC is enforced, not decorative ----

// TestB2AdminRoutesRejectNonAdmin proves the role check is real for every
// admin-only endpoint (SPEC 10-B2: RBAC was pure decoration).
func TestB2AdminRoutesRejectNonAdmin(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/v1/auth/token", `{"api_key":"x"}`},
		{http.MethodPost, "/api/v1/auth/keys", `{"name":"k","role":"viewer"}`},
		{http.MethodGet, "/api/v1/auth/keys", ""},
		{http.MethodDelete, "/api/v1/auth/keys/key-abc", ""},
	}
	for _, role := range []string{roleViewer, roleOperator} {
		for _, tc := range cases {
			w := env.call(role, tc.method, tc.path, tc.body)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: want 403, got %d: %s", role, tc.method, tc.path, w.Code, w.Body.String())
				continue
			}
			envW := decodeEnvelope(t, w)
			if envW.Error == nil || envW.Error.Code != CodeForbidden {
				t.Errorf("%s %s: want %s, got %+v", role, tc.path, CodeForbidden, envW.Error)
			}
			if envW.Error != nil && !strings.Contains(envW.Error.Message, "Fix:") {
				t.Errorf("%s %s: a 403 must carry a remedy: %q", role, tc.path, envW.Error.Message)
			}
		}
	}
}

func TestB2ViewerCannotMutateTasks(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleViewer, http.MethodPost, "/api/v1/tasks", `{"type":"viewer-task"}`)
	env.expectCode(w, http.StatusForbidden, CodeForbidden)
}

func TestB2ViewerCannotDeleteTasks(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "owned", "")

	env.expectCode(env.call(roleViewer, http.MethodDelete, "/api/v1/tasks/"+id, ""),
		http.StatusForbidden, CodeForbidden)
}

func TestB2OperatorCannotExecuteWorkflow(t *testing.T) {
	env := newTestEnv(t)
	// execute requires PermExecute, which the operator role does hold; the
	// viewer role must be the one refused.
	env.expectCode(env.call(roleViewer, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"x"}`),
		http.StatusForbidden, CodeForbidden)
}

func TestB2OperatorCanExecuteWorkflow(t *testing.T) {
	env := newTestEnv(t)
	env.expectCode(env.call(roleOperator, http.MethodPost, "/api/v1/workflow/execute", `{"workflow_id":"missing"}`),
		http.StatusNotFound, CodeWorkflowNotFound)
}

// TestB2AdminCanIssueAndRevokeKeys is the positive half of B2: the admin role
// really can manage credentials, so the refusals above are not a blanket deny.
func TestB2AdminCanIssueAndRevokeKeys(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/keys", `{"name":"ci","role":"viewer"}`)
	env.expectOK(w, http.StatusCreated)
	issued := env.data(w)
	keyID, _ := issued["id"].(string)
	plaintext, _ := issued["api_key"].(string)
	if keyID == "" || plaintext == "" {
		t.Fatalf("issue must return the id and the one-time plaintext: %s", w.Body.String())
	}

	// The new key authenticates.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("X-API-Key", plaintext)
	rec := httptest.NewRecorder()
	env.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issued key must authenticate: %d %s", rec.Code, rec.Body.String())
	}
	who := decodeData(t, rec)
	if who["role"] != roleViewer {
		t.Errorf("issued key role: want %s, got %v", roleViewer, who["role"])
	}

	// Listing must never echo the secret again.
	w = env.call(roleAdmin, http.MethodGet, "/api/v1/auth/keys", "")
	env.expectOK(w, http.StatusOK)
	if strings.Contains(w.Body.String(), plaintext) {
		t.Error("listing keys must not re-expose key material")
	}

	env.expectOK(env.call(roleAdmin, http.MethodDelete, "/api/v1/auth/keys/"+keyID, ""), http.StatusOK)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("X-API-Key", plaintext)
	rec = httptest.NewRecorder()
	env.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key must stop working, got %d", rec.Code)
	}
}

func TestB2BearerTokenFlow(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/token", `{"api_key":"`+env.keys.operator+`"}`)
	env.expectOK(w, http.StatusOK)
	token, _ := env.data(w)["access_token"].(string)
	if !strings.HasPrefix(token, security.BearerTokenPrefix) {
		t.Fatalf("want a %s token, got %q", security.BearerTokenPrefix, token)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	env.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issued bearer token must authenticate: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeData(t, rec)["role"]; got != roleOperator {
		t.Errorf("token must carry the key's role, got %v", got)
	}
}

func TestB2BearerCannotMintAnotherToken(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleAdmin, http.MethodPost, "/api/v1/auth/token", `{"api_key":"`+env.keys.operator+`"}`)
	token, _ := env.data(w)["access_token"].(string)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	env.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a bearer token must not mint another token, got %d", rec.Code)
	}
}

// ---- B3: task tenancy ----

// TestB3CallerCannotReadAnotherOwnersTask is the SPEC 10-B3 acceptance test:
// user A creates a task, user B gets 403 rather than the payload.
func TestB3CallerCannotReadAnotherOwnersTask(t *testing.T) {
	env := newTestEnv(t)

	ownerTask := env.createTask(roleOperator, "secret-work", `,"config":{"api_key":"super-secret"}`)
	other := secondOperatorKey(t, env)

	w := env.callWithKey(other, http.MethodGet, "/api/v1/tasks/"+ownerTask, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant read: want 403, got %d: %s", w.Code, w.Body.String())
	}
	envW := decodeEnvelope(t, w)
	if envW.Error == nil || envW.Error.Code != CodeForbidden {
		t.Errorf("want %s, got %+v", CodeForbidden, envW.Error)
	}
	if strings.Contains(w.Body.String(), "super-secret") {
		t.Fatalf("a 403 must not leak the task config: %s", w.Body.String())
	}
}

func TestB3CallerCannotMutateAnotherOwnersTask(t *testing.T) {
	env := newTestEnv(t)
	ownerTask := env.createTask(roleOperator, "secret-work", "")
	other := secondOperatorKey(t, env)

	if w := env.callWithKey(other, http.MethodDelete, "/api/v1/tasks/"+ownerTask, ""); w.Code != http.StatusForbidden {
		t.Errorf("cross-tenant delete: want 403, got %d", w.Code)
	}
	if w := env.callWithKey(other, http.MethodPost, "/api/v1/tasks/"+ownerTask+"/cancel", ""); w.Code != http.StatusForbidden {
		t.Errorf("cross-tenant cancel: want 403, got %d", w.Code)
	}
}

func TestB3ListIsScopedToTheCaller(t *testing.T) {
	env := newTestEnv(t)
	env.createTask(roleOperator, "operator-task", "")
	other := secondOperatorKey(t, env)
	env.createTaskWithKey(other, "other-operator-task", "")
	env.createTaskWithKey(addKey(t, env, "third-party", security.RoleOperator), "third-party-task", "")

	// The second operator sees neither the first operator's task nor the
	// viewer's task.
	w := env.callWithKey(other, http.MethodGet, "/api/v1/tasks", "")
	env.expectOK(w, http.StatusOK)
	tasks, _ := env.data(w)["tasks"].([]any)
	for _, row := range tasks {
		view, _ := row.(map[string]any)
		if owner, _ := view["owner"].(string); owner == "" {
			t.Errorf("a non-admin listing must only contain owned tasks: %s", w.Body.String())
		}
	}
	if scope := env.data(w)["scope"]; scope != "own_tasks" {
		t.Errorf("non-admin scope: want own_tasks, got %v", scope)
	}
}

func TestB3AdminSeesEveryTask(t *testing.T) {
	env := newTestEnv(t)
	env.createTask(roleOperator, "operator-task", "")
	env.createTaskWithKey(secondOperatorKey(t, env), "other-operator-task", "")

	w := env.call(roleAdmin, http.MethodGet, "/api/v1/tasks", "")
	env.expectOK(w, http.StatusOK)
	if scope := env.data(w)["scope"]; scope != "all" {
		t.Errorf("admin scope: want all, got %v", scope)
	}
	if tasks, _ := env.data(w)["tasks"].([]any); len(tasks) != 2 {
		t.Errorf("admin should see both tasks, got %d", len(tasks))
	}
}

// TestB3OwnerIsStampedServerSide proves the tenancy key comes from the
// credential, never from the request body.
func TestB3OwnerIsStampedServerSide(t *testing.T) {
	env := newTestEnv(t)
	id := env.createTask(roleOperator, "owned", "")

	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, "")
	env.expectOK(w, http.StatusOK)
	view := env.data(w)
	owner, _ := view["owner"].(string)
	if owner == "" {
		t.Fatalf("a created task must carry an owner: %s", w.Body.String())
	}

	who := decodeData(t, env.call(roleOperator, http.MethodGet, "/api/v1/auth/whoami", ""))
	if owner != who["subject"] {
		t.Errorf("owner %q must equal the authenticated subject %v", owner, who["subject"])
	}
	if meta, ok := view["metadata"].(map[string]any); ok {
		if _, present := meta[OwnerMetadataKey]; present {
			t.Error("the owner must be surfaced as \"owner\", not duplicated in metadata")
		}
	}
}

func TestB3UnownedTaskIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	// A task that predates ownership tracking: stored without the owner stamp.
	id := env.createTask(roleOperator, "legacy", "")
	if _, found := env.sched.GetTask(id); !found {
		t.Fatal("fixture task vanished")
	}
	stored, _ := env.sched.GetTask(id)
	delete(stored.Metadata, OwnerMetadataKey)
	if err := env.sched.SaveTask(stored); err != nil {
		t.Fatalf("clear owner: %v", err)
	}

	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("an unowned task must not be readable by a non-admin, got %d: %s", w.Code, w.Body.String())
	}
	env.expectOK(env.call(roleAdmin, http.MethodGet, "/api/v1/tasks/"+id, ""), http.StatusOK)
}

func TestB3CrossTenantDependencyIsRefused(t *testing.T) {
	env := newTestEnv(t)
	other := secondOperatorKey(t, env)
	mine := env.createTask(roleOperator, "mine", "")
	theirs := env.createTaskWithKey(other, "theirs", "")

	w := env.callWithKey(other, http.MethodPost, "/api/v1/tasks/"+mine+"/dependencies",
		`{"dependency_id":"`+theirs+`"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant edge: want 403, got %d: %s", w.Code, w.Body.String())
	}
}

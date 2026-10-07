package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loopworker/pkg/security"
)

// TestLocalTrustServesTheAPIWithoutACredential is the whole point of the mode:
// a server bound to loopback with security.auth_required=false must be usable
// from a browser with nothing to copy, paste or configure. An operator who has
// explicitly turned authentication off should not be stopped at a login form.
//
// whoami is asserted strictly because it only reports the attached principal,
// so 200 proves the local operator was actually injected. The task route is
// asserted only as "not rejected on auth": without wired components it may
// legitimately fail for an unrelated reason, and this test is about the
// authentication barrier, not about handler dependencies.
func TestLocalTrustServesTheAPIWithoutACredential(t *testing.T) {
	server := NewAPIServerWithDependencies(Dependencies{}, WithLocalTrust(true))
	t.Cleanup(server.Close)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	w := httptest.NewRecorder()
	server.Router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous GET /api/v1/auth/whoami = %d, want 200 in local trust mode", w.Code)
	}
	if !strings.Contains(w.Body.String(), security.RoleAdmin) {
		t.Errorf("the local operator should be an administrator so the canvas and first-run setup work; body was %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"type":"hello"}`))
	w = httptest.NewRecorder()
	server.Router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("POST /api/v1/tasks without a credential = %d, want it not to be rejected on auth", w.Code)
	}
}

// TestLocalTrustIsNotTheDefault guards the blast radius: every server built the
// ordinary way must keep demanding a credential exactly as before.
func TestLocalTrustIsNotTheDefault(t *testing.T) {
	server := NewAPIServerWithDependencies(Dependencies{})
	t.Cleanup(server.Close)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	w := httptest.NewRecorder()
	server.Router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/v1/auth/whoami = %d, want 401", w.Code)
	}
}

// TestLocalTrustNeverLeaksIntoTheAdminListener is the boundary that matters most.
// The admin listener carries /metrics, the runtime statistics and POST
// /shutdown, so it must keep demanding an administrator credential even when the
// public listener trusts the loopback. Trusting the loopback is a convenience
// for one operator using one browser, not a decision to reopen the control
// surface.
func TestLocalTrustNeverLeaksIntoTheAdminListener(t *testing.T) {
	shutdownCalls, statusCalls := 0, 0
	server := NewAPIServerWithDependencies(Dependencies{},
		WithAuth(security.DefaultAuthConfig()), WithLocalTrust(true))
	t.Cleanup(server.Close)
	// Inject the lifecycle handlers, so this proves the boundary with the routes
	// actually registered rather than trivially 404ing on absent ones.
	server.SetAdminControl(stubAdminControl{&shutdownCalls, &statusCalls})

	admin := server.AdminHandler()
	for _, path := range []string{"/statusz", "/runtime/stats", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Errorf("anonymous admin GET %s = %d, want it refused even in local trust mode", path, w.Code)
		}
	}
	if statusCalls != 0 || shutdownCalls != 0 {
		t.Fatalf("an admin handler ran without a credential (status=%d shutdown=%d)", statusCalls, shutdownCalls)
	}
}

// TestLocalTrustStillAttributesEveryCaller pins the synthetic principal, because
// rate limiting buckets by subject+role and the audit trail of who acted would
// otherwise collapse into one anonymous hole.
func TestLocalTrustStillAttributesEveryCaller(t *testing.T) {
	server := NewAPIServerWithDependencies(Dependencies{}, WithLocalTrust(true))
	t.Cleanup(server.Close)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	w := httptest.NewRecorder()
	server.Router.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, LocalTrustSubject) {
		t.Errorf("whoami body %s should name the local operator %q", body, LocalTrustSubject)
	}
}

// TestValidateBindAddressRefusesPublicBindsWithoutCredentials is the guarantee
// that makes local trust mode safe rather than merely convenient. Trust is
// granted only on loopback; anything that binds every interface still refuses
// to start until real credentials exist, so this feature cannot become an
// unauthenticated API reachable from a network.
func TestValidateBindAddressRefusesPublicBindsWithoutCredentials(t *testing.T) {
	auth, err := security.NewAuthenticator(security.DefaultAuthConfig())
	if err != nil {
		t.Fatal(err)
	}
	if auth.HasCredentials() {
		t.Fatal("an ephemeral bootstrap key must not count as a configured credential")
	}

	for _, addr := range []string{"0.0.0.0:19527", ":19527", "192.168.1.10:19527", "0.0.0.0"} {
		if err := ValidateBindAddress(addr, auth); err == nil {
			t.Errorf("ValidateBindAddress(%q) = nil, want a refusal: it binds every interface", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:19527", "127.5.5.5:19527", "[::1]:19527", "localhost:19527"} {
		if err := ValidateBindAddress(addr, auth); err != nil {
			t.Errorf("ValidateBindAddress(%q) = %v, want nil for a loopback bind", addr, err)
		}
	}
}

// TestIsLoopbackAddr pins the address classification that decides whether trust
// may be granted at all. Getting this wrong turns "localhost" into a public
// bind, so the cases are enumerated rather than left to a reader.
func TestIsLoopbackAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:19527", true},
		{"127.0.0.1", true},
		{"127.1.2.3:19527", true},
		{"localhost:19527", true},
		{"[::1]:19527", true},
		{"::1", true},
		// An empty host is not loopback. ":19527" and "0.0.0.0:19527" both bind
		// every interface, so classifying them as loopback would let a server
		// reach the network while believing it is private. Fail closed.
		{"", false},
		{":19527", false},
		{"0.0.0.0:19527", false},
		{"192.168.1.10:19527", false},
		{"10.0.0.5:19527", false},
		{"example.com:19527", false},
	} {
		if got := IsLoopbackAddr(tc.addr); got != tc.want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

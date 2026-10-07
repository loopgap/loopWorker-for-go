package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"loopworker/pkg/security"
)

type stubAdminControl struct {
	shutdownCalls *int
	statusCalls   *int
}

func (s stubAdminControl) AdminShutdown(w http.ResponseWriter, r *http.Request) {
	*s.shutdownCalls++
	w.WriteHeader(http.StatusOK)
}

func (s stubAdminControl) AdminStatus(w http.ResponseWriter, r *http.Request) {
	*s.statusCalls++
	w.WriteHeader(http.StatusOK)
}

// TestAdminControlRequiresCredential is the core guarantee these routes lack
// today: shutdown and status must never answer an anonymous caller.
func TestAdminControlRequiresCredential(t *testing.T) {
	shutdownCalls, statusCalls := 0, 0
	server := NewAPIServerWithDependencies(Dependencies{}, WithAuth(security.DefaultAuthConfig()))
	t.Cleanup(server.Close)
	server.SetAdminControl(stubAdminControl{&shutdownCalls, &statusCalls})

	admin := server.AdminHandler()
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/shutdown"},
		{http.MethodGet, "/statusz"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Errorf("anonymous %s %s answered 200 on the admin listener", tc.method, tc.path)
		}
	}
	if shutdownCalls != 0 || statusCalls != 0 {
		t.Fatalf("a handler ran without a credential (shutdown=%d status=%d)", shutdownCalls, statusCalls)
	}

	key, _ := server.Authenticator().BootstrapKey()
	if key == "" {
		t.Fatal("a keyless server must mint an ephemeral bootstrap admin key")
	}

	for _, tc := range []struct {
		method, path string
		want         *int
	}{
		{http.MethodPost, "/shutdown", &shutdownCalls},
		{http.MethodGet, "/statusz", &statusCalls},
	} {
		before := *tc.want
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("admin %s %s = %d, want 200", tc.method, tc.path, w.Code)
		}
		if *tc.want != before+1 {
			t.Errorf("admin %s %s did not reach the injected handler", tc.method, tc.path)
		}
	}
}

// TestAdminControlAbsentRegistersNothing keeps pkg/api usable on its own: a
// build with no injected lifecycle handlers must not expose the routes at all,
// rather than panicking on a nil interface.
func TestAdminControlAbsentRegistersNothing(t *testing.T) {
	server := NewAPIServerWithDependencies(Dependencies{}, WithAuth(security.DefaultAuthConfig()))
	t.Cleanup(server.Close)

	key, _ := server.Authenticator().BootstrapKey()
	admin := server.AdminHandler()
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/shutdown"},
		{http.MethodGet, "/statusz"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 when no AdminControl is injected", tc.method, tc.path, w.Code)
		}
	}
}

// TestAdminShutdownIsPostOnly keeps the verb contract tight: the route is
// registered with Method, not Handle, so it cannot be triggered by a GET that a
// browser or crawler might make.
func TestAdminShutdownIsPostOnly(t *testing.T) {
	shutdownCalls := 0
	server := NewAPIServerWithDependencies(Dependencies{}, WithAuth(security.DefaultAuthConfig()))
	t.Cleanup(server.Close)
	server.SetAdminControl(stubAdminControl{&shutdownCalls, new(int)})

	key, _ := server.Authenticator().BootstrapKey()
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	server.AdminHandler().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /shutdown = %d, want 405", w.Code)
	}
	if shutdownCalls != 0 {
		t.Error("a GET triggered the shutdown handler")
	}
}

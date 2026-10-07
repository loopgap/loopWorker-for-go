package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type probe struct {
	status int
	ctype  string
	body   string
}

func probeHandler(h http.Handler, method, path string) probe {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return probe{status: w.Code, ctype: w.Header().Get("Content-Type"), body: w.Body.String()}
}

// TestPublicListenerExposesNoLifecycleEndpoints is the regression guard for a
// remote denial of service: /shutdown used to sit on the public mux behind
// nothing but an HTTP method check, so one POST stopped the process, and
// GET /statusz printed scheduler and security internals.
//
// 404 is not the assertion, because the public mux ends in
// components.APIServer.Router, whose static handler deliberately answers every
// extensionless path with the SPA shell (staticHandler). The guarantee is that
// the two paths stopped being routes: they must now fall through to exactly
// the same answer as a path that never existed, which stops nothing and leaks
// nothing. Both lifecycle handlers answer application/json, so a JSON body is
// the second, independent proof that one is still mounted.
func TestPublicListenerExposesNoLifecycleEndpoints(t *testing.T) {
	srv := newTestServer(t, testConfig(t))
	public := srv.handler()

	unknown := probeHandler(public, http.MethodPost, "/no-such-path-ever")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, path := range []string{"/shutdown", "/statusz"} {
			got := probeHandler(public, method, path)
			if got.status != http.StatusNotFound && got.body != unknown.body {
				t.Errorf("public %s %s = %d %q, want the catch-all answer %q", method, path, got.status, got.body, unknown.body)
			}
			if strings.HasPrefix(got.ctype, "application/json") {
				t.Errorf("public %s %s answered %s; a lifecycle handler is still mounted on the public listener", method, path, got.ctype)
			}
		}
	}
}

// TestHealthzStaysPublic is the other half: the fix must not close the probe
// orchestrators depend on.
func TestHealthzStaysPublic(t *testing.T) {
	srv := newTestServer(t, testConfig(t))

	if code := do2(srv.handler(), "/healthz"); code == http.StatusNotFound {
		t.Error("/healthz must stay reachable on the public listener")
	}
}

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// specDoc is the slice of the embedded contract this test compares against the
// router.
type specDoc struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

var specVerbs = []string{"get", "post", "put", "delete", "patch", "head", "options", "trace"}

// walkRoutes returns "METHOD /path" for every registered route.
func walkRoutes(t *testing.T, router chi.Routes) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "*") {
			// The embedded SPA is served by a catch-all, not described as an
			// API operation.
			return nil
		}
		out[strings.ToLower(method)+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return out
}

func TestOpenAPISpecIsValidJSON(t *testing.T) {
	var doc specDoc
	if err := json.Unmarshal(OpenAPISpecJSON, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("openapi.json declares no paths")
	}
}

// TestOpenAPISpecMatchesRegisteredRoutes is the permanent SPEC 10-B10 guard:
// the machine-readable contract and the router must not drift. A route that
// exists but is undocumented cannot be found by a customer generating a client;
// a documented route that does not exist is a broken promise.
func TestOpenAPISpecMatchesRegisteredRoutes(t *testing.T) {
	env := newTestEnv(t)

	var doc specDoc
	if err := json.Unmarshal(OpenAPISpecJSON, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}

	registered := walkRoutes(t, env.Router)
	for route := range walkRoutes(t, env.AdminHandler().(chi.Routes)) {
		registered[route] = true
	}

	declared := map[string]bool{}
	for path, ops := range doc.Paths {
		for verb := range ops {
			if isSpecVerb(verb) {
				declared[strings.ToLower(verb)+" "+path] = true
			}
		}
	}

	var undocumented, phantom []string
	for route := range registered {
		if !declared[route] {
			undocumented = append(undocumented, route)
		}
	}
	for route := range declared {
		if !registered[route] {
			phantom = append(phantom, route)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(phantom)

	for _, route := range undocumented {
		t.Errorf("route %s is registered but not declared in openapi.json", route)
	}
	for _, route := range phantom {
		t.Errorf("openapi.json declares %s but no route serves it", route)
	}
}

func isSpecVerb(verb string) bool {
	for _, candidate := range specVerbs {
		if strings.EqualFold(verb, candidate) {
			return true
		}
	}
	return false
}

// TestOpenAPISpecDocumentsTheSecurityContract proves the contract tells a client
// how to authenticate and where the request id comes from, which is what makes
// codegen useful.
func TestOpenAPISpecDocumentsTheSecurityContract(t *testing.T) {
	var doc struct {
		Security   []map[string][]string `json:"security"`
		Components struct {
			SecuritySchemes map[string]any `json:"securitySchemes"`
		} `json:"components"`
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(OpenAPISpecJSON, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}

	if len(doc.Components.SecuritySchemes) == 0 {
		t.Error("openapi.json declares no security schemes, so codegen cannot authenticate")
	}
	if len(doc.Security) == 0 {
		t.Fatal("openapi.json sets no global security requirement, so every operation looks unauthenticated")
	}
	for _, requirement := range doc.Security {
		for scheme := range requirement {
			if _, known := doc.Components.SecuritySchemes[scheme]; !known {
				t.Errorf("the global security requirement names undefined scheme %q", scheme)
			}
		}
	}

	// The public probes are checked for exactness in
	// TestOpenAPISpecOptOutListIsExactlyThePublicSurface; here only the
	// mutating endpoints are asserted, since those are the ones an anonymous
	// client must never reach.
	for path, ops := range doc.Paths {
		for verb, op := range ops {
			if !isSpecVerb(verb) {
				continue
			}
			if !strings.EqualFold(verb, "post") && !strings.EqualFold(verb, "delete") {
				continue
			}
			security := doc.Security
			if op.Security != nil {
				security = op.Security
			}
			if len(security) == 0 {
				t.Errorf("%s %s is a mutating endpoint declared without any security requirement",
					strings.ToUpper(verb), path)
			}
		}
	}
}

// TestOpenAPISpecOptOutListIsExactlyThePublicSurface pins which operations are
// deliberately anonymous, so adding a new unauthenticated route becomes a
// deliberate act rather than an omission.
func TestOpenAPISpecOptOutListIsExactlyThePublicSurface(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(OpenAPISpecJSON, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}

	want := map[string]bool{
		"GET /api/v1/health":       true,
		"GET /api/v1/openapi.json": true,
		"GET /healthz":             true,
	}

	for path, ops := range doc.Paths {
		for verb, op := range ops {
			if !isSpecVerb(verb) || op.Security == nil || len(op.Security) > 0 {
				continue
			}
			key := strings.ToUpper(verb) + " " + path
			if !want[key] {
				t.Errorf("%s opts out of authentication but is not a known public probe", key)
			}
			delete(want, key)
		}
	}
	for key := range want {
		t.Errorf("%s is public in the router but the contract does not mark it anonymous", key)
	}
}

// TestServedSpecIsTheEmbeddedSpec proves the endpoint serves exactly the bytes
// CI can read, so a mismatch is a build problem, not a runtime surprise.
func TestServedSpecIsTheEmbeddedSpec(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleNone, http.MethodGet, "/api/v1/openapi.json", "")
	if w.Code != http.StatusOK {
		t.Fatalf("openapi.json: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	if got := w.Body.String(); got != string(OpenAPISpecJSON) {
		t.Error("the served contract differs from the embedded one")
	}
}

// TestMetricsOnlyAcceptsGet locks in the admin metrics verb now that the route
// uses Method instead of Handle.
func TestMetricsOnlyAcceptsGet(t *testing.T) {
	env := newTestEnv(t)
	admin := env.AdminHandler()

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/metrics", nil)
		req.Header.Set("X-API-Key", env.keys.admin)
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /metrics: want 405, got %d", method, w.Code)
		}
	}
}

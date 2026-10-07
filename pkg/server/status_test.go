package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStatusHandlerIsNotWrappedInTheEnvelope is the difference between a
// documented endpoint and one a customer can code against.
//
// Every other JSON response this server writes is {"success":…,"data":…}, and
// the API reference says so throughout. GET /statusz is the exception: it hands
// back the bare StatusResponse object. An integrator who wrote one unwrapping
// helper from the rest of the reference gets undefined here, and there was
// nothing — no reference section, no test — to tell them.
//
// The assertion is deliberately on the shape rather than on the values, which
// change every call: the top level must carry the documented keys directly and
// must NOT carry the envelope.
func TestStatusHandlerIsNotWrappedInTheEnvelope(t *testing.T) {
	srv := newTestServer(t, testConfig(t))

	rec := httptest.NewRecorder()
	srv.StatusHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statusz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /statusz: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Error("/statusz must set a Content-Type")
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	for _, key := range []string{"status", "version", "uptime", "addr", "components", "stats"} {
		if _, ok := body[key]; !ok {
			t.Errorf("documented key %q missing from /statusz: %v", key, body)
		}
	}
	// The whole point: no envelope. If someone "fixes" this to match the rest
	// of the API, this fails and the reference has to change in the same commit.
	if _, wrapped := body["data"]; wrapped {
		t.Errorf("/statusz must answer with a bare object, but it is wrapped: %v", body)
	}
	if success, ok := body["success"]; ok {
		t.Errorf("/statusz must not carry a success field, got %v", success)
	}
}

// A host that holds a *Server can call GetStatus before Start, and until this
// was fixed it was told the process had been up for 2562047h47m16s — the
// saturation value of a duration measured from the zero time. A status document
// that lies about uptime is worse than one that says it has none.
func TestStatusHandlerReportsNotStartedRatherThanSaturatedUptime(t *testing.T) {
	srv := newTestServer(t, testConfig(t))

	rec := httptest.NewRecorder()
	srv.StatusHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statusz", nil))

	var body struct {
		Uptime string `json:"uptime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Uptime != "not started" {
		t.Errorf("uptime = %q, want \"not started\"; a duration measured from the zero time saturates "+
			"and reads as a real number", body.Uptime)
	}
}

// The uptime helper itself, including the branch that does the work in
// production. Zero means "not started"; anything else must render as a duration.
func TestUptimeRendersADurationOnceStarted(t *testing.T) {
	if got := uptime(time.Time{}); got != "not started" {
		t.Errorf("uptime(zero) = %q, want \"not started\"", got)
	}
	got := uptime(time.Now().Add(-90 * time.Second))
	if got != "1m30s" && !strings.HasPrefix(got, "1m30") {
		t.Errorf("uptime(90s ago) = %q, want a ~1m30s duration", got)
	}
}

func TestStatusHandlerNamesEveryComponent(t *testing.T) {
	srv := newTestServer(t, testConfig(t))

	rec := httptest.NewRecorder()
	srv.StatusHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statusz", nil))

	var body struct {
		Components map[string]string `json:"components"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	for _, want := range []string{"scheduler", "executor", "observer", "sandbox", "security"} {
		got, ok := body.Components[want]
		if !ok {
			t.Errorf("components is missing %q: %v", want, body.Components)
			continue
		}
		if got == "" {
			t.Errorf("components[%q] is empty; the endpoint exists to report state, not blanks", want)
		}
	}
}

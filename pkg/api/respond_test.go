package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestJSONResponsesDeclareJSON pins the media type on the envelope that every
// /api/v1 route returns.
//
// writeJSON used to write the body without ever setting Content-Type, so Go's
// sniffer saw plain bytes beginning with '{' and answered text/plain for every
// API response - including the ones openapi.json documents as JSON. Nothing in
// the browser notices, because fetch(...).json() and res.text() ignore the type,
// which is exactly why it survived so long: a customer finds out only after
// wiring their own strict client or a generated SDK, and that becomes a ticket
// we cannot answer.
func TestJSONResponsesDeclareJSON(t *testing.T) {
	env := newTestEnv(t)

	for _, tc := range []struct {
		name         string
		method, path string
	}{
		{"public success", http.MethodGet, "/api/v1/health"},
		{"authenticated success", http.MethodGet, "/api/v1/workers"},
		{"rejection envelope", http.MethodGet, "/api/v1/tasks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := env.call(roleNone, tc.method, tc.path, "")
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s answered %d with Content-Type %q, want application/json",
					tc.method, tc.path, w.Code, ct)
			}
		})
	}
}

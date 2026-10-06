package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// resolveLocation does what a browser does with a Location header: interpret it
// against the request it came from. A relative "./" against "/" is a new absolute
// path, which is how a redirect loop becomes observable from the outside.
func resolveLocation(from, location string) (string, error) {
	base, err := url.Parse(from)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).Path, nil
}

// The static canvas is the first thing a prospective customer opens, so a request
// that never settles is worse than no UI at all. The Go standard library redirects
// any path ending in /index.html to "./" with a 301; the handler used to rewrite
// extensionless paths to exactly that, so the browser bounced forever.
//
// These tests follow the redirects the way a browser does - a client that keeps
// asking is the only honest way to see an infinite loop.

func staticGet(t *testing.T, e *testEnv, path string) *httptest.ResponseRecorder {
	t.Helper()
	return e.call("admin", http.MethodGet, path, "")
}

func TestStaticRootServesTheCanvasWithoutRedirectingForever(t *testing.T) {
	e := newTestEnv(t)

	w := staticGet(t, e, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200: a landing page that redirects is a broken landing page", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("GET / content type = %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), "<html") {
		t.Errorf("GET / did not return an HTML document; first 200 bytes: %.200q", w.Body.String())
	}
}

func TestStaticRootRedirectLoopIsBounded(t *testing.T) {
	e := newTestEnv(t)

	// Follow redirects exactly like a browser, with a hard ceiling.
	const maxHops = 5
	path := "/"
	for hop := 0; hop < maxHops; hop++ {
		w := staticGet(t, e, path)
		if w.Code == http.StatusOK {
			return // settled
		}
		if w.Code < 300 || w.Code > 399 {
			t.Fatalf("hop %d: GET %s = %d, want either 200 or a redirect", hop, path, w.Code)
		}
		location := w.Header().Get("Location")
		if location == "" {
			t.Fatalf("hop %d: GET %s returned %d with no Location header", hop, path, w.Code)
		}
		next, err := resolveLocation(path, location)
		if err != nil {
			t.Fatalf("hop %d: Location %q from GET %s: %v", hop, location, path, err)
		}
		if next == path {
			t.Fatalf("hop %d: GET %s redirects to itself (%q) - that is a loop", hop, path, location)
		}
		path = next
	}
	t.Fatalf("GET / never settled within %d redirects; last hop was GET %s", maxHops, path)
}

func TestStaticAssetsAreStillServedFromDist(t *testing.T) {
	e := newTestEnv(t)

	// The SPA needs its bundle. A missing asset must be a plain 404, not a 200
	// carrying index.html - otherwise every typo'd URL silently renders the app.
	w := staticGet(t, e, "/assets/does-not-exist.js")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /assets/does-not-exist.js = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Error("a missing asset returned the HTML shell instead of a 404")
	}
}

func TestStaticHandlerDoesNotShadowAPIRoutes(t *testing.T) {
	e := newTestEnv(t)

	// The canvas is mounted on a catch-all, so anything the API does not claim
	// falls through to it. That must never turn an API 404 into a 200.
	w := staticGet(t, e, "/api/v1/nonexistent")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/nonexistent = %d, want 404 (the canvas must not answer API paths)", w.Code)
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Error("an unknown API path returned the HTML shell instead of a JSON 404")
	}
}

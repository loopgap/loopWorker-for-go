package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"loopworker/pkg/security"
)

func securityClientIP(r *http.Request, trustProxy bool) string {
	return security.ClientIP(r, trustProxy)
}

// ---- B5: rate limiting ----

// TestB5OverBudgetIs429WithRetryAfter is the SPEC 10-B5 acceptance test: the
// old limiter keyed on RemoteAddr, so 130 requests from one bucket all passed.
func TestB5OverBudgetIs429WithRetryAfter(t *testing.T) {
	env := newTestEnv(t, WithRateLimits(3, 3, time.Minute))

	// /api/v1/health is unauthenticated, so the anonymous bucket is the one
	// under test; one IP, budget of 3.
	allowed := 0
	for i := 0; i < 3; i++ {
		w := env.call(roleNone, http.MethodGet, "/api/v1/health", "")
		if w.Code != http.StatusOK {
			t.Fatalf("request %d within budget: want 200, got %d: %s", i, w.Code, w.Body.String())
		}
		allowed++
	}

	w := env.call(roleNone, http.MethodGet, "/api/v1/health", "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("over budget: want 429, got %d: %s", w.Code, w.Body.String())
	}
	env.expectCode(w, http.StatusTooManyRequests, CodeRateLimited)

	retry := w.Header().Get("Retry-After")
	if retry == "" {
		t.Error("429 must carry Retry-After so a client knows when to come back")
	}
	if seconds, err := strconv.Atoi(retry); err != nil || seconds < 1 {
		t.Errorf("Retry-After should be a positive number of seconds, got %q", retry)
	}
	envW := decodeEnvelope(t, w)
	if !strings.Contains(envW.Error.Message, "Fix:") {
		t.Errorf("429 must carry a remedy: %q", envW.Error.Message)
	}
	if allowed != 3 {
		t.Errorf("expected exactly 3 requests to pass, got %d", allowed)
	}
}

// TestB5XForwardedForIsIgnoredByDefault proves a client cannot mint itself a new
// bucket by inventing an X-Forwarded-For header (the SPEC defect: 130 requests,
// 0 blocked).
func TestB5XForwardedForIsIgnoredByDefault(t *testing.T) {
	env := newTestEnv(t, WithRateLimits(2, 2, time.Minute))

	// Three requests, three different claimed source IPs, one real peer.
	for i, xff := range []string{"10.0.0.1", "192.168.1.100", "172.16.0.9"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		env.Router.ServeHTTP(w, req)

		if i < 2 {
			if w.Code != http.StatusOK {
				t.Fatalf("request %d (XFF %s) within budget: want 200, got %d", i, xff, w.Code)
			}
			continue
		}
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("a forged X-Forwarded-For must not buy a fresh bucket: want 429 on request %d, got %d", i, w.Code)
		}
	}

	// A different real peer is a different bucket.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("a different client IP should have its own budget, got %d", w.Code)
	}
}

// TestB5XForwardedForRespectedWhenTrusted is the other half: behind a proxy the
// operator controls, XFF must actually bucket by client.
func TestB5XForwardedForRespectedWhenTrusted(t *testing.T) {
	env := newTestEnv(t, WithRateLimits(1, 1, time.Minute), WithTrustProxy(true))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "10.0.0.1:1111"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	if w := serve(env, req); w.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "10.0.0.1:1111"
	req.Header.Set("X-Forwarded-For", "203.0.113.6")
	if w := serve(env, req); w.Code != http.StatusOK {
		t.Errorf("a different forwarded client should have its own budget, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "10.0.0.1:1111"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	if w := serve(env, req); w.Code != http.StatusTooManyRequests {
		t.Errorf("the same forwarded client must exhaust its budget, got %d", w.Code)
	}
}

// TestB5AuthenticatedAndAnonymousBucketsAreSeparate proves a valid credential
// is not punished for anonymous traffic and vice versa.
func TestB5AuthenticatedAndAnonymousBucketsAreSeparate(t *testing.T) {
	env := newTestEnv(t, WithRateLimits(2, 5, time.Minute))

	// Burn the anonymous budget from two distinct IPs.
	for _, addr := range []string{"10.0.0.1:1", "10.0.0.2:1"} {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			req.RemoteAddr = addr
			if w := serve(env, req); w.Code != http.StatusOK {
				t.Fatalf("anonymous budget: want 200, got %d", w.Code)
			}
		}
	}
	// Now exceed one anonymous bucket.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "10.0.0.1:1"
	if w := serve(env, req); w.Code != http.StatusTooManyRequests {
		t.Fatalf("anonymous over budget: want 429, got %d", w.Code)
	}

	// The credentialed bucket is untouched.
	for i := 0; i < 4; i++ {
		w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks", "")
		if w.Code != http.StatusOK {
			t.Fatalf("authenticated request %d: want 200, got %d", i, w.Code)
		}
	}
}

func TestB5ClientIPResolution(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		trustProxy bool
		want       string
	}{
		{"no proxy header", "10.1.2.3:5555", "", false, "10.1.2.3"},
		{"xff ignored", "10.1.2.3:5555", "9.9.9.9", false, "10.1.2.3"},
		{"xff trusted, single hop", "10.1.2.3:5555", "9.9.9.9", true, "9.9.9.9"},
		{"xff trusted, rightmost hop wins", "10.1.2.3:5555", "1.1.1.1, 2.2.2.2, 9.9.9.9", true, "9.9.9.9"},
		{"ipv6 remote", "[::1]:5555", "", false, "::1"},
		{"ipv6 forwarded", "10.0.0.1:1", "2001:db8::1", true, "2001:db8::1"},
		{"empty remote", "", "", false, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := securityClientIP(req, tc.trustProxy); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// ---- B9: CORS ----

// TestB9CORSRejectsOriginOutsideTheAllowList is the SPEC 10-B9 acceptance test:
// the old middleware answered with a wildcard for every origin.
func TestB9CORSRejectsOriginOutsideTheAllowList(t *testing.T) {
	env := newTestEnv(t)

	for _, origin := range []string{
		"https://evil.example.com",
		"http://localhost:3000",
		"null",
		"http://127.0.0.1:19527.evil.com",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		env.Router.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q must not be allowed, got ACAO=%q", origin, got)
		}
		if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("origin %q must not receive credential permission, got %q", origin, got)
		}
	}
}

func TestB9CORSAllowsListedOrigin(t *testing.T) {
	env := newTestEnv(t)

	for _, origin := range DefaultAllowedOrigins() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		env.Router.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("listed origin %q: want it echoed, got %q", origin, got)
		}
		if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("listed origin %q: want credentials allowed, got %q", origin, got)
		}
		if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Origin") {
			t.Errorf("listed origin %q: response must vary on Origin so caches do not mix them up", origin)
		}
	}
}

func TestB9CORSPreflightIs204(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/tasks", nil)
	req.Header.Set("Origin", DefaultAllowedOrigins()[0])
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight: want 204, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("preflight must advertise allowed methods")
	}
	if w.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("preflight must advertise allowed headers")
	}
	// The request id header must be preflightable, or browsers drop it.
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Request-ID") {
		t.Error("X-Request-ID must be an allowed request header")
	}
}

// TestB9WildcardNeverPairsWithCredentials pins the invariant that makes a
// wildcard safe to offer at all.
func TestB9WildcardNeverPairsWithCredentials(t *testing.T) {
	env := newTestEnv(t, func(cfg *Config) {
		cfg.AllowedOrigins = []string{"*"}
		cfg.AllowWildcard = true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Origin", "https://anywhere.example")
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("wildcard mode should answer *, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("a wildcard must never be paired with credentialed requests, got %q", got)
	}
}

// ---- request id ----

func TestRequestIDMiddleware(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleOperator, http.MethodGet, "/api/v1/auth/whoami", "")
	id := w.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("X-Request-ID header must be set")
	}
	who := decodeData(t, w)
	if who["request_id"] != id {
		t.Errorf("the body request_id %v must match the header %q", who["request_id"], id)
	}
}

func TestRequestIDHonoursACleanCallerValue(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("X-Request-ID", "my-custom-id_1.2:3")
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)

	if got := w.Header().Get("X-Request-ID"); got != "my-custom-id_1.2:3" {
		t.Errorf("a safe caller id must be preserved, got %q", got)
	}
}

func TestRequestIDRejectsHostileCallerValues(t *testing.T) {
	env := newTestEnv(t)

	// An injected id would poison logs and the support correlation field.
	for _, hostile := range []string{
		"has space",
		"has\nnewline",
		strings.Repeat("a", 65),
		"quote\"and\\backslash",
		"<script>",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("X-Request-ID", hostile)
		w := httptest.NewRecorder()
		env.Router.ServeHTTP(w, req)

		got := w.Header().Get("X-Request-ID")
		if got == hostile {
			t.Errorf("hostile request id %q was accepted verbatim", hostile)
		}
		if got == "" {
			t.Errorf("hostile request id %q must be replaced by a minted one", hostile)
		}
	}
}

// TestRequestIDFromContextReadsTheKey covers the accessor contract handlers rely
// on: present, absent, and wrong-typed values all behave.
func TestRequestIDFromContextReadsTheKey(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("a context without the key must yield an empty id, got %q", got)
	}
	ctx := context.WithValue(context.Background(), requestIDKey{}, "test-id-123")
	if got := RequestIDFromContext(ctx); got != "test-id-123" {
		t.Errorf("want test-id-123, got %q", got)
	}
	wrong := context.WithValue(context.Background(), requestIDKey{}, 42)
	if got := RequestIDFromContext(wrong); got != "" {
		t.Errorf("a wrongly typed value must yield an empty id, got %q", got)
	}
}

// ---- body limit ----

// TestB7BodyLimitRejectsOversizedRequest is the body-limit evidence: the limit
// must be enforced before any handler work, and reported through the contract.
func TestBodyLimitRejectsOversizedRequest(t *testing.T) {
	env := newTestEnv(t, withSmallBodyLimit(2048))

	huge := strings.Repeat("x", 8192)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks",
		strings.NewReader(`{"type":"big","input_text":"`+huge+`"}`))
	req.Header.Set("X-API-Key", env.keys.operator)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: want 413, got %d: %s", w.Code, w.Body.String())
	}
	envW := decodeEnvelope(t, w)
	if envW.Error.Code != CodeRequestTooLarge {
		t.Errorf("want %s, got %s", CodeRequestTooLarge, envW.Error.Code)
	}
}

func TestBodyLimitAllowsASmallRequest(t *testing.T) {
	env := newTestEnv(t, withSmallBodyLimit(2048))
	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"small","input_text":"hello"}`),
		http.StatusCreated)
}

// TestBodyLimitExposesItsCeiling proves a caller can discover the limit instead
// of guessing it from a 413.
func TestBodyLimitExposesItsCeiling(t *testing.T) {
	env := newTestEnv(t, withSmallBodyLimit(4096))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks",
		strings.NewReader(`{"type":"big","input_text":"`+strings.Repeat("x", 9000)+`"}`))
	req.Header.Set("X-API-Key", env.keys.operator)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.Router.ServeHTTP(w, req)

	envW := decodeEnvelope(t, w)
	limit, _ := envW.Error.Details["limit_bytes"].(float64)
	if int64(limit) != 4096 {
		t.Errorf("the 413 should report the configured limit, got %v", envW.Error.Details)
	}
	if !strings.Contains(envW.Error.Message, "Fix:") {
		t.Errorf("413 must carry a remedy: %q", envW.Error.Message)
	}
}

// ---- security headers and timeout ----

func TestSecurityHeadersArePresent(t *testing.T) {
	env := newTestEnv(t)
	w := env.call(roleNone, http.MethodGet, "/api/v1/health", "")

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s: want %q, got %q", header, want, got)
		}
	}
}

// TestTimeoutDoesNotApplyToStreams proves the request deadline cannot truncate
// an SSE connection (SPEC 10-B6: "Timeout cut the stream").
func TestTimeoutDoesNotApplyToStreams(t *testing.T) {
	env := newTestEnv(t, WithRequestTimeout(50*time.Millisecond))

	cfg := newConfig(WithAuth(mustAuth(t)))
	bundle := &middlewareBundle{auth: env.auth, limits: env.limits, cfg: cfg, streams: env.streams}

	if !isStreamingPath("/api/v1/events/live") {
		t.Fatal("/api/v1/events/live must be recognised as a streaming path")
	}
	if isStreamingPath("/api/v1/tasks") {
		t.Error("ordinary paths must not be exempt from the deadline")
	}

	// The middleware must pass a streaming request through without a deadline.
	sawDeadline := false
	handler := bundle.timeout(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, sawDeadline = r.Context().Deadline()
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/events/live", nil))
	if sawDeadline {
		t.Error("a streaming request must not carry the handler deadline")
	}

	sawDeadline = false
	ordinary := bundle.timeout(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, sawDeadline = r.Context().Deadline()
	}))
	ordinary.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil))
	if !sawDeadline {
		t.Error("an ordinary request must carry the handler deadline")
	}
}

func serve(e *testEnv, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.Router.ServeHTTP(w, req)
	return w
}

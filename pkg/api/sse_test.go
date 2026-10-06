package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loopworker/pkg/event"
	"loopworker/pkg/security"
)

// sseSession is one live stream under test.
type sseSession struct {
	resp    *http.Response
	frames  chan string
	cancel  context.CancelFunc
	stopped chan struct{}
}

// openStream starts GET /api/v1/events/live against a real HTTP server, which
// is the only way the Flusher chain and the socket timeouts are genuinely
// exercised (httptest.ResponseRecorder fakes both).
func openStream(t *testing.T, srv *httptest.Server, key string, query string) *sseSession {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/live"+query, nil)
	if err != nil {
		cancel()
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-API-Key", key)

	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("stream handshake: want 200, got %d", resp.StatusCode)
	}

	session := &sseSession{resp: resp, frames: make(chan string, 64), cancel: cancel}
	session.stopped = make(chan struct{})
	go func() {
		defer close(session.stopped)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 4096), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case session.frames <- line:
			default: // a slow test must not stall the stream
			}
		}
	}()
	t.Cleanup(session.close)
	return session
}

// nextFrame waits for the first line matching want.
func (s *sseSession) nextFrame(t *testing.T, want string, within time.Duration) string {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case line, open := <-s.frames:
			if !open {
				t.Fatalf("stream closed while waiting for %q", want)
			}
			if strings.Contains(line, want) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for a %q frame", within, want)
		}
	}
}

// alive reports whether the connection is still open and not closed by the
// server, using the channel rather than a sleep.
func (s *sseSession) alive() bool {
	select {
	case <-s.stopped:
		return false
	default:
		return true
	}
}

func (s *sseSession) close() {
	s.cancel()
	select {
	case <-s.stopped:
	case <-time.After(2 * time.Second):
	}
}

// TestB6StreamEventsLiveIsAWorkingSSEEndpoint is the SPEC 10-B6 acceptance
// test. The old handler answered 500 because http.Flusher was not forwarded
// through the middleware chain; this asserts 200, the media type, at least one
// data frame, and that the connection is still alive 5 seconds later.
func TestB6StreamEventsLiveIsAWorkingSSEEndpoint(t *testing.T) {
	env := newTestEnv(t, withKeepalive(200*time.Millisecond))
	srv := httptest.NewServer(env.Router)
	t.Cleanup(srv.Close)

	session := openStream(t, srv, env.keys.operator, "")

	if ct := session.resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type: want text/event-stream, got %q", ct)
	}
	if cc := session.resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: want no-cache, got %q", cc)
	}
	if buffering := session.resp.Header.Get("X-Accel-Buffering"); buffering != "no" {
		t.Errorf("X-Accel-Buffering: want no (proxies must not buffer SSE), got %q", buffering)
	}

	// The opening frame must arrive before the deadline, not only eventually.
	session.nextFrame(t, "data:", 2*time.Second)

	// Publish an event and prove it reaches the stream.
	if err := env.bus.Publish(context.Background(), event.NewEvent(
		event.EventTaskCompleted,
		event.TaskCompletedPayload{TaskID: "task-1", Result: "done"},
		nil,
	)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// An SSE frame is "event:" then "data:"; assert both halves arrive.
	session.nextFrame(t, "event: task.completed", 3*time.Second)
	session.nextFrame(t, "data:", 3*time.Second)

	// Liveness across five seconds, observed rather than slept through. The
	// keepalive interval is 200ms, so a stream the handler or the server closed
	// would stop delivering within a fraction of that: counting real frames
	// proves the connection is still being written to.
	deadline := time.After(5 * time.Second)
	keepalives := 0
	for {
		select {
		case <-deadline:
			if keepalives < 5 {
				t.Fatalf("stream delivered only %d keepalives in 5s at a 200ms interval; it is not staying connected", keepalives)
			}
			if !session.alive() {
				t.Fatal("stream closed within the first 5 seconds")
			}
			t.Logf("stream stayed connected for 5s with %d keepalives", keepalives)
			return
		case line, open := <-session.frames:
			if !open {
				t.Fatalf("stream closed after %d frames, expected it to stay open for 5s", keepalives)
			}
			if strings.HasPrefix(line, ": keep-alive") {
				keepalives++
			}
		}
	}
}

// TestB6StreamSurvivesRequestTimeout proves the handler deadline does not cut a
// long-lived stream (SPEC 10-B6: "Timeout cut the stream").
func TestB6StreamSurvivesRequestTimeout(t *testing.T) {
	env := newTestEnv(t, WithRequestTimeout(100*time.Millisecond), withKeepalive(100*time.Millisecond))
	srv := httptest.NewServer(env.Router)
	t.Cleanup(srv.Close)

	session := openStream(t, srv, env.keys.operator, "")
	session.nextFrame(t, "data:", 2*time.Second)

	// The deadline is 100ms; several keepalives must still arrive well after.
	keepalives := 0
	deadline := time.After(2 * time.Second)
	for keepalives < 3 {
		select {
		case line, open := <-session.frames:
			if !open {
				t.Fatalf("stream closed after %d keepalives; the request deadline cut it", keepalives)
			}
			if strings.HasPrefix(line, ": keep-alive") {
				keepalives++
			}
		case <-deadline:
			t.Fatalf("only %d keepalives in 2s with a 100ms request timeout", keepalives)
		}
	}
}

// TestB6StreamReleasesItsSlotOnDisconnect proves a departing client frees the
// concurrency slot, so a browser refresh cannot deadlock the endpoint.
func TestB6StreamReleasesItsSlotOnDisconnect(t *testing.T) {
	env := newTestEnv(t, withStreamLimits(1, 4, 50*time.Millisecond))
	srv := httptest.NewServer(env.Router)
	t.Cleanup(srv.Close)

	first := openStream(t, srv, env.keys.operator, "")
	first.nextFrame(t, "data:", 2*time.Second)

	// The single per-caller slot is taken.
	w := env.call(roleOperator, http.MethodGet, "/api/v1/events/live", "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("a second stream: want 429, got %d: %s", w.Code, w.Body.String())
	}
	env.expectCode(w, http.StatusTooManyRequests, CodeStreamLimitReached)
	if w.Header().Get("Retry-After") == "" {
		t.Error("a stream rejection should tell the client when to retry")
	}

	first.close()

	// Poll the limiter until the handler's deferred release has run. Probing the
	// limiter (rather than opening another stream) keeps the assertion
	// deterministic: a successful probe would itself occupy the slot and block
	// until it is cancelled.
	deadline := time.After(3 * time.Second)
	for {
		if env.streams.Total() == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the stream slot was never released: %d still held", env.streams.Total())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// The freed slot must now accept a new stream.
	second := openStream(t, srv, env.keys.operator, "")
	second.nextFrame(t, "data:", 2*time.Second)
}

// TestB6StreamSubscriptionIsBounded is the other half of "subscriptions had no
// ceiling": the global ceiling must also refuse.
func TestB6StreamSubscriptionIsBounded(t *testing.T) {
	env := newTestEnv(t, withStreamLimits(4, 1, 50*time.Millisecond))
	srv := httptest.NewServer(env.Router)
	t.Cleanup(srv.Close)

	first := openStream(t, srv, env.keys.operator, "")
	first.nextFrame(t, "data:", 2*time.Second)

	// A different caller must still hit the global ceiling.
	other := addKey(t, env, "second-streamer", security.RoleOperator)
	w := env.callWithKey(other, http.MethodGet, "/api/v1/events/live", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("global stream ceiling: want 503, got %d: %s", w.Code, w.Body.String())
	}
	env.expectCode(w, http.StatusServiceUnavailable, CodeStreamLimitReached)
}

// TestB6StreamDoesNotLeakOtherTenantsEvents proves the SSE surface respects
// task ownership: a caller learns the event happened, not what it contains.
func TestB6StreamDoesNotLeakOtherTenantsEvents(t *testing.T) {
	env := newTestEnv(t)
	srv := httptest.NewServer(env.Router)
	t.Cleanup(srv.Close)

	theirs := env.createTask(roleOperator, "other-tenant", `,"config":{"api_key":"top-secret"}`)

	spy := secondOperatorKey(t, env)
	session := openStream(t, srv, spy, "")
	session.nextFrame(t, "data:", 2*time.Second)

	if err := env.bus.Publish(context.Background(), event.NewEvent(
		event.EventTaskCreated,
		event.TaskCreatedPayload{TaskID: theirs, TaskType: "other-tenant"},
		nil,
	)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// An SSE frame is an "event:" line followed by a "data:" line, so assert on
	// the data half.
	session.nextFrame(t, "event: task.created", 3*time.Second)
	frame := session.nextFrame(t, "data:", 3*time.Second)
	if strings.Contains(frame, "top-secret") {
		t.Fatalf("the stream leaked another tenant's task config: %q", frame)
	}
	if !strings.Contains(frame, "hidden") {
		t.Errorf("an event about someone else's task must be marked hidden, got %q", frame)
	}
}

// TestB6StreamRejectsUnknownEventTypes proves the ?types= filter is validated
// rather than silently ignored.
func TestB6StreamRejectsUnknownEventTypes(t *testing.T) {
	env := newTestEnv(t)
	env.expectCode(env.call(roleOperator, http.MethodGet, "/api/v1/events/live?types=not.a.type", ""),
		http.StatusBadRequest, CodeInvalidRequest)

	// The resolver itself must treat an empty filter as "everything published".
	all, err := requestedEventTypes(httptest.NewRequest(http.MethodGet, "/api/v1/events/live?types=", nil))
	if err != nil {
		t.Fatalf("empty types filter should not be an error, got %v", err)
	}
	if len(all) != len(streamEventTypes) {
		t.Errorf("empty filter should subscribe to the whole whitelist, got %d of %d", len(all), len(streamEventTypes))
	}

	// "types=task" is the documented shorthand for the six task events.
	tasksOnly, err := requestedEventTypes(httptest.NewRequest(http.MethodGet, "/api/v1/events/live?types=task", nil))
	if err != nil {
		t.Fatalf("task shorthand: %v", err)
	}
	if len(tasksOnly) != 6 {
		t.Errorf("task shorthand should select 6 event types, got %d", len(tasksOnly))
	}
}

func withKeepalive(d time.Duration) Option {
	return func(cfg *Config) { cfg.StreamKeepalive = d }
}

// withStreamLimits sets the per-caller and global stream ceilings.
func withStreamLimits(perCaller, total int, keepalive time.Duration) Option {
	return func(cfg *Config) {
		cfg.MaxStreamsPerCaller = perCaller
		cfg.MaxStreamsTotal = total
		cfg.StreamKeepalive = keepalive
	}
}

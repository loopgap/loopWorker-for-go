package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loopworker/pkg/event"
)

// This endpoint is the whole live-update path for the web canvas, and until now
// it had no test that actually opened a stream: the only coverage was a 503
// table entry for a missing event bus and a plumbing test proving the response
// writer exposes a Flusher. Nothing checked the frame format, the type filter,
// the cross-tenant rule, or that a published event arrives at all.
//
// A ResponseRecorder cannot be used here. The handler holds the response open
// until the client goes away - that is what a stream is - so a recorder-based
// call blocks until the test binary times out. These tests run the API server in
// this process against a real listener; nothing here launches the built binary.

// streamTimeout is generous on purpose. It is a hang detector, not a timing
// assertion: every wait below should complete in microseconds, and on a loaded
// CI runner a few hundred milliseconds of scheduling is normal.
const streamTimeout = 5 * time.Second

// streamResponse is the part of the HTTP response a test may inspect. The
// response itself stays inside openStream: its body is still being streamed, and
// the returned stop function is what closes it.
type streamResponse struct {
	status int
	header http.Header
}

// openStream opens GET /api/v1/events/live on a real listener and returns a
// channel of response lines plus the status and headers, so a test can assert on
// what the handler set before the first frame.
func (e *testEnv) openStream(t *testing.T, role, query string) (<-chan string, streamResponse, func()) {
	t.Helper()
	srv := httptest.NewServer(e.Router)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/live"+query, nil)
	if err != nil {
		cancel()
		t.Fatalf("build stream request: %v", err)
	}
	if role != roleNone {
		req.Header.Set("X-API-Key", e.keys.forRole(role))
	}
	//nolint:bodyclose // the body is read by the scanner goroutine below and closed by the returned stop function
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}

	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines, streamResponse{status: resp.StatusCode, header: resp.Header}, func() {
		cancel()
		_ = resp.Body.Close()
	}
}

// awaitLine waits for the first response line containing want and returns it.
// SSE frames are "event: <name>", "data: <json>", then a blank line, so a test
// asserting on the payload has to consume the name line first.
func awaitLine(t *testing.T, lines <-chan string, want string) string {
	t.Helper()
	deadline := time.After(streamTimeout)
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("stream closed while waiting for a line containing %q", want)
			}
			if strings.Contains(line, want) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a line containing %q", want)
			return ""
		}
	}
}

func awaitAbsent(t *testing.T, lines <-chan string, unwanted string) {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case line, open := <-lines:
			if !open {
				return
			}
			if strings.Contains(line, unwanted) {
				t.Fatalf("stream carried %q, which should have been filtered out: %q", unwanted, line)
			}
		case <-deadline:
			return
		}
	}
}

// TestEventStreamSetsTheHeadersAnIntermediaryNeeds pins the header contract.
// Without Cache-Control: no-cache a browser will not fire onmessage for an
// EventSource, and without X-Accel-Buffering: no nginx sits on the connection
// and holds every frame until the buffer fills - which presents as "the canvas
// just stopped updating", the hardest kind of report to reproduce.
func TestEventStreamSetsTheHeadersAnIntermediaryNeeds(t *testing.T) {
	env := newTestEnv(t)
	lines, resp, stop := env.openStream(t, roleAdmin, "")
	defer stop()

	if resp.status != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.status)
	}
	for header, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	} {
		if got := resp.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	awaitLine(t, lines, "event: stream.opened")
	awaitLine(t, lines, "data:")
}

// TestEventStreamDeliversAPublishedEvent is the contract the canvas is built
// on. The canvas tests stub fetch, so this is the only place the real frame
// format is checked on either side of the wire.
func TestEventStreamDeliversAPublishedEvent(t *testing.T) {
	env := newTestEnv(t)
	lines, _, stop := env.openStream(t, roleAdmin, "?types=task.created")
	defer stop()
	awaitLine(t, lines, "event: stream.opened")

	if err := env.bus.Publish(context.Background(), event.NewEvent(event.EventTaskCreated,
		map[string]any{"task_id": "task-42"}, nil)); err != nil {
		t.Fatalf("publish task.created: %v", err)
	}

	if name := awaitLine(t, lines, "event: task.created"); !strings.HasPrefix(name, "event: ") {
		t.Errorf("event line is not an SSE event line: %q", name)
	}
	data := awaitLine(t, lines, "task-42")
	if !strings.HasPrefix(data, "data: ") {
		t.Fatalf("payload line is not an SSE data line: %q", data)
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &frame); err != nil {
		t.Fatalf("data line is not JSON: %v\n%q", err, data)
	}
	if frame["task_id"] != "task-42" {
		t.Errorf("frame carried task_id %v, want task-42", frame["task_id"])
	}
}

// A filter that matches nothing must stay silent rather than erroring: the
// canvas opens the stream with an explicit type list, and a stream that dies on
// the first unrelated event looks like a dropped connection.
func TestEventStreamFiltersOutUnrequestedTypes(t *testing.T) {
	env := newTestEnv(t)
	lines, _, stop := env.openStream(t, roleAdmin, "?types=task.completed")
	defer stop()
	awaitLine(t, lines, "event: stream.opened")

	if err := env.bus.Publish(context.Background(), event.NewEvent(event.EventTaskCreated,
		map[string]any{"task_id": "task-99"}, nil)); err != nil {
		t.Fatalf("publish task.created: %v", err)
	}
	awaitAbsent(t, lines, "task-99")
}

// An unknown type is a client mistake, so it is answered with the allowed set
// rather than an empty 200 that never produces anything.
func TestEventStreamRejectsAnUnknownTypeFilter(t *testing.T) {
	env := newTestEnv(t)
	srv := httptest.NewServer(env.Router)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events/live?types=bogus", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-API-Key", env.keys.forRole(roleAdmin))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown type filter = %d, want 400", resp.StatusCode)
	}
	var env0 wire
	if err := json.NewDecoder(resp.Body).Decode(&env0); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env0.Error == nil || !strings.Contains(env0.Error.Message, "task.completed") {
		t.Errorf("the 400 does not tell the caller which types are allowed: %+v", env0.Error)
	}
}

// TestEventStreamHidesAnotherCallersTask pins the content rule: the frame still
// arrives, with the payload replaced by an opaque stub, so a subscriber cannot
// read a task they do not own.
//
// AGENT-COLLABORATION-SPEC §8 records an open product question about whether the
// *existence* of such an event should be observable at all. That is a decision,
// not a defect, so this test pins the content rule only and says nothing about
// whether the empty frame should exist.
func TestEventStreamHidesAnotherCallersTask(t *testing.T) {
	env := newTestEnv(t)
	taskID := env.createTask(roleOperator, "owned-by-operator", "")

	lines, _, stop := env.openStream(t, roleViewer, "?types=task.created")
	defer stop()
	awaitLine(t, lines, "event: stream.opened")

	if err := env.bus.Publish(context.Background(), event.NewEvent(event.EventTaskCreated,
		map[string]any{"task_id": taskID, "input": "secret-input"}, nil)); err != nil {
		t.Fatalf("publish task.created: %v", err)
	}

	awaitLine(t, lines, "event: task.created")
	data := awaitLine(t, lines, "hidden")
	if strings.Contains(data, "secret-input") {
		t.Fatalf("stream leaked another caller's task payload: %q", data)
	}
}

// The stream holds a slot in a bounded limiter. A client that disconnects must
// give it back, or a canvas that reloads a few times exhausts the limit and the
// operator is told to raise api.max_streams_total - a limit they then have to
// raise in production to work around a leak.
func TestEventStreamReleasesItsSlotWhenTheClientGoesAway(t *testing.T) {
	env := newTestEnv(t)
	perCaller := env.cfg.MaxStreamsPerCaller
	if perCaller < 1 {
		t.Fatalf("MaxStreamsPerCaller = %d; this test needs at least 1", perCaller)
	}

	stops := make([]func(), 0, perCaller)
	for i := 0; i < perCaller; i++ {
		lines, resp, stop := env.openStream(t, roleAdmin, "")
		if resp.status != http.StatusOK {
			t.Fatalf("stream %d = %d, want 200", i+1, resp.status)
		}
		awaitLine(t, lines, "event: stream.opened")
		stops = append(stops, stop)
	}

	// The caller's quota is now spent. A further stream must be refused, which
	// is what makes the release below observable rather than vacuous.
	if w := env.call(roleAdmin, http.MethodGet, "/api/v1/events/live", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("stream beyond the per-caller quota = %d, want 429", w.Code)
	}

	for _, stop := range stops {
		stop()
	}

	// The handler returns on r.Context().Done(), which the client cancelling the
	// request triggers asynchronously, so poll rather than assume it is instant.
	deadline := time.Now().Add(streamTimeout)
	for {
		_, resp, stop2 := env.openStream(t, roleAdmin, "")
		status := resp.status
		stop2()
		switch status {
		case http.StatusOK:
			return
		case http.StatusTooManyRequests:
			if time.Now().After(deadline) {
				t.Fatalf("slots were not released within %s: a later stream is still refused with 429", streamTimeout)
			}
			time.Sleep(20 * time.Millisecond)
		default:
			t.Fatalf("stream after disconnect = %d, want 200 once the slots are free", status)
		}
	}
}

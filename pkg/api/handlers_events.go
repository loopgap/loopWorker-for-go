package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/security"
)

// streamEventTypes is the whitelist of event types published to SSE clients.
var streamEventTypes = []event.EventType{
	event.EventTaskCreated, event.EventTaskStarted, event.EventTaskCompleted,
	event.EventTaskFailed, event.EventTaskRetried, event.EventTaskCancelled,
	event.EventPluginExecuted, event.EventSkillInvoked, event.EventResearchFinding,
	event.EventWorkflowStepCompleted, event.EventWorkflowStarted,
	event.EventWorkflowCompleted, event.EventWorkflowFailed,
}

// streamEventNames maps the ?types= filter values onto event types.
var streamEventNames = func() map[string]event.EventType {
	out := make(map[string]event.EventType, len(streamEventTypes))
	for _, t := range streamEventTypes {
		out[string(t)] = t
	}
	return out
}()

// sseFrame is one merged event plus its rendering.
type sseFrame struct {
	Name string
	Data []byte
}

// streamEventsLive answers GET /api/v1/events/live. Subscriptions are bounded per
// caller and globally, every subscriber is released when the client goes away.
func (s *APIServer) streamEventsLive(w http.ResponseWriter, r *http.Request) {
	if s.deps.Events == nil {
		sendError(w, r, missingDependency("events", "the event bus is not wired into this server"))
		return
	}

	requested, err := requestedEventTypes(r)
	if err != nil {
		sendError(w, r, err)
		return
	}

	caller := principalSubject(r)
	release, granted, kind := s.streams.Acquire(caller)
	if !granted {
		status, message := s.streamRejection(kind, caller)
		w.Header().Set("Retry-After", "5")
		sendError(w, r, &FieldError{Reason: message,
			Fix:    "close an existing stream, or raise api.max_streams_per_caller / api.max_streams_total",
			Status: status, Code: CodeStreamLimitReached})
		return
	}
	defer release()

	flusher, ok := unwrapFlusher(w)
	if !ok {
		sendError(w, r, &FieldError{Reason: "this response writer cannot stream",
			Fix:    "disable response buffering (reverse proxy buffering, or the logging wrapper must propagate http.Flusher)",
			Status: http.StatusNotImplemented, Code: CodeStreamingUnsupported})
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	header.Set("X-Request-ID", RequestIDFromContext(r.Context()))
	w.WriteHeader(http.StatusOK)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	frames := make(chan sseFrame, streamBuffer)
	var subs []*event.Subscriber
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		for _, sub := range subs {
			s.deps.Events.Unsubscribe(sub)
		}
	}()

	for _, eventType := range requested {
		sub := s.deps.Events.Subscribe(eventType, subscriberBuffer)
		subs = append(subs, sub)
		wg.Add(1)
		go func(name event.EventType, ch <-chan event.Event) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case evt, open := <-ch:
					if !open {
						return
					}
					frame, include := s.renderEvent(r, evt)
					if !include {
						continue
					}
					select {
					case frames <- frame:
					case <-ctx.Done():
						return
					}
				}
			}
		}(eventType, sub.Chan())
	}

	_, _ = fmt.Fprintf(w, "event: stream.opened\ndata: {\"caller\":%q,\"types\":%d}\n\n", caller, len(requested))
	flusher.Flush()

	keepalive := s.cfg.StreamKeepalive
	if keepalive <= 0 {
		keepalive = 15 * time.Second
	}
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case frame := <-frames:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.Name, frame.Data); err != nil {
				logger.Get().Debug("sse write failed; closing stream", zap.Error(err))
				return
			}
			flusher.Flush()
		}
	}
}

const (
	streamBuffer     = 64
	subscriberBuffer = 32
)

// streamRejection maps a limiter denial onto a status and message.
func (s *APIServer) streamRejection(kind security.LimitKind, caller string) (int, string) {
	switch kind {
	case security.LimitGlobal:
		return http.StatusServiceUnavailable,
			"This server already serves the maximum of " + itoa(s.streams.MaxTotal()) + " concurrent event streams"
	case security.LimitPerKey:
		return http.StatusTooManyRequests,
			"Caller " + caller + " already holds the maximum of " + itoa(s.streams.MaxPerKey()) + " concurrent event streams"
	default:
		return http.StatusServiceUnavailable, "No event stream slot could be allocated"
	}
}

// requestedEventTypes resolves the ?types= filter, defaulting to the whitelist.
func requestedEventTypes(r *http.Request) ([]event.EventType, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("types"))
	if raw == "" {
		return streamEventTypes, nil
	}
	if raw == "task" {
		return []event.EventType{
			event.EventTaskCreated, event.EventTaskStarted, event.EventTaskCompleted,
			event.EventTaskFailed, event.EventTaskRetried, event.EventTaskCancelled,
		}, nil
	}

	parts := strings.Split(raw, ",")
	out := make([]event.EventType, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		found, ok := streamEventNames[name]
		if !ok {
			allowed := make([]string, 0, len(streamEventNames))
			for key := range streamEventNames {
				allowed = append(allowed, key)
			}
			sortStrings(allowed)
			return nil, &FieldError{Field: "types",
				Reason: "\"" + name + "\" is not a published event type",
				Fix:    "choose from: " + strings.Join(allowed, ", "),
				Status: http.StatusBadRequest, Code: CodeInvalidRequest,
				Details: map[string]any{"allowed": allowed}}
		}
		out = append(out, found)
	}
	if len(out) == 0 {
		return nil, &FieldError{Field: "types", Reason: "types was provided but empty after parsing",
			Fix: "omit types to receive every published event", Status: http.StatusBadRequest, Code: CodeInvalidRequest}
	}
	return out, nil
}

// renderEvent serialises one event, replacing task payloads the caller may not
// see with an opaque stub so streams cannot leak other tenants' data.
func (s *APIServer) renderEvent(r *http.Request, evt event.Event) (sseFrame, bool) {
	name := string(evt.Type())
	requested := map[string][]byte{}

	if payload := evt.Payload(); payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			encoded = []byte("null")
		}
		requested["payload"] = encoded
	}

	if taskID := taskIDOf(payloadMap(evt.Payload())); taskID != "" {
		if task, found := s.deps.Tasks.GetTask(taskID); found {
			if canSee(principal(r), ownerOf(task)) {
				if encoded, err := json.Marshal(newTaskView(task)); err == nil {
					requested["task"] = encoded
				}
			} else {
				// The event happened; its content is not this caller's to read.
				return sseFrame{Name: name, Data: []byte(`{"hidden":true,"reason":"task_owned_by_another_caller","task_id":"` + taskID + `"}`)}, true
			}
		}
	}

	data := requested["payload"]
	if len(requested["task"]) > 0 {
		data = mergeJSONObjects(requested["payload"], requested["task"])
	}
	if len(data) == 0 {
		data = []byte("{}")
	}
	return sseFrame{Name: name, Data: data}, true
}

func payloadMap(payload any) map[string]any {
	if typed, ok := payload.(map[string]any); ok {
		return typed
	}
	if typed, ok := payload.(map[string]string); ok {
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = v
		}
		return out
	}
	// Struct payloads: re-encode through JSON so field tags decide the keys.
	if payload != nil {
		if encoded, err := json.Marshal(payload); err == nil {
			var out map[string]any
			if json.Unmarshal(encoded, &out) == nil {
				return out
			}
		}
	}
	return nil
}

func taskIDOf(m map[string]any) string {
	if m == nil {
		return ""
	}
	if id, ok := m["task_id"].(string); ok {
		return id
	}
	if id, ok := m["TaskID"].(string); ok {
		return id
	}
	return ""
}

// mergeJSONObjects overlays task fields onto the payload object.
func mergeJSONObjects(payload, task []byte) []byte {
	base := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &base)
	}
	overlay := map[string]any{}
	if len(task) > 0 {
		_ = json.Unmarshal(task, &overlay)
	}
	base["task"] = overlay
	encoded, err := json.Marshal(base)
	if err != nil {
		return payload
	}
	return encoded
}

// stateNameOf is kept for tests that assert state rendering.
func stateNameOf(state scheduler.TaskState) string { return string(state) }

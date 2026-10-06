package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// writeJSON emits the envelope with stable field ordering and no HTML escaping.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, env Envelope) {
	env.Timestamp = time.Now().UTC()
	env.RequestID = RequestIDFromContext(r.Context())
	if env.Error != nil {
		env.Error.RequestID = env.RequestID
		env.Data = nil
		env.Success = false
	}
	if status > 0 {
		w.WriteHeader(status)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(env)
}

// sendSuccess writes a 2xx envelope.
func sendSuccess(w http.ResponseWriter, r *http.Request, data any, status int) {
	writeJSON(w, r, status, Envelope{Success: true, Data: data})
}

// sendError classifies err and writes the matching envelope.
func sendError(w http.ResponseWriter, r *http.Request, err error) {
	entry := classify(err)
	if entry.status >= http.StatusInternalServerError {
		logRequestError(r, entry, err)
	}
	body := &ErrorBody{Code: entry.code, Message: entry.message, Details: entry.details}
	writeJSON(w, r, entry.status, Envelope{Success: false, Error: body})
}

// sendFieldError reports a validated request-field problem with its fix hint.
func sendFieldError(w http.ResponseWriter, r *http.Request, e *FieldError) {
	sendError(w, r, e)
}

// sendRaw writes a pre-encoded document (used by the OpenAPI route).
func sendRaw(w http.ResponseWriter, status int, contentType string, doc []byte) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(doc)
}

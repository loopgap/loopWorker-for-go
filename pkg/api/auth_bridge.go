package api

import (
	"net/http"
)

// failureWriter adapts the envelope to security.AuthFailureWriter so 401/403
// responses from middleware are shaped exactly like handler errors.
func failureWriter(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	body := &ErrorBody{Code: ErrorCode(code), Message: message, Details: details}
	writeJSON(w, r, status, Envelope{Success: false, Error: body})
}

// writeFailure emits an authorization rejection in the standard envelope.
func (s *APIServer) writeFailure(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	if s.auth != nil {
		s.auth.WriteFailure(w, r, status, code, message, details)
		return
	}
	body := &ErrorBody{Code: ErrorCode(code), Message: message, Details: details}
	writeJSON(w, r, status, Envelope{Success: false, Error: body})
}

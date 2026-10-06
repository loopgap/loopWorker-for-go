package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// decodeJSON reads and validates a JSON object body. Unknown fields are rejected
// with the accepted field list so a typo never turns into a silent no-op.
func decodeJSON(r *http.Request, target any, accepted []string, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	limited := io.LimitReader(r.Body, maxBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return newFieldError("body", "the request body could not be read: "+err.Error(),
			"send a complete body; Content-Length must match the bytes sent", CodeRequestTooLarge, http.StatusRequestEntityTooLarge)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return newFieldError("body", "no request body was provided",
			"send a JSON object such as {\"type\":\"echo\",\"input\":\"hello\"}", CodeMalformedJSON, http.StatusBadRequest)
	}
	if !strings.HasPrefix(trimmed, "{") {
		return newFieldError("body", "the body must be a single JSON object",
			"wrap the payload in { }; arrays and bare strings are not accepted here", CodeMalformedJSON, http.StatusBadRequest)
	}

	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return describeJSONError(err, accepted)
	}
	return nil
}

// describeJSONError converts stdlib JSON errors into cause+fix field errors.
func describeJSONError(err error, accepted []string) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unknown field"):
		name := extractBetween(msg, "unknown field ", "")
		fix := "remove or rename this key"
		details := map[string]any{}
		if len(accepted) > 0 {
			suggest := closestField(name, accepted)
			if suggest != "" {
				fix = "did you mean \"" + suggest + "\"?"
			}
			details["accepted_fields"] = accepted
		}
		return &FieldError{
			Field:   name,
			Reason:  "the field \"" + name + "\" is not part of this endpoint's contract",
			Fix:     fix,
			Status:  http.StatusBadRequest,
			Code:    CodeUnknownField,
			Details: details,
			Cause:   err,
		}
	case strings.Contains(msg, "cannot unmarshal"):
		return &FieldError{
			Field:  "body",
			Reason: strings.TrimPrefix(msg, "json: cannot unmarshal "),
			Fix:    "match the types declared in GET /api/v1/openapi.json",
			Status: http.StatusBadRequest,
			Code:   CodeInvalidRequest,
			Cause:  err,
		}
	default:
		return &FieldError{
			Field:  "body",
			Reason: "the body is not valid JSON (" + msg + ")",
			Fix:    "send one JSON object with double-quoted keys and no trailing commas",
			Status: http.StatusBadRequest,
			Code:   CodeMalformedJSON,
			Cause:  err,
		}
	}
}

func extractBetween(msg, prefix, suffix string) string {
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return "body"
	}
	rest := msg[idx+len(prefix):]
	if suffix != "" {
		if cut := strings.Index(rest, suffix); cut >= 0 {
			rest = rest[:cut]
		}
	}
	return strings.Trim(rest, "\" ")
}

// closestField offers the nearest accepted field name for a typo.
func closestField(given string, accepted []string) string {
	given = strings.ToLower(strings.TrimSpace(given))
	if given == "" {
		return ""
	}
	best, bestScore := "", 0
	for _, cand := range accepted {
		score := similarity(given, strings.ToLower(cand))
		if score > bestScore {
			best, bestScore = cand, score
		}
	}
	if bestScore < 3 {
		return ""
	}
	return best
}

func similarity(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return 0
	}
	common := 0
	for i := 0; i < min(la, lb); i++ {
		if a[i] == b[i] {
			common++
		}
	}
	score := 2 * common / (la + lb) * 10
	if score < 1 {
		return 0
	}
	return score
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

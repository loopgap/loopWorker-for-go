package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"loopworker/internal/core/scheduler"
)

// OwnerMetadataKey records the principal that created a task. It is persisted in
// the existing task metadata column, so tenancy works before the scheduler gains
// a first-class owner field.
const OwnerMetadataKey = "lw_owner"

// CreateTaskRequest is the body of POST /api/v1/tasks.
type CreateTaskRequest struct {
	Type          string                 `json:"type"`
	Config        map[string]any         `json:"config,omitempty"`
	Metadata      map[string]string      `json:"metadata,omitempty"`
	Priority      *int                   `json:"priority,omitempty"`
	Input         json.RawMessage        `json:"input,omitempty"`
	InputText     string                 `json:"input_text,omitempty"`
	InputB64      string                 `json:"input_b64,omitempty"`
	InputEncoding string                 `json:"input_encoding,omitempty"`
	IsAgent       bool                   `json:"is_agent,omitempty"`
	AgentConfig   *scheduler.AgentConfig `json:"agent_config,omitempty"`
}

// CreateTaskFields is the accepted key list reported in unknown-field errors.
var CreateTaskFields = []string{
	"type", "config", "metadata", "priority",
	"input", "input_text", "input_b64", "input_encoding",
	"is_agent", "agent_config",
}

// validate checks the shape of the request before any storage work happens.
func (req *CreateTaskRequest) validate(cfg Config) error {
	typeName := strings.TrimSpace(req.Type)
	if typeName == "" {
		return &FieldError{
			Field:  "type",
			Reason: "\"type\" is required and must not be blank",
			Fix:    "set \"type\" to a loaded plugin name; the plugin name is the file stem of the .wasm module in the plugins directory",
			Status: http.StatusBadRequest,
			Code:   CodeInvalidRequest,
		}
	}
	if len(typeName) > 255 {
		return &FieldError{
			Field:   "type",
			Reason:  "\"type\" is " + itoa(len(typeName)) + " characters, over the 255 limit",
			Fix:     "shorten the task type name",
			Status:  http.StatusBadRequest,
			Code:    CodeInvalidRequest,
			Details: map[string]any{"max_length": 255},
		}
	}
	if !utf8.ValidString(typeName) {
		return &FieldError{Field: "type", Reason: "\"type\" contains invalid UTF-8",
			Fix: "use an ASCII plugin name", Status: http.StatusBadRequest, Code: CodeInvalidRequest}
	}
	switch strings.ToLower(strings.TrimSpace(req.InputEncoding)) {
	case "", "text", "base64":
	default:
		return &FieldError{
			Field:   "input_encoding",
			Reason:  "\"" + req.InputEncoding + "\" is not a known encoding",
			Fix:     "omit input_encoding (plain text is the default) or set it to \"base64\"; alternatively use input_text or input_b64",
			Status:  http.StatusBadRequest,
			Code:    CodeInputEncodingInvalid,
			Details: map[string]any{"allowed": []string{"text", "base64"}},
		}
	}
	if req.Priority != nil && (*req.Priority < 0 || *req.Priority > 3) {
		return &FieldError{
			Field:   "priority",
			Reason:  itoa(*req.Priority) + " is outside the supported range",
			Fix:     "use 0=low, 1=normal (default), 2=high, 3=critical",
			Status:  http.StatusBadRequest,
			Code:    CodeInvalidRequest,
			Details: map[string]any{"min": 0, "max": 3},
		}
	}
	for key := range req.Metadata {
		if key == OwnerMetadataKey {
			return &FieldError{
				Field:  "metadata." + OwnerMetadataKey,
				Reason: "the owner field is server-assigned and cannot be set by clients",
				Fix:    "drop \"" + OwnerMetadataKey + "\" from metadata; it is filled from your credential",
				Status: http.StatusForbidden,
				Code:   CodeForbidden,
			}
		}
	}
	return nil
}

// resolveInput turns the flexible input fields into bytes. Text is the default
// path, which is why the documented example {"input":"Hello, World!"} works.
func (req *CreateTaskRequest) resolveInput(cfg Config) ([]byte, error) {
	maxInput := cfg.MaxInputBytes
	if maxInput <= 0 {
		maxInput = DefaultMaxInputBytes
	}

	tooLarge := func(n int, field string) error {
		return &FieldError{
			Field:   field,
			Reason:  "the encoded input is " + itoa(n) + " bytes, over the " + itoa64(maxInput) + " byte limit",
			Fix:     "store the blob in object storage and reference it from \"config\"; the input ceiling is not configurable, so reducing the payload is the only lever",
			Status:  http.StatusRequestEntityTooLarge,
			Code:    CodeRequestTooLarge,
			Details: map[string]any{"limit_bytes": maxInput},
		}
	}

	if s := strings.TrimSpace(req.InputB64); s != "" {
		raw, err := decodeBase64Loose(s)
		if err != nil {
			return nil, &FieldError{
				Field:  "input_b64",
				Reason: "input_b64 is not valid base64: " + err.Error(),
				Fix:    "encode the bytes with standard base64 (RFC 4648) including padding, e.g. `printf '%s' text | base64`",
				Status: http.StatusBadRequest,
				Code:   CodeInputEncodingInvalid,
			}
		}
		if len(raw) > int(maxInput) {
			return nil, tooLarge(len(raw), "input_b64")
		}
		return raw, nil
	}

	if req.InputText != "" {
		if len(req.InputText) > int(maxInput) {
			return nil, tooLarge(len(req.InputText), "input_text")
		}
		return []byte(req.InputText), nil
	}

	if len(req.Input) == 0 {
		return nil, nil
	}

	trimmed := strings.TrimSpace(string(req.Input))
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if len(trimmed) > int(maxInput) {
			return nil, tooLarge(len(trimmed), "input")
		}
		return []byte(trimmed), nil
	}

	var text string
	if err := json.Unmarshal(req.Input, &text); err != nil {
		return nil, &FieldError{
			Field:  "input",
			Reason: "input must be a string, an object or an array",
			Fix:    "send \"input\": \"plain text\", or \"input_b64\" for binary payloads, or an object for structured plugin arguments",
			Status: http.StatusBadRequest,
			Code:   CodeInvalidRequest,
		}
	}

	if strings.EqualFold(strings.TrimSpace(req.InputEncoding), "base64") {
		raw, err := decodeBase64Loose(text)
		if err != nil {
			return nil, &FieldError{
				Field:  "input",
				Reason: "input_encoding is \"base64\" but input is not valid base64: " + err.Error(),
				Fix:    "either base64-encode the payload (RFC 4648, padded) or drop input_encoding to send plain text",
				Status: http.StatusBadRequest,
				Code:   CodeInputEncodingInvalid,
			}
		}
		if len(raw) > int(maxInput) {
			return nil, tooLarge(len(raw), "input")
		}
		return raw, nil
	}

	if len(text) > int(maxInput) {
		return nil, tooLarge(len(text), "input")
	}
	return []byte(text), nil
}

func decodeBase64Loose(s string) ([]byte, error) {
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func isUTF8(raw []byte) bool { return utf8.Valid(raw) }

func base64Std(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

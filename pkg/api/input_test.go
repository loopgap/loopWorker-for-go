package api

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// TestB8ThreeInputShapes is the SPEC 10-B8 acceptance test. The old handler took
// `Input []byte`, so every documented example had to be base64 and plain text
// was rejected.
func TestB8ThreeInputShapes(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantInput string
		// wantEncoding is the declared encoding of the round-tripped input;
		// empty means "text for a UTF-8 payload, absent for no payload".
		wantEncoding string
	}{
		{
			name:         "input as a plain string",
			body:         `{"type":"t","input":"Hello, World!"}`,
			wantInput:    "Hello, World!",
			wantEncoding: "text",
		},
		{
			name:         "input_text explicitly",
			body:         `{"type":"t","input_text":"Hello, World!"}`,
			wantInput:    "Hello, World!",
			wantEncoding: "text",
		},
		{
			name: "input_b64 for binary payloads",
			body: `{"type":"t","input_b64":"` + base64.StdEncoding.EncodeToString([]byte{0x00, 0xff, 0xfe}) + `"}`,
			// Binary bytes come back base64-encoded with the encoding declared;
			// TestB8BinaryInputRoundTripsAsBase64 decodes them.
			wantInput:    base64.StdEncoding.EncodeToString([]byte{0x00, 0xff, 0xfe}),
			wantEncoding: "base64",
		},
		{
			name:         "input as a JSON object",
			body:         `{"type":"t","input":{"query":"hello","top_k":3}}`,
			wantInput:    `{"query":"hello","top_k":3}`,
			wantEncoding: "text",
		},
		{
			name:         "input as a JSON array",
			body:         `{"type":"t","input":[1,2,3]}`,
			wantInput:    `[1,2,3]`,
			wantEncoding: "text",
		},
		{
			name:         "input as base64 text via input_encoding",
			body:         `{"type":"t","input":"` + base64.StdEncoding.EncodeToString([]byte("decoded payload")) + `","input_encoding":"base64"}`,
			wantInput:    "decoded payload",
			wantEncoding: "text",
		},
		{
			name: "no input at all",
			body: `{"type":"t"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", tc.body)
			env.expectOK(w, http.StatusCreated)

			id, _ := env.data(w)["id"].(string)
			got := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, "")
			env.expectOK(got, http.StatusOK)
			view := env.data(got)

			if tc.wantInput != "" && view["input"] != tc.wantInput {
				t.Errorf("input: want %q, got %q", tc.wantInput, view["input"])
			}
			// The encoding must be declared so a client can decode the payload.
			if tc.wantEncoding == "" {
				if v, present := view["input"]; present && v != "" {
					t.Errorf("a task with no input must not carry one, got %v", v)
				}
			} else if view["input_encoding"] != tc.wantEncoding {
				t.Errorf("input_encoding: want %q, got %v", tc.wantEncoding, view["input_encoding"])
			}
		})
	}
}

func TestB8BinaryInputRoundTripsAsBase64(t *testing.T) {
	env := newTestEnv(t)
	raw := []byte{0x00, 0x01, 0xff, 0xfe, 0x7f}
	encoded := base64.StdEncoding.EncodeToString(raw)

	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"t","input_b64":"`+encoded+`"}`)
	env.expectOK(w, http.StatusCreated)

	id, _ := env.data(w)["id"].(string)
	got := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, "")
	env.expectOK(got, http.StatusOK)
	view := env.data(got)

	if view["input_encoding"] != "base64" {
		t.Fatalf("binary input must be declared as base64, got %v", view["input_encoding"])
	}
	decoded, err := base64.StdEncoding.DecodeString(view["input"].(string))
	if err != nil {
		t.Fatalf("client cannot decode the payload: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Errorf("round trip changed the bytes: want %v, got %v", raw, decoded)
	}
}

// TestB8InvalidBase64Is400WithACode is the failure half of B8.
func TestB8InvalidBase64Is400WithACode(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		name, body string
	}{
		{"input_b64 is not base64", `{"type":"t","input_b64":"!!!not base64!!!"}`},
		{"input_encoding base64 with text", `{"type":"t","input":"plain text","input_encoding":"base64"}`},
		{"unknown encoding name", `{"type":"t","input":"x","input_encoding":"rot13"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", tc.body)
			env.expectCode(w, http.StatusBadRequest, CodeInputEncodingInvalid)
			envW := decodeEnvelope(t, w)
			if !strings.Contains(envW.Error.Message, "Fix:") {
				t.Errorf("must carry a remedy: %q", envW.Error.Message)
			}
		})
	}
}

// TestB8OversizedInputIs413 proves the input ceiling is enforced with a code
// rather than accepted and stored.
func TestB8OversizedInputIs413(t *testing.T) {
	env := newTestEnv(t, func(cfg *Config) { cfg.MaxInputBytes = 1024 })

	huge := strings.Repeat("x", 4096)
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"t","input_text":"`+huge+`"}`)
	env.expectCode(w, http.StatusRequestEntityTooLarge, CodeRequestTooLarge)
}

// TestB8InputPriority proves the documented precedence: input_b64 wins over
// input_text wins over input.
func TestB8InputPriority(t *testing.T) {
	cfg := DefaultConfig()
	req := CreateTaskRequest{
		Input:     []byte(`"from-input"`),
		InputText: "from-input-text",
		InputB64:  base64.StdEncoding.EncodeToString([]byte("from-input-b64")),
	}
	got, err := req.resolveInput(cfg)
	if err != nil {
		t.Fatalf("resolveInput: %v", err)
	}
	if string(got) != "from-input-b64" {
		t.Errorf("input_b64 should win, got %q", got)
	}

	req = CreateTaskRequest{Input: []byte(`"from-input"`), InputText: "from-input-text"}
	got, err = req.resolveInput(cfg)
	if err != nil {
		t.Fatalf("resolveInput: %v", err)
	}
	if string(got) != "from-input-text" {
		t.Errorf("input_text should win over input, got %q", got)
	}
}

// TestB8ValidateRejectsBadRequests covers the field-level validation the input
// path depends on.
func TestB8ValidateRejectsBadRequests(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		name       string
		req        CreateTaskRequest
		wantStatus int
		wantCode   ErrorCode
	}{
		{"blank type", CreateTaskRequest{Type: "   "}, http.StatusBadRequest, CodeInvalidRequest},
		{"long type", CreateTaskRequest{Type: strings.Repeat("a", 300)}, http.StatusBadRequest, CodeInvalidRequest},
		{"bad encoding", CreateTaskRequest{Type: "t", InputEncoding: "rot13"}, http.StatusBadRequest, CodeInputEncodingInvalid},
		{"priority too low", CreateTaskRequest{Type: "t", Priority: ptrInt(-1)}, http.StatusBadRequest, CodeInvalidRequest},
		{"priority too high", CreateTaskRequest{Type: "t", Priority: ptrInt(9)}, http.StatusBadRequest, CodeInvalidRequest},
		{"client-set owner", CreateTaskRequest{Type: "t", Metadata: map[string]string{OwnerMetadataKey: "me"}},
			http.StatusForbidden, CodeForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.validate(cfg)
			if err == nil {
				t.Fatal("validate should have rejected this request")
			}
			c := classify(err)
			if c.status != tc.wantStatus || c.code != tc.wantCode {
				t.Errorf("want %d/%s, got %d/%s (%v)", tc.wantStatus, tc.wantCode, c.status, c.code, err)
			}
			if !strings.Contains(c.message, "Fix:") {
				t.Errorf("message must carry a remedy: %q", c.message)
			}
		})
	}

	// A good request passes.
	good := CreateTaskRequest{Type: "t", Priority: ptrInt(2)}
	if err := good.validate(cfg); err != nil {
		t.Errorf("a valid request was rejected: %v", err)
	}
}

// TestB8AcceptedFieldListIsReported proves a typo yields the accepted keys, so
// a client can self-correct without reading the source.
func TestB8AcceptedFieldListIsReported(t *testing.T) {
	env := newTestEnv(t)

	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks", `{"type":"t","inpt":"typo"}`)
	env.expectCode(w, http.StatusBadRequest, CodeUnknownField)

	envW := decodeEnvelope(t, w)
	accepted, _ := envW.Error.Details["accepted_fields"].([]any)
	if len(accepted) == 0 {
		t.Fatalf("an unknown-field error must list the accepted fields: %s", w.Body.String())
	}
	found := false
	for _, name := range accepted {
		if name == "input" || name == "input_text" {
			found = true
		}
	}
	if !found {
		t.Errorf("the accepted list should mention the input fields, got %v", accepted)
	}
}

// TestConfigCredentialsAreMasked proves secrets in a task config never come back
// out, which is the SPEC 10-B7 "leaks internal detail" concern applied to data.
func TestConfigCredentialsAreMasked(t *testing.T) {
	env := newTestEnv(t)

	id := env.createTask(roleOperator, "secretive", `,"config":{
		"api_key":"lwk_super_secret",
		"db_password":"hunter2",
		"nested":{"access_token":"lwt_abc"},
		"harmless":"visible"
	}`)

	w := env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, "")
	env.expectOK(w, http.StatusOK)
	body := w.Body.String()

	for _, secret := range []string{"lwk_super_secret", "hunter2", "lwt_abc"} {
		if strings.Contains(body, secret) {
			t.Errorf("secret %q leaked through the task view: %s", secret, body)
		}
	}
	if !strings.Contains(body, "visible") {
		t.Errorf("non-sensitive config should still be readable: %s", body)
	}
}

func ptrInt(v int) *int { return &v }

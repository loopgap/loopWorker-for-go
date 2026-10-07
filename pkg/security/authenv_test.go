package security

import (
	"strings"
	"testing"
	"time"
)

// clearAuthEnv empties every variable AuthConfigFromEnv reads, so a test cannot
// pass or fail because of whatever the developer's shell happens to export.
func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvAPIKeys, EnvAPIKeysPlaintext, EnvAuthTokenTTL} {
		t.Setenv(k, "")
	}
}

// digestOf is a syntactically valid at-rest hash. The value is irrelevant: these
// tests are about parsing and validation, not about hashing.
const digestOf = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAuthConfigFromEnvEmpty(t *testing.T) {
	clearAuthEnv(t)

	cfg, err := AuthConfigFromEnv()
	if err != nil {
		t.Fatalf("an empty environment is not a configuration error: %v", err)
	}
	if len(cfg.Keys) != 0 {
		t.Errorf("expected no keys, got %d", len(cfg.Keys))
	}
	if cfg.TokenTTL <= 0 {
		t.Errorf("expected the default token TTL to be filled in, got %v", cfg.TokenTTL)
	}
}

func TestAuthConfigFromEnvHashedKey(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(EnvAPIKeys, "ci:operator:"+strings.ToUpper(digestOf))

	cfg, err := AuthConfigFromEnv()
	if err != nil {
		t.Fatalf("a well-formed hashed key must load: %v", err)
	}
	if len(cfg.Keys) != 1 {
		t.Fatalf("expected exactly one key, got %d", len(cfg.Keys))
	}
	k := cfg.Keys[0]
	if k.ID != "ci" || k.Name != "ci" || k.Role != RoleOperator {
		t.Errorf("got id=%q name=%q role=%q, want ci/ci/%s", k.ID, k.Name, k.Role, RoleOperator)
	}
	if k.KeyHash != digestOf {
		t.Errorf("the digest must be normalised to lower case, got %q", k.KeyHash)
	}
}

func TestAuthConfigFromEnvMultipleEntriesAndSpacing(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(EnvAPIKeys, " a : admin : "+digestOf+" , b:viewer:"+digestOf)

	cfg, err := AuthConfigFromEnv()
	if err != nil {
		t.Fatalf("surrounding spaces must be tolerated: %v", err)
	}
	if len(cfg.Keys) != 2 {
		t.Fatalf("expected two keys, got %d", len(cfg.Keys))
	}
	if cfg.Keys[0].ID != "a" || cfg.Keys[0].Role != RoleAdmin {
		t.Errorf("first key: got %+v", cfg.Keys[0])
	}
	if cfg.Keys[1].ID != "b" || cfg.Keys[1].Role != RoleViewer {
		t.Errorf("second key: got %+v", cfg.Keys[1])
	}
}

// The plaintext variable is hashed on the way in and never retained: that is the
// whole reason it is allowed to exist at all.
func TestAuthConfigFromEnvPlaintextIsHashedImmediately(t *testing.T) {
	clearAuthEnv(t)
	const secret = APIKeyPrefix + "notarealkey"
	t.Setenv(EnvAPIKeysPlaintext, "dev:viewer:"+secret)

	cfg, err := AuthConfigFromEnv()
	if err != nil {
		t.Fatalf("a well-formed plaintext key must load: %v", err)
	}
	if len(cfg.Keys) != 1 {
		t.Fatalf("expected exactly one key, got %d", len(cfg.Keys))
	}
	if cfg.Keys[0].KeyHash != HashAPIKey(secret) {
		t.Errorf("the plaintext key was not hashed on the way in")
	}
	for _, k := range cfg.Keys {
		if strings.Contains(k.KeyHash, secret) || strings.Contains(k.Name, secret) || strings.Contains(k.ID, secret) {
			t.Errorf("plaintext leaked into the config: %+v", k)
		}
	}
}

func TestAuthConfigFromEnvBothVariablesInOrder(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(EnvAPIKeys, "hashed:admin:"+digestOf)
	t.Setenv(EnvAPIKeysPlaintext, "plain:viewer:"+APIKeyPrefix+"x")

	cfg, err := AuthConfigFromEnv()
	if err != nil {
		t.Fatalf("both variables may be set at once: %v", err)
	}
	if len(cfg.Keys) != 2 {
		t.Fatalf("expected two keys, got %d", len(cfg.Keys))
	}
	if cfg.Keys[0].ID != "hashed" || cfg.Keys[1].ID != "plain" {
		t.Errorf("expected hashed keys before plaintext ones, got %+v", cfg.Keys)
	}
}

func TestAuthConfigFromEnvTokenTTL(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(EnvAuthTokenTTL, "90m")

	cfg, err := AuthConfigFromEnv()
	if err != nil {
		t.Fatalf("a valid duration must be accepted: %v", err)
	}
	if cfg.TokenTTL != 90*time.Minute {
		t.Errorf("got TTL %v, want 90m", cfg.TokenTTL)
	}
}

// A rejected credential is the single most common configuration mistake, so the
// message has to name the variable, the offending entry and the way out.
func TestAuthConfigFromEnvRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		value   string
		wantIn  []string
		wantNot []string
	}{
		{
			name: "ttl-not-a-duration", env: EnvAuthTokenTTL, value: "banana",
			wantIn: []string{EnvAuthTokenTTL, "15m"},
		},
		{
			name: "ttl-zero", env: EnvAuthTokenTTL, value: "0s",
			wantIn: []string{EnvAuthTokenTTL},
		},
		{
			name: "ttl-negative", env: EnvAuthTokenTTL, value: "-5m",
			wantIn: []string{EnvAuthTokenTTL},
		},
		{
			name: "too-few-fields", env: EnvAPIKeys, value: "ci:admin",
			wantIn: []string{EnvAPIKeys, "id:role:credential", RoleAdmin},
		},
		{
			name: "too-many-fields", env: EnvAPIKeys, value: "ci:admin:" + digestOf + ":extra",
			wantIn: []string{EnvAPIKeys, "id:role:credential"},
		},
		{
			name: "empty-id", env: EnvAPIKeys, value: ":" + RoleAdmin + ":" + digestOf,
			wantIn: []string{EnvAPIKeys, "empty id or credential"},
		},
		{
			name: "empty-credential", env: EnvAPIKeys, value: "ci:" + RoleAdmin + ":",
			wantIn: []string{EnvAPIKeys, "empty id or credential"},
		},
		{
			name: "unknown-role", env: EnvAPIKeys, value: "ci:root:" + digestOf,
			wantIn: []string{EnvAPIKeys, "root", RoleAdmin, RoleOperator, RoleViewer},
		},
		{
			name: "plaintext-in-hashed-var", env: EnvAPIKeys, value: "ci:admin:" + APIKeyPrefix + "oops",
			wantIn: []string{EnvAPIKeys, "64-char hex", EnvAPIKeysPlaintext},
		},
		{
			name: "digest-too-short", env: EnvAPIKeys, value: "ci:admin:abcdef",
			wantIn: []string{EnvAPIKeys, "64-char hex", EnvAPIKeysPlaintext},
		},
		{
			name: "digest-not-hex", env: EnvAPIKeys, value: "ci:admin:" + strings.Repeat("z", 64),
			wantIn: []string{EnvAPIKeys, "64-char hex", EnvAPIKeysPlaintext},
		},
		{
			name: "empty-entry", env: EnvAPIKeys, value: "ci:admin:" + digestOf + ",",
			wantIn: []string{EnvAPIKeys, "empty entry"},
		},
		{
			name: "malformed-plaintext-entry", env: EnvAPIKeysPlaintext, value: "dev:viewer",
			wantIn: []string{EnvAPIKeysPlaintext, "id:role:credential"},
		},
		{
			name: "plaintext-without-prefix", env: EnvAPIKeysPlaintext, value: "dev:viewer:notaprefixkey",
			wantIn: []string{EnvAPIKeysPlaintext, APIKeyPrefix},
		},
		{
			name: "plaintext-empty-entry", env: EnvAPIKeysPlaintext, value: APIKeyPrefix + "x,,y",
			wantIn: []string{EnvAPIKeysPlaintext, "empty entry"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAuthEnv(t)
			t.Setenv(tc.env, tc.value)

			cfg, err := AuthConfigFromEnv()
			if err == nil {
				t.Fatalf("%s=%q was accepted; it must not be", tc.env, tc.value)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message must mention %q, got:\n%s", want, err)
				}
			}
			for _, unwanted := range tc.wantNot {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("message must not mention %q, got:\n%s", unwanted, err)
				}
			}
			// A rejected configuration must not leave a half-built config that a
			// caller could ignore the error and use.
			if len(cfg.Keys) != 0 {
				t.Errorf("a rejected environment must not yield usable keys, got %+v", cfg.Keys)
			}
		})
	}
}

func TestIsHexDigest(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{digestOf, true},
		{strings.ToUpper(digestOf), true},
		{"0123456789ABCDEF", false}, // right alphabet, wrong length
		{strings.Repeat("g", 64), false},
		{"", false},
		{digestOf + "0", false},
	}
	for _, tc := range cases {
		if got := isHexDigest(tc.in); got != tc.want {
			t.Errorf("isHexDigest(%.12q...) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSplitEnvList(t *testing.T) {
	got, err := splitEnvList("X", "  a , b  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %q, want [a b]", got)
	}

	if got, err := splitEnvList("X", "   "); err != nil || got != nil {
		t.Errorf("an unset variable must yield no entries and no error, got %q, %v", got, err)
	}

	if _, err := splitEnvList("X", "a,,b"); err == nil {
		t.Error("an empty entry in the middle must be rejected, not silently dropped")
	}
}

package security

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// API keys may be supplied without ever writing plaintext to disk:
//
//	LOOPWORKER_API_KEYS=key-id:role:hex-sha256-digest[,...]      (preferred, at-rest hash)
//	LOOPWORKER_API_KEYS_PLAIN=key-id:role:lwk_...[,...]          (dev only, plaintext from env)
//	LOOPWORKER_AUTH_SIGNING_SECRET=<secret>                      (bearer token HMAC key)
//	LOOPWORKER_AUTH_TOKEN_TTL=1h                                 (bearer token lifetime)
const (
	EnvAPIKeys          = "LOOPWORKER_API_KEYS"
	EnvAPIKeysPlaintext = "LOOPWORKER_API_KEYS_PLAIN"
	EnvAuthTokenTTL     = "LOOPWORKER_AUTH_TOKEN_TTL"
)

// AuthConfigFromEnv builds an AuthConfig from the environment. Each entry is
// "id:role:credential"; the credential is a hex SHA-256 digest in
// LOOPWORKER_API_KEYS, or an lwk_ plaintext key in LOOPWORKER_API_KEYS_PLAIN
// which is hashed immediately and never retained.
func AuthConfigFromEnv() (AuthConfig, error) {
	cfg := DefaultAuthConfig()

	if ttl := os.Getenv(EnvAuthTokenTTL); ttl != "" {
		d, err := time.ParseDuration(ttl)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("%s: invalid duration %q (use e.g. 15m, 1h)", EnvAuthTokenTTL, ttl)
		}
		cfg.TokenTTL = d
	}

	entries, err := splitEnvList(EnvAPIKeys, os.Getenv(EnvAPIKeys))
	if err != nil {
		return cfg, err
	}
	for _, e := range entries {
		id, role, cred, err := parseKeyEntry(e, EnvAPIKeys)
		if err != nil {
			return cfg, err
		}
		if !isHexDigest(cred) {
			return cfg, fmt.Errorf("%s: %q credential must be a 64-char hex SHA-256 digest; put plaintext keys in %s instead", EnvAPIKeys, id, EnvAPIKeysPlaintext)
		}
		cfg.Keys = append(cfg.Keys, APIKeyRecord{ID: id, Name: id, Role: role, KeyHash: strings.ToLower(cred)})
	}

	plain, err := splitEnvList(EnvAPIKeysPlaintext, os.Getenv(EnvAPIKeysPlaintext))
	if err != nil {
		return cfg, err
	}
	for _, e := range plain {
		id, role, cred, err := parseKeyEntry(e, EnvAPIKeysPlaintext)
		if err != nil {
			return cfg, err
		}
		if !strings.HasPrefix(cred, APIKeyPrefix) {
			return cfg, fmt.Errorf("%s: %q credential must start with %q", EnvAPIKeysPlaintext, id, APIKeyPrefix)
		}
		cfg.Keys = append(cfg.Keys, APIKeyRecord{ID: id, Name: id, Role: role, KeyHash: HashAPIKey(cred)})
	}

	return cfg, nil
}

func splitEnvList(envName, raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("%s: empty entry (trailing comma?)", envName)
		}
		out = append(out, p)
	}
	return out, nil
}

func parseKeyEntry(entry, envName string) (id, role, cred string, err error) {
	parts := strings.Split(entry, ":")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("%s: entry %q must be id:role:credential (role is one of admin, operator, viewer)", envName, entry)
	}
	id, role, cred = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	if id == "" || cred == "" {
		return "", "", "", fmt.Errorf("%s: entry %q has an empty id or credential", envName, entry)
	}
	if !validRole(role) {
		return "", "", "", fmt.Errorf("%s: entry %q has unknown role %q; valid roles: %s, %s, %s", envName, entry, role, RoleAdmin, RoleOperator, RoleViewer)
	}
	return id, role, cred, nil
}

func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

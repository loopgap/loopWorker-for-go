package api

import "strings"

// sensitiveKeyFragments gate which config/metadata values leave the process.
// Anything matching is replaced with a fixed-length mask.
var sensitiveKeyFragments = []string{
	"password", "passwd", "secret", "token", "api_key", "apikey", "credential",
	"authorization", "access_key", "private_key", "session", "cookie",
}

const redactedPlaceholder = "***redacted***"

// isSensitiveKey reports whether a config/metadata key must be masked.
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, frag := range sensitiveKeyFragments {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

// publicConfig copies a task config, masking credential-like values. Keys are
// preserved so callers can still see what was configured.
func publicConfig(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if isSensitiveKey(k) {
			out[k] = redactedPlaceholder
			continue
		}
		out[k] = publicValue(v)
	}
	return out
}

func publicValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return publicConfig(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, publicValue(item))
		}
		return out
	default:
		return v
	}
}

// publicMetadata copies task metadata, keeping the server-assigned owner out of
// the map (it is surfaced as the dedicated "owner" field) and masking secrets.
func publicMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == OwnerMetadataKey {
			continue
		}
		if isSensitiveKey(k) {
			out[k] = redactedPlaceholder
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

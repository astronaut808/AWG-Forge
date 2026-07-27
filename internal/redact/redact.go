package redact

import (
	"net/url"
	"strings"
)

const maxStringLength = 500

func Fields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	clean := make(map[string]any, len(fields))
	for key, value := range fields {
		clean[key] = Value(key, value)
	}
	return clean
}

func Value(key string, value any) any {
	if SensitiveKey(key) {
		return "<redacted>"
	}
	switch v := value.(type) {
	case string:
		return String(v)
	case error:
		return String(v.Error())
	case map[string]any:
		return Fields(v)
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = String(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Value(key, item)
		}
		return out
	default:
		return value
	}
}

func String(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "vpn://") || strings.Contains(value, "[Interface]") || strings.Contains(value, "PrivateKey") || strings.Contains(value, "PresharedKey") {
		return "<redacted>"
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() && (parsed.User != nil || parsed.RawQuery != "") {
		return "<redacted-url>"
	}
	if len(value) > maxStringLength {
		return value[:maxStringLength] + "..."
	}
	return value
}

func SensitiveKey(key string) bool {
	key = strings.ToLower(key)
	for _, needle := range []string{
		"private", "preshared", "password", "secret", "session", "token",
		"config", "conf", "key", "import", "backup_password", "ciphertext",
		"authorization", "cookie",
	} {
		if strings.Contains(key, needle) {
			return true
		}
	}
	return false
}

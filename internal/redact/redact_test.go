package redact

import (
	"strings"
	"testing"
)

func TestFieldsRedactsSensitiveValues(t *testing.T) {
	fields := Fields(map[string]any{
		"password": "do-not-log",
		"nested": map[string]any{
			"private_key": "also-secret",
		},
		"endpoint":    "https://example.test/api?token=do-not-log",
		"config_text": "[Interface]\nPrivateKey = do-not-log",
	})

	if got := fields["password"]; got != "<redacted>" {
		t.Fatalf("password = %v", got)
	}
	nested := fields["nested"].(map[string]any)
	if got := nested["private_key"]; got != "<redacted>" {
		t.Fatalf("private_key = %v", got)
	}
	if got := fields["endpoint"]; got != "<redacted-url>" {
		t.Fatalf("endpoint = %v", got)
	}
	if got := fields["config_text"]; got != "<redacted>" {
		t.Fatalf("config_text = %v", got)
	}
}

func TestStringTruncatesLongValues(t *testing.T) {
	value := String(strings.Repeat("x", 501))
	if len(value) != 503 || value[len(value)-3:] != "..." {
		t.Fatalf("unexpected truncation: len=%d suffix=%q", len(value), value[len(value)-3:])
	}
}

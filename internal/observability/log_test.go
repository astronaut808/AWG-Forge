package observability

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLoggerWritesStructuredRedactedEvents(t *testing.T) {
	var output bytes.Buffer
	logger := NewWithWriter("debug", &output)
	logger.Info(t.Context(), "tunnel", "tunnel.apply.succeeded", "tunnel applied", map[string]any{
		"interface":   "awg20",
		"private_key": "do-not-log",
		"endpoint":    "https://example.test/path?token=do-not-log",
	})

	line := output.String()
	if strings.Contains(line, "do-not-log") {
		t.Fatalf("runtime log leaked a secret: %s", line)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("parse JSON log: %v", err)
	}
	if event["event"] != "tunnel.apply.succeeded" || event["component"] != "tunnel" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event["private_key"] != "<redacted>" || event["endpoint"] != "<redacted-url>" {
		t.Fatalf("unexpected sanitized fields: %#v", event)
	}
}

func TestLoggerHonorsLevel(t *testing.T) {
	var output bytes.Buffer
	logger := NewWithWriter("info", &output)
	logger.Debug(t.Context(), "runtime", "runtime.reconcile", "reconciliation started", nil)
	if output.Len() != 0 {
		t.Fatalf("debug event was emitted at info level: %s", output.String())
	}
}

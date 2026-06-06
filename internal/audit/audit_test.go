package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestFileLoggerWritesRedactedJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	logger := New(config.Config{
		AuditLogEnabled:  true,
		AuditLogPath:     path,
		AuditLogMaxSize:  DefaultMaxSize,
		AuditLogMaxFiles: DefaultMaxFiles,
	})

	logger.Log(context.Background(), Event{
		Level:   "info",
		Event:   "client.created",
		Message: "ok",
		Fields: map[string]any{
			"client_name":   "phone",
			"private_key":   "secret-private",
			"preshared_key": "secret-psk",
			"import_key":    "vpn://secret",
		},
	})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"secret-private", "secret-psk", "vpn://secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("audit log contains secret %q: %s", secret, text)
		}
	}
	events, err := ReadFile(path, ReadOptions{Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Fields["client_name"] != "phone" {
		t.Fatalf("client_name was not preserved: %#v", events[0].Fields)
	}
}

func TestReadFiltersAndTails(t *testing.T) {
	reader := strings.NewReader(strings.Join([]string{
		`{"time":"2026-06-06T00:00:00Z","level":"info","event":"one"}`,
		`{"time":"2026-06-06T00:00:01Z","level":"warn","event":"two"}`,
		`{"time":"2026-06-06T00:00:02Z","level":"warn","event":"three"}`,
	}, "\n"))

	events, err := Read(reader, ReadOptions{Tail: 1, Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "three" {
		t.Fatalf("events = %#v, want last warn event", events)
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	logger := New(config.Config{
		AuditLogEnabled:  true,
		AuditLogPath:     path,
		AuditLogMaxSize:  120,
		AuditLogMaxFiles: 2,
	})
	for i := 0; i < 5; i++ {
		logger.Log(context.Background(), Event{Level: "info", Event: "event", Message: strings.Repeat("x", 80)})
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
}

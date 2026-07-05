package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/sqldb"
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

func TestDatabaseLoggerWritesAndReadsAuditEvents(t *testing.T) {
	cfg := testDBConfig(t)
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	logger := New(cfg)
	logger.Log(context.Background(), Event{
		Level:   "warn",
		Event:   "client.created",
		Message: "created",
		Fields: map[string]any{
			"client_name": "phone",
			"private_key": "secret-private",
		},
	})
	events, err := ReadConfigured(context.Background(), cfg, ReadOptions{Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Event != "client.created" || events[0].Level != "warn" {
		t.Fatalf("unexpected event: %#v", events[0])
	}
	if events[0].Fields["client_name"] != "phone" {
		t.Fatalf("client_name was not preserved: %#v", events[0].Fields)
	}
	if events[0].Fields["private_key"] != "<redacted>" {
		t.Fatalf("private key field was not redacted: %#v", events[0].Fields)
	}
}

func TestReadConfiguredFallsBackToFileWhenDatabaseMissing(t *testing.T) {
	cfg := testDBConfig(t)
	logger := New(cfg)
	logger.Log(context.Background(), Event{Level: "info", Event: "fallback.file", Message: "ok"})
	events, err := ReadConfigured(context.Background(), cfg, ReadOptions{Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "fallback.file" {
		t.Fatalf("events = %#v, want fallback file event", events)
	}
	if _, err := os.Stat(cfg.DatabasePath); !os.IsNotExist(err) {
		t.Fatalf("logging before migrate created database or unexpected stat error: %v", err)
	}
}

func TestReadConfiguredMergesDatabaseAndJSONL(t *testing.T) {
	cfg := testDBConfig(t)
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	logger := New(cfg)
	logger.Log(context.Background(), Event{
		Time:    time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
		Level:   "info",
		Event:   "db.and.file",
		Message: "both",
	})
	fileOnly := &fileLogger{path: cfg.AuditLogPath, maxSize: DefaultMaxSize, maxFiles: DefaultMaxFiles}
	fileOnly.Log(context.Background(), Event{
		Time:    time.Date(2026, 7, 5, 10, 0, 1, 0, time.UTC),
		Level:   "warn",
		Event:   "file.only",
		Message: "file",
	})
	events, err := ReadConfigured(context.Background(), cfg, ReadOptions{Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2: %#v", len(events), events)
	}
	if events[0].Event != "db.and.file" || events[1].Event != "file.only" {
		t.Fatalf("unexpected merged events: %#v", events)
	}
}

func testDBConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		ConfigDir:            dir,
		AuditLogEnabled:      true,
		AuditLogPath:         filepath.Join(dir, "audit.log"),
		AuditLogMaxSize:      DefaultMaxSize,
		AuditLogMaxFiles:     DefaultMaxFiles,
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseRetention:    90,
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
}

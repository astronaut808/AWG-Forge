package sqldb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestApplyRetentionDeletesExpiredRows(t *testing.T) {
	now := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	insertRetentionFixture(t, db, now.AddDate(0, 0, -100), now.AddDate(0, 0, -1))
	report, err := db.ApplyRetention(context.Background(), cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedAuditEvents != 1 {
		t.Fatalf("DeletedAuditEvents = %d, want 1", report.DeletedAuditEvents)
	}
	if report.DeletedTrafficSamples != 1 {
		t.Fatalf("DeletedTrafficSamples = %d, want 1", report.DeletedTrafficSamples)
	}
	if got := countRows(t, db, "SELECT count(*) FROM audit_events"); got != 1 {
		t.Fatalf("audit_events rows = %d, want 1", got)
	}
	if got := countRows(t, db, "SELECT count(*) FROM traffic_samples"); got != 1 {
		t.Fatalf("traffic_samples rows = %d, want 1", got)
	}
}

func retentionTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		ConfigDir:            dir,
		DatabaseMode:         ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseRetention:    90,
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
}

func insertRetentionFixture(t *testing.T, db *DB, oldTime, freshTime time.Time) {
	t.Helper()
	for _, ts := range []time.Time{oldTime, freshTime} {
		if _, err := db.sql.ExecContext(context.Background(),
			"INSERT INTO audit_events (time, level, event) VALUES (?, ?, ?)",
			ts.Format(time.RFC3339Nano), "info", "test.event",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.ExecContext(context.Background(),
			"INSERT INTO traffic_samples (sampled_at, tunnel_id, client_id, rx_bytes, tx_bytes, present) VALUES (?, ?, ?, ?, ?, ?)",
			ts.Format(time.RFC3339Nano), "tunnel", "client", 1, 2, 1,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func countRows(t *testing.T, db *DB, query string) int {
	t.Helper()
	var count int
	if err := db.sql.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

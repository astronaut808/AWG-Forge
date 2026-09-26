package sqldb_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/sqldb"
)

func TestSnapshotIntoCapturesSQLiteWithoutWALCopy(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := db.SnapshotInto(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := sqldb.VerifySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot database is empty")
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot mode = %o, want 0600", info.Mode().Perm())
	}
	if err := db.SnapshotInto(ctx, snapshot); err == nil {
		t.Fatal("existing snapshot destination was overwritten")
	}
}

func TestSnapshotIntoWithConcurrentWALWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := testConfig(t)
	reader, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if err := reader.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	started := make(chan error, 1)
	finished := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			err := writer.AppendAuditEvent(ctx, sqldb.AuditEvent{Level: "info", Event: "snapshot.concurrent", Message: "test"})
			if i == 0 {
				started <- err
			}
			if err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "concurrent.db")
	if err := reader.SnapshotInto(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := sqldb.VerifySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
}

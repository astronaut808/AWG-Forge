package sqldb_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

func TestCheckDisabled(t *testing.T) {
	status, err := sqldb.Check(context.Background(), config.Config{DatabaseMode: sqldb.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatal("disabled database reported enabled")
	}
}

func TestCheckZeroConfigIsDisabled(t *testing.T) {
	status, err := sqldb.Check(context.Background(), config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatal("zero config database reported enabled")
	}
}

func TestOpenDisabled(t *testing.T) {
	_, err := sqldb.Open(context.Background(), config.Config{DatabaseMode: sqldb.ModeOff})
	if !errors.Is(err, sqldb.ErrDisabled) {
		t.Fatalf("Open disabled error = %v", err)
	}
}

func TestMigrateCreatesSQLiteSchema(t *testing.T) {
	cfg := testConfig(t)
	status, err := sqldb.Migrate(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists {
		t.Fatal("database file was not created")
	}
	if status.SchemaVersion != 2 {
		t.Fatalf("schema version = %d, want 2", status.SchemaVersion)
	}
	if status.JournalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", status.JournalMode)
	}
	if status.ForeignKeys != "1" {
		t.Fatalf("foreign keys = %q, want 1", status.ForeignKeys)
	}
	assertMode(t, cfg.DatabasePath, 0600)

	status, err = sqldb.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 2 {
		t.Fatalf("checked schema version = %d, want 2", status.SchemaVersion)
	}
}

func TestCheckEnabledMissingDatabaseDoesNotCreateFile(t *testing.T) {
	cfg := testConfig(t)
	status, err := sqldb.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.Exists {
		t.Fatal("missing database reported as existing")
	}
	if _, err := os.Stat(cfg.DatabasePath); !os.IsNotExist(err) {
		t.Fatalf("Check created database or unexpected stat error: %v", err)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		ConfigDir:            dir,
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
		DatabaseRetention:    90,
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

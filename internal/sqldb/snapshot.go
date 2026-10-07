package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// SnapshotInto creates a consistent SQLite snapshot at a new path. The caller
// owns the private staging directory and removes an incomplete file on error.
func (db *DB) SnapshotInto(ctx context.Context, path string) error {
	if db == nil || db.sql == nil {
		return errors.New("database is unavailable")
	}
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("snapshot path must be absolute")
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("snapshot destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect snapshot destination: %w", err)
	}
	if _, err := db.sql.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("protect snapshot database: %w", err)
	}
	if err := VerifySnapshot(ctx, path); err != nil {
		return err
	}
	return nil
}

// VerifySnapshot opens a snapshot read-only and checks its SQLite integrity.
func VerifySnapshot(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect snapshot database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("snapshot database must be a private regular file")
	}
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return fmt.Errorf("open snapshot database: %w", err)
	}
	defer func() { _ = db.Close() }()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("verify snapshot database: %w", err)
	}
	if !strings.EqualFold(result, "ok") {
		return errors.New("snapshot database failed integrity check")
	}
	return nil
}

// VerifyControllerSnapshot also checks that the snapshot contains initialized
// controller authentication data, rather than only a structurally valid DB.
func VerifyControllerSnapshot(ctx context.Context, path string) error {
	if err := VerifySnapshot(ctx, path); err != nil {
		return err
	}
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	conn, err := sql.Open("sqlite", uri)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM controller_users").Scan(&count); err != nil {
		return fmt.Errorf("verify controller users in snapshot: %w", err)
	}
	if count != 1 {
		return errors.New("controller snapshot must contain exactly one administrator")
	}
	var version int
	if err := conn.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil {
		return fmt.Errorf("verify controller snapshot schema: %w", err)
	}
	if version < 5 || version > CurrentSchemaVersion {
		return fmt.Errorf("controller snapshot schema %d is outside supported range 5..%d", version, CurrentSchemaVersion)
	}
	return nil
}

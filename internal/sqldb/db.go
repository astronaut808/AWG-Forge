package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"

	// Register the CGo-free SQLite database/sql driver.
	_ "modernc.org/sqlite"
)

const (
	ModeOff      = "off"
	ModeSQLite   = "sqlite"
	ModePostgres = "postgres"

	CurrentSchemaVersion = 4
)

var (
	ErrDisabled    = errors.New("database is disabled")
	ErrUnsupported = errors.New("database mode is not supported yet")
)

type DB struct {
	sql  *sql.DB
	path string
}

type Status struct {
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode"`
	Path          string `json:"path,omitempty"`
	Exists        bool   `json:"exists"`
	SchemaVersion int    `json:"schema_version,omitempty"`
	JournalMode   string `json:"journal_mode,omitempty"`
	Synchronous   string `json:"synchronous,omitempty"`
	ForeignKeys   string `json:"foreign_keys,omitempty"`
	BusyTimeout   string `json:"busy_timeout,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	WALSizeBytes  int64  `json:"wal_size_bytes,omitempty"`
}

func Open(ctx context.Context, cfg config.Config) (*DB, error) {
	switch normalizeMode(cfg.DatabaseMode) {
	case ModeOff:
		return nil, ErrDisabled
	case ModeSQLite:
		return openSQLite(ctx, cfg)
	case ModePostgres:
		return nil, fmt.Errorf("%w: postgres", ErrUnsupported)
	default:
		return nil, fmt.Errorf("unknown database mode %q", cfg.DatabaseMode)
	}
}

func Check(ctx context.Context, cfg config.Config) (Status, error) {
	status := Status{
		Enabled: normalizeMode(cfg.DatabaseMode) != ModeOff,
		Mode:    normalizeMode(cfg.DatabaseMode),
		Path:    cfg.DatabasePath,
	}
	switch normalizeMode(cfg.DatabaseMode) {
	case ModeOff:
		return status, nil
	case ModePostgres:
		return status, fmt.Errorf("%w: postgres", ErrUnsupported)
	}
	info, err := os.Stat(cfg.DatabasePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return status, nil
		}
		return status, err
	}
	status.Exists = true
	status.SizeBytes = info.Size()
	if wal, err := os.Stat(cfg.DatabasePath + "-wal"); err == nil {
		status.WALSizeBytes = wal.Size()
	}
	db, err := openSQLite(ctx, cfg)
	if err != nil {
		return status, err
	}
	defer func() { _ = db.Close() }()
	if err := db.fillStatus(ctx, &status); err != nil {
		return status, err
	}
	return status, nil
}

func normalizeMode(mode string) string {
	if mode == "" {
		return ModeOff
	}
	return mode
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

func openSQLite(ctx context.Context, cfg config.Config) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(cfg.DatabasePath), 0700); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("sqlite", cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.DatabaseMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DatabaseMaxIdleConns)
	sqlDB.SetConnMaxLifetime(0)

	db := &DB{sql: sqlDB, path: cfg.DatabasePath}
	if err := db.configure(ctx, cfg); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := chmodIfExists(cfg.DatabasePath, 0600); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) configure(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.DatabaseQueryTimeout)
	defer cancel()
	if _, err := db.sql.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("set sqlite journal_mode: %w", err)
	}
	pragmas := []string{
		"PRAGMA synchronous=NORMAL",
		fmt.Sprintf("PRAGMA busy_timeout=%d", int(cfg.DatabaseBusyTimeout/time.Millisecond)),
		"PRAGMA foreign_keys=ON",
		"PRAGMA temp_store=MEMORY",
	}
	for _, pragma := range pragmas {
		if _, err := db.sql.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("set sqlite pragma %q: %w", pragma, err)
		}
	}
	if err := db.sql.PingContext(ctx); err != nil {
		return err
	}
	return nil
}

func (db *DB) fillStatus(ctx context.Context, status *Status) error {
	if err := db.sql.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&status.JournalMode); err != nil {
		return err
	}
	if err := db.sql.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&status.Synchronous); err != nil {
		return err
	}
	if err := db.sql.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&status.ForeignKeys); err != nil {
		return err
	}
	if err := db.sql.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&status.BusyTimeout); err != nil {
		return err
	}
	version, err := db.schemaVersion(ctx)
	if err != nil {
		return err
	}
	status.SchemaVersion = version
	return nil
}

func (db *DB) schemaVersion(ctx context.Context) (int, error) {
	var exists int
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", "schema_migrations").Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var version sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func chmodIfExists(path string, mode os.FileMode) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.Chmod(path, mode)
}

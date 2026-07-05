package sqldb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

//go:embed migrations/sqlite/*.sql
var migrationsFS embed.FS

type migration struct {
	version  int
	name     string
	path     string
	sql      string
	checksum string
}

func Migrate(ctx context.Context, cfg config.Config) (Status, error) {
	db, err := Open(ctx, cfg)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return Status{}, err
	}
	status := Status{Enabled: true, Mode: cfg.DatabaseMode, Path: cfg.DatabasePath, Exists: true}
	if err := db.fillStatus(ctx, &status); err != nil {
		return status, err
	}
	return status, nil
}

func (db *DB) Migrate(ctx context.Context) error {
	migrations, err := loadSQLiteMigrations()
	if err != nil {
		return err
	}
	if _, err := db.sql.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	for _, migration := range migrations {
		applied, err := db.appliedMigration(ctx, migration.version)
		if err != nil {
			return err
		}
		if applied != "" {
			if applied != migration.checksum {
				return fmt.Errorf("migration %06d checksum mismatch", migration.version)
			}
			continue
		}
		if err := db.applyMigration(ctx, migration); err != nil {
			return err
		}
	}
	return chmodIfExists(db.path, 0600)
}

func (db *DB) appliedMigration(ctx context.Context, version int) (string, error) {
	var checksum string
	err := db.sql.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = ?", version).Scan(&checksum)
	if err == nil {
		return checksum, nil
	}
	if err == sql.ErrNoRows {
		return "", nil
	}
	return "", err
}

func (db *DB) applyMigration(ctx context.Context, migration migration) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range splitStatements(migration.sql) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?, ?, datetime('now'))", migration.version, migration.checksum); err != nil {
		return err
	}
	return tx.Commit()
}

func loadSQLiteMigrations() ([]migration, error) {
	paths, err := fs.Glob(migrationsFS, "migrations/sqlite/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	migrations := make([]migration, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid migration name %q", base)
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid migration version %q: %w", base, err)
		}
		body, err := migrationsFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		migrations = append(migrations, migration{
			version:  version,
			name:     base,
			path:     path,
			sql:      string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	return migrations, nil
}

func splitStatements(sqlText string) []string {
	raw := strings.Split(sqlText, ";")
	statements := make([]string, 0, len(raw))
	for _, statement := range raw {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

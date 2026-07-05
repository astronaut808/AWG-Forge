package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

type AuditEvent struct {
	Time       time.Time
	Level      string
	Event      string
	Message    string
	FieldsJSON string
	Error      string
	RequestID  string
}

type AuditFilter struct {
	Tail  int
	Level string
	Event string
}

func AppendAuditEvent(ctx context.Context, cfg config.Config, event AuditEvent) error {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.AppendAuditEvent(ctx, event)
}

func ListAuditEvents(ctx context.Context, cfg config.Config, filter AuditFilter) ([]AuditEvent, error) {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListAuditEvents(ctx, filter)
}

func (db *DB) AppendAuditEvent(ctx context.Context, event AuditEvent) error {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if strings.TrimSpace(event.FieldsJSON) == "" {
		event.FieldsJSON = "{}"
	}
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO audit_events (time, level, event, message, fields_json, error, request_id)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.Time.UTC().Format(time.RFC3339Nano),
		event.Level,
		event.Event,
		event.Message,
		event.FieldsJSON,
		event.Error,
		event.RequestID,
	)
	return err
}

func (db *DB) ListAuditEvents(ctx context.Context, filter AuditFilter) ([]AuditEvent, error) {
	tail := filter.Tail
	if tail <= 0 {
		tail = 100
	}
	if tail > 1000 {
		tail = 1000
	}
	query := `
SELECT time, level, event, message, fields_json, error, request_id
FROM audit_events`
	var (
		where []string
		args  []any
	)
	if filter.Level != "" {
		where = append(where, "level = ?")
		args = append(args, filter.Level)
	}
	if strings.TrimSpace(filter.Event) != "" {
		where = append(where, "event = ?")
		args = append(args, strings.TrimSpace(filter.Event))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY time DESC, id DESC LIMIT ?"
	args = append(args, tail)
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var newestFirst []AuditEvent
	for rows.Next() {
		var (
			event AuditEvent
			raw   string
		)
		if err := rows.Scan(&raw, &event.Level, &event.Event, &event.Message, &event.FieldsJSON, &event.Error, &event.RequestID); err != nil {
			return nil, err
		}
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			event.Time = ts
		}
		newestFirst = append(newestFirst, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	events := make([]AuditEvent, len(newestFirst))
	for i := range newestFirst {
		events[len(newestFirst)-1-i] = newestFirst[i]
	}
	return events, nil
}

func OpenExisting(ctx context.Context, cfg config.Config) (*DB, error) {
	if normalizeMode(cfg.DatabaseMode) != ModeSQLite {
		return nil, ErrDisabled
	}
	if _, err := os.Stat(cfg.DatabasePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	return openSQLite(ctx, cfg)
}

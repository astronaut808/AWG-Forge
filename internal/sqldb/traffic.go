package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

type TrafficSample struct {
	SampledAt         time.Time
	TunnelID          string
	ClientID          string
	RxBytes           uint64
	TxBytes           uint64
	LatestHandshakeAt time.Time
	Present           bool
}

type TrafficSummaryRow struct {
	TunnelID string `json:"tunnel_id"`
	ClientID string `json:"client_id"`
	RxTotal  uint64 `json:"rx_total"`
	TxTotal  uint64 `json:"tx_total"`
	RxToday  uint64 `json:"rx_today"`
	TxToday  uint64 `json:"tx_today"`
	Rx7d     uint64 `json:"rx_7d"`
	Tx7d     uint64 `json:"tx_7d"`
	Rx30d    uint64 `json:"rx_30d"`
	Tx30d    uint64 `json:"tx_30d"`
}

func RecordTrafficSamples(ctx context.Context, cfg config.Config, samples []TrafficSample) error {
	if len(samples) == 0 {
		return nil
	}
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.RecordTrafficSamples(ctx, samples)
}

func ListTrafficSummary(ctx context.Context, cfg config.Config, now time.Time) ([]TrafficSummaryRow, error) {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListTrafficSummary(ctx, now)
}

func (db *DB) RecordTrafficSamples(ctx context.Context, samples []TrafficSample) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, sample := range samples {
		if sample.SampledAt.IsZero() {
			sample.SampledAt = time.Now().UTC()
		}
		sample.SampledAt = sample.SampledAt.UTC()
		rxBytes, err := sqliteInt(sample.RxBytes)
		if err != nil {
			return err
		}
		txBytes, err := sqliteInt(sample.TxBytes)
		if err != nil {
			return err
		}
		rxDelta, txDelta, err := previousTrafficDelta(ctx, tx, sample)
		if err != nil {
			return err
		}
		rxDeltaValue, err := sqliteInt(rxDelta)
		if err != nil {
			return err
		}
		txDeltaValue, err := sqliteInt(txDelta)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO traffic_samples (sampled_at, tunnel_id, client_id, rx_bytes, tx_bytes, latest_handshake_at, present)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			formatTime(sample.SampledAt),
			sample.TunnelID,
			sample.ClientID,
			rxBytes,
			txBytes,
			formatTime(sample.LatestHandshakeAt),
			boolInt(sample.Present),
		); err != nil {
			return err
		}
		if rxDelta == 0 && txDelta == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO client_traffic_daily (day, tunnel_id, client_id, rx_bytes, tx_bytes, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(day, tunnel_id, client_id) DO UPDATE SET
    rx_bytes = rx_bytes + excluded.rx_bytes,
    tx_bytes = tx_bytes + excluded.tx_bytes,
    updated_at = excluded.updated_at`,
			sample.SampledAt.Format("2006-01-02"),
			sample.TunnelID,
			sample.ClientID,
			rxDeltaValue,
			txDeltaValue,
			formatTime(sample.SampledAt),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func previousTrafficDelta(ctx context.Context, tx *sql.Tx, sample TrafficSample) (uint64, uint64, error) {
	var prevRx, prevTx sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT rx_bytes, tx_bytes
FROM traffic_samples
WHERE tunnel_id = ? AND client_id = ?
ORDER BY sampled_at DESC, id DESC
LIMIT 1`, sample.TunnelID, sample.ClientID).Scan(&prevRx, &prevTx)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return positiveDelta(prevRx, sample.RxBytes), positiveDelta(prevTx, sample.TxBytes), nil
}

func positiveDelta(previous sql.NullInt64, current uint64) uint64 {
	if !previous.Valid || previous.Int64 < 0 {
		return 0
	}
	prev := uint64(previous.Int64)
	if current < prev {
		return 0
	}
	return current - prev
}

func (db *DB) ListTrafficSummary(ctx context.Context, now time.Time) ([]TrafficSummaryRow, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	dayToday := now.UTC().Format("2006-01-02")
	day7d := now.UTC().AddDate(0, 0, -6).Format("2006-01-02")
	day30d := now.UTC().AddDate(0, 0, -29).Format("2006-01-02")
	rows, err := db.sql.QueryContext(ctx, `
SELECT
    tunnel_id,
    client_id,
    COALESCE(SUM(rx_bytes), 0) AS rx_total,
    COALESCE(SUM(tx_bytes), 0) AS tx_total,
    COALESCE(SUM(CASE WHEN day >= ? THEN rx_bytes ELSE 0 END), 0) AS rx_today,
    COALESCE(SUM(CASE WHEN day >= ? THEN tx_bytes ELSE 0 END), 0) AS tx_today,
    COALESCE(SUM(CASE WHEN day >= ? THEN rx_bytes ELSE 0 END), 0) AS rx_7d,
    COALESCE(SUM(CASE WHEN day >= ? THEN tx_bytes ELSE 0 END), 0) AS tx_7d,
    COALESCE(SUM(CASE WHEN day >= ? THEN rx_bytes ELSE 0 END), 0) AS rx_30d,
    COALESCE(SUM(CASE WHEN day >= ? THEN tx_bytes ELSE 0 END), 0) AS tx_30d
FROM client_traffic_daily
GROUP BY tunnel_id, client_id
ORDER BY tunnel_id, client_id`, dayToday, dayToday, day7d, day7d, day30d, day30d)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TrafficSummaryRow
	for rows.Next() {
		var row TrafficSummaryRow
		if err := rows.Scan(&row.TunnelID, &row.ClientID, &row.RxTotal, &row.TxTotal, &row.RxToday, &row.TxToday, &row.Rx7d, &row.Tx7d, &row.Rx30d, &row.Tx30d); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sqliteInt(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, errors.New("traffic counter exceeds sqlite integer range")
	}
	return int64(value), nil
}

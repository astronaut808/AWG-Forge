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
	TunnelID   string  `json:"tunnel_id"`
	ClientID   string  `json:"client_id"`
	RxTotal    uint64  `json:"rx_total"`
	TxTotal    uint64  `json:"tx_total"`
	RxToday    uint64  `json:"rx_today"`
	TxToday    uint64  `json:"tx_today"`
	Rx7d       uint64  `json:"rx_7d"`
	Tx7d       uint64  `json:"tx_7d"`
	Rx30d      uint64  `json:"rx_30d"`
	Tx30d      uint64  `json:"tx_30d"`
	LimitBytes *uint64 `json:"limit_bytes"`
}

type ClientTrafficLimit struct {
	TunnelID   string
	ClientID   string
	LimitBytes uint64
}

type ExceededTrafficLimit struct {
	TunnelID   string
	ClientID   string
	LimitBytes uint64
	TotalBytes uint64
	ExceededAt time.Time
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

func SetClientTrafficLimit(ctx context.Context, cfg config.Config, tunnelID, clientID string, limitBytes *uint64) error {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.SetClientTrafficLimit(ctx, tunnelID, clientID, limitBytes)
}

func ListClientTrafficLimits(ctx context.Context, cfg config.Config) ([]ClientTrafficLimit, error) {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListClientTrafficLimits(ctx)
}

func ListExceededTrafficLimits(ctx context.Context, cfg config.Config, now time.Time) ([]ExceededTrafficLimit, error) {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListExceededTrafficLimits(ctx, now)
}

func DeleteClientTrafficLimit(ctx context.Context, cfg config.Config, clientID string) error {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.DeleteClientTrafficLimit(ctx, clientID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	limits, err := db.ListClientTrafficLimits(ctx)
	if err != nil {
		return nil, err
	}
	byClient := make(map[string]uint64, len(limits))
	for _, limit := range limits {
		byClient[limit.TunnelID+"\x00"+limit.ClientID] = limit.LimitBytes
	}
	for i := range out {
		if limit, ok := byClient[out[i].TunnelID+"\x00"+out[i].ClientID]; ok {
			out[i].LimitBytes = uint64Ptr(limit)
		}
	}
	return out, nil
}

func (db *DB) SetClientTrafficLimit(ctx context.Context, tunnelID, clientID string, limitBytes *uint64) error {
	if limitBytes == nil {
		_, err := db.sql.ExecContext(ctx, "DELETE FROM client_traffic_limits WHERE tunnel_id = ? AND client_id = ?", tunnelID, clientID)
		return err
	}
	if *limitBytes == 0 {
		return errors.New("traffic limit must be positive")
	}
	value, err := sqliteInt(*limitBytes)
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx, `
INSERT INTO client_traffic_limits (tunnel_id, client_id, limit_bytes, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(tunnel_id, client_id) DO UPDATE SET
    limit_bytes = excluded.limit_bytes,
    updated_at = excluded.updated_at`,
		tunnelID,
		clientID,
		value,
		formatTime(time.Now().UTC()),
	)
	return err
}

func (db *DB) DeleteClientTrafficLimit(ctx context.Context, clientID string) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM client_traffic_limits WHERE client_id = ?", clientID)
	return err
}

func (db *DB) ListClientTrafficLimits(ctx context.Context) ([]ClientTrafficLimit, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT tunnel_id, client_id, limit_bytes
FROM client_traffic_limits
ORDER BY tunnel_id, client_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ClientTrafficLimit
	for rows.Next() {
		var row ClientTrafficLimit
		var limit int64
		if err := rows.Scan(&row.TunnelID, &row.ClientID, &limit); err != nil {
			return nil, err
		}
		if limit > 0 {
			row.LimitBytes = uint64(limit)
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (db *DB) ListExceededTrafficLimits(ctx context.Context, now time.Time) ([]ExceededTrafficLimit, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := db.sql.QueryContext(ctx, `
SELECT
    limits.tunnel_id,
    limits.client_id,
    limits.limit_bytes,
    COALESCE(SUM(daily.rx_bytes + daily.tx_bytes), 0) AS total_bytes
FROM client_traffic_limits AS limits
LEFT JOIN client_traffic_daily AS daily
    ON daily.tunnel_id = limits.tunnel_id AND daily.client_id = limits.client_id
GROUP BY limits.tunnel_id, limits.client_id, limits.limit_bytes
HAVING total_bytes >= limits.limit_bytes
ORDER BY limits.tunnel_id, limits.client_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ExceededTrafficLimit
	for rows.Next() {
		var row ExceededTrafficLimit
		var limit, total int64
		if err := rows.Scan(&row.TunnelID, &row.ClientID, &limit, &total); err != nil {
			return nil, err
		}
		if limit <= 0 || total < limit {
			continue
		}
		row.LimitBytes = uint64(limit)
		row.TotalBytes = uint64(total)
		row.ExceededAt = now.UTC()
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

func uint64Ptr(value uint64) *uint64 {
	return &value
}

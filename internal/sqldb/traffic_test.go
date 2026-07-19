package sqldb

import (
	"context"
	"testing"
	"time"
)

func TestRecordTrafficSamplesAggregatesPositiveDeltas(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	samples := []TrafficSample{
		{SampledAt: now.Add(-2 * time.Minute), TunnelID: "tunnel", ClientID: "client", RxBytes: 1000, TxBytes: 2000, Present: true},
		{SampledAt: now.Add(-1 * time.Minute), TunnelID: "tunnel", ClientID: "client", RxBytes: 1500, TxBytes: 2700, Present: true},
		{SampledAt: now, TunnelID: "tunnel", ClientID: "client", RxBytes: 100, TxBytes: 100, Present: true},
	}
	if err := db.RecordTrafficSamples(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListTrafficSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("summary rows = %d, want 1", len(rows))
	}
	if rows[0].RxTotal != 500 || rows[0].TxTotal != 700 {
		t.Fatalf("total summary rx/tx = %d/%d, want 500/700", rows[0].RxTotal, rows[0].TxTotal)
	}
	if rows[0].RxToday != 500 || rows[0].TxToday != 700 {
		t.Fatalf("today summary rx/tx = %d/%d, want 500/700", rows[0].RxToday, rows[0].TxToday)
	}
	if got := countRows(t, db, "SELECT count(*) FROM traffic_samples"); got != 3 {
		t.Fatalf("traffic_samples rows = %d, want 3", got)
	}
}

func TestRecordTrafficSamplesUsesDailyWindows(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	_, err = db.sql.ExecContext(context.Background(), `
INSERT INTO client_traffic_daily (day, tunnel_id, client_id, rx_bytes, tx_bytes, updated_at)
VALUES
    (?, 'tunnel', 'client', 10, 20, ?),
    (?, 'tunnel', 'client', 30, 40, ?),
    (?, 'tunnel', 'client', 50, 60, ?),
    (?, 'tunnel', 'client', 70, 80, ?)`,
		now.Format("2006-01-02"), formatTime(now),
		now.AddDate(0, 0, -6).Format("2006-01-02"), formatTime(now),
		now.AddDate(0, 0, -20).Format("2006-01-02"), formatTime(now),
		now.AddDate(0, 0, -40).Format("2006-01-02"), formatTime(now),
	)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListTrafficSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("summary rows = %d, want 1", len(rows))
	}
	if rows[0].RxTotal != 160 || rows[0].TxTotal != 200 {
		t.Fatalf("total summary = %d/%d, want 160/200", rows[0].RxTotal, rows[0].TxTotal)
	}
	if rows[0].RxToday != 10 || rows[0].TxToday != 20 {
		t.Fatalf("today summary = %d/%d, want 10/20", rows[0].RxToday, rows[0].TxToday)
	}
	if rows[0].Rx7d != 40 || rows[0].Tx7d != 60 {
		t.Fatalf("7d summary = %d/%d, want 40/60", rows[0].Rx7d, rows[0].Tx7d)
	}
	if rows[0].Rx30d != 90 || rows[0].Tx30d != 120 {
		t.Fatalf("30d summary = %d/%d, want 90/120", rows[0].Rx30d, rows[0].Tx30d)
	}
}

func TestClientTrafficLimitRoundTrip(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	limit := uint64(50 * 1024 * 1024 * 1024)
	if err := db.SetClientTrafficLimit(context.Background(), "tunnel", "client", &limit); err != nil {
		t.Fatal(err)
	}
	limits, err := db.ListClientTrafficLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || limits[0].LimitBytes != limit {
		t.Fatalf("limits = %#v, want one %d-byte limit", limits, limit)
	}

	if err := db.SetClientTrafficLimit(context.Background(), "tunnel", "client", nil); err != nil {
		t.Fatal(err)
	}
	limits, err = db.ListClientTrafficLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 0 {
		t.Fatalf("limits after clear = %#v, want none", limits)
	}
}

func TestClientTrafficLimitPeriodRoundTrip(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	limit := uint64(50 * 1024 * 1024 * 1024)
	if err := db.SetClientTrafficLimitWithPeriod(context.Background(), "tunnel", "rolling", &limit, TrafficLimitPeriodRolling30Days); err != nil {
		t.Fatal(err)
	}
	if err := db.SetClientTrafficLimit(context.Background(), "tunnel", "lifetime", &limit); err != nil {
		t.Fatal(err)
	}
	limits, err := db.ListClientTrafficLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 2 {
		t.Fatalf("limits = %#v, want two limits", limits)
	}
	if limits[0].ClientID != "lifetime" || limits[0].Period != TrafficLimitPeriodLifetime {
		t.Fatalf("lifetime limit = %#v, want lifetime period", limits[0])
	}
	if limits[1].ClientID != "rolling" || limits[1].Period != TrafficLimitPeriodRolling30Days {
		t.Fatalf("rolling limit = %#v, want rolling 30-day period", limits[1])
	}
}

func TestTrafficLimitMigrationPreservesLifetimePeriod(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	migrations, err := loadSQLiteMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:2] {
		if err := db.applyMigration(context.Background(), migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(context.Background(), `
INSERT INTO client_traffic_limits (tunnel_id, client_id, limit_bytes, updated_at)
VALUES ('tunnel', 'client', 1000, ?)`, formatTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	limits, err := db.ListClientTrafficLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || limits[0].Period != TrafficLimitPeriodLifetime {
		t.Fatalf("limits after migration = %#v, want lifetime period", limits)
	}
}

func TestTrafficLimitBlockRoundTrip(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	limit := uint64(1000)
	if err := db.SetClientTrafficLimitWithPeriod(context.Background(), "tunnel", "client", &limit, TrafficLimitPeriodRolling30Days); err != nil {
		t.Fatal(err)
	}
	blockedAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if err := db.MarkClientTrafficLimitBlocked(context.Background(), "tunnel", "client", blockedAt); err != nil {
		t.Fatal(err)
	}
	blocks, err := db.ListTrafficLimitBlocks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Period != TrafficLimitPeriodRolling30Days || !blocks[0].BlockedAt.Equal(blockedAt) {
		t.Fatalf("blocks = %#v, want one rolling block", blocks)
	}
	if err := db.ClearClientTrafficLimitBlock(context.Background(), "client"); err != nil {
		t.Fatal(err)
	}
	blocks, err = db.ListTrafficLimitBlocks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("blocks after clear = %#v, want none", blocks)
	}
}

func TestTrafficSummaryIncludesConfiguredLimit(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	limit := uint64(10 * 1024 * 1024 * 1024)
	if err := db.SetClientTrafficLimit(context.Background(), "tunnel", "client", &limit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `
INSERT INTO client_traffic_daily (day, tunnel_id, client_id, rx_bytes, tx_bytes, updated_at)
VALUES (?, 'tunnel', 'client', 1024, 2048, ?)`, now.Format("2006-01-02"), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListTrafficSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].LimitBytes == nil || *rows[0].LimitBytes != limit {
		t.Fatalf("summary rows = %#v, want limit %d", rows, limit)
	}
}

func TestTrafficSummaryUsesRolling30DayUsageForLimit(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	limit := uint64(1000)
	if err := db.SetClientTrafficLimitWithPeriod(context.Background(), "tunnel", "client", &limit, TrafficLimitPeriodRolling30Days); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `
INSERT INTO client_traffic_daily (day, tunnel_id, client_id, rx_bytes, tx_bytes, updated_at)
VALUES
    ('2026-06-06', 'tunnel', 'client', 4000, 0, ?),
    ('2026-07-01', 'tunnel', 'client', 300, 200, ?),
    ('2026-07-06', 'tunnel', 'client', 100, 100, ?)`, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListTrafficSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("summary rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.RxTotal+row.TxTotal != 4700 {
		t.Fatalf("total usage = %d, want 4700", row.RxTotal+row.TxTotal)
	}
	if row.LimitPeriod != TrafficLimitPeriodRolling30Days || row.LimitUsageBytes != 700 {
		t.Fatalf("rolling limit summary = %#v, want usage 700", row)
	}
}

func TestListExceededTrafficLimits(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	limit := uint64(3000)
	if err := db.SetClientTrafficLimit(context.Background(), "tunnel", "client", &limit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `
INSERT INTO client_traffic_daily (day, tunnel_id, client_id, rx_bytes, tx_bytes, updated_at)
VALUES (?, 'tunnel', 'client', 2000, 1000, ?)`, now.Format("2006-01-02"), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListExceededTrafficLimits(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TotalBytes != 3000 || rows[0].LimitBytes != limit {
		t.Fatalf("exceeded rows = %#v, want one exact-limit row", rows)
	}
}

func TestListExceededTrafficLimitsUsesConfiguredPeriod(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	limit := uint64(1000)
	for _, item := range []struct {
		clientID string
		period   TrafficLimitPeriod
	}{
		{clientID: "lifetime", period: TrafficLimitPeriodLifetime},
		{clientID: "rolling", period: TrafficLimitPeriodRolling30Days},
	} {
		if err := db.SetClientTrafficLimitWithPeriod(context.Background(), "tunnel", item.clientID, &limit, item.period); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(context.Background(), `
INSERT INTO client_traffic_daily (day, tunnel_id, client_id, rx_bytes, tx_bytes, updated_at)
VALUES
    ('2026-06-06', 'tunnel', 'lifetime', 1500, 0, ?),
    ('2026-06-06', 'tunnel', 'rolling', 1500, 0, ?),
    ('2026-07-01', 'tunnel', 'rolling', 400, 0, ?),
    ('2026-07-06', 'tunnel', 'rolling', 600, 0, ?)`, formatTime(now), formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListExceededTrafficLimits(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ClientID != "lifetime" || rows[0].Period != TrafficLimitPeriodLifetime || rows[1].ClientID != "rolling" || rows[1].Period != TrafficLimitPeriodRolling30Days {
		t.Fatalf("exceeded rows = %#v, want lifetime and rolling clients", rows)
	}
	if rows[0].TotalBytes != 1500 {
		t.Fatalf("lifetime usage = %d, want 1500", rows[0].TotalBytes)
	}
	if rows[1].TotalBytes != 1000 {
		t.Fatalf("rolling usage = %d, want 1000", rows[1].TotalBytes)
	}
}

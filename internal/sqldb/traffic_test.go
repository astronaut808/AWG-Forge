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

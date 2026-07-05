package sqldb

import (
	"context"
	"database/sql"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

type RetentionReport struct {
	DeletedAuditEvents      int64 `json:"deleted_audit_events"`
	DeletedLoginAttempts    int64 `json:"deleted_login_attempts"`
	DeletedHealthChecks     int64 `json:"deleted_health_checks"`
	DeletedTLSEvents        int64 `json:"deleted_tls_events"`
	DeletedTrafficSamples   int64 `json:"deleted_traffic_samples"`
	DeletedTrafficDailyRows int64 `json:"deleted_traffic_daily_rows"`
	RetentionDays           int   `json:"retention_days"`
	TrafficSamplesDays      int   `json:"traffic_samples_days"`
	DailyTrafficDays        int   `json:"daily_traffic_days"`
}

func ApplyRetention(ctx context.Context, cfg config.Config, now time.Time) (RetentionReport, error) {
	db, err := OpenExisting(ctx, cfg)
	if err != nil {
		return RetentionReport{}, err
	}
	defer func() { _ = db.Close() }()
	return db.ApplyRetention(ctx, cfg, now)
}

func (db *DB) ApplyRetention(ctx context.Context, cfg config.Config, now time.Time) (RetentionReport, error) {
	days := cfg.DatabaseRetention
	if days <= 0 {
		days = 90
	}
	report := RetentionReport{
		RetentionDays:      days,
		TrafficSamplesDays: 14,
		DailyTrafficDays:   400,
	}
	generalCutoff := now.UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano)
	trafficCutoff := now.UTC().AddDate(0, 0, -report.TrafficSamplesDays).Format(time.RFC3339Nano)
	dailyCutoff := now.UTC().AddDate(0, 0, -report.DailyTrafficDays).Format("2006-01-02")

	var err error
	if report.DeletedAuditEvents, err = affectedRows(db.sql.ExecContext(ctx, "DELETE FROM audit_events WHERE time < ?", generalCutoff)); err != nil {
		return report, err
	}
	if report.DeletedLoginAttempts, err = affectedRows(db.sql.ExecContext(ctx, "DELETE FROM login_attempts WHERE time < ?", generalCutoff)); err != nil {
		return report, err
	}
	if report.DeletedHealthChecks, err = affectedRows(db.sql.ExecContext(ctx, "DELETE FROM health_checks WHERE time < ?", generalCutoff)); err != nil {
		return report, err
	}
	if report.DeletedTLSEvents, err = affectedRows(db.sql.ExecContext(ctx, "DELETE FROM tls_events WHERE time < ?", generalCutoff)); err != nil {
		return report, err
	}
	if report.DeletedTrafficSamples, err = affectedRows(db.sql.ExecContext(ctx, "DELETE FROM traffic_samples WHERE sampled_at < ?", trafficCutoff)); err != nil {
		return report, err
	}
	if report.DeletedTrafficDailyRows, err = affectedRows(db.sql.ExecContext(ctx, "DELETE FROM client_traffic_daily WHERE day < ?", dailyCutoff)); err != nil {
		return report, err
	}
	return report, nil
}

func affectedRows(result sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

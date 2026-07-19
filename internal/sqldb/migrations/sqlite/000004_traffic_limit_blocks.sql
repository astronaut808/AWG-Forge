ALTER TABLE client_traffic_limits
ADD COLUMN quota_blocked_at TEXT NOT NULL DEFAULT '';

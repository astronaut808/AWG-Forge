CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
    id INTEGER PRIMARY KEY,
    time TEXT NOT NULL,
    level TEXT NOT NULL CHECK (level IN ('info', 'warn', 'error')),
    event TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    fields_json TEXT NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS audit_events_time_idx ON audit_events (time DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_events_event_time_idx ON audit_events (event, time DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_events_level_time_idx ON audit_events (level, time DESC, id DESC);

CREATE TABLE IF NOT EXISTS login_attempts (
    id INTEGER PRIMARY KEY,
    time TEXT NOT NULL,
    success INTEGER NOT NULL CHECK (success IN (0, 1)),
    reason TEXT NOT NULL DEFAULT '',
    remote_addr_hash TEXT NOT NULL DEFAULT '',
    user_agent_hash TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS login_attempts_time_idx ON login_attempts (time DESC, id DESC);
CREATE INDEX IF NOT EXISTS login_attempts_remote_time_idx ON login_attempts (remote_addr_hash, time DESC);

CREATE TABLE IF NOT EXISTS health_checks (
    id INTEGER PRIMARY KEY,
    time TEXT NOT NULL,
    area TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ok', 'warn', 'fail')),
    message TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    details_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS health_checks_time_idx ON health_checks (time DESC, id DESC);
CREATE INDEX IF NOT EXISTS health_checks_area_time_idx ON health_checks (area, time DESC, id DESC);
CREATE INDEX IF NOT EXISTS health_checks_status_time_idx ON health_checks (status, time DESC, id DESC);

CREATE TABLE IF NOT EXISTS tls_certificates (
    id INTEGER PRIMARY KEY,
    observed_at TEXT NOT NULL,
    mode TEXT NOT NULL,
    source TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    issuer TEXT NOT NULL DEFAULT '',
    serial_number TEXT NOT NULL DEFAULT '',
    not_before TEXT NOT NULL,
    not_after TEXT NOT NULL,
    fingerprint_sha256 TEXT NOT NULL UNIQUE,
    dns_names_json TEXT NOT NULL DEFAULT '[]',
    ip_addresses_json TEXT NOT NULL DEFAULT '[]',
    key_algorithm TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS tls_certificates_not_after_idx ON tls_certificates (not_after);
CREATE INDEX IF NOT EXISTS tls_certificates_observed_idx ON tls_certificates (observed_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS tls_events (
    id INTEGER PRIMARY KEY,
    time TEXT NOT NULL,
    mode TEXT NOT NULL,
    domain TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    result TEXT NOT NULL CHECK (result IN ('ok', 'warn', 'fail')),
    error TEXT NOT NULL DEFAULT '',
    cert_fingerprint_sha256 TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS tls_events_time_idx ON tls_events (time DESC, id DESC);
CREATE INDEX IF NOT EXISTS tls_events_domain_time_idx ON tls_events (domain, time DESC, id DESC);

CREATE TABLE IF NOT EXISTS traffic_samples (
    id INTEGER PRIMARY KEY,
    sampled_at TEXT NOT NULL,
    tunnel_id TEXT NOT NULL,
    client_id TEXT NOT NULL,
    rx_bytes INTEGER NOT NULL CHECK (rx_bytes >= 0),
    tx_bytes INTEGER NOT NULL CHECK (tx_bytes >= 0),
    latest_handshake_at TEXT NOT NULL DEFAULT '',
    present INTEGER NOT NULL CHECK (present IN (0, 1))
);

CREATE INDEX IF NOT EXISTS traffic_samples_client_time_idx ON traffic_samples (client_id, sampled_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS traffic_samples_tunnel_time_idx ON traffic_samples (tunnel_id, sampled_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS client_traffic_daily (
    day TEXT NOT NULL,
    tunnel_id TEXT NOT NULL,
    client_id TEXT NOT NULL,
    rx_bytes INTEGER NOT NULL DEFAULT 0 CHECK (rx_bytes >= 0),
    tx_bytes INTEGER NOT NULL DEFAULT 0 CHECK (tx_bytes >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (day, tunnel_id, client_id)
);

CREATE INDEX IF NOT EXISTS client_traffic_daily_client_day_idx ON client_traffic_daily (client_id, day DESC);

CREATE TABLE IF NOT EXISTS app_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

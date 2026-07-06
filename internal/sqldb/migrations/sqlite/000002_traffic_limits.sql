CREATE TABLE IF NOT EXISTS client_traffic_limits (
    tunnel_id TEXT NOT NULL,
    client_id TEXT NOT NULL,
    limit_bytes INTEGER NOT NULL CHECK (limit_bytes > 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (tunnel_id, client_id)
);

CREATE INDEX IF NOT EXISTS client_traffic_limits_client_idx ON client_traffic_limits (client_id);

CREATE TABLE control_refresh_operations (
    operation_id TEXT PRIMARY KEY,
    controller_id TEXT NOT NULL,
    node_id TEXT NOT NULL REFERENCES control_node_bindings(node_id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    envelope_json BLOB NOT NULL CHECK (length(envelope_json) BETWEEN 1 AND 4096),
    status TEXT NOT NULL CHECK (status IN ('queued','leased','succeeded','conflict','failed','expired','rejected')),
    code TEXT NOT NULL DEFAULT '',
    issued_at_unix_ms INTEGER NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    completed_at_unix_ms INTEGER,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_expires_at_unix_ms INTEGER NOT NULL DEFAULT 0,
    owner_json BLOB NOT NULL DEFAULT X'',
    snapshot_sequence INTEGER NOT NULL DEFAULT 0 CHECK (snapshot_sequence >= 0),
    receipt_json BLOB NOT NULL DEFAULT X'' CHECK (length(receipt_json) <= 4096),
    UNIQUE (controller_id, node_id, idempotency_key),
    CHECK (expires_at_unix_ms > issued_at_unix_ms)
);
CREATE INDEX control_refresh_node_status ON control_refresh_operations(node_id, status, issued_at_unix_ms);
CREATE INDEX control_refresh_retention ON control_refresh_operations(expires_at_unix_ms);

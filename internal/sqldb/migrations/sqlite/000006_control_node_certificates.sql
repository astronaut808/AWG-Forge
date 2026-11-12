CREATE TABLE IF NOT EXISTS control_node_bindings (
    node_id TEXT PRIMARY KEY,
    controller_id TEXT NOT NULL,
    binding_epoch INTEGER NOT NULL CHECK (binding_epoch > 0),
    revoked_at_unix_ms INTEGER
);

CREATE TABLE IF NOT EXISTS control_node_certificates (
    issuer_generation TEXT NOT NULL,
    serial TEXT NOT NULL,
    node_id TEXT NOT NULL REFERENCES control_node_bindings(node_id),
    controller_id TEXT NOT NULL,
    binding_epoch INTEGER NOT NULL CHECK (binding_epoch > 0),
    certificate_sha256 BLOB NOT NULL CHECK (length(certificate_sha256) = 32),
    public_key_sha256 BLOB NOT NULL CHECK (length(public_key_sha256) = 32),
    csr_sha256 BLOB NOT NULL UNIQUE CHECK (length(csr_sha256) = 32),
    certificate_der BLOB NOT NULL CHECK (length(certificate_der) BETWEEN 1 AND 16384),
    not_before_unix_ms INTEGER NOT NULL,
    not_after_unix_ms INTEGER NOT NULL,
    superseded_at_unix_ms INTEGER,
    revoked_at_unix_ms INTEGER,
    PRIMARY KEY (issuer_generation, serial),
    CHECK (not_after_unix_ms > not_before_unix_ms)
);

CREATE INDEX IF NOT EXISTS control_node_certificates_node_idx
    ON control_node_certificates (node_id, binding_epoch);

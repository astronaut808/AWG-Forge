CREATE TABLE IF NOT EXISTS control_enrollment_invitations (
    id TEXT PRIMARY KEY,
    controller_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    secret_digest BLOB NOT NULL CHECK (length(secret_digest) = 32),
    created_at_unix_ms INTEGER NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    CHECK (expires_at_unix_ms > created_at_unix_ms)
);

CREATE TABLE IF NOT EXISTS control_enrollments (
    id TEXT PRIMARY KEY,
    invitation_id TEXT NOT NULL UNIQUE REFERENCES control_enrollment_invitations(id) ON DELETE CASCADE,
    controller_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    requested_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    issuer_generation TEXT NOT NULL DEFAULT '',
    serial TEXT NOT NULL DEFAULT '',
    csr_der BLOB NOT NULL CHECK (length(csr_der) BETWEEN 1 AND 16384),
    csr_sha256 BLOB NOT NULL CHECK (length(csr_sha256) = 32),
    claim_digest BLOB NOT NULL CHECK (length(claim_digest) = 32),
    created_at_unix_ms INTEGER NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    certificate_der BLOB NOT NULL DEFAULT '',
    CHECK (expires_at_unix_ms > created_at_unix_ms),
    CHECK ((status = 'approved' AND issuer_generation != '' AND serial != '' AND length(certificate_der) BETWEEN 1 AND 16384) OR
           (status != 'approved' AND issuer_generation = '' AND serial = '' AND length(certificate_der) = 0))
);

CREATE INDEX IF NOT EXISTS control_enrollments_status_idx
    ON control_enrollments (status, expires_at_unix_ms);

CREATE TABLE IF NOT EXISTS control_node_presence (
    node_id TEXT PRIMARY KEY REFERENCES control_node_bindings(node_id) ON DELETE CASCADE,
    controller_id TEXT NOT NULL,
    binding_epoch INTEGER NOT NULL CHECK (binding_epoch > 0),
    state_epoch TEXT NOT NULL,
    boot_id TEXT NOT NULL,
    boot_sequence INTEGER NOT NULL CHECK (boot_sequence > 0),
    session_id TEXT NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL
);

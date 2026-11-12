CREATE TABLE IF NOT EXISTS controller_users (
    id TEXT PRIMARY KEY,
    singleton INTEGER NOT NULL DEFAULT 1 UNIQUE CHECK (singleton = 1),
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    totp_secret_ciphertext TEXT NOT NULL,
    totp_last_step INTEGER NOT NULL DEFAULT -1 CHECK (totp_last_step >= -1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    disabled_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS controller_sessions (
    token_digest BLOB PRIMARY KEY CHECK (length(token_digest) = 32),
    user_id TEXT NOT NULL REFERENCES controller_users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    authenticated_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    revoked_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS controller_sessions_user_idx
    ON controller_sessions (user_id, expires_at);
CREATE INDEX IF NOT EXISTS controller_sessions_expiry_idx
    ON controller_sessions (expires_at_unix_ms);

CREATE TABLE IF NOT EXISTS controller_recovery_codes (
    user_id TEXT NOT NULL REFERENCES controller_users(id) ON DELETE CASCADE,
    code_digest BLOB NOT NULL CHECK (length(code_digest) = 32),
    created_at TEXT NOT NULL,
    used_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, code_digest)
);

CREATE INDEX IF NOT EXISTS controller_recovery_codes_unused_idx
    ON controller_recovery_codes (user_id, used_at);

CREATE TABLE IF NOT EXISTS controller_auth_gate (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    sequence INTEGER NOT NULL CHECK (sequence >= 0)
);

INSERT INTO controller_auth_gate (singleton, sequence) VALUES (1, 0);

CREATE TABLE IF NOT EXISTS controller_auth_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    attempted_at_unix_ms INTEGER NOT NULL,
    finished_at_unix_ms INTEGER,
    account_digest BLOB NOT NULL CHECK (length(account_digest) = 32),
    source_digest BLOB NOT NULL CHECK (length(source_digest) = 32),
    outcome TEXT NOT NULL CHECK (outcome IN ('pending', 'failed', 'success')),
    reason TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS controller_auth_attempts_time_idx
    ON controller_auth_attempts (attempted_at_unix_ms);
CREATE INDEX IF NOT EXISTS controller_auth_attempts_account_idx
    ON controller_auth_attempts (account_digest, attempted_at_unix_ms);
CREATE INDEX IF NOT EXISTS controller_auth_attempts_source_idx
    ON controller_auth_attempts (source_digest, attempted_at_unix_ms);

ALTER TABLE control_node_presence ADD COLUMN application_version TEXT NOT NULL DEFAULT '';
ALTER TABLE control_node_presence ADD COLUMN contract_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE control_node_presence ADD COLUMN capabilities_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE control_node_presence ADD COLUMN confirmed_at_unix_ms INTEGER NOT NULL DEFAULT 0;

CREATE TABLE control_node_projections (
    node_id TEXT PRIMARY KEY REFERENCES control_node_bindings(node_id) ON DELETE CASCADE,
    controller_id TEXT NOT NULL,
    binding_epoch INTEGER NOT NULL CHECK (binding_epoch > 0),
    state_epoch TEXT NOT NULL,
    boot_id TEXT NOT NULL,
    boot_sequence INTEGER NOT NULL CHECK (boot_sequence > 0),
    session_id TEXT NOT NULL,
    desired_generation INTEGER NOT NULL CHECK (desired_generation >= 0),
    snapshot_sequence INTEGER NOT NULL CHECK (snapshot_sequence > 0),
    observed_at_unix_ms INTEGER NOT NULL,
    received_at_unix_ms INTEGER NOT NULL,
    desired_json BLOB NOT NULL CHECK (length(desired_json) BETWEEN 1 AND 524288),
    observations_json BLOB NOT NULL CHECK (length(observations_json) BETWEEN 1 AND 524288),
    CHECK (length(desired_json) + length(observations_json) <= 524288)
);

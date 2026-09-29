-- An epoch has one initial certificate. Renewal successors have a predecessor
-- link and are intentionally outside this constraint.
CREATE UNIQUE INDEX control_node_certificates_one_initial_per_binding_idx
    ON control_node_certificates (node_id, binding_epoch)
    WHERE predecessor_issuer_generation IS NULL AND predecessor_serial IS NULL;

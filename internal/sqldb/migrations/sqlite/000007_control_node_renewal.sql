ALTER TABLE control_node_certificates ADD COLUMN predecessor_issuer_generation TEXT;
ALTER TABLE control_node_certificates ADD COLUMN predecessor_serial TEXT;

CREATE UNIQUE INDEX control_node_certificates_one_successor_idx
    ON control_node_certificates (predecessor_issuer_generation, predecessor_serial)
    WHERE predecessor_issuer_generation IS NOT NULL AND predecessor_serial IS NOT NULL;

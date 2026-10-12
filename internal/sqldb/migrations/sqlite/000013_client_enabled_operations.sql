-- Mutation results must survive execution expiry until the node durably
-- acknowledges their controller commit. The legacy table name is retained.
ALTER TABLE control_refresh_operations ADD COLUMN node_acknowledged_at_unix_ms INTEGER;

ALTER TABLE client_traffic_limits
ADD COLUMN period TEXT NOT NULL DEFAULT 'lifetime' CHECK (period IN ('lifetime', 'rolling_30d'));

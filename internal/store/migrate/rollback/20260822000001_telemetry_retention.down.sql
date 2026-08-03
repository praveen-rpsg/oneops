-- Reverses 20260822000001_telemetry_retention.
--
-- Dropping the table drops its row-level-security policy and its hypertable
-- chunks with it, mirroring 20260821000001's own rollback.

DROP TABLE IF EXISTS telemetry_rollup_5m;

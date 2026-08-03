-- Reverses 20260821000001_telemetry.
--
-- Dropping the table drops its row-level-security policy and its hypertable
-- chunks with it (mirroring 20260816000001's asset rollback: no separate
-- policy reversal is needed).
--
-- The timescaledb extension is deliberately NOT dropped, for the same
-- reason 20260804000003's citext rollback leaves citext installed: it is a
-- database-wide object a later migration may already depend on, and
-- CREATE EXTENSION IF NOT EXISTS is idempotent on re-apply, so leaving it
-- costs nothing.

DROP TABLE IF EXISTS telemetry_sample;

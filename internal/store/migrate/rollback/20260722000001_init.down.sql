-- Operational rollback for 20260722000001_init.
-- Kept out of the Atlas migration directory (which holds forward files only).
DROP TABLE IF EXISTS idempotency_key;
DROP TABLE IF EXISTS configuration_metadata;
DROP TABLE IF EXISTS artifact_version;
DROP TABLE IF EXISTS configuration_object;
DROP TABLE IF EXISTS schema_migrations;

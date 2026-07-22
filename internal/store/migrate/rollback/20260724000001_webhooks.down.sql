-- Rollback of PRS-018 webhook tables (event delivery). Additive-only forward
-- migration; this drops exactly what it created and nothing else.
DROP TABLE IF EXISTS webhook_cursor;
DROP TABLE IF EXISTS webhook_delivery;
DROP TABLE IF EXISTS webhook;

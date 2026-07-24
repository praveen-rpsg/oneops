-- Reverses 20260730000001_idempotency_tenant_scope.sql.
--
-- Restoring a global primary key reopens cross-tenant idempotency poisoning,
-- and fails outright if two tenants already hold the same key — which is the
-- normal state after this migration has been in service. Both are intended:
-- the rollback should require a deliberate decision about whose keys to drop.

ALTER TABLE idempotency_key DROP CONSTRAINT IF EXISTS idempotency_key_pkey;
ALTER TABLE idempotency_key ADD CONSTRAINT idempotency_key_pkey PRIMARY KEY (id);

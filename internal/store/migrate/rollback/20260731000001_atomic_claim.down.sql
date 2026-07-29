-- Reverses 20260731000001_atomic_claim.sql.

DROP INDEX IF EXISTS ix_policy_execution_due;
CREATE INDEX IF NOT EXISTS ix_policy_execution_due
    ON policy_execution (next_attempt_at)
    WHERE status IN ('pending', 'failed');
DROP INDEX IF EXISTS ix_webhook_delivery_due;
CREATE INDEX IF NOT EXISTS ix_webhook_delivery_due
    ON webhook_delivery (next_attempt_at)
    WHERE status IN ('pending', 'failed');

ALTER TABLE policy_execution DROP COLUMN IF EXISTS claimed_at;
ALTER TABLE webhook_delivery DROP COLUMN IF EXISTS claimed_at;

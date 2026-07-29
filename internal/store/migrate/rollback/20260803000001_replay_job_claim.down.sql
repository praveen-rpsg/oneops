-- Reverses 20260803000001_replay_job_claim. Dropping claimed_at returns the
-- replay queue to a non-exclusive claim and unfenced completion
-- (ADR-CONCURRENCY-007).
DROP INDEX IF EXISTS ix_webhook_replay_job_pending;
ALTER TABLE webhook_replay_job DROP COLUMN IF EXISTS claimed_at;
CREATE INDEX IF NOT EXISTS ix_webhook_replay_job_pending
    ON webhook_replay_job (created_at) WHERE status = 'pending';

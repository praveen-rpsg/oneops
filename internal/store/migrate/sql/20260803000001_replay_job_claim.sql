-- The replay-job queue gets the atomic claim and fencing token the other two
-- queues already have (ADR-CONCURRENCY-007).
--
-- Trust Register audit: entries 14 (atomic claim) and 18 (claim fencing) were
-- recorded as eliminated *classes* but verified only on webhook_delivery and
-- policy_execution. webhook_replay_job is a third claimed resource and had
-- neither: ClaimPendingJobs was a plain `SELECT ... WHERE status='pending'` and
-- UpdateJob was an unconditional `UPDATE ... WHERE id=$1`.
--
-- Proven live: two workers claiming at the same instant each received all 8
-- pending jobs, and a worker that no longer owned a job overwrote the owner's
-- `completed` outcome with its own `failed` verdict.
--
-- claimed_at is nullable and unset for existing rows: NULL means "not currently
-- claimed", so rows written before this migration are claimable normally — the
-- same convention 20260731000001_atomic_claim used for the other two queues.

ALTER TABLE webhook_replay_job ADD COLUMN IF NOT EXISTS claimed_at timestamptz;

-- The pending index now covers the claimed state so lease recovery is indexed:
-- a job whose worker crashed is found by the same scan that finds pending jobs.
DROP INDEX IF EXISTS ix_webhook_replay_job_pending;
CREATE INDEX IF NOT EXISTS ix_webhook_replay_job_pending
    ON webhook_replay_job (created_at)
    WHERE status IN ('pending', 'running');

-- Atomic claim + lease for the delivery and policy-execution queues
-- (ADR-CONCURRENCY-002).
--
-- ClaimDue was a plain SELECT of pending/failed rows with no claim, so two
-- workers running at once — the overlap window during a leadership handoff or a
-- lock-loss — both selected the same rows and both performed the outbound
-- action. Leadership makes that window small; it does not close it, and a paused
-- process (GC, SIGSTOP) can widen it arbitrarily.
--
-- The claim becomes an atomic compare-and-set: a row is moved to a claimed state
-- (`inflight` for deliveries, `running` for executions) with claimed_at set,
-- under FOR UPDATE SKIP LOCKED, so no two workers ever hold the same row. A
-- worker that crashes after claiming leaves the row claimed; it is recovered
-- once claimed_at is older than the lease, which is what bounds re-execution
-- after a crash rather than replaying immediately.
--
-- claimed_at is nullable and unset for existing rows: a NULL claimed_at is
-- treated as "not currently claimed", so rows written before this migration are
-- claimable normally.

ALTER TABLE webhook_delivery ADD COLUMN IF NOT EXISTS claimed_at timestamptz;
ALTER TABLE policy_execution ADD COLUMN IF NOT EXISTS claimed_at timestamptz;

-- The due indexes now include the claimed state so lease recovery is indexed:
-- a stale claimed row is found by the same scan that finds due pending/failed
-- rows.
DROP INDEX IF EXISTS ix_webhook_delivery_due;
CREATE INDEX IF NOT EXISTS ix_webhook_delivery_due
    ON webhook_delivery (next_attempt_at)
    WHERE status IN ('pending', 'failed', 'inflight');

DROP INDEX IF EXISTS ix_policy_execution_due;
CREATE INDEX IF NOT EXISTS ix_policy_execution_due
    ON policy_execution (next_attempt_at)
    WHERE status IN ('pending', 'failed', 'running');

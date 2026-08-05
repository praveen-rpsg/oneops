-- OPS-S<E5.2b-2> Escalation engine (seed -> page -> escalate -> cancel).
--
-- ONE new table: escalation_state, the per-incident escalation work-queue
-- (ADR-ONCALL-003). At most one row per incident
-- (uq_escalation_state_tenant_incident), tracking which tier of policy_id's
-- ladder the incident is currently at and when it is next due to be
-- (re-)paged. The queue's own claim/fence shape mirrors notification
-- (20260811000001) exactly: claimed_at is the sole fencing token, status
-- does not change on claim (unlike webhook_delivery's separate "inflight"
-- value) — a leader-gated Worker claims due active rows under
-- FOR UPDATE SKIP LOCKED, pages/escalates/terminates them, and fences every
-- outcome write on claimed_at (ADR-CONCURRENCY-002/005/006/007).
--
-- escalation_state is TENANT-SCOPED, exactly like escalation_policy/
-- escalation_tier (20260902000001): tenant_id is denormalised onto the row
-- and is the RLS key. Unlike escalation_policy/escalation_tier, this table
-- has NO admin CRUD API and no HTTP write path at all — the leader-gated
-- Seeder (internal/escalation) is its ONLY writer at INSERT time, sourcing
-- tenant_id/incident_id/policy_id directly from one query's own JOIN of
-- incident and escalation_policy on tenant_id, so the two foreign keys below
-- can never straddle a tenant boundary even though nothing re-verifies them
-- at write time the way EscalationPolicyStore.AddTier does for
-- escalation_tier.on_call_schedule_id (ADR-ASSET-001 §6) — there is no
-- untrusted caller here to re-verify against.
--
-- incident_id REFERENCES incident (incident_id), no ON DELETE clause:
-- Incident has no Delete method (domain.IncidentRepository's own doc
-- comment), so this can never actually fire.
--
-- policy_id REFERENCES escalation_policy (policy_id), no ON DELETE clause
-- (defaults to RESTRICT): deleting a policy an incident is actively
-- escalating through must be refused, not silently orphan the row — the
-- identical "refuse, don't cascade past it" choice escalation_tier.
-- on_call_schedule_id's own migration comment already documents for
-- deleting an in-use on-call schedule.
--
-- FOUR STATUSES, ONE FENCING TOKEN, STATUS DOES NOT DEFAULT TO 'pending':
-- active/acked/resolved/exhausted, defaulting to 'active'. Unlike
-- notification's pending/sent/failed, "the row was just created and nothing
-- has happened to it yet" and "the row is actively being worked" are the
-- SAME state here (there is no separate not-yet-started value to default
-- to), so arch.TestEveryWorkQueue_HasAFencingToken's pending-default
-- detector does not classify this table as a queue — it is one by
-- construction (claimed_at + FOR UPDATE SKIP LOCKED in the store), just not
-- by that particular derivation rule.
--
-- One DDL statement per line/clause, exactly as every migration in this tree
-- does: the derived-schema extractor (internal/kg/extract/schema) captures
-- only the first ADD of a multi-clause ALTER.
CREATE TABLE IF NOT EXISTS escalation_state (
    state_id            text        PRIMARY KEY,
    tenant_id           text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    incident_id         text        NOT NULL REFERENCES incident (incident_id),
    policy_id           text        NOT NULL REFERENCES escalation_policy (policy_id),
    current_tier_index  integer     NOT NULL DEFAULT 0,
    next_attempt_at     timestamptz NOT NULL,
    claimed_at          timestamptz,
    status              text        NOT NULL DEFAULT 'active',
    attempts            integer     NOT NULL DEFAULT 0,
    row_version         bigint      NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_escalation_state_status CHECK (status IN ('active', 'acked', 'resolved', 'exhausted')),
    CONSTRAINT ck_escalation_state_tier_index CHECK (current_tier_index >= 0),
    CONSTRAINT ck_escalation_state_attempts CHECK (attempts >= 0),
    CONSTRAINT ck_escalation_state_row_version CHECK (row_version >= 1),
    CONSTRAINT uq_escalation_state_tenant_incident UNIQUE (tenant_id, incident_id)
);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3).
-- ix_escalation_state_claim is the Worker's own claim query index: ClaimDue
-- orders due rows by next_attempt_at under FOR UPDATE SKIP LOCKED, the same
-- shape ix_notification_claim uses. uq_escalation_state_tenant_incident's own
-- backing index already serves both the "does this incident have a row"
-- existence check the Seeder's NOT EXISTS uses and the tenant-scoped
-- lookup-by-incident path, so neither needs a separate index.
CREATE INDEX IF NOT EXISTS ix_escalation_state_claim
    ON escalation_state (next_attempt_at)
    WHERE status = 'active';
CREATE INDEX IF NOT EXISTS ix_escalation_state_policy ON escalation_state (policy_id);

COMMENT ON TABLE escalation_state IS
    'The per-incident escalation work-queue (E5.2b-2, ADR-ONCALL-003): at most one row per incident, tracking which tier of policy_id''s ladder it is at and when it is next due. Written ONLY by the leader-gated Seeder (internal/escalation) — there is no HTTP write path.';
COMMENT ON COLUMN escalation_state.current_tier_index IS
    '0-based index into policy_id''s ordered escalation_tier ladder. >= the ladder''s length means every tier has been reached and the row is (or is about to be marked) exhausted.';
COMMENT ON COLUMN escalation_state.next_attempt_at IS
    'When this row is next due to be claimed: immediately at seed time, or now() + the just-paged tier''s wait_seconds after every advance.';
COMMENT ON COLUMN escalation_state.claimed_at IS
    'The fencing token a Worker is handed by ClaimDue and must present back to Advance/MarkTerminal (ADR-CONCURRENCY-005). NULL means unclaimed.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring 20260902000001's policy).
ALTER TABLE escalation_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE escalation_state FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON escalation_state;
CREATE POLICY tenant_isolation ON escalation_state
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

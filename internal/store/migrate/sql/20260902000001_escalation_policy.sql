-- OPS-S<E5.2b-1> Escalation policy + tier config (CRUD only).
--
-- Two tenant-owned tables, the SAME shape on_call_schedule/on_call_participant
-- (20260901000001) already established for "a named object with an ordered
-- child sequence": escalation_policy is the named ladder, escalation_tier is
-- one rung in it.
--
-- CONFIG ONLY. There is deliberately no escalation_state work-queue, no
-- worker, no paging, and no incident/notification wiring here — that is
-- E5.2b-2, an entirely separate, later story
-- (docs/PLATFORM-BUILD-PLAN.md E5.2b-1/E5.2b-2 split). This migration stores
-- only the ladder's own configuration.
--
-- escalation_policy/escalation_tier are TENANT-SCOPED, exactly like
-- on_call_schedule/on_call_participant: tenant_id is denormalised onto each
-- row and is the RLS key (ADR-IDENTITY-002 §6).
--
-- One DDL statement per line/clause, exactly as every migration in this tree
-- does: the derived-schema extractor (internal/kg/extract/schema) captures
-- only the first ADD of a multi-clause ALTER.
CREATE TABLE IF NOT EXISTS escalation_policy (
    policy_id     text        PRIMARY KEY,
    tenant_id     text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    name          text        NOT NULL,
    status        text        NOT NULL DEFAULT 'active',
    row_version   bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_escalation_policy_name_length CHECK (char_length(name) BETWEEN 1 AND 200),
    CONSTRAINT ck_escalation_policy_status CHECK (status IN ('active', 'archived')),
    CONSTRAINT ck_escalation_policy_row_version CHECK (row_version >= 1)
);

-- on_call_schedule_id REFERENCES on_call_schedule (schedule_id), no ON DELETE
-- clause: a tier that pages a schedule someone then deletes would silently
-- point at nothing, which is worse than refusing the delete — an operator
-- must remove or repoint the tier first. This mirrors
-- on_call_participant.schedule_id's own FK declaration style but with the
-- opposite cascade direction, because here the referenced row (the schedule)
-- is NOT owned by the referencing row's parent (the policy) the way a
-- schedule owns its own participants.
--
-- EscalationPolicyStore.AddTier re-verifies on_call_schedule_id against this
-- store's own tenant-scoped connection before this row is written — the
-- foreign key alone cannot enforce a tenant boundary here either, because
-- PostgreSQL's foreign-key trigger runs with the constraint's own privileges
-- and BYPASSES row-level security on the referenced table (ADR-ASSET-001 §6,
-- ADR-ONCALL-001 §5). Without that check, a caller could name another
-- tenant's schedule_id and have the FK accept it, creating a tier that would
-- page across a tenant boundary.
--
-- policy_id REFERENCES escalation_policy (policy_id) ON DELETE CASCADE: a
-- tier has no meaning once the policy itself is gone (the same reasoning
-- on_call_participant.schedule_id's ON DELETE CASCADE already applies).
--
-- uq_escalation_tier_policy_position is DEFERRABLE INITIALLY DEFERRED, for
-- exactly the reason uq_on_call_participant_schedule_position already is:
-- EscalationPolicyStore.ReorderTiers rewrites every tier's position in one
-- transaction to realise an arbitrary permutation (e.g. swapping positions 0
-- and 1), and PostgreSQL checks a non-deferrable unique constraint after each
-- row is written, not at end of statement/transaction — so a plain swap
-- would transiently collide with itself even though the final state is
-- perfectly valid. Deferring the check to COMMIT is the standard, correct fix
-- for exactly this "permutation of a unique column" shape.
CREATE TABLE IF NOT EXISTS escalation_tier (
    tier_id             text        PRIMARY KEY,
    tenant_id           text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    policy_id           text        NOT NULL REFERENCES escalation_policy (policy_id) ON DELETE CASCADE,
    position            integer     NOT NULL,
    on_call_schedule_id text        NOT NULL REFERENCES on_call_schedule (schedule_id),
    wait_seconds        integer     NOT NULL,
    row_version         bigint      NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_escalation_tier_position CHECK (position >= 0),
    CONSTRAINT ck_escalation_tier_wait_seconds_positive CHECK (wait_seconds > 0),
    CONSTRAINT ck_escalation_tier_row_version CHECK (row_version >= 1),
    CONSTRAINT uq_escalation_tier_policy_position UNIQUE (tenant_id, policy_id, position) DEFERRABLE INITIALLY DEFERRED
);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3). tenant_id leads
-- the keyset-pagination index on escalation_policy, the same shape
-- ix_on_call_schedule_tenant_schedule uses. escalation_tier's own ladder-order
-- access path ("this policy's tiers, in escalation order") is served by
-- uq_escalation_tier_policy_position's own backing index (tenant_id,
-- policy_id, position) — no separate index is declared for it, the same
-- reasoning that keeps on_call_participant from declaring one beyond
-- uq_on_call_participant_schedule_position's backing index. on_call_schedule_id
-- gets its own index: it is the FK a delete/status-change on on_call_schedule
-- would otherwise scan escalation_tier without.
CREATE INDEX IF NOT EXISTS ix_escalation_policy_tenant_policy ON escalation_policy (tenant_id, policy_id);
CREATE INDEX IF NOT EXISTS ix_escalation_tier_tenant ON escalation_tier (tenant_id);
CREATE INDEX IF NOT EXISTS ix_escalation_tier_on_call_schedule ON escalation_tier (on_call_schedule_id);

COMMENT ON TABLE escalation_policy IS
    'A named, ordered chain of tiers (escalation_tier) an incident is escalated through (E5.2b-1). CONFIG ONLY: no engine, worker, paging or incident/notification wiring exists yet — that is E5.2b-2.';
COMMENT ON TABLE escalation_tier IS
    'One rung in a policy''s escalation ladder: page on_call_schedule_id''s current on-call user, wait wait_seconds for an acknowledgement, then (E5.2b-2) advance to the next tier. position is 0-based and kept contiguous 0..N-1 by AddTier (appends at N), RemoveTier (closes the gap) and ReorderTiers (rewrites the whole sequence atomically).';
COMMENT ON COLUMN escalation_tier.on_call_schedule_id IS
    'Re-verified to belong to the caller''s tenant on the writer''s own tenant-scoped connection before this row is written — the foreign key alone bypasses row-level security via the FK trigger''s own privileges (ADR-ASSET-001 §6).';
COMMENT ON COLUMN escalation_tier.wait_seconds IS
    'How long the (E5.2b-2) engine waits for an acknowledgement at this tier before advancing to the next one. Must be > 0.';
COMMENT ON COLUMN escalation_tier.position IS
    '0-based rank in escalation order within (tenant_id, policy_id). Kept contiguous by every mutating operation.';

ALTER TABLE escalation_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE escalation_policy FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON escalation_policy;
CREATE POLICY tenant_isolation ON escalation_policy
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE escalation_tier ENABLE ROW LEVEL SECURITY;
ALTER TABLE escalation_tier FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON escalation_tier;
CREATE POLICY tenant_isolation ON escalation_tier
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

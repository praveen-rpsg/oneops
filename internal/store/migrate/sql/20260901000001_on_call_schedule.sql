-- OPS-S<E5.2a> On-call schedules & rotations (who's on call now).
--
-- Two tenant-owned tables, exactly the shape team/team_membership
-- (20260810000001) already established for "a named object with an ordered
-- roster of users": on_call_schedule is the named rotation, on_call_participant
-- is one seat in it.
--
-- WHO IS ON CALL IS NEVER STORED. There is deliberately no per-shift/
-- per-period row here, and no third table recording "who held the seat
-- when" — that is the framework this story explicitly rejects
-- (docs/PLATFORM-BUILD-PLAN.md E5.2a, ADR-ONCALL-001). "Who is on call at
-- time T" is a pure, deterministic function of a schedule's
-- rotation_start_at/handoff_interval_seconds and its current ordered
-- participant list (domain.OnCallRotationIndex) — computed on every read,
-- never written anywhere. These two tables hold only the rotation's OWN
-- configuration and roster, the same reduced-concept discipline
-- maintenance_window's own header comment already applies to a different
-- shape of "don't reify the derived thing" (Vol II §5.3).
--
-- on_call_schedule/on_call_participant are TENANT-SCOPED, exactly like
-- team/team_membership: tenant_id is denormalised onto each row and is the
-- RLS key (ADR-IDENTITY-002 §6).
--
-- One DDL statement per line/clause, exactly as every migration in this tree
-- does: the derived-schema extractor (internal/kg/extract/schema) captures
-- only the first ADD of a multi-clause ALTER.
CREATE TABLE IF NOT EXISTS on_call_schedule (
    schedule_id               text        PRIMARY KEY,
    tenant_id                 text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    name                      text        NOT NULL,
    handoff_interval_seconds  integer     NOT NULL,
    rotation_start_at         timestamptz NOT NULL,
    status                    text        NOT NULL DEFAULT 'active',
    row_version               bigint      NOT NULL DEFAULT 1,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_on_call_schedule_name_length CHECK (char_length(name) BETWEEN 1 AND 200),
    CONSTRAINT ck_on_call_schedule_interval_positive CHECK (handoff_interval_seconds > 0),
    CONSTRAINT ck_on_call_schedule_status CHECK (status IN ('active', 'archived')),
    CONSTRAINT ck_on_call_schedule_row_version CHECK (row_version >= 1)
);

-- user_id REFERENCES app_user (user_id), no ON DELETE clause: app_user is
-- never hard-deleted (deactivated only, ADR-IDENTITY-001 §8.3), so the
-- referenced row always survives. As ADR-ASSET-001 §6 records for every
-- other owner-style reference in this schema, PostgreSQL's foreign-key
-- trigger runs with the constraint's own privileges and BYPASSES row-level
-- security — app_user itself carries no tenant_id at all
-- (ADR-IDENTITY-002 §3.1), so this reference alone cannot even express a
-- tenant boundary, let alone enforce one. OnCallScheduleStore.AddParticipant
-- re-verifies user_id against an ACTIVE `membership` row on this store's own
-- tenant-scoped connection before this row is written — the identical
-- defense IncidentStore.verifyAssigneeExists already applies to
-- incident.assignee_user_id.
--
-- schedule_id REFERENCES on_call_schedule (schedule_id) ON DELETE CASCADE: a
-- participant's seat has no meaning once the rotation itself is gone (the
-- same reasoning alert_rule.asset_id's ON DELETE CASCADE already applies to
-- "this row's owner disappearing empties this row of meaning too").
--
-- uq_on_call_participant_schedule_position is DEFERRABLE INITIALLY DEFERRED:
-- ReorderParticipants rewrites every participant's position in one
-- transaction to realise an arbitrary permutation (e.g. swapping positions 0
-- and 1), and PostgreSQL checks a non-deferrable unique constraint after
-- each row is written, not at end of statement/transaction — so a plain
-- swap would transiently collide with itself even though the final state is
-- perfectly valid. Deferring the check to COMMIT is the standard, correct
-- fix for exactly this "permutation of a unique column" shape and needs no
-- application-side workaround (e.g. a temporary negative-offset pass).
-- uq_on_call_participant_schedule_user carries no such requirement — a
-- participant is never re-pointed at a different user, only added/removed —
-- so it stays immediate.
CREATE TABLE IF NOT EXISTS on_call_participant (
    participant_id  text        PRIMARY KEY,
    tenant_id       text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    schedule_id     text        NOT NULL REFERENCES on_call_schedule (schedule_id) ON DELETE CASCADE,
    user_id         text        NOT NULL REFERENCES app_user (user_id),
    position        integer     NOT NULL,
    row_version     bigint      NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_on_call_participant_position CHECK (position >= 0),
    CONSTRAINT ck_on_call_participant_row_version CHECK (row_version >= 1),
    CONSTRAINT uq_on_call_participant_schedule_position UNIQUE (tenant_id, schedule_id, position) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT uq_on_call_participant_schedule_user UNIQUE (tenant_id, schedule_id, user_id)
);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3). tenant_id leads
-- the keyset-pagination index on on_call_schedule, the same shape
-- ix_incident_tenant_incident uses. on_call_participant's own roster-order
-- access path ("this schedule's participants, in rotation order") is served
-- by uq_on_call_participant_schedule_position's own backing index
-- (tenant_id, schedule_id, position) — no separate index is declared for it,
-- the same reasoning that keeps team_membership from declaring one beyond
-- uq_team_membership_team_user's backing index for its own most common
-- lookup shape.
CREATE INDEX IF NOT EXISTS ix_on_call_schedule_tenant_schedule ON on_call_schedule (tenant_id, schedule_id);
CREATE INDEX IF NOT EXISTS ix_on_call_participant_tenant ON on_call_participant (tenant_id);

COMMENT ON TABLE on_call_schedule IS
    'A named on-call rotation: participants (on_call_participant) hand the on-call duty to one another every handoff_interval_seconds, starting at rotation_start_at (E5.2a). "Who is on call now" is never stored here or anywhere else — it is computed on every read by domain.OnCallRotationIndex.';
COMMENT ON COLUMN on_call_schedule.handoff_interval_seconds IS
    'Seconds between handoffs. UTC/seconds-based rotation math only — no calendar, no timezone, no DST (ADR-ONCALL-001).';
COMMENT ON COLUMN on_call_schedule.rotation_start_at IS
    'Anchors the rotation: at this instant, and every handoff_interval_seconds after it, on-call duty hands to the next participant in position order.';
COMMENT ON TABLE on_call_participant IS
    'One person''s seat in a schedule''s rotation order (E5.2a). position is 0-based and kept contiguous 0..N-1 by AddParticipant (appends at N), RemoveParticipant (closes the gap) and ReorderParticipants (rewrites the whole sequence atomically). Never a per-shift/per-period row — one row per (schedule, participant) regardless of how many times the rotation has cycled past them.';
COMMENT ON COLUMN on_call_participant.user_id IS
    'Re-verified against an ACTIVE membership row on the writer''s own tenant-scoped connection before this row is written — the foreign key alone cannot express a tenant boundary at all, since app_user carries no tenant_id (ADR-IDENTITY-002 §3.1).';
COMMENT ON COLUMN on_call_participant.position IS
    '0-based rank in rotation order within (tenant_id, schedule_id). Kept contiguous by every mutating operation; never a per-shift timestamp.';

ALTER TABLE on_call_schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE on_call_schedule FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON on_call_schedule;
CREATE POLICY tenant_isolation ON on_call_schedule
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE on_call_participant ENABLE ROW LEVEL SECURITY;
ALTER TABLE on_call_participant FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON on_call_participant;
CREATE POLICY tenant_isolation ON on_call_participant
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- OPS-S<incident> (E5.1) Incident work item + governed lifecycle + append-only
-- timeline — the ITSM core. Incident is the ratified reduced noun for a
-- stateful ITSM work item: modelled as a state machine (domain.IncidentStatus),
-- NOT as a reified generic "Ticket" (docs/PLATFORM-BUILD-PLAN.md E5).
--
-- One DDL statement per line/clause, exactly as every migration in this tree
-- does: the derived-schema extractor (internal/kg/extract/schema) captures
-- only the first ADD of a multi-clause ALTER.
--
-- incident is TENANT-SCOPED, exactly like asset (20260816000001): tenant_id
-- is denormalised onto the row and is the RLS key (ADR-IDENTITY-002 §6).
--
-- asset_id REFERENCES asset (asset_id) ON DELETE SET NULL. Unlike alert_rule
-- (ON DELETE CASCADE — a rule about a deleted CI is meaningless), an Incident
-- is itself an operational record with standalone value: a hard Delete of
-- the affected CI must not delete the incident that references it, only
-- clear the link. As ADR-ASSET-001 §6 records for asset_relationship,
-- telemetry_sample, collector_check and alert_rule, PostgreSQL's foreign-key
-- trigger runs with the constraint's own privileges and BYPASSES row-level
-- security on asset — this reference alone cannot stop a caller naming
-- another tenant's asset_id. IncidentStore.Create/Update re-verifies
-- asset_id against its own tenant-scoped connection before any row naming it
-- is written (the same defense AssetStore.verifyOwnerTeamExists and
-- AlertRuleStore.Create already apply).
--
-- assignee_user_id REFERENCES app_user (user_id), no ON DELETE clause:
-- app_user is never hard-deleted (deactivated only, ADR-IDENTITY-001 §8.3),
-- so the referenced row always survives. app_user itself carries no
-- tenant_id at all (ADR-IDENTITY-002 §3.1); IncidentStore re-verifies an
-- assignee via an ACTIVE `membership` row instead, exactly as
-- AssetStore.verifyOwnerUserExists does for Asset.OwnerUserID (E1.2)
-- extended here to an assignee rather than an owner.
CREATE TABLE IF NOT EXISTS incident (
    incident_id       text        PRIMARY KEY,
    tenant_id         text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    title             text        NOT NULL,
    description       text        NOT NULL DEFAULT '',
    severity          text        NOT NULL,
    status            text        NOT NULL DEFAULT 'open',
    asset_id          text        NULL REFERENCES asset (asset_id) ON DELETE SET NULL,
    assignee_user_id  text        NULL REFERENCES app_user (user_id),
    row_version       bigint      NOT NULL DEFAULT 1,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    acknowledged_at   timestamptz NULL,
    resolved_at       timestamptz NULL,
    closed_at         timestamptz NULL,
    CONSTRAINT ck_incident_title_length CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT ck_incident_description_length CHECK (char_length(description) <= 10000),
    CONSTRAINT ck_incident_severity CHECK (severity IN ('critical', 'high', 'medium', 'low')),
    CONSTRAINT ck_incident_status CHECK (status IN ('open', 'acknowledged', 'investigating', 'resolved', 'closed', 'reopened')),
    CONSTRAINT ck_incident_row_version CHECK (row_version >= 1)
);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3). tenant_id leads
-- the keyset-pagination index because it is the RLS-filtered column every
-- List query already carries — mirrors ix_asset_tenant_status_not_retired's
-- reasoning. The owner-style indexes on asset_id/assignee_user_id are
-- partial, the same shape ix_asset_owner_team/ix_asset_owner_user use, since
-- most incidents start unlinked/unassigned.
CREATE INDEX IF NOT EXISTS ix_incident_tenant_incident ON incident (tenant_id, incident_id);
CREATE INDEX IF NOT EXISTS ix_incident_tenant_status ON incident (tenant_id, status);
CREATE INDEX IF NOT EXISTS ix_incident_asset ON incident (asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_incident_assignee ON incident (assignee_user_id) WHERE assignee_user_id IS NOT NULL;

COMMENT ON TABLE incident IS
    'A stateful ITSM work item tracking operational work against a CI (E5.1): governed lifecycle, assignee, optional linked Asset. The ratified reduced noun for this kind of thing — never a reified generic "Ticket".';
COMMENT ON COLUMN incident.asset_id IS
    'The affected Configuration Item, or NULL when unlinked. Re-verified against the writer''s own tenant-scoped connection before write — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6). ON DELETE SET NULL: a hard-deleted CI clears the link, it does not delete the incident.';
COMMENT ON COLUMN incident.assignee_user_id IS
    'The responder accountable for this incident, or NULL when unassigned. Re-verified against an ACTIVE membership row on write, not trusted to the foreign key alone: app_user itself carries no tenant_id (ADR-IDENTITY-002 §3.1).';
COMMENT ON COLUMN incident.acknowledged_at IS
    'Set by SetStatus when acknowledged is (re-)entered. Overwritten on re-entry, not latched — see domain.Incident''s doc comment.';
COMMENT ON COLUMN incident.resolved_at IS
    'Set by SetStatus when resolved is (re-)entered. Overwritten on re-entry (e.g. reopened then resolved again) — see domain.Incident''s doc comment.';
COMMENT ON COLUMN incident.closed_at IS
    'Set by SetStatus when the terminal closed state is entered.';

ALTER TABLE incident ENABLE ROW LEVEL SECURITY;
ALTER TABLE incident FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON incident
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- ---------------------------------------------------------------------------
-- incident_event: append-only timeline of an Incident's case history — who
-- did what, and when (E5.1). Reuses asset_change_history's hardened
-- append-only pattern EXACTLY (20260818000001): ENABLE ALWAYS triggers +
-- REVOKEd UPDATE/DELETE/TRUNCATE, built in from this table's first
-- migration, and registered in postgres.SchemaValidator.immutableAuditTables
-- so boot and the sentinel both verify it.
--
-- UNLIKE asset_change_history, incident_id DOES carry a foreign key to
-- incident. asset_change_history omits one deliberately because Asset CAN be
-- hard-deleted (AssetRepository.Delete) and its history must outlive that
-- (ADR-AUDIT-007 §6.3's reasoning, applied to a tenant-owned table). Incident
-- has NO Delete method at all (E5.1 decision: an incident is operational
-- history from the moment it exists, so "removing" one is SetStatus to
-- closed, mirroring Team's archive-only lifecycle) — the subject this table
-- describes is never gone, so a normal referential-integrity FK is strictly
-- safer here: it catches a bug that would otherwise let a row be written
-- against a nonexistent incident_id, at no cost, since the "outlive a
-- deleted subject" concern that forces asset_change_history's design simply
-- does not arise.
--
-- TENANT-OWNED, RLS-isolated AND append-only at once — the same combination
-- audit_event and asset_change_history already prove is not a contradiction.
CREATE TABLE IF NOT EXISTS incident_event (
    event_id    text        PRIMARY KEY,
    tenant_id   text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    incident_id text        NOT NULL REFERENCES incident (incident_id),
    kind        text        NOT NULL,
    field       text        NOT NULL DEFAULT '',
    old_value   text,
    new_value   text,
    actor       text        NOT NULL,
    row_version bigint      NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_incident_event_kind CHECK (kind IN ('created', 'status_transitioned', 'assigned')),
    CONSTRAINT ck_incident_event_actor CHECK (actor <> ''),
    CONSTRAINT ck_incident_event_row_version CHECK (row_version >= 1)
);

-- The query this store serves: "this incident's timeline, oldest first" —
-- keyset-paginated over event_id (a ULID, and therefore chronological), the
-- same shape ix_asset_change_history_asset uses.
CREATE INDEX IF NOT EXISTS ix_incident_event_incident ON incident_event (tenant_id, incident_id, event_id);

COMMENT ON TABLE incident_event IS
    'Append-only timeline of an Incident''s case history — created, status transitions, assignment changes (E5.1). Unlike asset_change_history, DOES carry a foreign key to its subject: Incident is never hard-deleted, so the row can never be orphaned.';
COMMENT ON COLUMN incident_event.field IS
    'The field this row describes: "" for the single "created" row (no prior field to diff), "status" for a transition, "assignee_user_id" for an assignment change.';

ALTER TABLE incident_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE incident_event FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON incident_event;
CREATE POLICY tenant_isolation ON incident_event
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Append-only enforcement, built in from day one — mirrors
-- asset_change_history's identical hardening verbatim (20260818000001).
-- ENABLE ALWAYS fires regardless of session_replication_role, closing the
-- bypass 20260809000001 proved live against origin-mode triggers.
CREATE OR REPLACE FUNCTION incident_event_immutable() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'incident_event is append-only: % is not permitted', TG_OP;
END;
$$;

CREATE OR REPLACE TRIGGER trg_incident_event_no_row_mutate
    BEFORE UPDATE OR DELETE ON incident_event
    FOR EACH ROW EXECUTE FUNCTION incident_event_immutable();
ALTER TABLE incident_event ENABLE ALWAYS TRIGGER trg_incident_event_no_row_mutate;

CREATE OR REPLACE TRIGGER trg_incident_event_no_truncate
    BEFORE TRUNCATE ON incident_event
    FOR EACH STATEMENT EXECUTE FUNCTION incident_event_immutable();
ALTER TABLE incident_event ENABLE ALWAYS TRIGGER trg_incident_event_no_truncate;

-- Privilege, the second, independent layer (ADR-AUDIT-008 §5's reasoning,
-- applied from the start, exactly as asset_change_history's does). SELECT
-- and INSERT are RETAINED: the request path both records the timeline (on
-- Create/SetStatus/Assign) and reads it back (GET .../timeline) on this same
-- tenant-scoped connection. UPDATE/DELETE/TRUNCATE are revoked because
-- nothing may ever hold them on an append-only table.
REVOKE UPDATE, DELETE, TRUNCATE ON incident_event FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON incident_event FROM oneops_app';
    END IF;
END;
$$;

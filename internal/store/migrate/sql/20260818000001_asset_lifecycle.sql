-- OPS-CMDB-003 (E1.3) CI lifecycle + append-only change history + soft-retire.
-- Extends ADR-ASSET-001. asset remains operational data, not a governance
-- artifact; this migration widens its status lifecycle and adds the record
-- of how a CI got to its current state.
--
-- One ADD/DDL change per statement, exactly as 20260817000001 documents: the
-- derived-schema extractor (internal/kg/extract/schema) captures only the
-- first ADD of a multi-clause ALTER.
--
-- LIFECYCLE (ADR-ASSET-001 §5, extended). §5 modelled AssetStatus as a
-- two-value active/retired pair mirroring TeamStatus. A CMDB CI has states
-- TeamStatus never needed: a server can be racked and named before it ever
-- serves traffic (planned), and taken down for patching without being
-- decommissioned (maintenance). The legal moves — planned->active,
-- planned->retired, active<->maintenance, active->retired,
-- maintenance->retired, and retired->active ("reinstate": unlike
-- app_user.status's terminal deactivated, an Asset's retirement is an
-- operational fact, not a governance act, so a decommissioned CI can be
-- redeployed) — are enforced in Go (domain.AssetStatus.CanTransitionTo,
-- mirroring UserStatus's map-of-edges idiom) and enforced here as the set of
-- values the column accepts; the CHECK constraint cannot express the edges
-- themselves, only the vertices, so an illegal move is still only caught by
-- the application layer's guard (AssetStore.SetStatus) — the same shape
-- ck_user_status/UserStatus.CanTransitionTo already split the work in.
--
-- The existing default of 'active' is UNCHANGED (E1.3 decision): the common
-- case registering a CMDB entry describes infrastructure that already
-- exists and already serves, not one being pre-provisioned.
ALTER TABLE asset DROP CONSTRAINT ck_asset_status;
ALTER TABLE asset ADD CONSTRAINT ck_asset_status
    CHECK (status IN ('planned', 'active', 'maintenance', 'retired'));

-- Soft-retire: a retired asset is excluded from the DEFAULT active-listing
-- view (AssetStore.List's zero-value status filter) but remains individually
-- fetchable, and its relationships/history are untouched — no schema change
-- is needed for this, only a WHERE clause in the store, since ADR-ASSET-001
-- §5 already decided retirement never deletes the row. This partial index
-- accelerates that default view (most queries exclude retired; few ask for
-- retired specifically), the same reasoning ix_asset_owner_team/user already
-- use for their own partial predicates.
CREATE INDEX IF NOT EXISTS ix_asset_tenant_status_not_retired
    ON asset (tenant_id, asset_id) WHERE status <> 'retired';

-- ---------------------------------------------------------------------------
-- asset_change_history: append-only record of what changed on a CI, by whom,
-- and when — the basis for compliance reporting and "what changed before
-- this incident?" forensics (E1.3).
--
-- ONE ROW PER FIELD CHANGED, not one row per API call carrying a JSON diff:
-- a single PATCH that changes three fields writes three rows sharing the
-- same resulting row_version and occurred_at. This is the same shape a
-- dedicated field-history table (e.g. ServiceNow's sys_history_line) uses,
-- and it is what makes "what changed on this field, ever" a plain WHERE
-- rather than a JSONB scan of every row.
--
-- NO FOREIGN KEY on asset_id, DELIBERATELY. A change-history record must
-- outlive the asset it describes — a hard Delete removes the asset (and its
-- relationships, ON DELETE CASCADE) but must not erase the record of how it
-- got there. This mirrors ADR-AUDIT-007 §6.3's identical decision for
-- admin_audit_event's subject columns ("An administrative record must
-- outlive its subject and must never be blocked, cascaded, or nulled by the
-- subject's lifecycle"), applied here to a tenant-owned table instead of a
-- global one.
--
-- TENANT-OWNED, unlike admin_audit_event: this table holds per-tenant CMDB
-- forensic data, not a platform-wide administrative record, so it takes the
-- audit_event shape instead — RLS-isolated AND append-only, both at once
-- (audit_event proves the combination is not a contradiction: TenantOwnedTables
-- already carries it).
CREATE TABLE IF NOT EXISTS asset_change_history (
    change_id   text        PRIMARY KEY,
    tenant_id   text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id    text        NOT NULL,
    kind        text        NOT NULL,
    field       text        NOT NULL DEFAULT '',
    old_value   text,
    new_value   text,
    actor       text        NOT NULL,
    row_version bigint      NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_asset_change_kind CHECK (kind IN ('created', 'updated', 'status_transitioned')),
    CONSTRAINT ck_asset_change_actor CHECK (actor <> ''),
    CONSTRAINT ck_asset_change_row_version CHECK (row_version >= 1)
);

-- The query this store serves: "this asset's history, oldest first" —
-- keyset-paginated over change_id (a ULID, and therefore chronological),
-- the same shape asset_id's own pagination uses.
CREATE INDEX IF NOT EXISTS ix_asset_change_history_asset
    ON asset_change_history (tenant_id, asset_id, change_id);

COMMENT ON TABLE asset_change_history IS
    'Append-only record of every change to a CMDB asset — field, old/new value, actor, resulting row_version (E1.3). No foreign key to asset: the record must outlive its subject, mirroring ADR-AUDIT-007 §6.3.';
COMMENT ON COLUMN asset_change_history.asset_id IS
    'The asset this change describes, as a fact rather than a reference — deliberately no foreign key (see the table comment), so a hard Delete of the asset does not erase or block on its history.';
COMMENT ON COLUMN asset_change_history.field IS
    'The field changed ("" for the single AssetChangeCreated row, which has no prior field to diff). One row per field for an update that touches several.';

ALTER TABLE asset_change_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_change_history FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON asset_change_history;
CREATE POLICY tenant_isolation ON asset_change_history
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- ---------------------------------------------------------------------------
-- Append-only enforcement, built in from day one (this is a NEW table, so
-- unlike audit_event it needs no separate hardening migration): both guards
-- are ENABLE ALWAYS from the start, exactly as 20260809000001_audit_event_
-- harden.sql armed audit_event's after the fact. ENABLE ALWAYS fires
-- regardless of session_replication_role, closing the bypass that migration
-- proved live against origin-mode triggers.
CREATE OR REPLACE FUNCTION asset_change_history_immutable() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'asset_change_history is append-only: % is not permitted', TG_OP;
END;
$$;

CREATE OR REPLACE TRIGGER trg_asset_change_history_no_row_mutate
    BEFORE UPDATE OR DELETE ON asset_change_history
    FOR EACH ROW EXECUTE FUNCTION asset_change_history_immutable();
ALTER TABLE asset_change_history ENABLE ALWAYS TRIGGER trg_asset_change_history_no_row_mutate;

CREATE OR REPLACE TRIGGER trg_asset_change_history_no_truncate
    BEFORE TRUNCATE ON asset_change_history
    FOR EACH STATEMENT EXECUTE FUNCTION asset_change_history_immutable();
ALTER TABLE asset_change_history ENABLE ALWAYS TRIGGER trg_asset_change_history_no_truncate;

-- Privilege, the second, independent layer (a trigger is droppable by one
-- operator ALTER; a privilege can be re-granted, but neither alone is
-- sufficient — ADR-AUDIT-008 §5's reasoning, applied here from the start
-- rather than after the fact). 20260729000001_rls_policies.sql's ALTER
-- DEFAULT PRIVILEGES grants SELECT/INSERT/UPDATE/DELETE on every later table
-- to oneops_app, the role every tenant-scoped connection assumes. SELECT and
-- INSERT are RETAINED: the request path both records history (on Create/
-- Update/SetStatus) and reads it back (GET .../history) on this same
-- tenant-scoped connection, unlike admin_audit_event where a different role
-- appends. UPDATE/DELETE/TRUNCATE are revoked because nothing may ever hold
-- them on an append-only table.
REVOKE UPDATE, DELETE, TRUNCATE ON asset_change_history FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON asset_change_history FROM oneops_app';
    END IF;
END;
$$;

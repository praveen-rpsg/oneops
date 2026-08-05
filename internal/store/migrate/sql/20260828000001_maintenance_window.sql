-- OPS-S3.3a (E3.3a) maintenance_window — operator-declared, one-shot
-- [starts_at, ends_at) suppression window over a single asset's incident/
-- notify side effect (ADR-ALERTING-002). Planned maintenance must not page.
--
-- TARGET MODEL (ADR-ALERTING-002 §1): a single asset_id, not a tag/label
-- selector and not a dependency-aware set. Dependency-aware suppression
-- (silencing a CI's downstream/dependent CIs too) is E3.3b and explicitly
-- deferred — no CMDB graph traversal is implied or required by this table.
--
-- ONE-SHOT ONLY: [starts_at, ends_at) is a single explicit interval. A
-- recurring maintenance schedule (e.g. "every Sunday 02:00-04:00 UTC") is
-- deliberately out of scope — it would require a scheduling/recurrence
-- framework this story does not build (docs/PLATFORM-BUILD-PLAN.md E3.3a).
--
-- maintenance_window is TENANT-OWNED, exactly like alert_rule/incident
-- (20260824000001, 20260825000001): tenant_id is denormalised onto the row
-- and is the RLS key (ADR-IDENTITY-002 §6).
--
-- asset_id REFERENCES asset (asset_id) ON DELETE CASCADE, like alert_rule
-- (20260824000001) rather than incident's ON DELETE SET NULL: a maintenance
-- window describes a period over a SPECIFIC CI's alerting, not standalone
-- operational history in its own right — a window whose CI no longer exists
-- has nothing left to suppress, exactly the reasoning alert_rule's own
-- header comment already applies. As ADR-ASSET-001 §6 records, PostgreSQL's
-- foreign-key trigger runs with the constraint's own privileges and BYPASSES
-- row-level security on asset — this reference alone cannot stop a caller
-- naming another tenant's asset_id. MaintenanceWindowStore.Create
-- re-verifies asset_id against its own tenant-scoped connection before any
-- row naming it is written (the same defense AlertRuleStore.Create already
-- applies).
--
-- NO append-only hardening: unlike incident_event/asset_change_history, a
-- maintenance window is not case history — an operator may cancel/withdraw
-- one at any time (Delete), before or during it. There is no lifecycle
-- beyond "exists" / "does not exist".
--
-- suppressed_count/last_suppressed_at are the visible, queryable record that
-- a suppression happened (E3.3a's success criterion: suppression must be
-- observable, never a silent drop). Both are written ONLY by the leader-gated
-- evaluator's privileged background path (alerting.MaintenanceWindowChecker.
-- Suppress), atomically alongside the read that finds the window active —
-- see that method's own doc comment for why this is one statement, not a
-- check then a separate write.
CREATE TABLE IF NOT EXISTS maintenance_window (
    window_id          text        PRIMARY KEY,
    tenant_id          text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id           text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    starts_at          timestamptz NOT NULL,
    ends_at            timestamptz NOT NULL,
    reason             text        NOT NULL DEFAULT '',
    created_by         text        NOT NULL,
    suppressed_count   bigint      NOT NULL DEFAULT 0,
    last_suppressed_at timestamptz NULL,
    row_version        bigint      NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_maintenance_window_bounds CHECK (ends_at > starts_at),
    CONSTRAINT ck_maintenance_window_reason_length CHECK (char_length(reason) <= 500),
    CONSTRAINT ck_maintenance_window_created_by CHECK (created_by <> ''),
    CONSTRAINT ck_maintenance_window_suppressed_count CHECK (suppressed_count >= 0),
    CONSTRAINT ck_maintenance_window_row_version CHECK (row_version >= 1)
);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3). tenant_id leads
-- the keyset-pagination index, the same shape ix_incident_tenant_incident
-- uses. The second index is the query this table actually serves at
-- evaluation time — alerting.MaintenanceWindowChecker.Suppress's
-- WHERE tenant_id = $1 AND asset_id = $2 AND starts_at <= $3 AND ends_at > $3
-- — led by (tenant_id, asset_id) so it is selective per-asset, then ends_at
-- so a half-open "still active" check can use the index directly.
CREATE INDEX IF NOT EXISTS ix_maintenance_window_tenant_window ON maintenance_window (tenant_id, window_id);
CREATE INDEX IF NOT EXISTS ix_maintenance_window_active_lookup ON maintenance_window (tenant_id, asset_id, ends_at);

COMMENT ON TABLE maintenance_window IS
    'An operator-declared, one-shot [starts_at, ends_at) window during which a single asset''s alert firing->incident/notify side effect is suppressed (E3.3a, ADR-ALERTING-002). Never suppresses the underlying firing-state derivation itself, only the side effect. Dependency-aware suppression is E3.3b, deferred.';
COMMENT ON COLUMN maintenance_window.asset_id IS
    'The single Configuration Item this window covers (ADR-ALERTING-002 §1 — not a tag selector, not dependency-aware). Re-verified against the writer''s own tenant-scoped connection before write — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6). ON DELETE CASCADE: a hard-deleted CI has nothing left to suppress.';
COMMENT ON COLUMN maintenance_window.starts_at IS
    'Inclusive lower bound of the half-open suppression interval: a firing at exactly starts_at IS suppressed.';
COMMENT ON COLUMN maintenance_window.ends_at IS
    'Exclusive upper bound of the half-open suppression interval: a firing at exactly ends_at is NOT suppressed — it pages normally on the evaluation tick that observes the window has ended.';
COMMENT ON COLUMN maintenance_window.created_by IS
    'The authenticated actor who declared this window, set by the repository from ActorFrom(ctx) at Create time — never accepted from caller-supplied request data (mirrors IncidentStore.Create''s discipline for its own timeline actor).';
COMMENT ON COLUMN maintenance_window.suppressed_count IS
    'How many evaluation ticks found this window active for a would-be firing and suppressed its incident/notify side effect. Written only by the evaluator''s privileged background path, never through the caller-facing Create/Delete path. The visible, queryable record that a suppression happened.';
COMMENT ON COLUMN maintenance_window.last_suppressed_at IS
    'When this window last suppressed a firing, or NULL if it never has.';

ALTER TABLE maintenance_window ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_window FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON maintenance_window;
CREATE POLICY tenant_isolation ON maintenance_window
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

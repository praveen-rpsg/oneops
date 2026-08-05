-- OPS-S3.3b (E3.3b) dependency_suppression — the visible, queryable record
-- that an ok->firing transition on affected_asset_id was suppressed because
-- it transitively depends on root_asset_id, which was itself "down" at the
-- time (ADR-ALERTING-003). The dependency-aware counterpart to
-- maintenance_window's suppressed_count/last_suppressed_at (20260828000001,
-- E3.3a) — same visibility discipline, different trigger: an inferred
-- topology fact instead of an operator's explicit declaration.
--
-- There is deliberately no reified Alert/Event/Signal here
-- (docs/PLATFORM-BUILD-PLAN.md §4, Vol II §5.3): this row is bookkeeping
-- about a SUPPRESSION DECISION the evaluator made, exactly the same
-- reduced-concept discipline maintenance_window's header comment already
-- states for its own suppressed_count.
--
-- UNLIKE maintenance_window, THERE IS NO OPERATOR-FACING CREATE. A
-- dependency suppression is never declared — it is inferred, every
-- evaluation tick, from the CMDB graph (asset_relationship) and the current
-- firing-state of every asset in a rule's dependency closure
-- (alert_rule.enabled/last_state). The only writer is the leader-gated
-- evaluator's privileged background path
-- (alerting.DependencySuppressionChecker.Suppress); there is no Create/
-- Update/Delete route at all, mirroring alert_rule.last_state's
-- evaluator-only write discipline (ADR-ALERTING-001 Decision 4).
--
-- affected_asset_id/root_asset_id REFERENCE asset (asset_id) ON DELETE
-- CASCADE, exactly like maintenance_window.asset_id: a suppression record
-- naming a hard-deleted CI on either end has nothing left to describe. As
-- ADR-ASSET-001 §6 records, PostgreSQL's foreign-key trigger runs with the
-- constraint's own privileges and BYPASSES row-level security on asset —
-- this reference alone cannot stop a caller naming another tenant's
-- asset_id. DependencySuppressionStore.Suppress never accepts a
-- caller-supplied asset_id at all (both come from the CMDB traversal and
-- the alert_rule scan, run on this row's own resolved tenant_id), so there
-- is no equivalent of MaintenanceWindowStore.Create's asset-existence
-- re-verification to add here.
--
-- suppressed_count/last_suppressed_at are written atomically, one row per
-- (tenant_id, affected_asset_id, root_asset_id): the same asset pair
-- suppressed on two different ticks increments the SAME row rather than
-- inserting a second one, exactly the "never a silent drop" success
-- criterion maintenance_window.suppressed_count already states, applied to
-- an INSERT ... ON CONFLICT DO UPDATE upsert instead of an UPDATE ... FROM
-- CTE (there is no pre-existing row to find-and-lock the first time a pair
-- suppresses; Postgres serialises the upsert itself under the unique
-- index).
CREATE TABLE IF NOT EXISTS dependency_suppression (
    suppression_id     text        PRIMARY KEY,
    tenant_id          text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    affected_asset_id  text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    root_asset_id      text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    suppressed_count   bigint      NOT NULL DEFAULT 0,
    last_suppressed_at timestamptz NULL,
    row_version        bigint      NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_dependency_suppression_no_self     CHECK (affected_asset_id <> root_asset_id),
    CONSTRAINT ck_dependency_suppression_count       CHECK (suppressed_count >= 0),
    CONSTRAINT ck_dependency_suppression_row_version CHECK (row_version >= 1),
    -- The atomic upsert's own conflict target (ON CONFLICT (tenant_id,
    -- affected_asset_id, root_asset_id)): one row per suppressing pair per
    -- tenant, never a growing history of near-duplicate rows.
    CONSTRAINT uq_dependency_suppression UNIQUE (tenant_id, affected_asset_id, root_asset_id)
);

-- tenant_id leads, for the same keyset-pagination shape every other
-- tenant-owned table's simple listing index uses
-- (ix_maintenance_window_tenant_window). The write path itself (the upsert)
-- is served by uq_dependency_suppression's own unique index, which already
-- leads with (tenant_id, affected_asset_id) — no second lookup index is
-- needed for that.
CREATE INDEX IF NOT EXISTS ix_dependency_suppression_tenant ON dependency_suppression (tenant_id, suppression_id);

COMMENT ON TABLE dependency_suppression IS
    'The visible, queryable record that an ok->firing transition on affected_asset_id was suppressed because it transitively depends (depends_on/runs_on only) on root_asset_id, which was down at the time (E3.3b, ADR-ALERTING-003). Never a reified Alert/Event/Signal — bookkeeping about a suppression decision, mirroring maintenance_window.suppressed_count. Written ONLY by the evaluator''s privileged background path; there is no operator-facing create route.';
COMMENT ON COLUMN dependency_suppression.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason maintenance_window.tenant_id is (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN dependency_suppression.affected_asset_id IS
    'The CI whose ok->firing transition was suppressed (X in ADR-ALERTING-003). ON DELETE CASCADE: a hard-deleted CI has nothing left to describe.';
COMMENT ON COLUMN dependency_suppression.root_asset_id IS
    'The CI, transitively reachable from affected_asset_id via depends_on/runs_on edges only, that was down at the time of suppression (Y in ADR-ALERTING-003). "Down" means Y itself has >=1 enabled, firing alert_rule.';
COMMENT ON COLUMN dependency_suppression.suppressed_count IS
    'How many suppressed evaluation ticks this (affected, root) pair absorbed. Written only by the evaluator''s privileged background path via an atomic upsert — never through a caller-facing write route, because there is none.';
COMMENT ON COLUMN dependency_suppression.last_suppressed_at IS
    'When this (affected, root) pair last suppressed a firing, or NULL if it never has.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring maintenance_window's policy
-- verbatim).
ALTER TABLE dependency_suppression ENABLE ROW LEVEL SECURITY;
ALTER TABLE dependency_suppression FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON dependency_suppression;
CREATE POLICY tenant_isolation ON dependency_suppression
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

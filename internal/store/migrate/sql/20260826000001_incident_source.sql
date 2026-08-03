-- OPS-S<e4.1> (E4.1) Incident.source: manual vs alert-correlated (E4.1),
-- extends E5.1's incident table exactly as 20260819000001 extended asset.
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (see 20260819000001's identical
-- rule): the derived-schema extractor (internal/kg/extract/schema) captures
-- only the first ADD of a multi-clause ALTER.
--
-- source is CLOSED (manual/alert), unlike asset.source (open, integration
-- names are unbounded): incident correlation (internal/alerting's evaluator,
-- ADR-TENANCY-012) decides what to do based on this value — "is this an
-- alert-sourced incident eligible for auto-linking" — so a caller minting an
-- arbitrary third value would silently opt an incident out of, or into,
-- correlation in a way nothing else guards against. It is written ONLY at
-- Create (domain.NewIncident/NewAlertIncident); there is no Update path for
-- it, the same immutable-after-create treatment domain.Incident.Source's doc
-- comment describes.
ALTER TABLE incident ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'manual';
ALTER TABLE incident ADD CONSTRAINT ck_incident_source CHECK (source IN ('manual', 'alert'));

-- An alert-sourced incident always names the CI the firing rule watches
-- (domain.NewAlertIncident requires assetID) — this constraint makes that a
-- schema-level guarantee, not merely an application one, the same
-- application-then-database double enforcement ck_incident_title_length
-- already sets the pattern for.
ALTER TABLE incident ADD CONSTRAINT ck_incident_alert_source_has_asset
    CHECK (source <> 'alert' OR asset_id IS NOT NULL);

-- The correlation invariant (E4.1's no-duplicate-incident requirement): at
-- most one OPEN (not resolved/closed) alert-sourced incident per
-- (tenant, asset), enforced at the database, not merely by application
-- logic that happened to check first — the same idempotency-by-constraint
-- shape ux_asset_tenant_source_external_ref gives AssetStore.Upsert
-- (20260819000001). postgres.IncidentStore.FindOrCreateOpenAlertIncident
-- races an INSERT ... ON CONFLICT (tenant_id, asset_id) WHERE ... DO NOTHING
-- against exactly this index, so two rules firing on the same asset
-- concurrently, from different evaluator goroutines, can never both believe
-- they are the first: the second blocks on the first's uncommitted insert,
-- then sees the conflict once it commits.
--
-- A manually-filed incident (source = 'manual') never matches this
-- predicate, so an operator's own incident on the same asset never blocks,
-- or is confused with, a correlated one.
CREATE UNIQUE INDEX IF NOT EXISTS ux_incident_open_alert_per_asset
    ON incident (tenant_id, asset_id)
    WHERE source = 'alert' AND status NOT IN ('resolved', 'closed');

COMMENT ON COLUMN incident.source IS
    'manual (operator-filed) or alert (E4.1 correlation-created/linked). Closed vocabulary — internal/alerting''s evaluator decides eligibility for auto-linking on this value. Written once at Create, never changed.';

-- incident_event.kind gains alert_note (E4.1): a free-text correlation note
-- ("alert <metric> fired"/"... recovered"), written only by the evaluator,
-- never by an HTTP handler — see domain.IncidentEventKind's doc comment.
-- CHECK constraints cannot be alterations in place; drop and recreate widens
-- the same constraint rather than adding a second one.
ALTER TABLE incident_event DROP CONSTRAINT ck_incident_event_kind;
ALTER TABLE incident_event ADD CONSTRAINT ck_incident_event_kind
    CHECK (kind IN ('created', 'status_transitioned', 'assigned', 'alert_note'));

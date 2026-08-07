# ADR-SOC-001 — Security observations are append-only, tenant-isolated facts on a sibling hypertable — not a reified security event

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Decider** | Acting CTO |
| **Related** | ADR-TELEMETRY-001 (the telemetry hypertable + pluggable interface this mirrors), ADR-IDENTITY-002 §6 (denormalized tenant_id as the RLS key), ADR-ASSET-001 §6 (asset_id re-verification), PLATFORM-BUILD-PLAN §4:119-133 (Alert/Event/Signal are derived, never stored nouns), `docs/PLATFORM-BUILD-PLAN.md` E8.1a. **Foundation of the E8 SOC epic; detection/correlation is E8.1b (separate).** |

## Context

E8 (SOC) begins with ingesting security signals. The instinct is a "security
event" store, but Vol III §4 / PLATFORM-BUILD-PLAN §4 classify *Event* as a
false-noun — a derived projection, never a stored entity — exactly as the ops
side avoided a reified `Event` (telemetry is *samples*, alerting is
*firing-state*, correlation is an *incident link*). SOC must hold the same line.

## Decision

**Security signals are ingested as `security_observation` — an append-only,
tenant-isolated FACT, in the same conceptual category as `telemetry_sample`,
on a SIBLING TimescaleDB hypertable. It is not a reified `SecurityEvent`/
`Alert`/`Finding`: no status, no lifecycle, no update/delete, no management
API.** Detection and correlation (turning observations into incidents) are a
separate later story (E8.1b) and will be *derived* passes, never stored nouns.

### Sibling hypertable, not `telemetry_sample` reuse

Telemetry samples are numeric measurements (`value float`); security
observations are categorical/attributed facts — `observation_type`, `source`,
`severity`, free-form `attributes` — with no numeric value to share. Forcing
them into the sample shape would be a lossy compromise. So `security_observation`
**reuses the telemetry patterns, not its table**:

- A hypertable on `observed_at` (`create_hypertable`), same as telemetry.
- FORCE + ENABLE row-level security with the fail-closed `tenant_isolation`
  policy (`tenant_id = current_setting('app.tenant_id', true)`, USING +
  WITH CHECK), registered in `postgres.TenantOwnedTables`.
- Natural key `(tenant_id, asset_id, observation_type, source, observed_at)` —
  tenant_id is IN the key, so no `serverGeneratedIdentifiers` justification is
  needed and cross-tenant key collision is impossible. `ON CONFLICT DO NOTHING`
  (idempotent re-ingest), the same accepted semantics telemetry documents.
- Ingestion over the tenant-scoped `appPool` (NOT the privileged pool), with
  `asset_id` re-verified against the caller's own RLS-confined connection before
  write (mirrors `TelemetryStore.WriteSamples`, ADR-ASSET-001 §6).
- A pluggable `SecurityObservationRepository` (WriteObservations / QueryRange /
  QueryRangeForTenant), mirroring `TelemetryRepository`.

Endpoints `POST` + `GET /v1/admin/security-observations` at the `PermAdmin`
tier (matching telemetry; `security_observation` is tenant-owned, not a global
registry). `Severity` is its own closed type (`info|low|medium|high|critical`)
rather than reusing `AlertSeverity` (`critical|warning|info`) — the
vocabularies differ.

## Consequences

**Guaranteed.** SOC has a tenant-isolated, append-only ingestion foundation
that behaves exactly like telemetry for isolation, retention, and query, proven
by a two-tenant isolation test and swept into the existing RLS/uniqueness/
tenant-column arch guards automatically (no exemption needed). It introduces no
reified security noun, so E8.1b's detection/correlation can be derivations over
these facts — the same shape as alerting over telemetry.

**Not built here / deferred.** No detection rules, no correlation, no incident
creation, no background worker, no status/lifecycle. `QueryRangeForTenant`
exists for interface parity with the future E8.1b consumer but is wired to
nothing privileged today. Retention/legal-hold and chain-of-custody (the E8
edge cases) are not addressed yet — observations inherit telemetry's retention
posture until a SOC-specific decision is made.

## Enforcement

- `TestSecurityObservationIsolation_RLSByTenant`,
  `..._RejectsCrossTenantOrNonexistentAsset`, `..._IngestQueryRoundTrip`,
  `TestSecurityObservation_IsARealHypertable`
  (`internal/store/postgres/security_observation_store_integration_test.go`,
  `//go:build integration`, `TEST_DATABASE_URL`) — the isolation assertions are
  the security gate.
- `TestRLS_EveryOwnedTableIsProtected` + `TestUniquenessIsScopedToTenant` +
  the tenant-column guards sweep the new table in automatically; they must stay
  green.
- Any move to add a status/lifecycle column, an update/delete API, or a
  reified `SecurityEvent` contradicts this ADR and must supersede it — the
  append-only-fact discipline is the decision, not an implementation detail.

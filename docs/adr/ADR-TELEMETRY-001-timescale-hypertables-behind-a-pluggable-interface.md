# ADR-TELEMETRY-001 — Telemetry time-series storage is Postgres + TimescaleDB hypertables, behind a pluggable ingestion interface

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-03 |
| **Decider** | Acting CTO (founder-approved, D1) |
| **Binding on** | `internal/domain/telemetry.go`, `internal/store/postgres/telemetry_store.go`, `internal/httpapi/handlers_telemetry.go`, `internal/store/migrate/sql/20260821000001_telemetry.sql`, `postgres.TenantOwnedTables`, `docker-compose.yml`, `Makefile` (`PG_IMAGE`) |
| **Related** | ADR-TENANCY-001 (row-level isolation model), ADR-TENANCY-002 (isolation is a property of wiring), ADR-ASSET-001 §6 (foreign-key checks bypass RLS on the referenced table — the same defense reused here for `asset_id`) |
| **Resolves** | `docs/PLATFORM-BUILD-PLAN.md` §6 D1 — "Time-series storage (E2): Postgres partitioning vs dedicated TSDB" |

## Context

E2.1 needs to ingest and query metric time-series tied to Configuration
Items — the first monitoring signal, and the spine E3 alerting will derive
from. The build plan flagged telemetry as "the scale extreme": high write
volume, range queries over time windows, and (later, E2.1b) retention and
downsampling. Two live constraints shape the decision:

1. **Tenant isolation is currently RLS-native.** Every tenant-owned table in
   this schema (`asset`, `team`, `notification`, ...) is isolated by
   PostgreSQL row-level security on the same connection pool, re-derived at
   the database layer rather than trusted from a caller (ADR-TENANCY-001/002).
   A new datastore that cannot carry that policy would be a second isolation
   model to audit and prove correct, for one table.
2. **Operational surface is currently one datastore.** Backup, DR, schema
   migration and the audit-integrity sweeper all reason about one Postgres
   cluster. Adding a dedicated TSDB adds a second thing to back up, restore,
   monitor and reason about in a DR drill, for a first increment whose actual
   scale is not yet measured against real traffic.

## Decision

**D1: Postgres + TimescaleDB hypertables, behind a pluggable ingestion
interface (`domain.TelemetryRepository`), not plain partitioning and not a
dedicated TSDB — founder-approved.**

1. **A hypertable is a regular Postgres table under the hood.** TimescaleDB
   partitions `telemetry_sample` into per-time-range chunks transparently;
   nothing about `ENABLE ROW LEVEL SECURITY` / `FORCE ROW LEVEL SECURITY` / a
   `USING`/`WITH CHECK` policy changes for a hypertable versus any other
   table in this schema. `telemetry_sample` is in `postgres.TenantOwnedTables`
   and carries the identical fail-closed `tenant_isolation` policy every
   other tenant-owned table does (`20260821000001_telemetry.sql`,
   `postgres.TestEveryGlobalRegistryRoute_RequiresPlatformAdmin`'s sibling
   sweeps for tenant-owned tables). Isolation stays **one model**, not two.

2. **One datastore, one DR story.** No second stateful dependency is added to
   backup (`make db-backup`), `dr-drill`, or the audit-integrity sweeper's
   worldview. `Makefile`'s `PG_IMAGE` and `docker-compose.yml`'s `postgres`
   service both point at the same Timescale-enabled image
   (`timescale/timescaledb:2.19.3-pg16-oss`) — pg_dump/pg_restore ship in it
   unchanged from the plain `postgres:16` image they replace.

3. **The domain/store split is the swap-later hedge, not a new abstraction
   invented for this decision.** `domain.TelemetryRepository`
   (`WriteSamples`/`QueryRange`) is the same port/adapter shape every other
   store in this schema already uses (`domain.AssetRepository` /
   `postgres.AssetStore`, `domain.TeamRepository` / `postgres.TeamStore`).
   Nothing in the domain type (`domain.Sample`) or the interface carries a
   Timescale-specific parameter (no chunk interval, no continuous-aggregate
   handle) — a future implementation of the same interface over a dedicated
   TSDB is a new `postgres`-sibling package, not a change to
   `internal/httpapi` or `internal/domain`.

4. **Plain partitioning was rejected**, not because it could not isolate
   correctly (it could — a partitioned table is still a table, RLS still
   applies), but because TimescaleDB gives the same RLS-native property
   *and* the chunk management, time-bucketed query planning, and (E2.1b)
   continuous-aggregate/retention-policy primitives this domain needs
   natively, for the cost of one extension rather than a hand-rolled
   partitioning scheme this platform would maintain itself.

5. **A dedicated TSDB (e.g. Prometheus, InfluxDB) was rejected for this
   increment**, not on technical merit but on the two constraints above: it
   would need its own isolation model proven correct (this schema's RLS
   guards, and the sweeps that verify them, do not reach an external store),
   and its own backup/DR story, for a first increment whose real scale is
   unmeasured. The interface exists precisely so this is not a permanent
   foreclosure — see Consequences.

## Deploy requirement — `CREATE EXTENSION`

`CREATE EXTENSION IF NOT EXISTS timescaledb` requires a role that can install
extensions: a superuser, or `CREATE` privilege on the database if the
extension's control file marks it trusted (it does not, as of 2.19). The
docker-compose/local role (`oneops`, the Timescale image's bootstrap
superuser) and CI's migration role both have it. **A production deployment
must grant this once, out of band, before this migration runs for the first
time** — either by running as a superuser for that one migration, or by
having a DBA pre-install the extension. This is a one-time bootstrap step,
not a standing privilege the application role needs afterward.

`CREATE EXTENSION ... SCHEMA public` is explicit, not incidental — see
Enforcement below for the defect this closes.

## Alternatives considered

- **Plain Postgres native partitioning** (declarative `PARTITION BY RANGE` on
  `ts`, hand-managed partitions). Rejected: point 4 above — TimescaleDB gives
  the same RLS-native property for less maintenance, plus primitives E2.1b
  needs that a hand-rolled scheme would have to reinvent.
- **A dedicated TSDB, in-process or as a sidecar service.** Rejected for this
  increment — point 5 above. The interface keeps this open as a later
  migration, not a redesign.
- **A single flat table, no partitioning at all.** Rejected outright: this is
  explicitly the platform's highest-volume data path
  (`docs/PLATFORM-BUILD-PLAN.md` §3); an unpartitioned table's query planner
  cost and vacuum/maintenance cost both degrade with exactly the write volume
  telemetry is expected to have.

## Consequences

**What is now guaranteed.** `telemetry_sample` is tenant-isolated by the same
fail-closed RLS policy every other tenant-owned table carries; `asset_id` is
re-verified against the writer's own tenant-scoped connection before any row
naming it is written, the same defense `AssetStore.CreateRelationship`
already applies to `asset_relationship`'s endpoints (ADR-ASSET-001 §6) —
because PostgreSQL's foreign-key trigger on `asset_id REFERENCES asset
(asset_id)` runs with the constraint's own privileges and bypasses
row-level security on the referenced table, exactly as it does there.
Ingestion and query are both bounded
(`domain.MaxTelemetryIngestBatch` = 1000 samples/call;
`domain.MaxTelemetryQueryLimit` = 5000 samples/page, keyset-paginated).

**What is not claimed.** This ADR covers ingestion, storage and a bounded
range query only. Retention policy, downsampling and continuous aggregates
(TimescaleDB's `add_retention_policy`/`add_continuous_aggregate_policy`) are
explicitly deferred to **E2.1b**, a separately gated follow-on — landing them
now would conflate the storage decision with a data-lifecycle policy decision
that deserves its own review. Agentless collectors (SNMP, cloud pollers),
agent/push ingestion, log ingestion, distributed tracing and alerting (E2.2,
E2.3, E2.4, E3) are later increments that will write through this same
interface, not extend this ADR.

## Enforcement

- `postgres.TenantOwnedTables` includes `telemetry_sample`;
  `SchemaValidator` and `OwnershipValidator` sweep it automatically, the same
  as every other tenant-owned table.
- `arch.TestServerWiringUsesTenantScopedPool` /
  `TestEveryServerCapability_IsWiredAtTheCompositionRoot` require
  `SetTelemetry` to be wired from `appPool` at the composition root.
- `postgres.TestTelemetryIsolation_RLSByTenant` (integration) is the live
  proof for tenant isolation: tenant B's `QueryRange` naming tenant A's own
  `asset_id` and metric directly returns nothing — row-level security
  filters on `tenant_id`, not on the caller's chosen filters.
- `postgres.TestTelemetryStore_RejectsCrossTenantOrNonexistentAsset`
  (integration) is the live proof for point 3 above: a batch naming another
  tenant's real `asset_id`, or an `asset_id` that does not exist at all, is
  rejected per-sample and writes no row.
- `postgres.TestTelemetrySample_IsARealHypertable` (integration) proves the
  D1 storage decision actually landed — `telemetry_sample` is registered in
  `timescaledb_information.hypertables`, not merely a plain table with the
  right name.
- **`CREATE EXTENSION ... SCHEMA public` and `public.create_hypertable(...)`
  are both explicitly schema-qualified — proven necessary live, not
  defensive style.** `timescaledb` is a per-*database* singleton extension
  (one `pg_extension` row for the whole database, however many schemas run
  this migration against it), so whichever caller installs it first fixes
  where its functions physically live. This repository's own integration
  suite runs several packages' migrations concurrently, each against its own
  private schema (`pgstore_itest`, `httpapi_itest`, several
  test-function-named schemas in `internal/httpapi`) with `search_path`
  narrowed to just that schema. Without the explicit `SCHEMA public` /
  `public.` qualification, the second and later packages to reach this
  migration failed with `function create_hypertable(...) does not exist`
  even though the extension itself was already installed — it had simply
  landed in the *first* package's schema, which is invisible to every other
  package's narrowed `search_path`. Pinning the schema and qualifying every
  call makes the outcome independent of which caller runs first; verified
  live by the full integration suite (`internal/httpapi`, `internal/ops`,
  `internal/store/postgres`, `internal/kg/extract/schema`) passing together
  under `-race`, and by `timescaledb_information.hypertables` showing
  `telemetry_sample` independently registered under five different schemas
  after that run.

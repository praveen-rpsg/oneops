# ADR-TELEMETRY-002 — Retention and downsampling are application-level workers over Apache-licensed primitives, not TimescaleDB's licensed `add_retention_policy`/continuous-aggregate features

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-03 |
| **Decider** | Acting CTO (implementer finding, surfaced and resolved within scope) |
| **Binding on** | `internal/store/migrate/sql/20260822000001_telemetry_retention.sql`, `internal/store/postgres/telemetry_retention_worker.go`, `internal/store/postgres/telemetry_rollup_worker.go`, `internal/store/postgres/telemetry_store.go`, `internal/domain/telemetry.go`, `internal/domain/setting.go`, `postgres.TenantOwnedTables` |
| **Related** | ADR-TELEMETRY-001 (Postgres + TimescaleDB hypertables behind `domain.TelemetryRepository`; pins the `-oss` image), ADR-TENANCY-001/002 (RLS isolation model), ADR-GOV-005 (the `governance_required_approvals` Setting precedent this reuses) |
| **Resolves** | `docs/PLATFORM-BUILD-PLAN.md` E2.1b — "Retention policy + downsampling/continuous aggregates over telemetry_sample" |

## Context

E2.1 shipped `telemetry_sample` as an unbounded-growth hypertable and
explicitly deferred retention and downsampling to E2.1b (ADR-TELEMETRY-001,
"What is not claimed"). E2.1b's brief named TimescaleDB's own primitives for
both: `add_retention_policy`/`add_continuous_aggregate_policy` and a
`CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)` continuous
aggregate.

**Both are unavailable on this platform's pinned image.**
`timescale/timescaledb:2.19.3-pg16-oss` — pinned by ADR-TELEMETRY-001
specifically because it is "the Apache-2.0-licensed build (no
Timescale-License-only features)" — reports `timescaledb.license = apache`.
Verified live against the running local instance:

```
=> SELECT public.add_retention_policy('t'::regclass, drop_after => interval '90 days');
ERROR:  function "add_retention_policy" is not supported under the current "apache" license
HINT:  Upgrade your license to 'timescale' to use this free community feature.

=> CREATE MATERIALIZED VIEW t_5m WITH (timescaledb.continuous) AS ...;
ERROR:  functionality not supported under the current "apache" license.
```

Both features require the Timescale License (a source-available, non-OSI
license with field-of-use restrictions — e.g. it prohibits offering
TimescaleDB itself as a hosted service to third parties), not Apache 2.0.
Switching the pinned image to the community build
(`timescale/timescaledb:2.19.3-pg16`, no `-oss` suffix) would relicense a
dependency ADR-TELEMETRY-001 deliberately chose for its license, which is a
decision for the CTO/founder, not something this story silently changes. This
ADR does NOT make that change; it records the alternative that stays inside
the existing decision.

## Decision

**D2: Retention and downsampling are built from Apache-licensed TimescaleDB
primitives, driven by application workers, not from the licensed
scheduler/continuous-aggregate features.**

`drop_chunks` (chunk management) and `time_bucket` (bucketed aggregation) are
both Apache-licensed — verified live, both execute successfully against the
pinned `-oss` image. `TelemetryRetentionWorker` and `TelemetryRollupWorker`
(`internal/store/postgres`) call them directly on their own schedule, the
same shape `events.RetentionWorker` already uses for webhook deliveries: a
`Run(ctx) error` loop, registered in `cmd/controlplane/main.go`'s `workers`
slice and started only under `ops.RunAsLeader`, exactly like every other
background worker in this codebase.

**D3: the raw retention horizon is a PLATFORM-scoped Setting, not a
per-tenant one; per-tenant retention is a deferred follow-up.**

`domain.TelemetryRawRetentionDaysKey` ("telemetry_raw_retention_days") is
added to `domain.SettingDefinitions` (int, 1–3650, default 90) — reusing the
same registry `governance_required_approvals` established (ADR-GOV-005), so
no new admin API surface is needed; the existing generic
`GET`/`PUT /admin/settings/{key}` already exposes it. It is read ONLY from
`domain.SystemTenantID`'s stored override; a value stored under any other
tenant is accepted (the generic `Setting`/`SettingRepository` machinery has
no way to know "this key is platform-scoped") but has no effect. This is
because `drop_chunks` on `telemetry_sample` is a single, whole-hypertable
operation: dropping a chunk older than the cutoff drops it for every
tenant's rows in that time range at once. There is no single retention
operation that could honour ten tenants' ten different horizons without
either a hypertable per tenant or a per-tenant filtered `DELETE` sweep —
neither is built here. **True per-tenant retention is recorded as a deferred
follow-up**, not silently approximated. The rollup's own retention
(`domain.DefaultTelemetryRollupRetentionDays`, 400 days) is a fixed constant
for the same reason, and is deliberately not exposed as a Setting at all —
exposing a tunable that resolves to the same "platform only" caveat twice
would not add anything the raw key does not already explain.

**D4: downsampling is a second hypertable this platform owns outright
(`telemetry_rollup_5m`), not a Timescale continuous aggregate — and this is a
STRONGER tenant-isolation position, not merely a workaround.**

The brief flagged, correctly, that a Timescale continuous aggregate's backing
materialization is a separate object (a `_timescaledb_internal` hypertable)
never proven to inherit `ENABLE`/`FORCE ROW LEVEL SECURITY` the way a plain
hypertable does — querying it as a caller who is not its owner could leak
every tenant's aggregated rows together. Because that feature is unavailable
here anyway, `telemetry_rollup_5m` is instead created exactly like
`telemetry_sample`: a normal table this platform's own migration creates,
carrying `ENABLE ROW LEVEL SECURITY`, `FORCE ROW LEVEL SECURITY`, and the
identical `tenant_isolation` policy. Tenant isolation on the rollup is
therefore not a defensive query-time filter layered on top of an
unverifiable object — it is the same, already-proven RLS mechanism every
other tenant-owned table in this schema uses. `TestTelemetryRollupIsolation_
RLSByTenant` proves it, and — mutation-tested live during this change, by
temporarily deleting the migration's RLS block — the test bites: with RLS
removed, tenant B's query for tenant A's own `asset_id`/metric returned
tenant A's rolled-up average, and the test failed with that leaked row
printed. Restoring the RLS block made it pass again.

`TelemetryRollupWorker.MaterializeRange` recomputes a TRAILING WINDOW every
pass (default: the last 24h, lagging 2m behind "now") and upserts via
`ON CONFLICT ... DO UPDATE`, rather than tracking a durable watermark —
recomputing an unchanged bucket is a wasted write, not a correctness risk,
which makes the worker idempotent and self-healing across restarts and
late-arriving data without a second cursor table.

## Consequences

**What is now guaranteed.** `telemetry_sample`'s raw retention horizon is
operator-tunable (platform-wide) via the existing Settings API and enforced
by a worker that actually calls `drop_chunks` — verified live:
`TestTelemetryRetentionWorker_DropsOldRawChunks` and `..._DropsOldRollupChunks`
write a sample 200 days / 2 years old in its own chunk, run one retention
pass, and prove the chunk is gone while a recent sample in a different chunk
survives. `telemetry_rollup_5m` gives a bounded, avg/min/max/count view over
wide time ranges (`TestTelemetryRollupWorker_MaterializesAvgMinMaxCount`), is
tenant-isolated by the platform's standard RLS mechanism
(`TestTelemetryRollupIsolation_RLSByTenant`, mutation-proven), and is itself
retained on its own 400-day horizon. `TelemetryRepository.QueryRange`'s
`resolution` parameter picks raw for a narrow window and the rollup for a
wide one automatically (`domain.AutoResolutionRawWindow` = 6h), and refuses
an explicit raw request over `domain.MaxRawQueryWindow` (7 days) — a caller
cannot force a wide per-sample scan by omission or by asking.

**What is not claimed.** This is not TimescaleDB's native retention/
continuous-aggregate feature set; it is a functionally equivalent
application-level substitute chosen specifically to avoid an undisclosed
licensing change. If the platform later adopts the Timescale-licensed
community image (a decision requiring its own ADR weighing the license's
field-of-use restrictions), `add_retention_policy` and a real continuous
aggregate could replace these workers behind the same
`domain.TelemetryRepository` interface without changing
`internal/httpapi` or any caller. Per-tenant raw retention is not delivered;
D3 records it as a deferred follow-up. `telemetry_rollup_5m`'s population
lags real time by `Interval` + `Lag` (defaults: up to ~7 minutes) — a
dashboard reading the rollup for "right now" sees the most recent bucket only
once a pass has materialized it, not instantly on ingest; a caller who needs
the last few minutes at full detail should use (or let `ResolutionAuto`
choose) raw.

## Alternatives considered

- **Switch to the Timescale-licensed community image** and use the native
  features as named in the brief. Rejected for THIS change: it reverses
  ADR-TELEMETRY-001's explicit, reasoned license choice, which is a decision
  this story's scope does not include making unilaterally. Left open as a
  future ADR if the platform decides the license's restrictions are
  acceptable.
- **A single retention worker doing `DELETE ... WHERE ts < cutoff`** instead
  of `drop_chunks`. Rejected: a row-by-row delete on a hypertable this size
  is exactly the "storage growth" problem retention exists to bound —
  `drop_chunks` removes whole chunks in one DDL-shaped operation with no
  per-row vacuum cost, which is the entire reason TimescaleDB's chunk model
  exists.
- **A plain Postgres materialized view, refreshed wholesale
  (`REFRESH MATERIALIZED VIEW`)**, instead of an appended/upserted table.
  Rejected: a full refresh recomputes its defining query over CURRENT data
  only, so once old raw chunks are dropped, the next refresh would silently
  erase the very rollup buckets the whole feature exists to preserve past
  the raw retention window. `telemetry_rollup_5m`'s upsert-per-window
  population is what lets it outlive the raw data it summarised.

## Enforcement

- `postgres.TenantOwnedTables` includes `telemetry_rollup_5m`; `SchemaValidator`
  and `OwnershipValidator` sweep it exactly as they do `telemetry_sample`.
- `TestTelemetryRollupIsolation_RLSByTenant` (integration, mutation-proven
  live during this change) is the load-bearing tenant-isolation proof.
- `TestTelemetryRollup_IsARealHypertable` proves the D4 storage decision
  landed, the same D1-shaped check `TestTelemetrySample_IsARealHypertable`
  gives.
- `TestTelemetryRetentionWorker_DropsOldRawChunks` /
  `..._DropsOldRollupChunks` prove retention actually bounds growth.
- `TestTelemetryQueryRange_ResolutionSelection` proves the resolution/rollup
  selection: narrow auto → raw, wide auto → rollup, explicit raw beyond
  `MaxRawQueryWindow` → rejected.
- Every `public.drop_chunks`/`public.time_bucket`/`public.create_hypertable`
  call is schema-qualified, per ADR-TELEMETRY-001's Enforcement section —
  proven necessary again live: an unqualified `($2 || ' days')::interval`
  parameter form left pgx unable to encode a Go `int` into the `text`
  parameter type Postgres inferred, and every retention pass failed
  silently until `make_interval(days => $2)` replaced it (caught by
  `TestTelemetryRetentionWorker_DropsOldRollupChunks`).

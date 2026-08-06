# ADR-NOC-008 — Incident-trends is a bounded, computed-at-request-time projection over `incident`; alert-volume is the incident-source proxy, not a firing log

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-NOC-001 (E7.1 overview — the RLS-only-isolation, transient-DTO pattern this story mirrors exactly), ADR-NOC-006 (E7.3b-1 asset-graph — the bounded-projection, mutation-proven-isolation-test precedent), `docs/PLATFORM-BUILD-PLAN.md` §4 (reduced-concept discipline — `Dashboard`/`Report` are ratified false-nouns) and E9 (split data-then-viz: this story is E9.1, the data half; E9.2 is the Cloudscape dashboard screen consuming it), ADR-ALERTING-004/E4.1 (`incident.source`, the field this story's honest bound is about) |

## Context

`docs/PLATFORM-BUILD-PLAN.md` E9.1 asks for one thing the E9.2 dashboards
screen can chart: incident volume over time, bucketed, split by severity and
by source. The scout finding that shaped this story's scope (recorded in the
plan before this ADR): **there is no alert-firing history in this system.**
`alert_rule.last_state` (E3.1) is the current derived state of a rule, not a
log — every prior transition is overwritten, never appended, by design (a
firing is DERIVED, never a reified `Alert` row, per §4). So "alert volume
over time" cannot mean "how many times a rule fired"; the only honest
alert-adjacent volume this system can report is **alert-*sourced incident*
volume** — `incident.source = 'alert'` (E4.1), i.e. how many incidents
E4.1's correlation pipeline created or linked off a firing. A true
alert-firing log would need a new `alert_event` table, explicitly deferred
(E3 backlog item, not part of this story).

`incident` already carries everything else this story needs:
`created_at`/`resolved_at`/`severity`/`status`/`source`, indexed by
`(tenant_id, status)` and `(tenant_id, incident_id)` (20260825000001) but by
nothing that leads with `created_at` — a range scan over an arbitrary
`[from, to)` window has nothing to walk but `tenant_id`.

## Decision

### 1. One endpoint, one transient DTO, nothing persisted

`GET /v1/admin/dashboards/incident-trends?from=&to=&bucket=`,
`requirePermission(auth.PermAdmin)` — `incident` is tenant-owned, so this is
tenant administration, not `requirePlatformAdmin`, exactly as every other
incident/NOC route in this package. The handler
(`Server.incidentTrends`, `internal/httpapi/handlers_dashboards.go`) parses
and validates `from`/`to`/`bucket`, runs one store call, and assembles a
plain Go struct (`incidentTrendsResponseDTO`) into JSON. Nothing is written,
nothing is cached; calling it twice a millisecond apart can return two
different answers, the same property `nocOverview` and the asset-graph
projection already have. No new table, no new entity type in
`internal/domain` beyond plain result-shape structs
(`IncidentOpenedTrendPoint`, `IncidentResolvedTrendPoint`,
`IncidentTrendsQuery`) — the census
(`internal/kg/extract/schema.TestCorpusCensus`) gains one INDEX (91, was 90)
and nothing else, the definitive proof nothing was reified.

### 2. Row-level security is the ONLY isolation mechanism — no explicit tenant predicate, no privileged pool

Exactly ADR-NOC-001 §2 and ADR-NOC-006 §2's reasoning, applied to
`IncidentStore.Trends`. It runs on the same `appPool`-backed
`*postgres.IncidentStore` every other incident route already uses
(`Server.incidents`, wired once at the composition root and already covered
by `arch.TestServerWiringUsesTenantScopedPool`). `incident` carries `FORCE
ROW LEVEL SECURITY` with the `tenant_id = current_setting('app.tenant_id',
true)` policy (20260825000001); neither of `Trends`'s two queries carries a
`WHERE tenant_id = ...` of its own, for the same reason no other tenant-scoped
`IncidentStore` method does — the database supplies the filter.

`IncidentStore` is dual-role (its privileged instance backs
`alerting.IncidentCorrelator`, ADR-TENANCY-012), so adding `Trends` to the
shared type in principle reaches `arch.TestPrivilegedReads_AreScopedToATenant`'s
sweep of privileged store methods. It is not flagged: that guard's detected
class is a `SELECT` on a tenant-owned table filtered by `asset_id` without a
`tenant_id` predicate (the one tenant-shared, non-globally-unique key this
schema uses as a read predicate elsewhere); `Trends`'s two queries filter by
`created_at`/`resolved_at` range alone, never by `asset_id`, so they are
outside that guard's detected shape regardless of which pool constructs the
instance. The actual isolation guarantee is proven live, not argued: two real
tests seed tenant A and tenant B with materially different incident volumes
and severities in the identical bucket window
(`TestIncidentTrendsAPI_TenantIsolation`), and a THIRD test
(`TestIncidentTrendsAPI_TenantIsolation_BitesWhenLoosened`) re-runs the
fixture against a router deliberately wired with `IncidentStore` built over
the privileged pool instead of the tenant-scoped one and requires the leak to
appear there — the same mutation-proof shape
`TestAssetGraphAPI_TenantIsolation_BitesWhenLoosened` established (ADR-NOC-006
§2). That mutation test asserts PRESENCE of the wrong tenant's signal (a
severity tenant A never creates, appearing nonzero in tenant A's own read),
not an exact count — the harness reuses a persistent database across test
runs (no reset, only `CREATE SCHEMA IF NOT EXISTS`), so an exact count at a
fixed calendar timestamp would accumulate across repeated invocations of the
very test that intentionally reads without tenant scoping; a presence check
is robust to that and was verified so by running the suite twice
consecutively against the same database.

### 3. The query shapes, the new index, and why neither is an unbounded scan

Two single-table, bounded-by-construction queries, neither joining the other
— `IncidentStore.Trends` (`internal/store/postgres/incident_store.go`):

- **Opened series**: `SELECT date_trunc($1, created_at AT TIME ZONE 'UTC')
  AT TIME ZONE 'UTC' AS bucket, severity, source, COUNT(*) FROM incident
  WHERE created_at >= $2 AND created_at < $3 GROUP BY bucket, severity,
  source` — backed by the new `ix_incident_tenant_created_at (tenant_id,
  created_at DESC)`: RLS supplies the `tenant_id` half for free, and
  `created_at >= $ AND created_at < $` is the index's own leading range
  predicate. Cost tracks the caller's own incidents inside `[from, to)`, not
  the tenant's whole incident history and never another tenant's.
- **Resolved series**: `SELECT date_trunc($1, resolved_at AT TIME ZONE
  'UTC') AT TIME ZONE 'UTC' AS bucket, COUNT(*) FROM incident WHERE
  resolved_at >= $2 AND resolved_at < $3 GROUP BY bucket` — **no dedicated
  index on `resolved_at`.** Honest bound, stated plainly rather than
  invented ahead of evidence (the same restraint ADR-NOC-001 §5 exercises for
  its own on-call N+1 shape): cost here tracks the caller's OWN tenant's
  total incident volume (RLS still confines the scan to that one tenant —
  never a cross-tenant scan), not an indexed range walk. incidents are an
  operational-record volume, not telemetry-scale (E2's own stated extreme),
  so this is judged acceptable for v1; add `ix_incident_tenant_resolved_at`
  as a follow-on if a real deployment's resolved-series query cost ever
  warrants it.

Both `date_trunc` calls force UTC regardless of the connection's session
`TimeZone` setting (`AT TIME ZONE 'UTC'` applied on the way in AND the way
back out — verified live: `date_trunc('hour', now() AT TIME ZONE 'UTC') AT
TIME ZONE 'UTC'` returns a proper `timestamptz`, not the naive `timestamp`
a single conversion would leave behind). This makes a `bucket_start` this
store returns always match `domain.IncidentTrendsQuery.BucketStarts`'s own
UTC-anchored computation on the Go side, independent of wherever Postgres
happens to be deployed.

**The cap.** `domain.IncidentTrendsQuery.Validate` rejects (422) any request
whose `[from, to)` window would produce more than
`MaxIncidentTrendBuckets = 744` contiguous buckets — a month of hourly
buckets — BEFORE either query runs. `BucketCount` computes this from `from`
truncated to its own bucket boundary (matching what `date_trunc` will
produce), not from the caller's exact, possibly off-boundary, instant, so
the enforced cap and the actual series length can never disagree. Proven
both at the unit level (`TestIncidentTrendsQuery_Validate_EnforcesTheBucketCap`,
exactly-at-cap accepted, one-over rejected) and end-to-end
(`TestIncidentTrends_AtTheCap_IsAccepted`, `TestIncidentTrends_ParamValidation`'s
`over cap` case).

### 4. Zero-fill assembly happens in Go, not SQL

Neither store query returns a contiguous series — a bucket with no matching
incidents is simply absent from either result set. The handler
(`buildIncidentTrendsResponse`) generates the full, contiguous,
boundary-aligned bucket shape from `IncidentTrendsQuery.BucketStarts` alone
— never from which buckets happen to appear in the store's output — indexes
it by `bucket_start`, and folds each raw row into its slot; a slot nothing
matches stays zeroed. A row whose `bucket_start` falls outside the generated
shape (which `Validate` having already run should make impossible) is
silently dropped rather than causing a panic on a missing map key — a
defensive property, not a load-bearing one. This mirrors keeping "is the
window bounded" (`domain.IncidentTrendsQuery`) and "how do these bounded
facts render" (the HTTP DTO) as separate concerns, the same layering every
other repository/handler pair in this package already draws.

### 5. The honest bound: `opened_by_source.alert` is an incident-source proxy, not a firing log

Stated for the third time, deliberately, because it is the single fact a
consumer of this endpoint (E9.2's chart, or anyone reading the response
directly) is most likely to over-read: `opened_by_source.alert` counts
incidents whose `source = 'alert'` (E4.1) — created or linked by the
correlation pipeline off a rule's `ok → firing` transition — inside the
bucket they were opened in. It is NOT a count of how many times a rule
fired. A flapping rule suppressed by E3.2's dwell never creates a second
incident for an already-open one (E4.1's find-or-create-by-asset semantics);
a rule that fires and recovers repeatedly against an ALREADY-open alert
incident adds zero to this count each time, only a timeline note. So this
series systematically UNDER-counts true firing frequency relative to
whatever a genuine `alert_event` firing log would show — an honest,
name-worthy gap, not a silently rounded one. A real firing log is future
work (a new `alert_event` table, out of scope here and not committed to by
this ADR).

## Alternatives considered

- **A privileged-pool bulk query, filtered by an explicit `tenant_id`
  predicate.** Rejected for the same reason ADR-NOC-006 rejects it for the
  asset graph: `IncidentStore`'s tenant-scoped instance already answers this
  correctly today; introducing a second, privileged code path for one method
  would be exactly the "isolation is a property of wiring, not of a
  predicate someone remembers" hazard ADR-TENANCY-002 exists to prevent.
- **Bucketing in Go by fetching every matching incident row and grouping in
  memory.** Rejected: it would turn a bounded `GROUP BY` into an unbounded
  `List`-then-tally, the same anti-pattern ADR-NOC-001 §4 explicitly warns
  against ("never a `List` fed into an in-memory tally") — the cap protects
  the RESPONSE size, not the scan size, and an in-memory tally would still
  need to fetch every row inside `[from, to)` regardless of how few buckets
  the response ultimately has.
- **A dedicated index on `resolved_at`, matching the one on `created_at`.**
  Deferred, not rejected outright: the CTO-locked migration scope for this
  story is exactly one additive index; a second one is easy to add later
  behind evidence (§3's honest bound), and inventing it now would be
  un-costed generality ahead of a real deployment's actual resolved-series
  query cost.

## Consequences

**What is now guaranteed.** A caller with `PermAdmin` in a tenant gets, for
any `[from, to)` window up to 744 contiguous buckets at hour or day
granularity, an exact, contiguous, zero-filled series of that tenant's own
incident opens (by severity and source) and resolutions (by `resolved_at`) —
proven against real fixtures spanning multiple buckets, severities and
sources with a deliberately empty middle bucket
(`TestIncidentTrendsAPI_BucketsBySeverityAndSourceAndZeroFills`), proven to
bucket resolutions by `resolved_at` rather than `created_at`
(`TestIncidentTrendsAPI_ResolvedCountsAreBucketedByResolvedAtNotCreatedAt`),
and confined to the caller's own tenant by row-level security alone, proven
live and proven to leak when that scoping is removed
(`TestIncidentTrendsAPI_TenantIsolation`,
`..._TenantIsolation_BitesWhenLoosened`). A brand-new, empty tenant gets a
clean `200` with a full, all-zero contiguous series — never a `500`, never a
sparse or empty `points` array
(`TestIncidentTrendsAPI_EmptyTenantIsAllZeroSeries`). A malformed or
oversized request — missing/unparseable `from`/`to`, an unsupported
`bucket`, `to` not after `from`, or a window/bucket combination over the 744
cap — is refused with `422` before either query runs, and the store is never
reached (`TestIncidentTrends_ParamValidation`). The contract is additive:
`/v1/admin/dashboards/incident-trends` and its four new schemas are the only
change to `internal/httpapi/openapi.yaml`, and `make contract-breaking`
against `master` reports no breaking change.

**What is not claimed.** `opened_by_source.alert` is an incident-source
volume proxy, not a true alert-firing log — §5's bound is permanent until an
`alert_event` table exists (a separate, undecided future story). The
resolved series has no dedicated index — cost tracks the caller's own
tenant's total incident volume, not an indexed range (§3). This is a v1
read, not a live feed — a client sees exactly what was true at the moment of
its own request, the same property every other NOC projection in this
package already has.

## Enforcement

- `arch.TestServerWiringUsesTenantScopedPool` — `SetIncidents` was already
  covered before this story; unchanged by it, and still green.
- `arch.TestPrivilegedReads_AreScopedToATenant` — confirms `Trends` adds no
  `asset_id`-scoped privileged read; still green, still non-vacuous (its own
  canary is unrelated to this method).
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — the one new route is exactly the
  published contract, no more, no less.
- `httpapi.TestIncidentTrendsAPI_TenantIsolation` /
  `..._TenantIsolation_BitesWhenLoosened` (real Postgres) — Decision 2's
  isolation claim, proven live and proven to fail when the scoping it depends
  on is removed.
- `httpapi.TestIncidentTrends_ParamValidation`,
  `domain.TestIncidentTrendsQuery_Validate_EnforcesTheBucketCap` — Decision
  3's cap, enforced before any query runs.
- `internal/kg/extract/schema.TestCorpusCensus` — bumped by exactly one
  index (91, was 90); a future change that reifies this projection into a
  table fails this test first.

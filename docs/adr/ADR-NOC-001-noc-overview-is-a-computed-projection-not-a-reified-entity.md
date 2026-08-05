# ADR-NOC-001 — The NOC operational-overview is a computed-at-request-time projection, never a stored Dashboard/Report/Overview entity

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-05 |
| **Decider** | Acting CTO |
| **Related** | Vol III §3.4 / `docs/PLATFORM-BUILD-PLAN.md` §4 (reduced-concept discipline — `Dashboard`/`Report` are ratified false-nouns), ADR-TENANCY-001/002 (row-level isolation is a property of wiring, not of a predicate), ADR-TENANCY-012 (privileged reads require an explicit tenant predicate — the reasoning this story deliberately does NOT need), E1.5 (`GET /admin/assets/health`, the precedent this story mirrors exactly), ADR-ONCALL-001 (`GET .../on-call-schedules/{id}/on-call`, the same computed-read shape), ADR-ALERTING-004 (E4.2 `incident.root_incident_id`, the grouping link this story reads), ADR-ONCALL-003 (E5.2b-2 `escalation_state`, the work-queue this story reads a count from) |

## Context

`docs/PLATFORM-BUILD-PLAN.md` E7 ("NOC Command Center") asks for one thing an
operator (or the E7.2 screen, next) can look at to see the whole NOC loop's
current state at once: open incidents, firing alerts, CMDB health, who is on
call, and how many incidents are actively escalating. Every one of those
facts already exists, scattered across five independent stores this platform
already built (E1.5, E3.1/E3.2, E4.1/E4.2, E5.1, E5.2a, E5.2b-2). The only
new work is assembling them into one answer.

The obvious wrong shape is a `Dashboard` or `Overview` table: a row an
operator's screen reads, refreshed by some job. Vol III §3.4 ratifies
`Dashboard`/`Report` as **derived projections, never stored domain types**
(`docs/PLATFORM-BUILD-PLAN.md` §4), and this codebase already has three
load-bearing precedents for the alternative — `GET /admin/assets/health`
(E1.5), `GET /admin/incidents/{id}/dependencies|dependents` (graph
traversal), and `GET /admin/on-call-schedules/{id}/on-call` (ADR-ONCALL-001)
— none of which reify what they compute. This story is the same shape,
fanned out across five stores instead of one.

## Decision

### 1. One endpoint, one transient DTO, nothing persisted

`GET /v1/admin/noc/overview`, `requirePermission(auth.PermAdmin)` (every
table it reads is tenant-owned, so this is tenant administration, not
`requirePlatformAdmin` — the same tier every other tenant-scoped admin route
in this package uses). The handler (`Server.nocOverview`,
`internal/httpapi/handlers_noc.go`) runs five reads and assembles a plain Go
struct (`nocOverviewDTO`) into a JSON response. Nothing is written. Calling
it twice a millisecond apart can return two different answers, because
nothing is cached or memoized — the same property `assetHealth` and
`getOnCallNow` already have. `generated_at` is the server clock at the
moment of that specific read, not a value that means anything stored
anywhere else.

No new table, no new entity type in `internal/domain`, no materialized view.
The census (`internal/kg/extract/schema.TestCorpusCensus`) is unchanged by
this story — the definitive proof that nothing was reified.

### 2. Every read is on the tenant-scoped `appPool`; row-level security is the ONLY isolation mechanism

This is the property most worth stating explicitly, because it is the
opposite of ADR-TENANCY-012's own subject. ADR-TENANCY-012 exists because a
**privileged** (RLS-bypassing) connection needs an *explicit* `tenant_id`
predicate on every read — RLS is off there, so nothing else confines it.
This story's five new/reused queries carry **no explicit tenant predicate at
all**, and that is correct, not an oversight: every store behind this
endpoint (`IncidentStore`, `AlertRuleStore`, `AssetStore`,
`OnCallScheduleStore`, and — new for this story — a **tenant-scoped**
`EscalationStateStore` instance) is built over `appPool`
(`postgres.NewTenantScopedPool`), whose `PrepareConn` hook binds
`app.tenant_id` from the request's own resolved tenant on every connection
acquisition (`internal/store/postgres/pool.go`). Every one of the tables
this story reads (`incident`, `alert_rule`, `asset`, `on_call_schedule`,
`on_call_participant`, `escalation_state`) carries `FORCE ROW LEVEL
SECURITY` with the identical `tenant_id = current_setting('app.tenant_id',
true)` policy. The database supplies the filter; the query does not need to
ask for it twice.

`internal/arch.TestServerWiringUsesTenantScopedPool` (parses
`cmd/controlplane/main.go`) fails the build if any dependency reachable from
`srv.Set*` was built from the privileged pool — this is what makes the claim
above a checked property, not a comment nobody re-verifies.
`internal/arch.TestPrivilegedReads_AreScopedToATenant` is the read-side
analogue for connections that DO run privileged; neither of this story's
production call sites (the appPool-bound stores above) appears in that
guard's privileged-type set for the code THIS story added, confirming no
privileged read was introduced.

Proven live, not just argued: `TestNOCOverviewAPI_TenantIsolation`
(`internal/httpapi/noc_overview_integration_test.go`) seeds three tenants
with materially different fixture volumes through the real write paths, then
asserts tenant A's overview and tenant B's overview each report exactly
their own five open-class incidents, one firing rule, one on-call entry
naming their own roster user, and one active escalation — never the other's,
and never the sum of all three.

### 3. `escalation_state`'s first tenant-scoped read role

`EscalationStateStore` (E5.2b-2, ADR-ONCALL-003) had exactly one instance in
this codebase before this story: privileged, built over `pool`, because its
only two callers (the Seeder and the Worker) are cross-tenant background
processes. This story adds `CountActive(ctx) (int, error)` — a plain `SELECT
COUNT(*) FROM escalation_state WHERE status = 'active'` — and a **second**
instance of the same type, built over `appPool`
(`cmd/controlplane/main.go`, wired via the new `Server.SetNOCEscalations`).
This is the identical dual-role split `AlertRuleStore`/`IncidentStore`
already draw between their admin-CRUD (or, for `IncidentStore`, correlation)
surface and their privileged-worker surface — applied here between a
read-only projection and the privileged Seeder/Worker instead of between two
writers. `CountActive` carries no explicit tenant predicate, correct ONLY
because it is called through the appPool-bound instance (documented on the
method itself, and on the type's own updated doc comment) — exactly the
same convention every other tenant-scoped store in this package already
relies on.

### 4. The aggregate query shapes, their indexes, and why none scans unboundedly

Every aggregate below is a `COUNT`/`GROUP BY` or a capped list — never a
`List` fed into an in-memory tally.

- **Incidents** (`IncidentStore.OverviewCounts`, new). Two queries, both
  filtered to `status IN ('open', 'acknowledged', 'investigating')` — the
  "open-class" set the NOC loop still has work against; resolved/closed/
  reopened is excluded, unlike `IncidentRepository.List`'s own no-default-
  exclusion shape:
  - `SELECT status, severity, COUNT(*) FROM incident WHERE status IN (...) GROUP BY status, severity` —
    backed by `ix_incident_tenant_status (tenant_id, status)`. RLS supplies
    the `tenant_id` half of that composite index for free; the `status IN`
    filter is the index's own second column. Cost tracks the caller's own
    open-incident count, never the whole table, regardless of how much
    resolved/closed history has accumulated.
  - `SELECT COUNT(*) FILTER (WHERE root_incident_id IS NOT NULL), COUNT(DISTINCT root_incident_id) FILTER (...) FROM incident WHERE status IN (...)` —
    same index, same bound. `collateral_count` is open-class incidents with
    a non-null `root_incident_id` (E4.2); `root_count` is the number of
    DISTINCT roots those collateral incidents name — not "how many open
    incidents are themselves a root," a related but different count this
    story does not need.
- **Alerts** (`AlertRuleStore.CountFiring`, new):
  `SELECT severity, COUNT(*) FROM alert_rule WHERE enabled = true AND last_state = 'firing' GROUP BY severity` —
  confined to enabled rows by `ix_alert_rule_enabled` (a partial index on
  `enabled = true`, the same one `EnabledRules`' own evaluator due-scan
  uses) before `last_state` is even checked, so cost tracks the caller's own
  enabled-rule count, never disabled rules accumulated over the tenant's
  history.
- **Assets**: `AssetRepository.Health()` (E1.5), reused completely
  unchanged — this story reads only the four category `Count` fields off its
  existing bounded, indexed report and drops the per-category `Samples`
  list (a drill-down `GET /admin/assets/health` remains the place to see
  which CIs).
- **On-call**: `OnCallScheduleRepository.List(ctx, nocOnCallScheduleCap,
  "")` (existing method, unfiltered by status — there is no status filter on
  it) capped at `nocOnCallScheduleCap = 100`
  (`internal/httpapi/handlers_noc.go`), then `OnCall(ctx, scheduleID, now)`
  (existing, E5.2a) once per **active** schedule in that page; an archived
  one is skipped, not resolved. This is the one place this story accepts an
  N+1 query shape (`buildNOCOnCall`) rather than a single aggregate: CTO-
  locked design reuses the existing per-schedule resolution rather than
  building a new bulk "who's on call for these N schedules" primitive.
  **Honest bound**: a tenant with more than 100 schedules gets a partial
  `on_call` section — the first 100 by `schedule_id`'s own keyset order,
  never an unbounded fan-out. Revisit the cap (or add a bulk resolution
  method) only if a real deployment approaches it; inventing that primitive
  ahead of evidence would be exactly the kind of un-costed generality
  `docs/PLATFORM-BUILD-PLAN.md` §3 warns against.
- **Escalations** (`EscalationStateStore.CountActive`, new):
  `SELECT COUNT(*) FROM escalation_state WHERE status = 'active'` — bounded
  by `ix_escalation_state_claim`, a partial index whose own condition
  (`WHERE status = 'active'`) matches this query's `WHERE` clause exactly,
  so Postgres can satisfy it by walking the index rather than the table;
  cost tracks the current work-queue depth, never `escalation_state`'s full
  (acked/resolved/exhausted) history.

No query in this story does a full unbounded scan of any table. Every one is
either an existing, already-reviewed bounded method (`Health`, `List`,
`OnCall`) or a new `COUNT`/`GROUP BY` sitting directly behind an existing
partial or composite index this codebase already built for a different
consumer of the same table.

### 5. What is deferred, and why

- **Telemetry summary.** E2's `telemetry_sample`/`telemetry_rollup_5m` are
  the platform's highest-volume tables by design (ADR-TELEMETRY-001/002); a
  meaningful telemetry summary needs its own aggregate shape and its own
  cost analysis, not a sixth section bolted onto this endpoint's response.
  Left out entirely rather than stubbed, so its absence is visible, not
  silently zeroed.
- **The screen (E7.2).** This story is API only — no HTML, no
  auto-refreshing view. `docs/PLATFORM-BUILD-PLAN.md` sequences E7.2
  immediately after this story.
- **Real-time / streaming (E11).** This endpoint is polled; a client wanting
  fresher-than-poll-interval data calls it again. A push-based layer is its
  own cross-cutting enabler, explicitly out of scope here.
- **A bulk "who's on call for many schedules" primitive.** See Decision 4's
  on-call bound above — the N+1 shape is accepted for v1, capped, and
  documented, not silently unbounded.

## Consequences

**What is now guaranteed.** A caller with `PermAdmin` in a tenant gets, in
one request, the current open-incident picture (by status, by severity, and
E4.2's root/collateral grouping), the current firing-alert picture (by
severity), the CMDB health category counts, who is on call right now for
every active schedule (up to the documented cap), and how many escalations
are actively working — every field traceable to a specific bounded,
indexed query, and every field confined to the caller's own tenant by row-
level security alone, proven live across three simultaneously-seeded tenants
(`TestNOCOverviewAPI_TenantIsolation`). A brand-new, empty tenant gets a
clean `200` with every section zeroed or empty — never a `500`, never a nil-
pointer panic on an empty on-call roster or an incident-free tenant
(`TestNOCOverviewAPI_EmptyTenantIsCleanZeroes`,
`TestNOCOverview_EmptyTenantReturnsCleanZeroes`). A missing dependency (any
of the five stores unwired) answers `501`, never a partial response silently
missing a whole section (`TestNOCOverview_NotImplementedWhenUnwired`). The
contract is additive: `/v1/admin/noc/overview` and its eight new schemas
are the only change to `internal/httpapi/openapi.yaml`, and
`make contract-breaking` against `master` reports no breaking change.

**What is not claimed.** This is a v1 read, not a live feed — a client sees
exactly what was true at the moment of its own request, and the
`on_call` section is silently partial (not erroring) past 100 active
schedules for one tenant. No telemetry signal appears anywhere in the
response. No screen exists yet to render any of this.

## Evidence

- `internal/domain/incident.go` (`IncidentOverviewCounts`,
  `IncidentRepository.OverviewCounts`) / `internal/domain/alertrule.go`
  (`AlertFiringCounts`, `AlertRuleRepository.CountFiring`) — the new
  aggregate contracts.
- `internal/store/postgres/incident_store.go` (`OverviewCounts`) /
  `alert_rule_store.go` (`CountFiring`) / `escalation_state_store.go`
  (`CountActive`) — the query shapes Decision 4 describes.
- `internal/httpapi/handlers_noc.go` — the handler, DTOs, and
  `buildNOCOnCall`'s capped-and-documented on-call resolution.
- `internal/httpapi/handlers_noc_test.go` — DTO-assembly unit tests against
  fakes: every response field traced to a specific fake return value
  (`TestNOCOverview_AssemblesDTO`), the archived-schedule exclusion, the
  401/403 authorization boundary with a proof the stores were never reached
  on refusal (`TestNOCOverview_Authorization`), the 501-until-fully-wired
  case (`TestNOCOverview_NotImplementedWhenUnwired`), and the empty-tenant
  zeroed response (`TestNOCOverview_EmptyTenantReturnsCleanZeroes`).
- `internal/httpapi/noc_overview_integration_test.go` — the real-Postgres
  proof: aggregate correctness against fixtures seeded through the ordinary
  write paths plus E4.2's grouping write and E5.2b-2's real Seeder
  (`TestNOCOverviewAPI_AggregatesAgainstRealStores`), the three-tenant
  isolation proof that bites (`TestNOCOverviewAPI_TenantIsolation`), and the
  empty-tenant clean-zeroes case
  (`TestNOCOverviewAPI_EmptyTenantIsCleanZeroes`).
- `internal/httpapi/openapi.yaml` — the additive contract
  (`/v1/admin/noc/overview`, `NOCOverview` and its seven nested schemas).
- `internal/kg/extract/schema.TestCorpusCensus` — unchanged; no table added.

## Enforcement

- `arch.TestServerWiringUsesTenantScopedPool` — Decision 2 (every dependency
  reachable from `srv.Set*`, including `SetNOCEscalations`, is built from
  `appPool`).
- `arch.TestPrivilegedReads_AreScopedToATenant` — confirms no privileged,
  unscoped read was introduced by this story.
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — the one new route is exactly the
  published contract, no more, no less.
- `httpapi.TestNOCOverviewAPI_TenantIsolation` (real Postgres) — Decision 2's
  isolation claim, proven live.
- `internal/kg/extract/schema.TestCorpusCensus` — stays exact; a future
  change that reifies this projection into a table fails this test first.

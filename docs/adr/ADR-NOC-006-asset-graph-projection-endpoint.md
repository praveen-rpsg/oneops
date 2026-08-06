# ADR-NOC-006 — The whole-tenant asset graph is a bounded, computed-at-request-time projection, never a reified Graph/Topology entity

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-ASSET-001 (Asset/AssetRelationship model, `asset_graph_repo.go`'s traversal engine reuse), ADR-NOC-001 (the E7.1 overview projection — the read-only-projection/RLS-only-isolation pattern this story mirrors exactly), `docs/PLATFORM-BUILD-PLAN.md` E7.3b (CMDB topology map, split into this Go-endpoint story and the E7.3b-2 UI story) |

## Context

`docs/PLATFORM-BUILD-PLAN.md` E7.3b asks for a CMDB topology map. Every read
this platform has over the CMDB graph today (`GET .../{id}/relationships`,
`GET .../{id}/dependencies`, `GET .../{id}/dependents`, `GET .../{id}/service-map`)
is scoped to **one** asset and its neighbourhood. A topology map needs the
**whole** tenant's graph — every node, every edge — in one call; fetching it
by walking every asset's relationships one at a time would be an N+1 query
pattern driven entirely by how many Configuration Items a tenant happens to
have registered.

`asset` and `asset_relationship` are both tenant-owned, RLS-enforced tables
(ADR-ASSET-001 §4) already read by `AssetStore` on the tenant-scoped
`appPool`. Nothing about answering "give me every node and edge" needs a new
table, a new entity, or a new pool: it needs one new bounded read on the
store this endpoint already has wired.

## Decision

### 1. One endpoint, one transient DTO, nothing persisted

`GET /v1/admin/assets/graph`, `requirePermission(auth.PermAdmin)` — the same
tier every other tenant-scoped CMDB route in this package uses, since `asset`/
`asset_relationship` are tenant administration, not platform administration.
The handler (`Server.getAssetGraph`, `internal/httpapi/handlers_assets.go`)
calls one new repository method, `AssetRepository.Graph`, and assembles a
plain DTO (`assetGraphResponse`) into JSON. Nothing is written; nothing is
cached. Calling it twice a millisecond apart can return two different
answers, exactly the same property `nocOverview` and `assetHealth` already
have. No new table, no new `internal/domain` entity beyond two plain
result-shape structs (`AssetGraphNode`, `AssetGraphEdge`, `AssetGraph`), no
materialized view — the census
(`internal/kg/extract/schema.TestCorpusCensus`) is unchanged, the definitive
proof nothing was reified.

### 2. Row-level security is the ONLY isolation mechanism — no explicit tenant predicate, no privileged pool

Exactly ADR-NOC-001 §2's reasoning, applied to one store instead of five.
`AssetStore.Graph` runs on the same `*pgxpool.Pool` every other `AssetStore`
method already uses — the tenant-scoped `appPool`
(`postgres.NewTenantScopedPool`), whose `PrepareConn` hook binds
`app.tenant_id` from the request's own resolved tenant on every connection
acquisition. `asset` and `asset_relationship` both carry `FORCE ROW LEVEL
SECURITY` with the identical `tenant_id = current_setting('app.tenant_id',
true)` policy (`20260816000001_asset.sql`). The two new queries carry no
`WHERE tenant_id = ...` of their own, for the same reason no other read in
`AssetStore` does: the database supplies the filter.

No new wiring was needed for this story: `Server.assets` is already
`domain.AssetRepository`, already set from `postgres.NewAssetStore(appPool)`
at the composition root (`cmd/controlplane/main.go`), already covered by
`arch.TestServerWiringUsesTenantScopedPool`. Adding a method to an existing,
already-scoped interface cannot introduce a privileged read; `AssetStore` is
never constructed over the privileged pool anywhere in this codebase, so it
is not even in `arch.TestPrivilegedReads_AreScopedToATenant`'s privileged-type
sweep — there is no `asset_id`-keyed privileged read to exempt or guard here.

Proven live, not just argued: `TestAssetGraphAPI_TenantIsolation`
(`internal/httpapi/asset_graph_integration_test.go`) seeds two tenants with
overlapping node counts and a relationship each, plus a third bystander
tenant, and asserts tenant A's graph and tenant B's graph each contain
exactly their own nodes and their own edge — never the other's asset names,
never the sum of all three. The same file's
`TestAssetGraphAPI_TenantIsolation_BitesWhenLoosened` re-runs the identical
fixture and assertions against a deliberately unscoped connection (built with
`postgres.NewPool`, not `NewTenantScopedPool`, so `app.tenant_id` is never
bound) and requires the isolation assertions to FAIL there — the "does this
test actually bite" proof the story's review criteria call for. If someone
ever changed the handler to build `AssetStore` from the privileged pool, this
second test documents exactly what would leak and confirms the isolation
proof above is not vacuous.

### 3. The query shapes, their indexes, and why the bound is a cheap "ask for one more" rather than a second COUNT

Two single-table `SELECT`s, neither joining the other:

- `SELECT asset_id, name, type, status, environment, criticality FROM asset
  ORDER BY asset_id LIMIT $1` — RLS supplies the tenant filter via
  `ix_asset_tenant`; `asset_id` is the table's own primary key, so the
  `ORDER BY ... LIMIT` needs no new index.
- `SELECT from_asset_id, to_asset_id, type FROM asset_relationship ORDER BY
  relationship_id LIMIT $1` — RLS supplies the tenant filter via
  `ix_asset_relationship_tenant`; `relationship_id` is that table's own
  primary key.

Each is called with `cap+1` as its `LIMIT` argument (`maxAssetGraphNodes =
2000`, `maxAssetGraphEdges = 5000`, `AssetStore.assetGraphNodes` /
`assetGraphEdges`). If more than `cap` rows come back, the `(cap+1)`-th is
dropped and `AssetGraph.Truncated` is set to `true` — no second `COUNT(*)`
query is issued to learn the true total, and no query ever scans more than
`cap+1` rows of the tenant's own data. This is the identical bound
`ConfigObjectRepo.List`'s keyset pagination already uses (`LIMIT
`+a.add(limit+1)`), applied to a graph projection rather than a paged list.
Both `ORDER BY` clauses are the table's own primary key, so a truncated page
is always the same first-N rows on a repeated call — never an arbitrary
sample that could differ between two requests against an unchanged CMDB.

Neither query is soft-retire-filtered, unlike `List`'s default view: a
topology map exists to show where a retired CI still sits in the graph (its
edges are not removed by retirement, only by `Delete`'s cascade), not to hide
it the way the ordinary asset list does. This is the same choice `Export`
already makes, for the same reason (ADR-ASSET-001 §11).

### 4. Nodes and edges are not joined server-side; overlay composition is E7.3b-2's job, not this endpoint's

The response is exactly `{nodes, edges, truncated}` — no incident count, no
health flag, no on-call information attached to a node. `docs/PLATFORM-BUILD-PLAN.md`
E7.3b-2 composes that overlay client-side from the existing
`GET /v1/admin/incidents` and `GET /v1/admin/assets/health` endpoints,
matched by `asset_id`, exactly as `nocOverview` fans out to per-domain
endpoints rather than each one enriching the others' rows. Folding incident/
health state into this endpoint's own query would turn one bounded,
two-table read into a join across three tenant-owned tables with three
different bounding rules, for a UI concern (color-by-status) this endpoint
does not need to answer.

## Alternatives considered

- **A privileged-pool bulk export, filtered by an explicit `tenant_id`
  predicate.** Rejected: `AssetStore` already has a correctly-scoped
  connection for every other read; introducing a second, privileged code
  path for this one method would be exactly the "isolation is a property of
  wiring, not of a predicate someone remembers" hazard ADR-TENANCY-002 exists
  to prevent, for no benefit — the tenant-scoped pool answers the same
  question correctly today.
- **Reuse `AssetGraphRepo`'s traversal engine (`domain.GraphTraversal`) to
  walk the whole graph node-by-node.** Rejected: that engine answers
  "reachable from one starting node," which is the wrong shape for "every
  node regardless of reachability" — a topology map must show a fully
  disconnected asset too (E1.5's own `OrphanedAssets` category proves these
  are common, not edge cases), and walking from every node to assemble that
  would be the N+1 pattern this story exists to remove, moved server-side
  instead of eliminated.
- **A join returning nodes annotated with incident/health state in one
  response.** Rejected per Decision 4 — a UI-only overlay concern, and a
  materially different bounding problem than this endpoint's own two-table
  read.

## Consequences

**What is now guaranteed.** A caller with `PermAdmin` in a tenant gets, in
one bounded request, every asset (as a topology node) and every relationship
(as a typed, directed edge) that tenant's CMDB contains — up to 2000 nodes
and 5000 edges — confined to their own tenant by row-level security alone,
proven live and proven to bite when that isolation is removed
(`TestAssetGraphAPI_TenantIsolation`,
`TestAssetGraphAPI_TenantIsolation_BitesWhenLoosened`). A brand-new, empty
tenant gets a clean `200` with `{"nodes":[],"edges":[],"truncated":false}` —
never a `500`, never `null` arrays
(`TestAssetGraph_EmptyTenantReturnsCleanEmptyArrays`,
`TestAssetGraphAPI_EmptyTenantIsCleanEmpty`). A tenant past either cap gets
the deterministic first-N page of that list plus `truncated:true`, proven
against a small test cap (`TestAssetGraphAPI_TruncatesAtCap`). The contract
is additive: `/v1/admin/assets/graph` and its three new schemas
(`AssetGraph`, `AssetGraphNode`, `AssetGraphEdge`) are the only change to
`internal/httpapi/openapi.yaml`, and `make contract-breaking` against
`master` reports no breaking change.

**What is not claimed.** This is a raw graph read, not a laid-out topology —
E7.3b-2 owns layout, pan/zoom and rendering. No incident, health, or on-call
state is attached to any node; E7.3b-2 composes that overlay client-side from
endpoints that already exist. A tenant with more than 2000 assets or 5000
relationships sees a partial graph on this call, not an error — revisiting
either cap (or adding a windowed/paginated variant) is deferred until a real
deployment approaches it, the same honesty ADR-NOC-001 §5 records for its own
on-call cap.

## Enforcement

- `arch.TestServerWiringUsesTenantScopedPool` — `SetAssets` was already
  covered before this story; unchanged by it, and still green.
- `arch.TestPrivilegedReads_AreScopedToATenant` — `AssetStore` is not a
  privileged type in this codebase, so this story adds no candidate to that
  guard's sweep; still green, still non-vacuous (its own canary is unrelated
  to this store).
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — the one new route is exactly the
  published contract, no more, no less.
- `httpapi.TestAssetGraphAPI_TenantIsolation` /
  `..._TenantIsolation_BitesWhenLoosened` (real Postgres) — Decision 2's
  isolation claim, proven live and proven to fail when the scoping it depends
  on is removed.
- `httpapi.TestAssetGraphAPI_TruncatesAtCap` (real Postgres) — Decision 3's
  bound, proven against a small test cap rather than by inspection alone.
- `internal/kg/extract/schema.TestCorpusCensus` — stays exact; a future
  change that reifies this projection into a table fails this test first.

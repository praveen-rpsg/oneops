# ADR-ASSET-001 — Asset (Configuration Item) is a new tenant-owned operational entity, distinct from configuration_object

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-01 |
| **Decider** | Acting CTO |
| **Binding on** | `internal/domain/asset.go`, `internal/store/postgres/asset_store.go`, `internal/store/postgres/asset_graph_repo.go`, `internal/httpapi/handlers_assets.go`, `postgres.TenantOwnedTables` |
| **Related** | ADR-TENANCY-001 (row-level isolation), ADR-TENANCY-002 (isolation is a property of wiring), ADR-AUDIT-007 §6.2 (the administrative chokepoint's closed scope table), the M2.1–M2.3 dependency-graph engine (`internal/graph`, `internal/store/postgres/graph_traversal_repo.go`) |

## Context

OneOps is becoming the unified NOC/SOC/ITSM control plane: monitoring,
alerting, incidents, tickets and the service catalog all need a common thing
to point at — a typed, tenant-owned Configuration Item and a graph of the
relationships between them (a CMDB). None of that exists yet. This ADR fixes
the model and the storage/traversal shape before any of those consumers are
built.

Two things already exist that this decision has to relate to correctly:

1. **`configuration_object`** — the constitutional governance artifact under
   the Configuration State Model (Authority, Retention, the §8 lifecycle
   operations, audit chains). It is a document-governance entity.
2. **The dependency-graph engine** — `dependency_edge` plus a recursive-CTE
   traversal (`internal/store/postgres/graph_traversal_repo.go`) and a
   transport-agnostic `graph.Service` (`internal/graph/service.go`) that walks
   any `domain.GraphTraversal` implementation to answer dependencies /
   dependents / cycles. It was built once, for `configuration_object`, and was
   already written generically enough to be a second thing's traversal layer
   without being copied.

## Decision

**1. Asset is a new entity, not an extension of configuration_object.**

An Asset is operational data: a server, a service, a network device, a
database — the things monitoring watches and incidents happen to. It has no
Authority, no Retention, no ratify/approve/suspend lifecycle, and no audit
chain of its own. Overloading `configuration_object` for it would make every
future asset a constitutional object subject to §8's replacement test and the
governance engine, which is not what an asset is, and would put operational
churn (an asset's attributes change constantly) inside the schema built for
governed, append-only-audited documents.

Asset.Type is an **open but validated** set — `server`, `service`,
`network_device`, `application`, `database` are the seeded examples, not a
closed enum, so downstream ITSM/monitoring increments can add a type without
a schema or code change. It is validated as a lower-case snake_case
identifier (`internal/domain/asset.go`'s `assetTypePattern`), the same
discipline this schema already applies to other identifiers, so a type is
always safe to appear in a filter, a metric label, or a route.

**2. The CMDB relationship graph is a CLOSED, separate table
(`asset_relationship`), traversed by the EXISTING graph engine.**

`RelationshipType` (`depends_on` / `runs_on` / `connected_to` / `member_of`)
is closed by contrast with Asset.Type: it is the vocabulary the traversal
engine and downstream correlation logic reason over, so extending it is a
deliberate schema change, not a value a caller happens to send.

The engine itself is not reimplemented. `internal/store/postgres/graph_traversal_repo.go`
was refactored to parameterise its recursive-CTE query builders
(`walkQueryOn`/`cycleQueryOn`) on table and column names instead of hardcoding
`dependency_edge`/`from_cfg`/`to_cfg`. `AssetGraphRepo`
(`internal/store/postgres/asset_graph_repo.go`) is ~70 lines that call the
same builders over `asset_relationship`/`from_asset_id`/`to_asset_id`, and
implements the same `domain.GraphTraversal` interface `GraphRepo` does.
`graph.Service` — `WalkDependencies`/`WalkDependents`/`DetectCycles` — is
reused completely unchanged; it was already written against the interface,
not the table.

**3. Neither table goes through the administrative audit chokepoint.**

ADR-AUDIT-007 §6.2 scopes `withAdminAudit` to five named
identity-governance tables. Asset is operational data, not an
identity-governance fact, so it follows the same pattern Team, Setting and
Notification already established: tenant-owned, RLS-isolated, optimistic
locking — without the chokepoint. Widening §6.2 is that ADR's decision to
make, not this one's.

**4. `asset` and `asset_relationship` are TENANT-OWNED: in
`TenantOwnedTables`, RLS-enabled and FORCE'd, `tenant_id NOT NULL`.** They are
built from the tenant-scoped pool exactly like `team`/`team_membership`
(20260810000001); no isolation logic lives in the store, all of it lives in
the connection.

**5. Asset retirement is a status change, not a delete; asset deletion is a
real DELETE.** A hard `Delete` is still offered (unlike Team) because an Asset
is not a named governed grouping; its relationships are removed with it via
`ON DELETE CASCADE`, the same way `dependency_edge` cascades off
`configuration_object`.

> **Amended by OPS-CMDB-003 (E1.3), 2026-08-01.** The originally-ratified
> `AssetStatus` was two states (`active`/`retired`) mirroring `TeamStatus`. A CI
> has a richer operational life than a governed grouping, so the lifecycle is
> now a **four-state machine** — `planned → active ⇄ maintenance → retired`,
> plus a `retired → active` **reinstate** edge (a decommissioned CI can be
> redeployed; this is an operational fact, not a governance act). Legal edges
> live as data (`domain.assetTransitions`, mirroring `user.go`'s
> `CanTransitionTo`); `SetStatus` is the single transition authority
> (`AssetPatch.Status` was removed). Rationale: `planned` models a CI registered
> before it is live; `maintenance` models a CI intentionally taken out of
> service without retiring it — both are needed for accurate health rollups
> (E7) and change/incident context (E5). Retirement remains a status, not a
> delete, and a retired asset keeps its relationships and history. This
> paragraph supersedes the `active`/`retired`-only text above.

**6. Relationship creation re-verifies both endpoints on the tenant-scoped
connection; it does not trust the foreign key alone.**

PostgreSQL foreign-key triggers execute with the constraint's own privileges
and **bypass row-level security on the referenced table** — a documented
PostgreSQL behaviour, not a bug in this schema. Left alone, `from_asset_id`/
`to_asset_id REFERENCES asset (asset_id)` would let a caller name another
tenant's `asset_id` and have the FK accept it, creating a cross-tenant edge
the CMDB graph would then traverse — exactly the "isolation is a property of
wiring, not schema" class ADR-TENANCY-002 records, applied to a foreign key
instead of a pool. `AssetStore.CreateRelationship` runs
`SELECT EXISTS(... FROM asset WHERE asset_id = $1)` for both endpoints on its
own RLS-enforced connection before inserting; a cross-tenant id is filtered
out by the same policy every other read in this store is, and is reported as
`ErrNotFound` — indistinguishable from an id that does not exist, which is
the correct answer either way.

## Alternatives considered

- **Extend `configuration_object` with an `asset` kind/discriminator.**
  Rejected: every Asset would inherit Authority/Retention/§8 semantics it does
  not have, and the governance engine would need to special-case a kind of
  row it was never built to reason about.
- **A second, independent traversal implementation for
  `asset_relationship`.** Rejected: the recursive CTE, cycle canonicalisation,
  and deterministic ordering are exactly the same problem `dependency_edge`
  already solved; a second implementation is a second place for that logic to
  drift.
- **Trust the foreign key for relationship-endpoint tenancy.** Rejected per
  point 6 — proven wrong by PostgreSQL's own documented RI-check privilege
  model, not merely by caution.

## Consequences

**What is now guaranteed.** A relationship can only be created between two
assets the caller's tenant can see; `asset`/`asset_relationship` isolation is
enforced by the same fail-closed RLS policy shape as every other tenant-owned
table; traversal (`dependencies`/`dependents`) over the CMDB graph reuses the
exact engine already proven over the configuration-object graph.

**What is not claimed.** This ADR covers the CMDB model, storage, and
CRUD/graph API only. Monitoring/telemetry ingestion, alerting, incidents,
discovery/auto-population and capacity are explicitly out of scope and are
later increments that will reference `asset_id` as their foreign key into
this table.

## Enforcement

- `postgres.TenantOwnedTables` includes `asset`/`asset_relationship`;
  `SchemaValidator` and `OwnershipValidator` sweep them automatically.
- `arch.TestServerWiringUsesTenantScopedPool` /
  `TestEveryServerCapability_IsWiredAtTheCompositionRoot` require
  `SetAssets`/`SetAssetGraph` to be wired from `appPool` at the composition
  root.
- `postgres.TestAssetRelationship_CannotCrossTenants` (integration) is the
  live proof for point 6: attempting to create a relationship naming another
  tenant's asset returns `ErrNotFound` and no row is created.
- `postgres.TestAssetIsolation_*` (integration) proves list/get isolation the
  same way `TestTeamIsolation_*` does for Team.

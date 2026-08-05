# ADR-ONCALL-002 — Escalation policy + tier is config-only, built on the same ordered-child-table shape as on-call schedules; the engine is a separate, later decision

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-05 |
| **Decider** | Acting CTO |
| **Related** | ADR-ONCALL-001 (on-call schedules are configuration, who's-on-call is computed; the deferred-position-unique pattern this ADR reuses), ADR-ASSET-001 §6 (a foreign key bypasses row-level security on the referenced table; re-verify on the writer's own connection — extended here a further time), ADR-TENANCY-001/002 (row-level security, tenant-scoped pool), docs/PLATFORM-BUILD-PLAN.md E5.2b-1/E5.2b-2 split |

## Context

E5.2b ("escalation policies & paging") was split by the CTO into E5.2b-1
(this story: policy + tier configuration, CRUD only) and E5.2b-2 (the
escalation engine: a work-queue, a leader-gated worker, actual paging). The
reason for the split, stated at the time: config is low-risk additive CRUD
that mirrors a pattern already proven in this codebase (E5.2a's on-call
schedule/participant shape); the engine is a concurrency-heavy state machine
(claim-and-fence, idempotent at-least-once delivery, page-time membership
re-verification) that deserves its own full review, independent of the
config model underneath it.

This ADR covers E5.2b-1 only: the two tables, their tenancy and foreign-key
defenses, and the CRUD/reorder contract built over them. It makes no claim
about paging, notification, or incident wiring — see Decision 4 for the
explicit boundary.

## Decision

### 1. Two tables, the identical shape `on_call_schedule`/`on_call_participant` already established

`escalation_policy` (name, status) and `escalation_tier` (an ordered,
`position`-ranked row per policy, naming which `on_call_schedule` it pages and
how long — `wait_seconds` — the (E5.2b-2) engine will wait for an
acknowledgement before advancing) are both real, tenant-owned, first-class
operational objects — the same reasoning `OnCallSchedule`'s own ADR
(ADR-ONCALL-001 §1) applies to a different kind of operator-declared
configuration. Neither is a reified reduced-noun
(`docs/PLATFORM-BUILD-PLAN.md` §4, Vol II §5.3): an operator actually names
and edits a policy and its tiers, the same way they name and edit a
schedule and its roster.

The MVP shape is one tenant-default policy, but the table itself is general —
a tenant may declare more than one. **Per-asset/per-service routing** (which
policy applies to which incident) is explicitly deferred to E5.2b-2 or later;
this story stores policies and their tier ladders, and nothing decides which
one an incident uses.

### 2. `escalation_tier.on_call_schedule_id` is re-verified against the caller's own tenant before the row is written — the FK alone is not enough

`escalation_tier` and `on_call_schedule` are both tenant-owned tables under
row-level security. PostgreSQL's foreign-key trigger runs with the
constraint's own privileges and **bypasses row-level security on the
referenced table** — the exact hazard ADR-ASSET-001 §6 first proved for
`asset_relationship`'s endpoints, and ADR-ONCALL-001 §5 already extended to
`on_call_participant.user_id`. Left alone, `on_call_schedule_id REFERENCES
on_call_schedule (schedule_id)` would accept ANY existing schedule_id
regardless of tenant, letting a caller build a tier that pages across a
tenant boundary.

`EscalationPolicyStore.AddTier` (`internal/store/postgres/escalation_store.go`,
`verifyScheduleInTenant`) runs `SELECT EXISTS(... FROM on_call_schedule WHERE
schedule_id = $1)` on this store's own tenant-scoped, RLS-enforced
connection before the INSERT — the same defense
`AssetStore.CreateRelationship` runs for both of `asset_relationship`'s
endpoints, applied here to one endpoint of a cross-table reference instead of
two endpoints of a same-table one. A cross-tenant or non-existent
`on_call_schedule_id` is indistinguishable from one that does not exist at
all, and both report `ErrNotFound` — never a 500, never a silently-written
row that would page the wrong tenant's on-call user once E5.2b-2 exists to
act on it.

Mutation-verified by hand (see Evidence): removing the
`AddTier`→`verifyScheduleInTenant` call makes
`TestEscalationPolicyStore_AddTierRejectsCrossTenantSchedule` pass a
cross-tenant schedule_id through, because the FK constraint alone accepts it.

### 3. Tier ordering reuses E5.2a's deferred-position-unique pattern exactly, unchanged

`escalation_tier.position` is 0-based and kept contiguous 0..N-1 by the same
three operations `on_call_participant.position` already has:
`AddTier` appends at the current count, `RemoveTier` closes the resulting
gap, `ReorderTiers` atomically rewrites the whole sequence under
`uq_escalation_tier_policy_position UNIQUE (tenant_id, policy_id, position)
DEFERRABLE INITIALLY DEFERRED`. The reasoning for `DEFERRABLE INITIALLY
DEFERRED` is identical to ADR-ONCALL-001 §6's for
`uq_on_call_participant_schedule_position`: a reorder that swaps two tiers'
positions (not merely appends) would otherwise collide against PostgreSQL's
own per-row unique-index check, which runs as each row is written, not once
at the end of the transaction. `ReorderTiers`
(`internal/store/postgres/escalation_store.go`) is, line for line, the same
lock-the-owning-row-then-verify-the-exact-set-then-rewrite-every-position
algorithm `ReorderParticipants` already uses, over `escalation_tier` instead
of `on_call_participant` and `lockPolicyTx` instead of `lockScheduleTx`. No
new concurrency primitive was invented; the pattern was proven once and
copied.

### 4. Config only — the engine is a separate, later decision

This story stores policies and tiers. It builds, deliberately:

- **No `escalation_state` work-queue.** There is no row anywhere recording
  "incident X is at tier Y, waiting until Z."
- **No worker, no reconciler, no leader-gated process of any kind.**
  `cmd/controlplane/main.go` wires exactly one `EscalationPolicyStore`, built
  over `appPool` (tenant-scoped), into the HTTP server — no privileged-pool
  counterpart exists, because nothing here runs across tenants in the
  background.
- **No paging.** Nothing in this story sends an email, a notification, or
  any other outbound signal. `wait_seconds` is stored as a plain integer; no
  code anywhere reads it to decide when to do anything.
- **No incident or notification wiring.** `escalation_tier` names an
  `on_call_schedule_id`; nothing connects a policy to an incident, and
  nothing connects a tier's wait to a clock.

All of the above is E5.2b-2: `escalation_state` (one active row per incident,
`current_tier_index`, `next_attempt_at`, `claimed_at` fence), a leader-gated
seeding reconciler, a leader-gated escalation worker mirroring
`notification.Worker`'s claim-act-advance shape, and the page-time
active-membership re-check E5.2a's own review already flagged as a
requirement for that later story
(`docs/PLATFORM-BUILD-PLAN.md`'s E5.2b-2 entry). None of it exists yet, and
none of it is implied by anything this story built.

## Alternatives considered

- **A single table with a JSON array of tiers.** Rejected for the same
  reason `on_call_participant` is a separate table from `on_call_schedule`,
  and for the same reason ADR-ONCALL-001 §3 rejected a JSON rules engine for
  rotation semantics: a JSON blob has no row-level unique constraint to
  enforce contiguous, collision-free ordering, no foreign key to enforce that
  a referenced schedule exists, and no way to add or remove one tier without
  reading, mutating, and rewriting the whole array under a lock the database
  itself would not be enforcing.
- **Trusting the foreign key for `on_call_schedule_id` tenancy.** Rejected
  per Decision 2 — proven wrong by the same documented PostgreSQL RI-check
  privilege model ADR-ASSET-001 §6 already established, not merely by
  caution.
- **Building the engine now, since the config shape was already proven.**
  Rejected by the CTO's own split decision: an engine is a concurrency-heavy
  state machine (claim-and-fence, idempotent at-least-once delivery,
  page-time re-verification against revoked membership) that is a materially
  different, larger review surface than additive CRUD, and conflating the two
  reviews would make either one harder to gate correctly.

## Consequences

**What is now guaranteed.** A tenant can declare one or more escalation
policies and order tiers within them; tier ordering survives an arbitrary
permutation atomically, including a genuine swap
(`TestEscalationPolicyStore_ReorderTiersIsAtomic`); adding, removing, and
reordering tiers keeps `position` contiguous 0..N-1 throughout
(`TestEscalationPolicyStore_AddTierAppendsAtNextPosition`,
`RemoveTierCompactsPositions`); a tier can only ever name an on-call schedule
that belongs to the same tenant as the policy it is added to, mutation-verified
to matter (`TestEscalationPolicyStore_AddTierRejectsCrossTenantSchedule`);
tenant isolation holds even naming the foreign tenant's own real,
RLS-visible schedule (`TestEscalationPolicyStore_RLSIsolatesByTenant`);
deleting a policy removes its tiers with it
(`TestEscalationPolicyStore_DeleteCascadesToTiers`); and `wait_seconds > 0`
is enforced at both the domain layer and a database `CHECK` constraint
(`TestEscalationTier_WaitSecondsCheckConstraintRejectsNonPositive`).

**What is not claimed.** No escalation ever actually happens: nobody is
paged, no incident is touched, no clock is consulted anywhere in this story.
No per-asset/per-service policy routing exists — which policy applies to
which incident is undecided, deliberately, until E5.2b-2 or later. No
override of `wait_seconds` mid-escalation, no multi-schedule tier (a tier
pages exactly one schedule), and no acknowledgement concept of any kind exist
yet — all of it is the engine, not this story.

## Evidence

- `internal/domain/escalation_test.go` — `NewEscalationPolicy`/
  `NewEscalationTier` field validation (empty/blank tenant, policy, schedule
  identifiers; an overlong name; a bad status), and the exact
  `MinEscalationWaitSeconds` floor (1 accepted, 0 rejected,
  `TestMinEscalationWaitSeconds_IsTheExactFloor`).
- `internal/store/postgres/escalation_store_integration_test.go` — CRUD +
  optimistic locking against real PostgreSQL
  (`TestEscalationPolicyStore_CreateGetListUpdateDelete`); two-tenant RLS
  isolation across Get/List/Update/Delete/AddTier, naming the foreign
  tenant's own real schedule
  (`TestEscalationPolicyStore_RLSIsolatesByTenant`); the cross-tenant
  schedule defense, mutation-verified to bite
  (`TestEscalationPolicyStore_AddTierRejectsCrossTenantSchedule`); append
  ordering (`TestEscalationPolicyStore_AddTierAppendsAtNextPosition`);
  removal compaction
  (`TestEscalationPolicyStore_RemoveTierCompactsPositions`); atomic reorder
  including a genuine position swap, both from the reorder call's own return
  value and from a subsequent list
  (`TestEscalationPolicyStore_ReorderTiersIsAtomic`); reorder's
  set-equality refusal
  (`TestEscalationPolicyStore_ReorderTiersRejectsMismatchedSet`); cascade
  delete (`TestEscalationPolicyStore_DeleteCascadesToTiers`); and the
  `wait_seconds > 0` database `CHECK` constraint as a backstop to domain
  validation
  (`TestEscalationTier_WaitSecondsCheckConstraintRejectsNonPositive`).
- `internal/httpapi/handlers_escalation_test.go` — authorization,
  501-until-configured, resolved-tenant (not caller-supplied) on create, 422
  on an empty name / a non-positive `wait_seconds` / a patch missing
  `row_version`, 409 on a version-mismatch patch, 404 on a cross-tenant/
  non-existent schedule add and on a missing tier remove, and the reorder
  round trip.
- `internal/store/postgres/tenant_key_scope_integration_test.go` /
  `uniqueness_integration_test.go` — `escalation_policy_pkey`/
  `escalation_tier_pkey` justified as server-minted;
  `uq_escalation_tier_policy_position` already carries `tenant_id` directly
  and needs no separate justification.
- `internal/store/postgres/app_user_migration_integration_test.go` —
  `escalation_tier.on_call_schedule_id`'s reference to `on_call_schedule` is
  now part of the populated-database rollback-ordering chain that test
  enforces: `escalation_policy`'s own rollback must run before
  `on_call_schedule`'s.
- `internal/kg/extract/schema.TestCorpusCensus` / `TestEveryTableIsANode` /
  `TestMultiLineAlterTableIsExtracted` — updated for the two new tables (42
  tables, 390 columns, 88 indexes, 135 constraints, 8 triggers unchanged; 36
  tenant-scoped tables, up from 34).
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — the nine new routes are exactly the
  published contract, no more, no less; `make contract-breaking` reports no
  breaking change against `origin/master` (additive only).

## Enforcement

- `postgres.TestEscalationPolicyStore_AddTierRejectsCrossTenantSchedule` —
  Decision 2.
- `postgres.TestEscalationPolicyStore_ReorderTiersIsAtomic` — Decision 3.
- `postgres.TestEscalationPolicyStore_RLSIsolatesByTenant` — tenant isolation
  (ADR-TENANCY-002 applied to a store with no privileged-pool role at all,
  same as ADR-ONCALL-001 §7).
- `arch.TestServerWiringUsesTenantScopedPool` /
  `TestEveryServerCapability_IsWiredAtTheCompositionRoot` — pass, confirming
  `EscalationPolicyStore` is wired only from `appPool` and every declared
  `Server` capability this story adds (`SetEscalationPolicies`) is called at
  the composition root.
- `postgres.TestEveryTenantScopedUniqueKey_IsTenantScoped` /
  `TestUniquenessIsScopedToTenant` — the two new surrogate PKs are justified,
  the composite tier-position key needs no separate justification.
- `internal/kg/extract/schema.TestCorpusCensus` — the schema census stays
  exact.
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — Decision 4's nine routes are exactly
  the published contract.

# ADR-ONCALL-001 — On-call schedules are configuration; who's on call is a pure, computed function, never stored

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-05 |
| **Decider** | Acting CTO |
| **Related** | ADR-IDENTITY-002 (identity data placement — `app_user` carries no `tenant_id`; membership re-verification extended here a further time), ADR-TENANCY-001/002 (row-level security, tenant-scoped pool), ADR-ASSET-001 §6 (a foreign key bypasses RLS; re-verify on the writer's own connection — extended to `on_call_participant.user_id`), ADR-ALERTING-002 (the same reduced-concept discipline applied to a different derived fact — "an active window" there, "who's on call" here), docs/PLATFORM-BUILD-PLAN.md §4 / Vol II §5.3 (reduced-concept discipline) |

## Context

E5.2 ("on-call, scheduling & paging") was split into E5.2a (this story: schedules,
rosters, "who's on call now") and E5.2b (escalation policies & paging, deferred —
depends on this story's resolution primitive). E5.2a's job is narrow and
self-contained: model a rotating on-call roster and answer "who is on call for
schedule S at time T", deterministically, with no escalation or notification
side effects at all.

The obvious wrong shape for this is a per-shift table: a row per person per
period ("Alice, 2026-01-06 09:00–2026-01-13 09:00"), generated ahead of time by
a background job, queried by `WHERE starts_at <= now() AND ends_at > now()`.
This is the framework the brief that produced this story explicitly rejects,
and this ADR records why, precisely, before recording what was built instead.

## Decision

### 1. Two tables: the rotation's configuration and its roster, nothing else

`on_call_schedule` (name, `handoff_interval_seconds`, `rotation_start_at`,
`status`) and `on_call_participant` (an ordered, `position`-ranked FK to
`app_user` per schedule) are both real, tenant-owned, first-class operational
objects — the same reasoning `MaintenanceWindow`'s own ADR (ADR-ALERTING-002
§1) applies to a different kind of operator-declared configuration. Neither is
a reified reduced-noun (`docs/PLATFORM-BUILD-PLAN.md` §4, Vol II §5.3): a
schedule is something an operator actually names and edits, not a projection
of something else.

What is NOT a table, anywhere: a stored "who is on call" fact. There is no
`on_call_assignment` row, no `current_on_call_user_id` column on the schedule,
no per-shift row of any kind.

### 2. Who's-on-call(S, T) is a pure function, computed on every read

`domain.OnCallRotationIndex(participantCount, rotationStartAt,
handoffIntervalSeconds, at) -> (index, ok)` takes four primitives and returns
which participant (by 0-based rank in `position` order) holds the seat. It
consults no database, no clock (the caller supplies `at`), and no environment.
The store (`OnCallScheduleStore.OnCall`) does nothing but fetch the schedule's
own configuration and its current ordered roster and apply this function.

The algorithm, exactly:

- `N` = live participant count. `N == 0` ⇒ `ok=false` — an empty rotation is a
  valid, ordinary state (nobody on call), never an error, never a 500, and the
  HTTP contract renders it as a clean 200 with `user_id`/`display_name`
  omitted, not a 404.
- `at < rotationStartAt` ⇒ index 0. The rotation has not begun; the first
  participant holds the seat until it does. This is a distinct rule from the
  modulo below, not an emergent property of it — see Decision 3.
- Otherwise: `elapsed = at - rotationStartAt`; `steps = floor(elapsed /
  handoffIntervalSeconds)`; `index = steps mod N`, computed with a POSITIVE
  modulo (`((steps % N) + N) % N`), because Go's `%` returns a
  sign-of-the-dividend result for a negative dividend (`-1 % 3 == -1`, not
  `2`).
- The interval is **half-open**: `[rotationStartAt + k·interval,
  rotationStartAt + (k+1)·interval)`. At exactly a handoff boundary the
  rotation has already advanced to the next participant — the same half-open
  convention `MaintenanceWindow`'s `[starts_at, ends_at)` and
  `TelemetryRepository.QueryRange`'s `[from, to)` already use in this
  codebase, chosen here for the identical reason: a boundary instant gets
  exactly one owner.

### 3. Why NOT per-shift rows, and why NOT a JSON rules engine

Two frameworks were available and rejected, both because they would make
"who's on call" a SECOND source of truth that could drift from the rotation's
own definition:

- **Per-shift rows.** Generating and storing a row per (participant, period)
  ahead of time needs a background job to keep generating future shifts (what
  happens when the generator falls behind, or a participant is added after
  shifts were already generated three months out?), a query to find "the"
  current row (which itself needs the same boundary reasoning this ADR's pure
  function already has, duplicated into SQL), and reconciliation logic for
  every edit to the roster (add/remove/reorder) to keep already-generated
  shifts consistent with a roster that changed after they were written. None
  of that exists here: the roster IS the schedule, at all times, and "who's on
  call" is answered fresh from it every time, so there is nothing to
  regenerate and nothing that can drift.
- **A JSON rules engine** (e.g. a stored expression describing rotation
  semantics, evaluated by an interpreter). Rejected for the same reason E3.1's
  threshold rules stayed primitive columns rather than a rule language: it
  would let a schedule express behavior this story does not need (arbitrary
  custom cadences, conditional overrides) at the cost of every future reader
  of this schema having to understand an interpreter instead of four typed
  columns and one pure function. `handoff_interval_seconds` +
  `rotation_start_at` + `position`-ordered participants is the smallest model
  that satisfies "deterministic rotating on-call", and it composes with
  nothing else.

### 4. UTC, seconds-based arithmetic only — no calendar, timezone, or DST

`rotation_start_at` is a `timestamptz`; `handoff_interval_seconds` is a plain
integer count of seconds; `OnCallRotationIndex`'s entire arithmetic is
`time.Duration` division and integer modulo. There is no "every Monday at
09:00 local time," no DST transition handling, no calendar month/week
semantics anywhere in this story. This is a deliberate simplification, not an
oversight: calendar-aware rotations (`ADR future work`) are a materially
harder problem (a DST transition changes how many seconds are actually in a
"day," a calendar week boundary is timezone-dependent) that this story does
not need to solve to satisfy its stated acceptance criterion — "who is on call
now," computed correctly for a seconds-based interval anchored to a fixed
instant.

### 5. `on_call_participant.user_id` is re-verified against an ACTIVE membership, exactly like `Incident.AssigneeUserID`

`app_user` is global and carries no `tenant_id` at all (ADR-IDENTITY-002 §3.1).
The foreign key `on_call_participant.user_id REFERENCES app_user (user_id)`
constrains the row to name a person who exists, but says nothing about
whether that person belongs to the tenant declaring the schedule — and, per
ADR-ASSET-001 §6, a PostgreSQL foreign-key trigger runs with the constraint's
own privileges and bypasses row-level security in any case, so the FK alone
cannot enforce a tenant boundary even in principle.
`OnCallScheduleStore.AddParticipant` re-verifies `userID` against
`membership WHERE user_id = $1 AND status = 'active'` on this store's own
tenant-scoped, RLS-enforced connection before the row is written — the
identical defense `IncidentStore.verifyAssigneeExists` already applies to
`Incident.AssigneeUserID`, applied here to a rotation seat instead of an
assignee. A cross-tenant or suspended (`status != 'active'`) user returns
`ErrNotFound`, mapped to HTTP 404 — never a 500, and never a silently-written
row. Mutation-verified: removing the call to `verifyActiveMember` from
`AddParticipant` makes both the cross-tenant and the revoked-membership case
of `TestOnCallScheduleStore_AddParticipantRejectsNonActiveMember` pass a row
that should have been refused.

### 6. Roster mutation concurrency: lock the schedule row, not a client-supplied version

`AddParticipant`/`RemoveParticipant`/`ReorderParticipants` each begin by
taking `SELECT ... FOR UPDATE` on the schedule's own row
(`OnCallScheduleStore.lockScheduleTx`) before reading or writing the roster —
the same lock-the-owning-row technique `IncidentStore.getForUpdateTx` already
uses before appending its own timeline. This serialises concurrent roster
edits against the same schedule without requiring the caller to supply and
track a separate "roster version" alongside the schedule's own
`row_version` — a client always knows which schedule it is editing, and that
identity is what is locked.

`ReorderParticipants` additionally requires `participantIDs` to be **exactly**
the schedule's current participant set (same length, same members, no
repeats), read inside the same lock, before anything is written — a
mismatched set is refused with a `*domain.ValidationError` (HTTP 422) and
nothing is persisted.

`uq_on_call_participant_schedule_position` is declared `DEFERRABLE INITIALLY
DEFERRED`: a reorder that swaps two participants' positions (not merely
appends) would otherwise collide against PostgreSQL's own per-row unique-index
insertion, which is checked as each row is written, not once at the end of the
statement or transaction. Deferring the check to `COMMIT` — once every row in
the reorder has its final, non-colliding position — is the standard,
correct fix for a permutation of a unique column, needing no
application-side workaround (e.g. a temporary negative-offset pass).
`uq_on_call_participant_schedule_user` carries no such requirement (a
participant is never re-pointed at a different user) and stays immediate.

### 7. No privileged pool, no background worker

Unlike `AlertRuleStore`/`MaintenanceWindowStore`, `OnCallScheduleStore` has
exactly one role. "Who's on call" is answered at request time, for one
already-known, already-tenant-scoped caller — there is no cross-tenant
background process in E5.2a that needs to consult every tenant's schedules
from one privileged connection (that category of consumer is what E5.2b's
escalation worker will be, and it is explicitly deferred, along with
everything else escalation/paging/notification-shaped — see Decision 8).
`cmd/controlplane/main.go` wires exactly one `OnCallScheduleStore`, built over
`appPool`, into the HTTP server.

### 8. Contract surface and what is deferred

CRUD on schedules (create/list/get/patch-name-interval-rotation_start-status/
delete); add/remove/reorder on participants; `GET
.../on-call-schedules/{id}/on-call?at=<RFC3339>` for resolution. Deferred,
explicitly, as an honest bound rather than a silent gap:

- **Escalation and paging of any kind.** E5.2b, unbuilt, depends on this
  story's `OnCall` resolution but this story implements no notification,
  evaluator hook, or trigger of any kind.
- **Overrides / coverage swaps** ("Bob covers Alice's Tuesday shift without
  changing the underlying rotation"). This story's roster IS the rotation;
  a temporary substitution that does not disturb the underlying order is a
  distinct feature this story does not attempt.
- **Multi-layer / follow-the-sun scheduling** (composing several schedules,
  e.g. region-by-region handoff). `OnCallSchedule` is a single flat rotation;
  layering multiple schedules together is future work.
- **Calendar-aware cadences** (Decision 4) — seconds-based UTC only.

## Consequences

**What is now guaranteed.** Who is on call for a schedule at any instant is
always derivable purely from that schedule's own configuration and its
CURRENT roster — there is no second, potentially-stale record of "who's on
call" anywhere that could disagree with it
(`TestOnCallScheduleStore_OnCallMatchesTheDomainRotationFunction`,
cross-checked against `domain.OnCallRotationIndex` applied directly).
Removing a participant is reflected immediately, with no reconciliation step
(`TestOnCallScheduleStore_RemoveParticipantCompactsPositionsAndRotationUpdates`).
Reordering is atomic and reflected immediately, including a genuine swap of
two participants' positions
(`TestOnCallScheduleStore_ReorderParticipantsIsAtomicAndRotationReflectsImmediately`).
Deleting a schedule removes its participants with it
(`TestOnCallScheduleStore_DeleteCascadesToParticipants`). Adding a
non-active-member — cross-tenant or revoked — is refused with 404, never a
500 or a silently-written row, mutation-verified to bite
(`TestOnCallScheduleStore_AddParticipantRejectsNonActiveMember`). Tenant
isolation holds even naming the foreign tenant's own real, active member —
the schedule itself is invisible under row-level security before the
membership check is ever reached
(`TestOnCallScheduleStore_RLSIsolatesByTenant`). The rotation math itself is
pure and DB-free, covering N=0, N=1, N=3 across several handoffs, the exact
half-open boundary, before-rotation-start, and a multiple decades in the
future without going out of bounds
(`internal/domain/oncall_test.go`), and the positive-modulo formula is
directly, adversarially unit-tested with negative inputs
(`TestPositiveModulo`) because the public function's own "rotation has not
begun" branch keeps every value it passes to that formula non-negative today
— a mutation of the formula back to a bare `x % n` would otherwise pass every
`OnCallRotationIndex` test in the file unchanged.

**What is not claimed.** No escalation, paging, or notification of any kind
exists yet (E5.2b). No override/coverage-swap mechanism exists — a temporary
substitution requires editing the roster itself. No multi-layer/
follow-the-sun composition exists — one schedule is one flat rotation. No
calendar-aware cadence exists — a rotation that needs "every Monday 09:00
local time" is out of this story's scope entirely, not merely deferred to a
later increment of the same primitive.

## Evidence

- `internal/domain/oncall_test.go` — the rotation math: N=0 (nobody on call,
  not an error), N=1 (always holds the seat, tested from far in the past to a
  decade in the future), N=3 across seven consecutive handoffs in expected
  order, the exact half-open boundary (one nanosecond before/at/after,
  `TestOnCallRotationIndex_BoundaryIsHalfOpen`), before-rotation-start pins to
  index 0 (`TestOnCallRotationIndex_BeforeRotationStartHoldsTheFirstParticipant`),
  ~50 years in the future cross-checked against an independently-written
  int64 formula (`TestOnCallRotationIndex_FarFutureStaysInBounds`), a
  non-positive interval does not panic
  (`TestOnCallRotationIndex_NonPositiveIntervalDoesNotPanic`), and the
  extracted positive-modulo helper directly, with negative inputs
  (`TestPositiveModulo`). Mutation-verified by hand: reverting
  `positiveModulo` to a bare `x % n` flips 4 of `TestPositiveModulo`'s 12
  cases to FAIL while leaving every `OnCallRotationIndex` test passing
  unchanged — proof that the direct unit test on the extracted helper, not
  the public function's own tests, is what makes this guard load-bearing.
- `internal/store/postgres/oncall_store_integration_test.go` — CRUD +
  optimistic locking against real PostgreSQL
  (`TestOnCallScheduleStore_CreateGetListUpdateDelete`); two-tenant RLS
  isolation across Get/List/Update/Delete/AddParticipant, naming the foreign
  tenant's own real active member
  (`TestOnCallScheduleStore_RLSIsolatesByTenant`); the membership
  re-verification defense against both a cross-tenant and a revoked user,
  mutation-verified to bite
  (`TestOnCallScheduleStore_AddParticipantRejectsNonActiveMember`); append
  ordering (`TestOnCallScheduleStore_AddParticipantAppendsAtNextPosition`);
  duplicate-add conflict
  (`TestOnCallScheduleStore_AddParticipantRejectsDuplicateUser`); removal
  compaction and immediate rotation recomputation
  (`TestOnCallScheduleStore_RemoveParticipantCompactsPositionsAndRotationUpdates`);
  atomic reorder including a genuine position swap, and immediate rotation
  reflection
  (`TestOnCallScheduleStore_ReorderParticipantsIsAtomicAndRotationReflectsImmediately`);
  reorder's set-equality refusal
  (`TestOnCallScheduleStore_ReorderParticipantsRejectsMismatchedSet`); the
  store's `OnCall` cross-checked against the domain function directly across
  seven handoffs
  (`TestOnCallScheduleStore_OnCallMatchesTheDomainRotationFunction`); the
  empty-roster non-error case
  (`TestOnCallScheduleStore_OnCallWithNoParticipantsIsEmptyNotAnError`); and
  cascade delete
  (`TestOnCallScheduleStore_DeleteCascadesToParticipants`).
- `internal/httpapi/handlers_oncall_test.go` — authorization, 501-until-
  configured, resolved-tenant (not caller-supplied) on create, 422 on a
  non-positive interval and on a patch missing `row_version`, 409 on a
  version-mismatch patch and on a duplicate-participant add, 404 on a
  non-active-member add/remove/get/delete, `at` defaulting to now versus an
  explicit RFC3339 value versus a malformed one, and the empty-roster
  resolution rendering `user_id`/`display_name` omitted rather than present-
  as-null or 404.
- `internal/store/postgres/tenant_key_scope_integration_test.go` /
  `uniqueness_integration_test.go` — `on_call_schedule_pkey`/
  `on_call_participant_pkey` justified as server-minted; both composite
  unique constraints already carry `tenant_id` directly and need no separate
  justification.
- `internal/store/postgres/app_user_migration_integration_test.go` —
  `on_call_participant.user_id`'s reference to `app_user` is now part of the
  populated-database rollback-ordering chain that test enforces.
- `internal/kg/extract/schema.TestCorpusCensus` / `TestEveryTableIsANode` /
  `TestMultiLineAlterTableIsExtracted` — updated for the two new tables (40
  tables, 374 columns, 85 indexes, 128 constraints, 8 triggers unchanged; 34
  tenant-scoped tables, up from 32).

## Enforcement

- `internal/domain.TestOnCallRotationIndex_*` / `TestPositiveModulo` —
  Decisions 2, 4.
- `postgres.TestOnCallScheduleStore_AddParticipantRejectsNonActiveMember` —
  Decision 5.
- `postgres.TestOnCallScheduleStore_ReorderParticipants*` — Decision 6.
- `postgres.TestOnCallScheduleStore_RLSIsolatesByTenant` — tenant isolation
  (ADR-TENANCY-002 applied to a store with no privileged-pool role at all,
  Decision 7).
- `arch.TestPrivilegedReads_AreScopedToATenant` /
  `TestPrivilegedMutations_AreScopedToAnOwner` /
  `TestServerWiringUsesTenantScopedPool` — pass trivially (no privileged-pool
  usage exists to flag), confirming Decision 7.
- `internal/kg/extract/schema.TestCorpusCensus` — the schema census stays
  exact.
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — Decision 8's ten routes are exactly
  the published contract, no more, no less.

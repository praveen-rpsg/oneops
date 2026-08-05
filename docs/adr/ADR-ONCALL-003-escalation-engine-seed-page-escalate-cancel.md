# ADR-ONCALL-003 — The escalation engine: a per-incident work-queue, seeded and paged by two decoupled leader-gated passes, cancelled by re-checking the incident on every cycle

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-05 |
| **Decider** | Acting CTO |
| **Related** | ADR-ONCALL-001 (on-call schedules, `domain.OnCallRotationIndex`, the page-time active-membership defense on `on_call_participant.user_id`), ADR-ONCALL-002 (escalation policy + tier config, explicitly deferring this engine), ADR-CONCURRENCY-002/005/006/007 (at-least-once, fenced claim, charged attempt, every work-queue is claimed and fenced — this table's shape is `notification`'s, restated), ADR-TENANCY-001/002/003/012 (row-level security, isolation is a property of wiring, re-derive ownership rather than trusting a queue row, privileged-read predicate discipline), ADR-ALERTING-004 (the "separate reconciliation pass, not a hook in the hot path" precedent this engine's Seeder reuses), `docs/PLATFORM-BUILD-PLAN.md` E5.2b-2 |

## Context

ADR-ONCALL-002 built `escalation_policy`/`escalation_tier` — an operator can
declare a named escalation ladder — and built nothing that *uses* it: no
work-queue, no worker, no paging, no incident wiring. That ADR's own
Consequences section states this plainly and names the reason for the split:
an engine is a concurrency-heavy state machine (claim-and-fence, idempotent
at-least-once delivery, page-time re-verification against revoked
membership) that deserves independent review from additive CRUD.

Separately, ADR-ONCALL-001's own review recorded a requirement this engine
must satisfy once it exists: "the page-time active-membership re-check
(E5.2a review nit 1)" — an on-call user resolved by rotation math may have
had their membership revoked since the schedule was last edited, and paging
them anyway is a silent failure a caller has no way to detect. E5.2a's own
`AddParticipant` re-verifies membership at *write* time; nothing until now
re-verified it at *page* time, which is the moment that actually matters
(days or weeks can pass between adding a participant and a rotation reaching
them).

This ADR is the engine: `escalation_state`, a leader-gated seeding pass, and
a leader-gated paging/escalating pass.

## Decision

### 1. `escalation_state` is a per-incident work-queue, shaped exactly like `notification`

One row per incident under escalation (`UNIQUE (tenant_id, incident_id)` —
CTO-locked, at most one active escalation per incident), tracking
`current_tier_index` (0-based, into the policy's ordered `escalation_tier`
ladder), `next_attempt_at` (when this row is next due), `claimed_at` (the
fencing token), `status` (`active`/`acked`/`resolved`/`exhausted`), and
`attempts`. This is deliberately the *same claim/fence shape*
`notification` already has (ADR-CONCURRENCY-007): `claimed_at` alone is the
lease, `status` does not change on claim, and every outcome write
(`Advance`, `MarkTerminal`) is fenced on the exact token `ClaimDue` handed
out. No new concurrency primitive was invented for this table; the pattern
proven once for `notification`'s delivery worker is copied verbatim
(`internal/store/postgres/escalation_state_store.go`).

`status` defaults to `'active'`, not `'pending'`, so
`arch.TestEveryWorkQueue_HasAFencingToken`'s pending-default detector does
not classify this table as a queue by that particular derivation rule — it
is one by construction (the migration's own header comment records this
explicitly, so the omission from that guard's subject set is not mistaken
for an oversight later).

`escalation_state` has **no HTTP write path and no admin API at all**. It is
config-derived bookkeeping — a legitimate work-queue row, not a reified
Alert/Event/Group noun (`docs/PLATFORM-BUILD-PLAN.md` §4, Vol II §5.3): an
operator never names or edits one, the same way nobody edits a
`webhook_delivery` row directly. Its only writer is the Seeder below.

### 2. Tenant-default policy (MVP), per-asset routing deferred

The Seeder enrols an eligible incident at "the tenant's default escalation
policy," defined exactly as the CTO locked it: the tenant's own single
*active* `escalation_policy`, or — when a tenant has declared more than one —
the earliest by `created_at` (ties broken by `policy_id` for a total order,
so the choice is deterministic even if two policies are created in the same
transaction-commit instant). A tenant with **no** active policy has its
eligible incidents left unseeded, not erroring — `Store.Seed` returns a
`skippedNoPolicy` count precisely so this is observable (`Seeder.RunOnce`
logs it at `debug`) without treating an ordinary, unconfigured-tenant state
as a fault.

**Explicitly deferred**: per-asset / per-service policy routing — *which*
policy an incident uses, chosen by something about the incident itself
(severity, the affected CI, its service tier) rather than "the one and only
default." ADR-ONCALL-002 already named this as future work; this ADR does
not close it, and the schema does not preclude it (`escalation_state.
policy_id` is an ordinary column a later routing decision could populate
differently — nothing here assumes "one policy per tenant" as an invariant,
only as today's *selection rule*).

### 3. Seeding is a separate, decoupled reconciliation pass — never a hook in the alerting hot path

`escalation.Seeder` mirrors `internal/grouping.Reconciler`'s own justification
exactly (ADR-ALERTING-004's precedent, restated here for a second feature):
a periodic sweep that reconsiders the *current* set of open, unacknowledged,
alert-sourced incidents with no `escalation_state` row, rather than an
inline side effect wired into `alerting.Evaluator`'s transition handling.
`internal/alerting` is untouched by this story — no new call, no new
dependency, no new field on anything that package owns. This is a
deliberate reduction of blast radius: a bug in the escalation engine cannot
stall or slow the alert-evaluation hot path, and the hot path's own
correctness proofs (ADR-TENANCY-012 §2's notify-then-persist ordering, the
transition-only write discipline) are untouched by this story.

The seeding query itself (`escalationSeedCandidatesQuery`,
`internal/store/postgres/escalation_state_store.go`) is one `LEFT JOIN
LATERAL` from `incident` to its tenant's own `escalation_policy` set,
filtered to `source = 'alert' AND status = 'open'` with `NOT EXISTS` against
`escalation_state` — privileged and cross-tenant by design (it discovers
*every* tenant's newly-eligible incidents in one pass, the same shape
`grouping.Store.OpenAlertIncidents`/`TenantsWithOpenAlertIncidents` already
draw), but the `ep.tenant_id = i.tenant_id` join condition is the ONLY place
`escalation_state.policy_id` is ever associated with `escalation_state.
incident_id`'s tenant — there is no untrusted caller to re-verify against
afterward (unlike `EscalationPolicyStore.AddTier`'s re-verification of a
*client-supplied* `on_call_schedule_id`, ADR-ASSET-001 §6), because nothing
about this INSERT is client-supplied at all.

### 4. Ack/resolve cancels — checked on every cycle, not via a callback

There is no hook anywhere that "cancels" an `escalation_state` row the
moment an incident is acknowledged. Instead, `Worker.process` re-loads the
incident from its own store on **every** claim cycle, before doing anything
else, and if `Status != IncidentOpen` it marks the row terminal
(`acked` if `IncidentAcknowledged`, `resolved` for every other non-open
status — investigating, resolved, closed, reopened) and returns without
paging. This is the entire cancellation mechanism: a `PATCH .../transition`
to `acknowledged` needs to know nothing about escalation, touch no new
column, and call no new hook — the next time (at most `Worker.Interval`
later, default 5s) a claim cycle reaches that row, it observes the change
and stops. This is exactly the same "separate pass observes changed state on
its own next tick" decoupling the Seeder gets from the hot path, applied to
cancellation instead of enrolment.

**Honest bound, stated once and not hidden in the code.** "Resolved" is a
catch-all label for four distinct statuses (investigating, resolved, closed,
reopened); this engine does not distinguish among them, and in particular a
`reopened` incident that already has a terminal `escalation_state` row is
**not** re-armed — re-escalation-on-reopen is explicitly out of scope. An
operator who reopens an incident that already escalated to exhaustion gets
no fresh paging; they must page manually or via some other channel. This is
a real gap, named rather than silently accepted.

### 5. Page-time active-membership re-check (E5.2a review nit 1) — the hard requirement this ADR closes

`Worker.page` resolves who is on call for the current tier's schedule
(`OnCallResolver.OnCall`, E5.2a's unchanged rotation math), and — this is the
new part — before ever building a `Notification`, calls
`MembershipChecker.ActiveMember(ctx, userID)`. If there is no on-call user at
all (an empty roster) **or** the resolved user is not an ACTIVE member of
the tenant, the tier is **not** paged. The ladder still advances
(`current_tier_index++`, due again after `WaitSeconds`) exactly as if it had
been paged — a revoked or absent on-call person must not silently stall the
whole chain, they must be skipped past. This is recorded, never silently
dropped: `Metrics.IncSkippedRevoked()` fires, and a `slog.Warn` names the
schedule/user/tier.

`MembershipChecker` is satisfied by a new, narrow, public method,
`MembershipStore.ActiveMember(ctx, userID) (bool, error)`
(`internal/store/postgres/membership_store.go`) — the identical `SELECT
EXISTS(... WHERE user_id = $1 AND status = 'active')` shape
`OnCallScheduleStore.verifyActiveMember`/`IncidentStore.verifyAssigneeExists`
each already carry privately, exposed here rather than duplicated a fourth
time, called on the tenant-scoped connection bound to the escalating
incident's own tenant (§7 below) so row-level security supplies the
isolation exactly as it does for every other tenant-scoped read in this
codebase.

Mutation-verified (not merely asserted): `TestWorker_RevokedMember_
NotPaged_MutationVerified` (`internal/escalation/worker_test.go`) runs the
*identical* scenario twice, flipping only `ActiveMember`'s answer — inactive
produces zero pages and an advance; active produces exactly one page. If the
check were dead code (always skipping, or never consulted), the second run
would also come back with zero pages; it does not. The live-Postgres
counterpart, `TestMembershipStore_ActiveMember`
(`internal/store/postgres/membership_store_integration_test.go`), grants
then revokes a real membership and asserts the boolean flips against actual
row-level-security-scoped SQL, not a fake.

### 6. No double-page: `ClaimDue`'s fence is the whole argument, restated for a fourth queue

Two workers (two replicas racing during a leadership handover, or — in this
single-leader design — two ticks of the same `Worker.RunOnce` loop that
somehow overlapped) must not page the same tier twice. The argument is
identical to every other queue in this codebase (ADR-CONCURRENCY-002/007):
`ClaimDue`'s `FOR UPDATE SKIP LOCKED` plus its `status = 'active' AND
next_attempt_at <= $1 AND (claimed_at IS NULL OR claimed_at < staleBefore)`
predicate mean at most one caller can ever hold a given row's claim at a
given moment; `Advance`/`MarkTerminal` are fenced on that exact
`claimed_at` value and return `ErrStaleClaim` if the row was reclaimed since
—the identical "duplicate over silent loss" doctrine ADR-TENANCY-012 §2
states for the alerting evaluator, applied here to a paging decision instead
of a notification-enqueue decision.

**What this proves, and what it does not.** Under normal operation (no
crash, no lease expiry mid-processing), `ClaimDue`'s row lock makes a second
concurrent claim of the *same* row impossible while the first is in flight —
`TestWorker_ConcurrentRunOnce_NeverPagesTheSameTierTwice`
(`internal/escalation/worker_test.go`) forces exactly this race with two
`Worker`s sharing one `Store` under `-race` and asserts exactly one page. It
does **not** prove a worker that pages a tier and then crashes or is
demoted *before* its `Advance` call lands can never have its page
duplicated by whichever process reclaims the row after the lease elapses —
that is the same accepted, named residual ADR-CONCURRENCY-006's retry budget
and ADR-TENANCY-012 §2 both already accept for every other at-least-once
delivery path in this platform: between "the operator is paged twice for
one real escalation" and "the operator is never paged at all," this
codebase has already, repeatedly, chosen the first. `TestWorker_
StaleClaimFence_OutcomeNotOverwritten` proves the narrower, real guarantee
instead: a stale-token outcome write is rejected and never overwrites
whatever the reclaiming worker already decided, so the *bookkeeping* stays
correct even in the crash-and-reclaim window, whatever happened to the page
itself.

### 7. Every tenant-scoped read is bound to the claimed row's own tenant — never an ambient one

`Worker.process` calls `domain.WithTenant(ctx, &domain.Tenant{TenantID:
st.TenantID})` and uses that bound context for every subsequent read: the
incident (`IncidentReader`, `*postgres.IncidentStore` over the tenant-scoped
pool), the policy's tiers (`PolicyReader`,
`*postgres.EscalationPolicyStore`), who is on call
(`OnCallResolver`, `*postgres.OnCallScheduleStore`), and active membership
(`MembershipChecker`, `*postgres.MembershipStore`) — all FOUR are the exact
same tenant-scoped store instances `cmd/controlplane/main.go` already wires
for the HTTP admin API, reused unchanged rather than reimplemented over a
privileged connection. `st.TenantID` is not a second, weaker source of
truth for "whose data is this": it was written into `escalation_state`
exactly once, by `Store.Seed`, from the SAME query's own `ep.tenant_id =
i.tenant_id` join — the incident row's own `tenant_id`, never a
client-supplied or guessed value (ADR-TENANCY-003). Only two things run on
the privileged pool at all: `EscalationStateStore` itself (the work-queue,
keyed by globally-unique `state_id`/`incident_id`, exactly like
`notification`/`webhook_delivery`) and `notificationSvc.Enqueue` (which
takes `TenantID` as an explicit struct field for the identical reason
`domain.Notification`'s own doc comment already states — the delivery
worker that eventually sends it is itself cross-tenant).

**Precisely what each test proves, stated without overclaiming.**
`TestWorker_BindsEachTenantScopedReadToTheStateRowsOwnTenant`
(`internal/escalation/worker_test.go`) is a UNIT-level proof only: each fake
dependency (`IncidentReader`/`PolicyReader`/`OnCallResolver`/
`MembershipChecker`) records the literal ctx tenant it was called with
(`tenantCalls`, not an inference from which fake-map entry was returned), and
the test asserts tenant A's own incident/policy/schedule/user were each
queried under `ctx` bound to `"tn-a"` and tenant B's under `"tn-b"` — never
crossed, never a third value. This is deliberately a **stronger** check than
"the two resulting notifications were not crossed": a broken binding that
bound every claimed row to the *same* wrong tenant (mutation-verified by
hand: hard-coding `scopedCtx := domain.WithTenant(ctx, &domain.
Tenant{TenantID: "WRONG-TENANT"})` at `Worker.process`, `worker.go`, made
every one of the eight assertions fail, reverted afterward) would, in this
test's specific fixture, also happen to produce zero notifications rather
than two crossed ones — the direct ctx-tenant assertion catches it either
way; a notification-count-only assertion would not necessarily have.
`fakeUsers` is deliberately excluded from this proof: `UserReader.Get`
resolves `app_user`, a GLOBAL table with no row-level security
(ADR-IDENTITY-002 §3.1), so it is correctly NOT tenant-scoped, and this test
does not claim otherwise.

This unit test proves the Worker's own Go code passes the right ctx to each
dependency. It does **not**, and cannot, prove PostgreSQL's row-level
security itself confines a real connection — fakes have no RLS to defeat.
That is a live-database property, proven separately:
`TestEscalationStateStore_RLSIsolatesByTenant`
(`internal/store/postgres/escalation_state_store_integration_test.go`)
proves the live-Postgres half — a connection bound to tenant B cannot read
tenant A's real `escalation_state` row even naming its `state_id` directly,
because `FORCE ROW LEVEL SECURITY` is in force on this table exactly as it
is on every other `TenantOwnedTables` entry.

### 8. Paging reuses `notification` and `internal/httpapi`'s user store unchanged

A page is an email `domain.Notification` (`domain.NewNotification(st.
TenantID, domain.NotificationEmail, user.Email, subject, body)`) enqueued
through the SAME `notification.Service` instance
`cmd/controlplane/main.go` already builds for the policy `"notification"`
action, over the SAME privileged `notification.Store`. `internal/
notification` has zero changes in this story — no new channel, no new
field, no new call shape. The on-call user's email is resolved through
`*postgres.UserStore.Get` (already wired for `srv.SetUsers`): `app_user` is
a GLOBAL table outside row-level security (ADR-IDENTITY-002 §3.1), so this
read needs no tenant binding of its own, the identical reasoning
`OnCallScheduleStore.OnCall`'s own doc comment already gives for resolving
`display_name` the same way.

## Alternatives considered

- **A hook inside `alerting.Evaluator`'s transition handling, instead of a
  separate Seeder.** Rejected for the identical reason ADR-ALERTING-004
  rejected it for grouping: escalation eligibility must be reconsidered
  against the *current* full set of open incidents on every pass (an
  incident can sit unacknowledged for any length of time before anyone
  looks at it again), not derived once at transition time, and coupling it
  to the hot path would let an escalation-engine defect stall alert
  evaluation.
- **A callback fired by `IncidentStore.SetStatus` on an acknowledge/resolve
  transition, instead of a re-check on every claim cycle.** Rejected: it
  would require `internal/store/postgres`'s incident package to know about
  escalation at all (a dependency this ADR's whole design avoids in both
  directions — escalation depends on incident, never the reverse), and it
  reintroduces exactly the "did the callback actually fire, and did it fire
  exactly once" concurrency question this ADR spends its effort routing
  around by making cancellation a *read*, not a write-time side effect.
- **Silently dropping a page when the on-call user is revoked, rather than
  advancing.** Rejected — this is the precise failure E5.2a's review named:
  a revoked on-call user would otherwise silently swallow the whole
  escalation chain, and nobody would ever be paged for a real incident.
- **A fourth status value for "reclaimed and abandoned," instead of relying
  on `claimed_at` + lease alone.** Rejected for the same reason `notification`
  already rejected it (that table's own migration comment): `claimed_at`
  going stale past the lease is already distinguishable without a status
  the `ClaimDue` predicate has to special-case.

## Consequences

**What is now guaranteed.** An open, unacknowledged, alert-sourced incident
in a tenant with an active escalation policy is paged at tier 0 within one
Seeder + one Worker interval of appearing (mutation-provable:
`TestWorker_PagesTier0ForOpenUnackedIncident`,
`TestEscalationStateStore_SeedEnrollsOpenUnackedAlertIncidentAtTenantDefaultPolicy`).
It re-pages the next tier after `WaitSeconds` if still unacknowledged
(`TestWorker_EscalatesToNextTierWhenStillUnacked`), and never pages an
already-acked or otherwise-non-open incident again
(`TestWorker_AckedIncident_StopsEscalationAndNeverPages`,
`TestWorker_ResolvedIncident_StopsEscalation`). A revoked or absent on-call
user is skipped, not silently stalling the ladder, and this is
mutation-verified to actually gate the send rather than being dead code
(`TestWorker_RevokedMember_NotPaged_MutationVerified`,
`TestMembershipStore_ActiveMember`). Exhaustion is terminal and does not
re-fire (`TestWorker_ExhaustedAfterLastTier`,
`TestWorker_EmptyTierLadder_ExhaustsImmediately`). At most one
`escalation_state` row exists per incident, enforced by the database, not
merely by application logic
(`TestEscalationStateStore_UniqueConstraintPreventsSecondRowPerIncident`).
Concurrent workers cannot page the same due tier twice under normal
operation, and a stale claim's outcome write is rejected rather than
overwriting a reclaiming worker's decision
(`TestWorker_ConcurrentRunOnce_NeverPagesTheSameTierTwice`,
`TestWorker_StaleClaimFence_OutcomeNotOverwritten`,
`TestEscalationStateStore_ClaimDueClaimsAndFencesTheOutcome`). The Worker's
own code binds each tenant-scoped read to the claimed row's own tenant, never
a fixed or crossed one, proven at the unit level by asserting the literal ctx
tenant each dependency call received
(`TestWorker_BindsEachTenantScopedReadToTheStateRowsOwnTenant`); that a real
PostgreSQL connection is actually confined by that binding is a separate,
live-database claim proven by
`TestEscalationStateStore_RLSIsolatesByTenant`. Neither `internal/alerting`
nor `internal/notification` nor `internal/grouping` has any change in this
story.

**What is not claimed.** Per-asset/per-service policy routing does not
exist — every incident in a tenant uses the same one default policy. A
reopened incident whose escalation already ran to a terminal state is not
re-armed. A worker that pages a tier and then crashes or is demoted before
recording the advance can, in the narrow reclaim window, cause that tier to
be paged twice — an accepted, named residual identical in kind to every
other at-least-once delivery path already in this codebase, not a defect
unique to this story. SMS/push channels do not exist; a page is email only,
constrained by whatever `internal/notification`'s own email channel
currently supports (nothing is wired there yet — see that package's own
honest bounds). There is no admin API to inspect `escalation_state`
directly; an operator's only visibility is the Prometheus counters
(`oneops_escalation_*`) and the incident/notification records themselves.

## Evidence

- `internal/domain/escalation_state_test.go` — `NewEscalationState` field
  validation, `EscalationStateStatus.Valid`/`Terminal`.
- `internal/escalation/worker_test.go` — the full state machine against
  fakes: seed→page, escalate-on-timeout, exhaustion (including a zero-tier
  ladder), ack/resolve cancellation (all four non-open statuses), the
  page-time membership mutation proof, concurrent-claim no-double-page,
  stale-claim fencing, tenant re-derivation across two tenants in one
  batch, claim/read-error handling, and mid-batch cancellation releasing
  unused claims.
- `internal/escalation/seeder_test.go` — `RunOnce` records seeded/skipped
  counts, survives a seed error without panicking, and treats "no active
  policy" as non-error.
- `internal/store/postgres/escalation_state_store_integration_test.go` —
  real-Postgres `Seed` (enrollment, idempotence, earliest-policy tie-break,
  no-active-policy skip, archived-policy skip, manual/acked incidents
  ignored), `ClaimDue`/`Advance`/`MarkTerminal` fencing, RLS tenant
  isolation naming a real cross-tenant `state_id`, and the database
  `UNIQUE` constraint rejecting a second row for one incident.
- `internal/store/postgres/membership_store_integration_test.go` —
  `TestMembershipStore_ActiveMember`: grant then revoke a real membership,
  assert the boolean flips against real row-level-security-scoped SQL.
- `internal/store/postgres/tenant_key_scope_integration_test.go` /
  `uniqueness_integration_test.go` — `escalation_state_pkey` justified as
  server-minted; `uq_escalation_state_tenant_incident` already carries
  `tenant_id` directly and needs no separate justification.
- `internal/store/postgres/app_user_migration_integration_test.go` —
  `escalation_state`'s references to `incident` and `escalation_policy` are
  now part of the populated-database rollback-ordering chain: its own
  rollback runs before either of theirs.
- `internal/kg/extract/schema` `TestCorpusCensus` / `TestEveryTableIsANode` /
  `TestIndexOnClauseMaySpanLines` / `TestMultiLineAlterTableIsExtracted` —
  updated for the one new table (43 tables, 402 columns, 90 indexes, 140
  constraints, 8 triggers unchanged; 37 tenant-scoped tables, up from 36).
- Full unit suite green under `-race`; full integration suite green under
  `-race` against real PostgreSQL (439 PASS / 0 FAIL / 2 pre-existing
  unrelated SKIPs — perf tests excluded under the race detector by their
  own design).

## Enforcement

- `escalation.TestWorker_RevokedMember_NotPaged_MutationVerified` — §5's
  page-time active-membership re-check.
- `postgres.TestMembershipStore_ActiveMember` — §5's live-Postgres half.
- `escalation.TestWorker_ConcurrentRunOnce_NeverPagesTheSameTierTwice` /
  `TestWorker_StaleClaimFence_OutcomeNotOverwritten` — §6's fencing
  argument.
- `postgres.TestEscalationStateStore_ClaimDueClaimsAndFencesTheOutcome` —
  §6's live-Postgres half.
- `escalation.TestWorker_BindsEachTenantScopedReadToTheStateRowsOwnTenant` —
  §7's unit-level claim: each tenant-scoped dependency call is bound to the
  claimed row's own tenant (asserted on the literal ctx tenant each fake
  received, not inferred from output), mutation-verified by hand
  (hard-coding a single wrong tenant at `Worker.process` fails all eight
  assertions; reverted).
- `postgres.TestEscalationStateStore_RLSIsolatesByTenant` — §7's SEPARATE
  live-database claim: a real connection bound to one tenant cannot read
  another's `escalation_state` row. Neither test stands in for the other.
- `postgres.TestEscalationStateStore_UniqueConstraintPreventsSecondRowPerIncident`
  — the CTO-locked "at most one escalation per incident" invariant.
- `arch.TestServerWiringUsesTenantScopedPool` /
  `TestWorkersStartOnlyUnderLeadership` — the Seeder and Worker are
  registered through `workers = append(...)` and started only under
  `ops.RunAsLeader`, never a direct goroutine.
- `internal/kg/extract/schema.TestCorpusCensus` — the schema census stays
  exact.

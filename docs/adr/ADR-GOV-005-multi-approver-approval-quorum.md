# ADR-GOV-005 — Approval requires a configurable number of distinct approvers, enforced fail-safe

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-14 |
| **Decider** | Acting CTO |
| **Related** | ADR-CP6 (governance owns every Lifecycle mutation — this ADR adds no second writer), ADR-AUDIT-005 (one atomic mutation + audit event per operation), ADR-TENANCY-001/002 (tenant ownership, RLS), ADR-IDENTITY-002/003 (why the approving identity is not resolvable against `app_user` today) |

## Context

§8 Approval today is a single transition: `OpApproval` moves an object
Draft/In Review → Approved the instant one caller with write permission calls
`POST /governance/{id}/approve`. `planTransition`'s comment names this
precisely — *"Review passed (non-constitutional) → Approved"* — but nothing
in the engine asks whether a *review* actually happened, only whether one
authorized actor said so once.

For ratification of governed content this is a gap a customer would
reasonably expect closed: many organisations require two (or more) named
people to approve before something is treated as approved, and today's
implementation cannot express that — the required number of approvers is
implicitly and permanently one.

Two things already exist that this decision leans on rather than
reinventing:

- **Settings.** `internal/domain/setting.go` has carried a validated,
  tenant-scoped key-value registry (`SettingDefinitions`) since OPS-S046, but
  every defined key so far has been storage-only — nothing in the platform
  *read* one to change its own behaviour. Quorum is the first real
  consumer.
- **The engine's optional-port idiom.** `ReplacementTester` is wired
  separately from `NewEngine` (`SetReplacementTester`), consulted with I/O
  *inside* the transaction on the row `GetForUpdate` already locked, and its
  absence fails the operation closed (`ErrReplacementTesterUnavailable`).
  Quorum needs the identical shape: an I/O-dependent precondition evaluated
  on a locked row, failing closed when unwired.

### A real gap this decision does not close

The identity `OpApproval`'s `Actor` field carries — the verified JWT `sub`,
passed straight through by `handlers_governance.go` since the engine's first
version — has never been resolved against `app_user` anywhere in this
codebase. `authenticate()`'s dev-mode branch hard-codes it to the literal
string `"system"`; `auth.Verifier.Verify` returns the raw, unverified-against-
any-registry `sub` claim in production. No request path looks it up. This
means:

1. **Self-approval prevention is not implementable today.** It would require
   knowing who *submitted* the governance object for review, and
   `ConfigObject` carries no such field — `RatifiedBy` is written *by*
   ratification, not read as an input to it. Inventing a submitter/creator
   field is a new entity concern, not a one-story addition to an existing
   one, so it is out of scope here and recorded as a follow-up below.
2. **`approver_user_id` cannot be a foreign key to `app_user`.** Requiring
   that binding would make Approval fail — with a foreign-key violation
   instead of a recorded approval — for exactly the identities that already
   exercise it today, including the dev-mode `"system"` actor. This is
   flagged, not silently designed around: see **Decision, point 3**.

Neither of these is a decision this ADR is entitled to make silently; both
are called out explicitly rather than worked around with an invented field.

## Decision

**Approval requires N distinct approvers, where N is a per-tenant
`Setting`, and the object never reaches `Approved` on fewer than N —
enforced inside the same transaction the Governance Engine already owns for
every other §8 operation.**

1. **`governance_required_approvals` is a new `SettingDefinition`**
   (`internal/domain/setting.go`): `int`, `Min: 1`, `Max: 10`, `Default: "1"`.
   Default 1 reproduces today's single-actor approve exactly — this is the
   backward-compatibility guarantee, not an incidental default.

2. **`approval_record` is a new tenant-owned table**
   (`20260814000001_approval_record.sql`): one row per distinct approver per
   governance object, `UNIQUE (tenant_id, governance_id, approver_user_id)`.
   That constraint is the entire duplicate-approver defence — a second
   approval from the same approver cannot be *recorded*, not merely
   discouraged by application logic a second code path could bypass
   (live-proven: `postgres.TestApprovalStore_DuplicateApproverRejectedByConstraint`).
   RLS is `ENABLE`/`FORCE` with the standard `tenant_id = current_setting(...)`
   policy, matching `setting` and `team`.

3. **`approver_user_id` is NOT a foreign key to `app_user`.** It is the same
   opaque, platform-verified string every other governance audit event
   already names as `Actor`. Requiring FK integrity here would be *more*
   strict than the platform is anywhere else about this identity, and the
   first thing it would break is the dev-mode `"system"` actor this
   platform's own tests and local runs depend on. This is recorded as the
   identity-binding gap it is, not resolved by this ADR.

4. **The Governance Engine gains one new optional port,
   `ApprovalRecorder`** (`Record`, `CountDistinct`), wired exactly like
   `ReplacementTester`: `SetApprovalRecorder`, consulted inside `Execute`
   between the plan and the mutation, on the `configuration_object` row
   `GetForUpdate` already locked. **Absence fails Approval closed**
   (`ErrApprovalRecorderUnavailable`) — quorum cannot be silently skipped by
   an engine that forgot to wire it, the same posture Replacement already
   has for its four-part test.

5. **Below quorum, the approval commits; the object does not move.**
   `Record` succeeds, `CountDistinct` runs in the same transaction, and if
   the count is still short of the tenant's threshold, `planTransition`'s
   `Approved` outcome is overridden back to the object's *current, unchanged*
   Lifecycle/Retention/Authority before the existing `ApplyDimensions` call
   runs. **Governance remains the sole writer of Lifecycle** (ADR-CP6): this
   is not a second writer or a bypass of `ApplyDimensions`, it is the same
   single writer choosing, per transaction, which value to write. One audit
   event is still appended (ADR-AUDIT-005) — recording that an approval was
   cast and by whom, and the resulting count against the required
   threshold — because an approval that leaves no trace when it does not
   immediately trigger a transition is not "no state changed", it is a
   fact this platform failed to write down.

6. **Duplicate approver is rejected, not silently absorbed.**
   A second `Record` call for the same `(tenant_id, governance_id,
   approver_user_id)` returns `governance.ErrAlreadyApproved`, mapped to
   `409 conflict`. It is deliberately *not* treated as an idempotent no-op
   that returns 200: doing so would make it indistinguishable, from the
   caller's side, from a second successful vote — exactly the ambiguity a
   quorum control exists to remove.

7. **`POST /governance/{id}/approve` reports quorum status.** The response
   gains `approvals: {count, required, met, summary}` where `summary` reads
   `"approvals: N of required M"`. `state.lifecycle` in the same response is
   the object's *actual* current lifecycle — unchanged below quorum, exactly
   as (5) requires. **`GET /governance/{id}/approvals`** is new and
   read-only: every distinct approver recorded so far, plus the same
   count/required/met status, without recording anything.

8. **The threshold is resolved per request**, not cached: `execGovernance`
   reads the tenant's `SettingRepository.GetAll` (already wired for
   `/admin/settings`) via `domain.EffectiveRequiredApprovals`, falling back
   to the definition's own default (1) when no `SettingRepository` is wired
   at all — the same "not configured yet" posture `listSettings` already
   has.

## What this does NOT violate

- **ADR-CP6 (governance owns every Lifecycle mutation).** No new writer is
  introduced. `ApplyDimensions` is called exactly as before, with a value
  the engine — and only the engine — chooses.
- **ADR-AUDIT-005 (one atomic mutation + audit event).** Every `Execute`
  call, quorum met or not, still produces exactly one audit event in the
  same transaction as whatever it mutates (which may be a no-op write to
  `configuration_object`, or nothing at all for Deletion/Extension-shaped
  operations — Approval is not those, so it always goes through
  `ApplyDimensions`, met or not).
- **§8's Approval precondition (Draft/In Review only).** Unchanged in
  `planTransition`; a repeated `TestApproval` unit test still asserts the
  pure planned outcome regardless of quorum, exactly as before this story.
- **Tenancy (ADR-TENANCY-001/002).** `approval_record` is in
  `TenantOwnedTables`, RLS-enforced, and its surrogate key
  (`approval_record_pkey`) is justified in both tenant-scope conformance
  suites the same way every other ULID primary key in this codebase is.

## What is NOT claimed

- **Self-approval prevention is not implemented.** There is no submitter/
  creator identity on `ConfigObject` to check the approver against. This is
  a genuine follow-up, not an oversight: it needs a new field (who
  submitted this object for review) before it can exist, which is a
  decision for the object model, not a quorum-story addition.
- **`approver_user_id` is not tied to a real identity registry.** It is
  exactly as trustworthy as `Actor` already was everywhere else in this
  engine — a platform-verified opaque string, not a claim checked against
  `app_user`. Binding governance identity to the identity/tenancy model
  (ADR-IDENTITY-002/003) is a larger, separate decision.
- **No delegation, weighting, role-based approval, veto/rejection, or
  time-boxing.** This is tenant-level distinct-approver *count* on the
  Approval step alone, as scoped.
- **Retry semantics are unchanged, including their existing rough edge.**
  A network retry of the *same* approver's *same* approve call (same
  Idempotency-Key) after the first attempt already committed hits
  `ErrAlreadyApproved` rather than a replayed 200 — the identical shape a
  retried Ratify already has today (a second `TransitionError` on the second
  call, because the object's state already moved). This story does not
  change that posture; it inherits it.
- **A quorum above 1 requires distinct *authenticated subjects*, and is
  unsatisfiable where they do not exist.** Distinctness is keyed on the
  approver's verified `sub`. With auth disabled, or in any deployment where a
  single operator's token always carries the same subject (e.g. the dev-mode
  `"system"` actor), the second approve hits `uq_approval_tenant_governance_approver`
  and returns `ErrAlreadyApproved` — so an object under
  `governance_required_approvals > 1` becomes **permanently un-approvable**.
  This is fail-safe (it never *under*-counts), but an operator who raises the
  threshold in such an environment will strand governance objects with no
  transition and no obvious cause. Raise the threshold only where distinct
  authenticated approvers actually sign in.
- **A below-quorum approve still advances `row_version`/`ETag`.** The fail-safe
  override records the approval through the same single atomic mutation, so the
  object's version changes even though its `Lifecycle` does not. An `If-Match`
  caller must re-read after each vote. This is deliberate — an approval *was*
  recorded — but it means a version bump is not evidence of a lifecycle change.

## Consequences

**What is now guaranteed.** An object's Approval quorum threshold is a
tenant-visible, tenant-configurable setting; the count of distinct approvers
recorded for an object can never be inflated by the same approver voting
twice (a database constraint, not an application check); and an object can
never read `Approved` while that count is below the threshold, because the
value written to `Lifecycle` is decided *after* the count is known, inside
the same locked transaction.

**Backward compatible by construction, not by testing alone.**
`governance_required_approvals` defaults to 1, and `Command.RequiredApprovals`
defaults to 1 when unset — every existing caller of the engine (every prior
test, every prior integration) that does not know this setting exists gets
exactly the single-actor approve behaviour it always had.

## Evidence

- `go test ./... -race -cover`: full suite green (paste in the story's
  report).
- `go test ./... -tags=integration -race -cover`: full suite green against
  real PostgreSQL, including the three quorum-specific integration tests
  below.
- The quorum gate bites live, not just in a mock: with
  `governance_required_approvals=2` set through the real `/admin/settings`
  route, one approver leaves a real row at `draft`; a second, distinct,
  bearer-token-authenticated approver moves it to `approved`
  (`httpapi.TestApprovalQuorum_TwoDistinctApproversRequired`). The same
  approver twice never meets that quorum
  (`httpapi.TestApprovalQuorum_SameApproverTwiceNeverMeetsQuorum`). The
  default setting reproduces single-actor approve end to end
  (`httpapi.TestApprovalQuorum_DefaultSettingReproducesSingleActorApprove`).
- RLS isolation on `approval_record` is proven the same way every other
  tenant-owned table's isolation is proven: two real tenants, one real
  governance object, a tenant-scoped connection that sees only its own
  approver (`postgres.TestApprovalStore_RLSIsolatesApprovalsByTenant`).

## Enforcement

- `governance.TestApproval_RecorderUnavailable_Refuses` — fail-closed when
  unwired.
- `governance.TestApproval_BelowQuorum_RecordsWithoutTransitioning` — the
  gate itself: one of two required approvals leaves Lifecycle unchanged.
- `governance.TestApproval_QuorumMet_Transitions` — the second, distinct,
  approver completes the quorum.
- `governance.TestApproval_DuplicateApprover_Rejected` — distinctness, at
  the engine.
- `governance.TestApproval_DefaultRequiredApprovals_SingleActorApproves` —
  backward compatibility, at the engine.
- `postgres.TestApprovalStore_DuplicateApproverRejectedByConstraint` —
  distinctness, at the database.
- `postgres.TestApprovalStore_RLSIsolatesApprovalsByTenant` — tenancy.
- `httpapi.TestGovernance_Approve*` — transport wiring (tenant resolution,
  threshold resolution, response shape, 409 mapping).
- `httpapi.TestApprovalQuorum_*` (integration) — the end-to-end guarantee,
  against a real service and a real database.

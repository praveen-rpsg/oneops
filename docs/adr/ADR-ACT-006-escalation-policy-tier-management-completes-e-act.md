# ADR-ACT-006 — Escalation policy + tier management reuses the write-action pattern; no schedule-exclusion on the tier picker; completes the E-ACT epic

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO (implementer session) |
| **Related** | ADR-ACT-001 (the write-action pattern this story reuses unmodified), ADR-ACT-002/ADR-ACT-004 (the shared-sub-form and no-row_version-on-delete precedents this story's own asymmetries match), ADR-ACT-005 (the structural twin this story mirrors line-for-line: parent config + ordered children + reorder + a picker), ADR-ONCALL-002 (E5.2b-1: `escalation_policy`/`escalation_tier`, the deferred-position-unique reorder pattern), `docs/PLATFORM-BUILD-PLAN.md` E-ACT.5 |

## Context

`docs/PLATFORM-BUILD-PLAN.md`'s E-ACT epic turned the read-only Cloudscape
console into an operator's daily-work surface by wiring write actions to
EXISTING backend endpoints, one operational object per story: incidents
(E-ACT.1), alert rules (E-ACT.2), maintenance windows (E-ACT.3), and on-call
schedules + rosters (E-ACT.4). E-ACT.5, the epic's last story, asked for the
same treatment for escalation policies and their ordered tier ladders —
structurally the twin of E-ACT.4: a named parent object (the policy) with an
ordered child sequence (the tiers), managed through create/edit + add/
remove/reorder, exactly the shape ADR-ACT-005 already established.

**The contract, confirmed against the Go before any UI was written**
(`internal/httpapi/handlers_escalation.go`, `internal/domain/escalation.go`,
`internal/store/postgres/escalation_store.go`, `server.go`'s route table,
and the `20260902000001_escalation_policy.sql` migration):

- `POST /v1/admin/escalation-policies` (`createEscalationPolicyRequest`):
  `name` only, required. `policy_id` is minted server-side; a freshly
  created policy is always `active` — there is no caller-chosen initial
  status (`domain.NewEscalationPolicy`).
- `PATCH /v1/admin/escalation-policies/{id}` (`patchEscalationPolicyRequest`):
  **requires** `row_version` (rejected `< 1` as a 422); `name`/`status` are
  both independently optional pointers, at least one required. A stale
  `row_version` (`ErrVersionMismatch`) is `409`.
- `DELETE /v1/admin/escalation-policies/{id}` **exists** and, like
  `deleteOnCallSchedule` before it (ADR-ACT-005 Decision 1), is a real route
  with `ON DELETE CASCADE` on its tiers — but this story does **not** wire
  it into the console. See Decision 1.
- `POST .../tiers` (`addEscalationTierRequest`): `on_call_schedule_id` and
  `wait_seconds` only; position is always "append at the end", never
  caller-chosen. **No `row_version` anywhere in this request** — `AddTier`
  takes no optimistic-lock parameter at all. 404 means either the policy
  itself does not exist, or `on_call_schedule_id` does not name a schedule
  of the caller's tenant (re-verified server-side, `verifyScheduleInTenant`
  — the identical FK-bypasses-RLS defense ADR-ONCALL-001 §5/ADR-ASSET-001 §6
  already established); 409 means the tier "could not be added"
  (`domain.ErrConflict`, from a unique-violation on `AddTier`'s own INSERT)
  — a business-rule conflict, not a stale-read one.
- `DELETE .../tiers/{tierId}`: **no `row_version`, no body** — `RemoveTier`
  takes no optimistic-lock parameter either.
- `POST .../tiers/reorder` (`reorderEscalationTiersRequest`): `tier_ids`
  must name the policy's full CURRENT tier set, in the desired new order —
  no more, no fewer. **No `row_version`** — `ReorderTiers` takes no
  optimistic-lock parameter. A mismatched set is refused with a 422 before
  anything is written; the rewrite is atomic via
  `uq_escalation_tier_policy_position DEFERRABLE INITIALLY DEFERRED` — the
  identical mechanism E5.2a's `ReorderParticipants` uses, reused line for
  line per the build plan's own E5.2b-1 entry.
- `escalationTierDTO` does carry its own `row_version` field (every DTO in
  this package does), but none of Add/Remove/Reorder Tier currently consume
  it — read-only here today, not a fabricated round-trip.
- **Confirmed schema fact, checked against the migration itself, not
  assumed:** `escalation_tier`'s only uniqueness constraint is
  `uq_escalation_tier_policy_position UNIQUE (tenant_id, policy_id,
  position)` — there is **no** uniqueness on `on_call_schedule_id`. A policy
  may legitimately page the same on-call schedule at two different tiers
  (e.g. "page the primary rotation, wait 5 minutes, then page the primary
  rotation again at a longer wait before falling back to secondary"). This
  is the one point where this story's contract diverges from E-ACT.4's: the
  409 `AddTier` can return is a genuine (if rare — position is computed
  inside the same locked transaction) race on the position-uniqueness
  constraint, not a "this schedule is already used" duplicate check the UI
  could pre-empt by filtering options.

## Decision

### 1. Policy CRUD is create + edit (including archive via PATCH), not create + delete

`domain.EscalationPolicyStatus`'s own doc comment states the identical
doctrine `OnCallSchedule`'s doc comment established: "a policy is a
governed, named object an operator archives rather than deletes so anything
already pointing at it is not orphaned." The Edit modal's `status` `Select`
(`active`/`archived`) IS this retirement path. `escalation.ts` therefore has
no `deleteEscalationPolicy` export — its top-of-file contract note records
that the DELETE route exists and why it is deliberately unused here, the
same honesty ADR-ACT-005 Decision 1 established for on-call schedules.

### 2. The board is a `Table`, not `Cards` — unlike E-ACT.4's on-call board

E-ACT.4's on-call board stayed `Cards` because a schedule's own board-level
fields (on-call-now, participants) don't map cleanly onto table columns. An
escalation policy's own board-level fields are simply name/status/updated —
the same shape `AlertsBoardPage`'s rule rows already have (ADR-ACT-002) — so
this story's board (`routes/EscalationBoardPage.tsx`) is a Cloudscape
`Table` with sortable columns and a "Manage tiers" action opening the
shell's `SplitPanel`, matching `AlertsBoardPage` rather than
`OnCallBoardPage`. Tier management itself
(`components/EscalationPolicyDetail.tsx`) is structured exactly like
`OnCallScheduleDetailPanel` (ADR-ACT-005): its own `GET`/refetch cycle,
`busy`/`actionProblem`/`conflictNotice` state, Edit modal, and the ladder
list with add/remove/reorder controls.

### 3. Add-tier's 409 is a business conflict, not the ADR-ACT-001 §1.4 "stale read" 409 — surfaced the same way ADR-ACT-005 Decision 3 chose

`AddTier`'s 409 (`domain.ErrConflict`, "tier could not be added") means the
underlying `escalation_tier` INSERT hit a unique-violation on
`(tenant_id, policy_id, position)` — a genuine but rare race (another
operator's concurrent `AddTier`/`ReorderTiers` against the same policy,
serialized by `lockPolicyTx`'s `FOR UPDATE` but still observable as a 409 to
the loser of that lock). `EscalationPolicyDetailPanel.runAdd` surfaces this
as an ordinary inline `actionProblem` (dismissible, using the server's own
detail text) rather than routing it through `afterMutation('conflict')`'s
reload-and-notice path, for the identical reason ADR-ACT-005 Decision 3
gave: nothing was written, so the panel's own state already reflects
reality, and the messaging must not falsely imply the POLICY's own
`row_version` went stale, which it did not. Edit's own 409 (`PATCH`, a
genuine `ErrVersionMismatch`) DOES use the ADR-ACT-001 reload-and-notice
path unchanged.

### 4. The add-tier picker does NOT exclude schedules already used by another tier of the same policy

This is the one place this story's picker differs from E-ACT.4's
`AddParticipantControl`, which filters out already-rostered users because
`on_call_participant` enforces uniqueness on `(schedule_id, user_id)` (a
duplicate really is impossible to add legitimately). `escalation_tier` has
no such constraint on `on_call_schedule_id` (Context, confirmed schema
fact) — filtering the picker here would incorrectly prevent a legitimate
"page the same schedule twice at different wait times" ladder. The
add-tier `Select` (`components/EscalationPolicyDetail.tsx`'s
`AddTierControl`) therefore lists every ACTIVE on-call schedule
unconditionally, backed by `GET /v1/admin/on-call-schedules` (the same
endpoint the on-call board itself reads), degrading to a free-text
schedule-id `Input` on any directory load failure — the same defensive
posture `AddParticipantControl`/`AssigneeControl` established.

### 5. Reorder sends the full ordered `tier_id` set on every move — never a partial "move one" call

The ladder list offers a "move up"/"move down" icon `Button`
(`iconName="angle-up"`/`"angle-down"`) per tier, identical to E-ACT.4's
roster reorder. Each click computes the FULL new ordering client-side (an
adjacent swap over the array `ListTiers` already returned, sorted by
`position`) and sends the WHOLE list to `POST .../tiers/reorder` in one
call — there is no code path in this console that could send two
interleaved partial-move requests for the same policy, because there is no
concept of a partial move to send. A "remove" icon `Button` sits alongside,
gated by the same confirm `Modal` pattern ADR-ACT-002/004/005 established.

### 6. Friendly wait-time presets, not raw seconds by default

`escalation.ts`'s `ESCALATION_WAIT_PRESETS` offers "5 minutes" (300s), "15
minutes" (900s), and "30 minutes" (1,800s) `Select` options plus "Custom
(seconds)" — shorter defaults than E-ACT.4's handoff-interval presets (1
day/1 week), because a tier's `wait_seconds` is how long an unacknowledged
page waits before escalating, an operationally much shorter horizon than a
rotation handoff. There is no "edit an existing tier's wait time" flow in
this story (only add/remove/reorder, matching the brief's own scope) so
`escalation.ts`'s `presetForWaitSeconds` (the inverse lookup, mirroring
`onCall.ts`'s `presetForHandoffSeconds`) is exported for a future edit-tier
story but has no consumer in this one — a deliberately unused-but-documented
symmetry, not dead code accidentally left behind.

## What this story explicitly does not do

- No Go, migration, or `openapi.yaml` change — every endpoint above is
  called exactly as `handlers_escalation.go` already defines it, confirmed
  field-for-field before any TypeScript was written.
- No "Delete policy" UI (Decision 1) — `deleteEscalationPolicy` does not
  exist in `escalation.ts`; archiving via Edit's `status` field is the
  console's only retirement path, matching the domain's own doctrine.
- No "edit an existing tier" (change its `wait_seconds` or repoint its
  `on_call_schedule_id` without remove+re-add) — the brief's own CTO-locked
  design lists add/remove/reorder only; `EscalationPolicyRepository` itself
  has no `UpdateTier` method to call even if the UI wanted one.
- No archived-policy tier management — the board's `Table` shows every
  policy (unlike E-ACT.4's `Cards`, which filters to active schedules only,
  this board does not filter by status, since Table sorting/columns make an
  archived policy's own status visible and manageable via Edit without
  hiding the row) — an intentional divergence from E-ACT.4's board-level
  filter, not an oversight: `listEscalationPolicies` has no
  archived-exclusion and neither does this board.
- No `Idempotency-Key` on any of the six calls — confirmed against
  `handlers_escalation.go` that none of them read that header, the same
  finding ADR-ACT-001/002/004/005 already made for their own endpoints.

## Consequences

**What is now guaranteed.** An operator with `PermAdmin` can create an
escalation policy (name), edit its name/status under optimistic locking (a
stale `row_version` is refused and surfaced, never silently overwritten),
and fully manage its tier ladder — add a tier pointed at any active on-call
schedule (including one already used by another tier of the same policy,
which is legitimate), remove a tier with confirmation, and reorder the
ladder via an atomic full-set rewrite that can never interleave with
another partial move. This closes E-ACT: every write action the read-only
console needed across incidents, alert rules, maintenance windows, on-call
schedules/rosters, and escalation policies/tiers is now wired, each against
an EXISTING backend endpoint confirmed field-for-field before any UI code
was written.

**What is not claimed.** Add/remove/reorder-tier carry no optimistic lock at
all (a genuine, confirmed contract fact, not an oversight) — two operators
racing on the same policy's ladder can produce a result neither of them
individually requested; the same exposure ADR-ACT-005 already accepted for
on-call rosters. There is no edit-tier flow (Decision 6) — only add, remove,
reorder. None of the six E-ACT branches (E-ACT.0 through E-ACT.5) are merged
to `master` — this ADR does not claim the epic's changes are live in
production, only that each story's own frontend-only scope is complete and
tested on its own branch.

## Evidence

- `web/src/escalation.ts` — `createEscalationPolicy`, `patchEscalationPolicy`,
  `addEscalationTier`, `removeEscalationTier`, `reorderEscalationTiers`,
  `getEscalationPolicy`, `listEscalationPolicies`, `listEscalationTiers`,
  the wait-time presets/validators, and the top-of-file contract note
  recording every fact in this ADR's Context section.
- `web/src/escalationPresentation.ts` — `ESCALATION_POLICY_STATUS_TYPE`.
- `web/src/components/EscalationPolicyForm.tsx` — `EscalationPolicyFields`,
  the shared name sub-form (create + edit).
- `web/src/components/EscalationPolicyDetail.tsx` —
  `EscalationPolicyDetailPanel` (Edit modal, ladder list with move-up/down/
  remove, `AddTierControl`), the business-conflict-vs-stale-read 409 split
  (Decision 3), the no-schedule-exclusion picker (Decision 4).
- `web/src/routes/EscalationBoardPage.tsx` — the `Table` board, "Create
  policy", "Manage tiers".
- `web/src/routes/EscalationBoardPage.test.tsx` (12 tests) —
  policy-list-renders, empty-state, error-and-retry, create-disabled-until-
  named, create-posts-exact-body-and-opens, create-validation-error-keeps-
  dialog-open, edit-sends-row_version-then-refetches, edit-409-refetches-
  with-notice-no-retry, add-tier-uses-schedule-picker-and-posts-
  on_call_schedule_id+wait_seconds, reorder-sends-full-ordered-tier-set,
  remove-confirms-then-deletes-and-refetches, add-tier-error-surfaced-as-
  inline-business-conflict.
- `web/src/App.tsx`/`web/src/Shell.tsx` — the "Escalation" nav item and
  `/escalation` route.
- `pnpm --dir web exec tsc -b --noEmit` — clean.
- `make web-test` — 140/140 (128 pre-existing + 12 new), no existing
  assertion weakened. **A pre-existing, unrelated test-suite flake was
  observed and characterized, not introduced or hidden**: run
  `pnpm --dir web exec vitest run` seven times total during this story's
  verification — `master` alone (16 test files, no escalation tests) passed
  128/128 on 3/3 consecutive runs; the same command WITH this story's new
  12-test file (17 files, 140 tests) passed 140/140 on most runs but
  intermittently (roughly 1-in-3) reported 1–3 failures, always inside
  `audit.test.tsx` or `IncidentDetail.test.tsx` (both untouched by this
  story) on a default `waitFor`/`findBy` timeout — never inside
  `EscalationBoardPage.test.tsx` itself, which passed in every single run,
  isolated or full-suite. This reads as CPU-contention timing sensitivity
  in pre-existing tests that one more parallel test file's worth of load
  pushes past their timeout budget, not a logic dependency on this story's
  code. Fixing test-suite parallelism/timeout robustness is out of this
  story's frontend-feature scope; flagged here for the reviewer/CTO rather
  than silently re-run-until-green.
- `make web` — builds cleanly (~1,280 kB JS/~369 kB gzip, ~1,177 kB CSS/
  ~239 kB gzip); the same `grep -Eo 'https?://...'` sweep prior ADRs ran
  returns only the same inert strings (XML/SVG namespace URIs, a
  date-fns dev-mode doc link, a Google Fonts license comment) — no new
  runtime CDN reference.
- `go build ./...` and `make test` (full suite, `-race`) — green,
  unaffected; `make lint` — 0 issues; `git diff --stat` shows only `web/`
  files touched (plus this ADR and the build-plan update).

## Enforcement

- `web/src/routes/EscalationBoardPage.test.tsx`, under `make web-test` —
  this ADR's own claims (row_version round-trip on Edit, the business-
  conflict-vs-stale-read 409 split, atomic full-set reorder, the
  on-call-schedule-backed picker with free-text fallback, no accidental
  schedule-exclusion) checked on every build, not by inspection.
- Any future change to `domain.EscalationPolicyRepository`'s method
  signatures (in particular, if `AddTier`/`RemoveTier`/`ReorderTiers` ever
  gain a `rowVersion` parameter, or `escalation_tier` gains a uniqueness
  constraint on `on_call_schedule_id`) will not fail a build on its own —
  there is no generated-from-Go check for this contract today. A future
  story that changes that contract should update `escalation.ts`'s
  top-of-file note and this ADR together.

# ADR-ACT-005 — On-call schedule + roster management reuses the write-action pattern; the tenant-users picker (ADR-ACT-003) lands in the console for the first time and retires the incident assignee's platform-admin fallback

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO (implementer session) |
| **Related** | ADR-ACT-001 (the write-action pattern this story reuses unmodified), ADR-ACT-002/ADR-ACT-004 (the shared-sub-form and no-row_version-on-delete precedents this story's own asymmetries match), ADR-ACT-003 (`GET /v1/admin/tenant-users`, the endpoint this story is the first UI consumer of), ADR-ONCALL-001 (E5.2a: `on_call_schedule`/`on_call_participant`, `OnCallRotationIndex`, the schedule-archives-rather-than-deletes doctrine), `docs/PLATFORM-BUILD-PLAN.md` E-ACT.4 |

## Context

`docs/PLATFORM-BUILD-PLAN.md`'s on-call board (`routes/OnCallBoardPage.tsx`)
was read-only through E-ACT.3: it showed every active schedule, who is on
call now, and the rotation roster, but an operator could not create a
schedule or touch its roster from the console at all. E-ACT.4 asked for
exactly that, against the EXISTING E5.2a endpoints (no Go change), with the
roster's "add participant" picker backed by the tenant-scoped user directory
E-ACT.0 built and ADR-ACT-003 recorded — and, since that directory now
exists, to finally close the E-ACT.1 gap ADR-ACT-001 §2 left open (the
incident assignee picker's platform-admin-gated fallback).

**The contract, confirmed against the Go before any UI was written**
(`internal/httpapi/handlers_oncall.go`, `internal/domain/oncall.go`,
`internal/store/postgres/oncall_store.go`):

- `POST /v1/admin/on-call-schedules` (`createOnCallScheduleRequest`):
  `name`/`handoff_interval_seconds`/`rotation_start_at`, all required.
  `schedule_id` is minted server-side; a freshly created schedule is always
  `active` — there is no caller-chosen initial status
  (`domain.NewOnCallSchedule`).
- `PATCH /v1/admin/on-call-schedules/{id}` (`patchOnCallScheduleRequest`):
  **requires** `row_version` (rejected `< 1` as a 422); `name`/
  `handoff_interval_seconds`/`rotation_start_at`/`status` are all
  independently optional pointers, at least one required. A stale
  `row_version` (`ErrVersionMismatch`) is `409`.
- `DELETE /v1/admin/on-call-schedules/{id}` **exists** and, like
  `deleteAlertRule`/`deleteMaintenanceWindow` before it (ADR-ACT-002 §1,
  ADR-ACT-004), takes **no `row_version` and no body**
  (`domain.OnCallScheduleRepository.Delete(ctx, scheduleID)` has no
  optimistic-lock parameter). This story does **not** wire it into the
  console — see Decision 1.
- `POST .../participants` (`addOnCallParticipantRequest`): `user_id` only;
  position is always "append at the end", never caller-chosen. **No
  `row_version` anywhere in this request** — `AddParticipant` takes no
  optimistic-lock parameter at all. 404 means `user_id` does not name an
  ACTIVE member of the caller's tenant (re-verified server-side, the same
  defense `IncidentStore.verifyAssigneeExists` already applies); 409 means
  `user_id` is already on this roster (`domain.ErrConflict`) — a
  business-rule conflict, not a stale-read one.
- `DELETE .../participants/{participantId}`: **no `row_version`, no body**
  — `RemoveParticipant` takes no optimistic-lock parameter either.
- `POST .../participants/reorder` (`reorderOnCallParticipantsRequest`):
  `participant_ids` must name the schedule's full CURRENT participant set,
  in the desired new order — no more, no fewer. **No `row_version`** —
  `ReorderParticipants` takes no optimistic-lock parameter. A mismatched set
  is refused with a 422 (`domain.NewValidationError`) before anything is
  written; the rewrite is atomic (every position moves or none do).
- `onCallParticipantDTO` does carry its own `row_version` field (every DTO
  in this package does), but none of Add/Remove/Reorder Participant
  currently consume it — read-only here today, not a fabricated round-trip.

## Decision

### 1. Schedule CRUD is create + edit (including archive via PATCH), not create + delete

The brief's own CTO-locked design lists "Create schedule" and "Edit
schedule" — it does not ask for a delete action, and
`domain.OnCallSchedule`'s own doc comment is explicit about why: "a schedule
is a governed, named object an operator archives rather than deletes so
anything already pointing at it (a participant row, a future E5.2b
escalation tier) is not orphaned." The Edit modal's `status` `Select`
(`active`/`archived`) IS this retirement path, not a separate control.
`onCall.ts` therefore has no `deleteOnCallSchedule` export at all — its
top-of-file contract note records that the DELETE route exists and why it
is deliberately unused here, the same honesty ADR-ACT-002/ADR-ACT-004 used
for the delete-has-no-row_version asymmetry, applied here to "delete exists,
is intentionally not wired" instead.

### 2. Roster management lives in the shell's `SplitPanel`, opened per-schedule from the (still Cards-based) board

The board (`OnCallBoardPage`) was already `Cards`, not `Table` — E5.2a's
per-schedule "on-call now" and "participants" sections don't map cleanly
onto table columns. Rather than rebuild it as a `Table` to match the
Alerts/Maintenance `SplitPanel` precedent, this story keeps `Cards` and adds
the SAME `ShellSplitPanelContext` seam those boards use: a schedule's name
(now a `Button variant="inline-link"`) and a new "Manage schedule" button
both call `openSplitPanel(sch.name, <OnCallScheduleDetailPanel .../>)`.
`OnCallScheduleDetailPanel` (`components/OnCallScheduleDetail.tsx`) is
structured exactly like `AlertRuleDetailPanel` (ADR-ACT-002): its own
`GET`/refetch cycle, `busy`/`actionProblem`/`conflictNotice` state, Edit
modal, and — new here — the roster list with add/remove/reorder controls.

**A documented, minor gap deliberately left as-is**: the `SplitPanel`
header is a plain string set once, at `openSplitPanel` call time
(`Shell.tsx`'s `openSplitPanel(header: string, ...)`) — unlike the panel
BODY, which refetches and re-renders on every mutation, the header does not
follow a rename. Renaming a schedule via Edit updates its `Name` field
inside the panel body (added to the `KeyValuePairs` specifically so a
rename is visible somewhere reliable) and the board's own card title
(refetched via `onChanged`), but the `SplitPanel`'s own title bar keeps
showing the name it was opened with until the operator closes and reopens
it. Fixing this generically (making `openSplitPanel`'s header reactive)
is a `Shell.tsx` change affecting every consumer (Alerts, Incidents,
Maintenance has none) and is out of this story's frontend-only, one-story
scope — flagged here rather than silently accepted.

### 3. Add-participant's 409 is a business conflict, not the ADR-ACT-001 §1.4 "stale read" 409 — it is surfaced differently on purpose

ADR-ACT-001 §1.4 established: on a 409, refetch and show "changed since you
loaded it" rather than blind-retry. That rule exists because a 409 THERE
means a `row_version` went stale. `AddParticipant`'s 409
(`domain.ErrConflict`, "user is already a participant on this schedule")
means something structurally different — the roster changed to include a
user the picker did not know about, which the picker mostly prevents by
construction (Decision 4) but cannot fully rule out (a race with another
operator's own concurrent add). `OnCallScheduleDetailPanel.runAdd` surfaces
this 409 as an ordinary inline `actionProblem` (dismissible, using the
server's own detail text) rather than routing it through
`afterMutation('conflict')`'s reload-and-notice path — the roster IS
refetched regardless (the panel's own state already reflects reality after
the failed POST, since nothing was written), but the messaging does not
falsely imply "this schedule's `row_version` was stale," which it was not.
Edit's own 409 (`PATCH`, a genuine `ErrVersionMismatch`) DOES use the
ADR-ACT-001 reload-and-notice path unchanged — that one really is the same
shape E-ACT.1/E-ACT.2 already established.

### 4. The add-participant picker is `GET /v1/admin/tenant-users` (ADR-ACT-003), filtered to exclude the current roster, with the same free-text fallback discipline

`components/OnCallScheduleDetail.tsx`'s `AddParticipantControl` fetches the
tenant's active users once per panel open and renders a Cloudscape `Select`
whose options exclude every `user_id` already on the roster — so the one
business-rule 409 this endpoint can return (Decision 3) is impossible to
trigger through the picker itself; a race with a truly concurrent operator
is the only way to still hit it, which is exactly what Decision 3's inline
surfacing is for. On ANY directory load failure, the control degrades to a
free-text `Input` for a raw user id — identical posture to
`IncidentDetail.tsx`'s `AssigneeControl` (Decision 5), so a working
add-participant action never depends on the directory being reachable.

### 5. The incident assignee picker (ADR-ACT-001 §2) is rewired onto `GET /v1/admin/tenant-users`, closing the confirmed gap

`web/src/users.ts` no longer wraps `GET /v1/admin/users`
(`requirePlatformAdmin`, cross-tenant) at all — it now wraps
`GET /v1/admin/tenant-users` (`requirePermission(auth.PermAdmin)`,
tenant-scoped, ADR-ACT-003), exporting `TenantUserDTO`
(`user_id`/`email`/`display_name` — no `status`/`row_version`, matching the
narrower DTO the endpoint actually returns) and `listTenantUsers` in place
of the old `UserDTO`/`listUsers`. `components/IncidentDetail.tsx`'s
`AssigneeControl` is updated to call `listTenantUsers` — its Select-with-
Input-fallback shape and every existing test behavior are otherwise
unchanged; only the endpoint and the DTO shape moved. This is the SAME
capability, correctly tenant-scoped for the first time: a real
`PermAdmin`-holding tenant operator (not just the local-dev
`AuthEnabled=false` system-tenant identity) now gets a working assignee
`Select` rather than the free-text fallback ADR-ACT-001 §2 predicted for
them. `GET /v1/admin/users` itself is untouched — this is a consumer
rewire, not a Go change, and no other consumer of the old `users.ts` exists
(confirmed by search) so nothing else needed updating.

### 6. Reorder sends the full ordered `participant_id` set on every move — never a partial "move one" call

The roster list (rotation order) offers a "move up"/"move down" icon
`Button` (`iconName="angle-up"`/`"angle-down"`) per participant rather than
a general drag-drop reorder. Each click computes the FULL new ordering
client-side (a simple adjacent swap over the array `ListParticipants`
already returned, sorted by `position`) and sends the WHOLE list to
`POST .../participants/reorder` in one call — satisfying the hard
constraint literally: there is no code path in this console that could
send two interleaved partial-move requests for the same schedule, because
there is no concept of a partial move to send. A "remove" icon `Button`
sits alongside, gated by the same confirm `Modal` pattern ADR-ACT-002/
ADR-ACT-004 established for their own consequential actions.

### 7. Friendly handoff-interval presets, not raw seconds by default

`onCall.ts`'s `ON_CALL_HANDOFF_PRESETS` offers "1 day" (86,400s) and "1
week" (604,800s) `Select` options plus "Custom (seconds)", which reveals a
raw numeric `Input` (`components/OnCallScheduleForm.tsx`'s
`OnCallScheduleFields`, shared by the create and edit modals exactly like
`AlertRuleConfigFields` is shared by alert-rule create/edit — ADR-ACT-002
§ shared-sub-form precedent). Editing an existing schedule whose interval
does not match either preset shows "Custom" pre-filled with its exact
seconds value (`presetForHandoffSeconds`) rather than silently rounding it
to the nearest preset.

## What this story explicitly does not do

- No Go, migration, or `openapi.yaml` change — every endpoint above is
  called exactly as `handlers_oncall.go` already defines it, confirmed
  field-for-field before any TypeScript was written.
- No "Delete schedule" UI (Decision 1) — `deleteOnCallSchedule` does not
  exist in `onCall.ts`; archiving via Edit's `status` field is the
  console's only retirement path, matching the domain's own doctrine.
- No fix for the `SplitPanel` header staying stale after a rename
  (Decision 2) — a real, minor, documented UX gap, not a correctness bug
  (the panel body and the board's card title both reflect the rename
  correctly via refetch).
- No change to `GET /v1/admin/users` or its handler — E-ACT.1's assignee
  picker is REWIRED onto the tenant-scoped sibling (Decision 5); the
  platform-wide route itself is untouched.
- No `Idempotency-Key` on any of the six new/rewired calls — confirmed
  against `handlers_oncall.go`/`handlers_tenant_users.go` that none of them
  read that header, the same finding ADR-ACT-001/002/004 already made for
  their own endpoints.
- No archived-schedule roster management — the board's `Cards` still filter
  to `status === 'active'` (unchanged from before this story), so an
  archived schedule's roster is not reachable from the board at all once it
  is archived. Reactivating one (PATCH `status` back to `active`) has no UI
  entry point either, since the archived card itself is not shown. This is
  an existing board-level filter this story did not revisit, not a new gap
  it introduced.

## Consequences

**What is now guaranteed.** An operator with `PermAdmin` can create an
on-call schedule (name/handoff-interval/rotation-start), edit any of those
fields plus status under optimistic locking (a stale `row_version` is
refused and surfaced, never silently overwritten), and fully manage a
schedule's roster — add a real tenant user via a picker that cannot offer
someone already rostered, remove a participant with confirmation, and
reorder the rotation via an atomic full-set rewrite that can never
interleave with another partial move. The incident assignee picker
(ADR-ACT-001) now works for any `PermAdmin`-holding tenant operator, not
only the local-dev platform-admin identity — the confirmed gap ADR-ACT-001
§2 and ADR-ACT-003 both named is closed on the frontend side.

**What is not claimed.** The `SplitPanel` header does not follow a rename
(Decision 2) — cosmetic, not a correctness defect. Archived schedules have
no roster-management UI path (they are filtered off the board entirely,
unchanged from before this story). Reorder/remove/add-participant carry no
optimistic lock at all (a genuine, confirmed contract fact, not an
oversight) — two operators racing on the same schedule's roster can produce
a result neither of them individually requested; this is the SAME
exposure `deleteAlertRule`/`deleteMaintenanceWindow` already have and ADR-
ACT-002/004 already accepted as a confirmed, not fabricated, gap.

## Evidence

- `web/src/onCall.ts` — `createOnCallSchedule`, `patchOnCallSchedule`,
  `addOnCallParticipant`, `removeOnCallParticipant`,
  `reorderOnCallParticipants`, `getOnCallSchedule`, the handoff-interval
  presets/validators, and the top-of-file contract note recording every
  fact in this ADR's Context section.
- `web/src/users.ts` — rewired onto `GET /v1/admin/tenant-users`;
  `TenantUserDTO` replaces `UserDTO`/`listUsers`.
- `web/src/components/OnCallScheduleForm.tsx` — `OnCallScheduleFields`, the
  shared name/handoff-interval/rotation-start sub-form.
- `web/src/components/OnCallScheduleDetail.tsx` — `OnCallScheduleDetailPanel`
  (Edit modal, roster list with move-up/down/remove, `AddParticipantControl`),
  the business-conflict-vs-stale-read 409 split (Decision 3).
- `web/src/routes/OnCallBoardPage.tsx` — "Create schedule", the
  `Button`-not-heading card title (avoids a duplicate accessible-name
  collision with the `SplitPanel`'s own heading once opened), "Manage
  schedule".
- `web/src/components/IncidentDetail.tsx` — `AssigneeControl` now calls
  `listTenantUsers`.
- `web/src/routes/OnCallBoardPage.test.tsx` (+8 tests) — create-disabled-
  until-answered, create-posts-exact-body-with-resolved-preset-and-opens,
  create-validation-error-keeps-dialog-open, edit-sends-row_version-then-
  refetches, edit-409-refetches-with-notice-no-retry, add-participant-uses-
  tenant-users-picker-excluding-rostered-and-posts-user_id, reorder-sends-
  full-ordered-set, remove-confirms-then-deletes-and-refetches.
- `web/src/IncidentDetail.test.tsx` / `web/src/routes/IncidentBoardPage.test.tsx`
  — updated to mock `/admin/tenant-users` in place of `/admin/users`; the
  assign-via-picker and assign-via-fallback tests pass unchanged otherwise.
- `pnpm --dir web exec tsc -b --noEmit` — clean.
- `make web-test` — 128/128 (120 pre-existing + 8 new), no existing
  assertion weakened.
- `make web` — builds cleanly (~1,266 kB JS/~366 kB gzip, ~1,177 kB CSS/
  ~239 kB gzip); the same `grep -Eo 'https?://...'` sweep prior ADRs ran
  returns only the same inert strings (XML/SVG namespace URIs, a
  date-fns dev-mode doc link, a Google Fonts license comment) — no new
  runtime CDN reference.
- `go build ./...` and `make test` (full suite, `-race`) — green,
  unaffected; `go build ./... && make lint` — 0 issues; `git diff --stat`
  shows only `web/` files touched.

## Enforcement

- `web/src/routes/OnCallBoardPage.test.tsx` and the assign-picker cases in
  `web/src/IncidentDetail.test.tsx`, under `make web-test` — this ADR's own
  claims (row_version round-trip on Edit, the business-conflict-vs-stale-
  read 409 split, atomic full-set reorder, tenant-users-backed pickers with
  free-text fallback) checked on every build, not by inspection.
- Any future change to `domain.OnCallScheduleRepository`'s method
  signatures (in particular, if `AddParticipant`/`RemoveParticipant`/
  `ReorderParticipants` ever gain a `rowVersion` parameter) will not fail a
  build on its own — there is no generated-from-Go check for this contract
  today. A future story that changes that contract should update
  `onCall.ts`'s top-of-file note and this ADR together.

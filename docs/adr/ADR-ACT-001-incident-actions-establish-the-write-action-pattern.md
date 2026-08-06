# ADR-ACT-001 — Incident actions establish the console's write-action pattern: row_version round-trip, legal-transition gating, confirm-then-send, 409-triggers-refetch

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-UI-001 (Cloudscape console foundation), ADR-NOC-004 (the incident board/detail this story turns from read-only to operational), `docs/PLATFORM-BUILD-PLAN.md` E-ACT epic intro + E-ACT.1, `internal/httpapi/handlers_incidents.go` (the four endpoints this story wires unchanged), `internal/domain/incident.go` (`CanTransitionTo`, the state machine this story mirrors client-side) |

## Context

Every console screen through ADR-NOC-004 is read-only: the estate/governance
flow's Ratify is the one exception, and it already established a write-action
shape (`ConfirmOperation`, `executeGovernance`, `explainFailure`) this story
deliberately does not fork from without reason. `docs/PLATFORM-BUILD-PLAN.md`'s
E-ACT epic turns the rest of the console into one operators write through,
and names E-ACT.1 (incident actions) first because the incident board is
"where operators live" and because it is the FIRST story in the epic — it has
to set the pattern (optimistic-lock round-trip, 409 handling, confirmation,
refetch) every later E-ACT story inherits, not just ship its own four buttons.

**The contract, confirmed against the Go before any UI was written**
(`internal/httpapi/handlers_incidents.go`, `internal/domain/incident.go`):

- `POST /v1/admin/incidents` (`createIncidentRequest`): `title` (required),
  `description`, `severity` (required, one of critical/high/medium/low),
  `asset_id`, `assignee_user_id` (both optional, tenant-re-verified
  server-side). Always creates `IncidentOpen`; there is no caller-chosen
  initial status. Returns `201` + the full `incidentDTO`.
- `POST /v1/admin/incidents/{id}/transition` (`transitionIncidentRequest`):
  **requires** `row_version` (rejected < 1 as a 422) and `status`. Guarded by
  `domain.IncidentStatus.CanTransitionTo` — the state machine is exactly
  `open→acknowledged→investigating→resolved→{closed,reopened}`,
  `reopened→investigating`, `closed` terminal. **Both** a stale `row_version`
  (`ErrVersionMismatch`) **and** an illegal move (`ErrInvalidTransition`) map
  to `409 Conflict` for this endpoint specifically — not the 412 the
  governance surface uses for its own version mismatch.
- `POST /v1/admin/incidents/{id}/assign` (`assignIncidentRequest`):
  **requires** `row_version`; `assignee_user_id` is a plain nullable string
  (no tri-state "leave unchanged" — every call states the assignee, `null`
  clears it). Stale `row_version` is also `409` here (same handler pattern).
- `incidentDTO` carries `row_version` (confirmed at
  `handlers_incidents.go:35`) — the optimistic-lock token every write below
  reads once and sends back exactly once.
- **No add-note / timeline-append HTTP endpoint exists.**
  `IncidentEventKind`'s own doc comment is explicit: "General operator
  comment support... remains a documented member of the target vocabulary
  with no HTTP write path — E5.1's SCOPE(in) §5 still enumerates exactly
  create/get/list/patch/transition/assign/timeline." `IncidentEventAlertNote`
  is written exclusively by `internal/alerting`'s evaluator, never by a
  handler. **Deferred, not fabricated** — see "What this story explicitly
  does not do" below.
- The obvious candidate for a user-directory endpoint,
  `GET /v1/admin/users`, exists but is gated by `requirePlatformAdmin`
  (`internal/httpapi/server.go`), not the `PermAdmin` tenant-administration
  permission the four incident endpoints use. This is a genuine, load-bearing
  finding — see §2 below.

## Decision

### 1. The write-action pattern (binding on every future E-ACT story)

Every mutation in this story, and the ones after it, follows the same shape:

1. **Read once, send back once.** The `row_version` a `GET` last returned is
   the only value ever sent in a mutation's body. Nothing is re-derived or
   guessed.
2. **Confirm only the consequential moves.** A `Modal` gates `resolved` and
   `closed` (the two transitions with real operational weight — one implies
   the incident is fixed, the other is terminal and cannot be undone through
   this API). Every other legal transition (acknowledge, start investigating,
   reopen) and assignment fire immediately — still disabled-while-pending and
   still error-surfacing, just without a click a routine triage move does not
   need. This is a narrower confirmation policy than `ConfirmOperation`'s
   (which confirms its one operation, Ratify, unconditionally) because this
   story has five possible transitions of clearly different weight, not one.
3. **On success: refetch, never trust the local mutation as the new truth.**
   Every action re-runs the incident `GET` (and, transitively, the timeline
   `GET`) after a mutation completes, and calls the host's `onChanged` so a
   list view (the board) refreshes too. The `POST` response body is not used
   to patch local state directly — the refetch is the single source of "what
   is true now," so a board and a detail panel can never show two different
   versions of the same incident.
4. **On 409: refetch and say so, never blind-retry.** `handlers_incidents.go`
   maps both a stale `row_version` and an illegal transition to `409` for
   these three write endpoints. The console treats both identically: it
   refetches (the same refetch success uses) and shows a dismissible warning
   ("Changed since you loaded it... reloaded... review it and try again")
   rather than resending the same request, or silently accepting the new
   state without telling the operator someone else moved it. Tested
   explicitly (`IncidentDetail.test.tsx`'s "on a 409 conflict" case): the
   mutation endpoint is hit exactly once, never automatically retried.
5. **On 422/403/404: surface the server's own `detail` in an inline `Alert`,
   dismissible, never silent.** The alert never blocks the rest of the panel
   — an operator can still read the current state and try a different action
   while a failed one is still showing.
6. **Only legal transitions are ever offered.** `web/src/incidents.ts`'s
   `INCIDENT_TRANSITIONS` is a client-side restatement of
   `domain.incidentTransitions` (`internal/domain/incident.go`), kept
   deliberately in lock-step: a status renders exactly the buttons
   `CanTransitionTo` would accept from it, nothing more. There is no runtime
   "what can I do next" field on `incidentDTO` to derive this from instead —
   restating the table is the only option available without a Go change.
   Tested directly: `open` offers only Acknowledge; `resolved` offers Close
   and Reopen and nothing else; `closed` offers nothing.

Every future E-ACT story (alert-rule CRUD, maintenance windows, on-call
rosters, escalation policies) inherits this shape rather than re-deriving it.

### 2. The assignee picker degrades to free text on ANY directory failure — a confirmed contract gap, not a workaround invented silently

The brief asked for "a Cloudscape `Select` of users (from the users
endpoint)." Reading the actual permission gate before wiring it up
(`internal/httpapi/middleware.go`'s `requirePlatformAdmin`) found a real
mismatch: `GET /v1/admin/users` requires `PermPlatformAdmin` **and**
resolution to the system tenant — a strictly more privileged, cross-tenant
gate than `PermAdmin`, the tenant-administration permission all four incident
endpoints use. `internal/auth/rbac.go`'s `rolePermissions` confirms
`PermAdmin` does **not** imply `PermPlatformAdmin`; they are independent
roles (`"oneops-admin"` vs `"oneops-platform-admin"`). A real tenant-scoped
operator who can manage incidents is therefore not guaranteed to be able to
list the platform's users at all — this call will 403 for them in a real
deployment, even though it happens to succeed against the `AuthEnabled=false`
local-dev identity (`internal/httpapi/middleware.go`'s `authenticate`
synthesizes `oneops-platform-admin` + the system tenant when auth is
disabled, which is also why this works out of the box in this story's own
local verification).

No tenant-scoped alternative exists either: `GET /v1/admin/memberships`
(`PermAdmin`-gated, correctly tenant-scoped) requires an `org_id` query
parameter the console has no way to obtain — it is not in the JWT subject the
console reads (`web/src/auth.ts`'s `getSubject`), and would need its own new
read surface to resolve. There is no existing frontend pattern for a
user/owner picker anywhere in the console to fall back on either (`AssetID`
was searched — no owner-picker precedent exists).

**Decision:** `components/IncidentDetail.tsx`'s `AssigneeControl` tries
`GET /v1/admin/users` once per panel open. On success, it renders a
Cloudscape `Select` (`display_name (email)`, filterable, plus "Unassigned").
On **any** failure — 403 is the expected, common one, but the control does
not special-case it — it degrades to a plain `Input` for a raw user id, with
an inline `FormField` description naming the gap ("User directory
unavailable here — enter a user id directly"). The `assign` call itself is
identical either way: `POST .../assign` with `row_version` +
`assignee_user_id`, same 409/error handling. This keeps the story frontend-
only (no Go touched, per the hard constraint) while still shipping a working
Assign action for every deployment shape, honestly labelled rather than
either blocking on a backend change out of this story's scope or silently
producing a picker that 403s for most real operators.

**This is real debt, not resolved.** A correctly-scoped fix is either (a) a
new tenant-scoped "list users I can assign to" read endpoint under
`PermAdmin` (mirroring how `/admin/teams`/`/admin/memberships` are already
tenant-scoped, unlike `/admin/users`), or (b) resolving `org_id` into the
console's own session state so `/admin/memberships` becomes usable, plus a
join to `userDTO` for display names. Both are Go changes and are explicitly
out of this story's scope — flagged here for whoever picks up the next
E-ACT story or a dedicated follow-up.

### 3. Add-note is deferred, not fabricated

The brief said: "confirm whether an add-note/timeline-append HTTP endpoint
exists — if it does NOT, DEFER." It does not
(`IncidentEventKind`'s own doc comment, quoted above, is unambiguous that
this is a deliberate, documented absence — not an oversight). No add-note UI
exists in this story. The timeline (`IncidentDetailPanel`'s existing render)
already shows `created`/`status_transitioned`/`assigned`/`alert_note`
events — this story's actions populate the first three of those
automatically as a side effect of the transition/assign endpoints
themselves, which is the closest thing to "a note" this story's scope
reaches.

### 4. Create incident: a `Modal` + `Form`, title required, everything else optional

Mirrors `createIncidentRequest` field-for-field: `title` (required,
client-validated non-empty before the `Create` button enables — the server's
own 422 is still the source of truth, exercised by a dedicated test),
`description` (optional `Textarea`), `severity` (required `Select`, defaulted
to `medium`), `asset_id` (optional `Input`). No `assignee_user_id` field was
added to the create form — the brief's own field list for this modal
(title/description/severity/asset_id) omits it, and assignment is available
immediately after via the same panel the created incident opens into. On
`201`, the board reloads its list and the new incident's detail opens in the
`SplitPanel` immediately — an operator who just filed an incident does not
have to go find it.

## What this story explicitly does not do

- No Go, migration, or `openapi.yaml` change. Every endpoint above is called
  exactly as `handlers_incidents.go` already defines it — verified
  field-for-field before writing any TypeScript, per the brief's own
  instruction to read the Go first.
- No add-note/timeline-append UI (§3) — no such endpoint exists to call.
- No bulk actions from the board (acknowledge/assign/transition many at
  once) — ADR-NOC-004 already scoped that out, and this story does not
  revisit it.
- No fix for the `/admin/users` permission-scope gap (§2) — a real,
  confirmed defect, explicitly left for a follow-up rather than an
  in-scope Go change.
- No `Idempotency-Key` on any of these four calls — confirmed against
  `handlers_incidents.go` that none of them read that header (unlike
  `/v1/governance/*`/`/v1/artifacts`, the only two surfaces that do); adding
  one would be a header nobody reads, not real retry-safety.
- Does not touch `ConfirmOperation.tsx`, `governance.ts`, or the Ratify flow
  — this story's confirm modal is a new, narrower component
  (`IncidentDetail.tsx`'s inline `Modal`) because it needs to gate on which
  of five possible transitions was clicked, not confirm one fixed operation.

## Consequences

**What is now guaranteed.** An operator can acknowledge, progress, resolve,
close, or reopen an incident (subject to the exact state machine
`domain.IncidentStatus.CanTransitionTo` defines), assign or reassign it, and
file a new one, all from the console, with every mutation optimistic-lock
correct: a stale `row_version` is refused server-side and surfaced, never
silently overwritten (`IncidentDetail.test.tsx`'s 409 test proves the
mutation is sent exactly once and the panel refetches rather than retrying).
Only legal transitions are ever offered as buttons — proven per-status, not
by inspection.

**What is not claimed.** The assignee picker is a real `Select` only where
the platform-admin-gated user directory happens to be reachable (§2); most
real tenant-scoped deployments will see the free-text fallback until that gap
is closed with a Go change. There is no add-note capability. There is no
optimistic UI update — every action's visible effect comes from a real
round-trip `GET`, which means a slower network makes the panel visibly wait
rather than show a value that might not have actually been accepted.

## Evidence

- `web/src/api.ts` — `postJSON`, the generic write helper every mutation
  below calls; doc comment records why no `Idempotency-Key` is sent.
- `web/src/incidents.ts` — `INCIDENT_TRANSITIONS`/`legalTransitions` (the
  client-side mirror of `domain.incidentTransitions`), `createIncident`,
  `transitionIncident`, `assignIncident`.
- `web/src/users.ts` — the platform user-directory client, with the §2 gap
  recorded in its own doc comment at the point of use.
- `web/src/components/IncidentDetail.tsx` — `IncidentDetailPanel`'s new
  action state (busy/actionProblem/conflictNotice/confirmTarget),
  `AssigneeControl` (Select-with-Input-fallback), the consequential-
  transition confirm `Modal`.
- `web/src/routes/IncidentBoardPage.tsx` — the "Create incident" `Button` +
  `CreateIncidentModal`, and the `reload` callback threaded into
  `IncidentDetailPanel`'s `onChanged`.
- `web/src/IncidentDetail.test.tsx` (9 tests) — only-legal-transitions per
  status, row_version+status sent then refetched, consequential-transition
  confirm-before-send, 409-refetches-and-notices-without-retry, assign via
  picker, assign via free-text fallback, mutation error surfaced inline.
- `web/src/routes/IncidentBoardPage.test.tsx` (+3 tests) — create form
  disabled until titled, posts the exact body and opens the new incident,
  validation error keeps the dialog open.
- `pnpm --dir web exec tsc -b --noEmit` — clean.
- `pnpm --dir web exec vitest run` — 101 tests green (89 pre-existing + 12
  new), no existing assertion weakened.
- `make web` — builds cleanly (~1,196 kB JS / ~347 kB gzip, ~1,173 kB CSS /
  ~238 kB gzip); the same `grep -Eo 'https?://...'` sweep ADR-UI-001/
  ADR-NOC-004 run returns only the same inert strings (XML/SVG namespace
  URIs, a date-fns/React dev-mode doc link, a Google Fonts license comment
  over an embedded `data:` font) — no runtime CDN reference, no regression.
- `go build ./...` and `make test` (full suite, `-race`) — green, unaffected;
  this story touches no Go source (`git diff --stat` shows only `web/`).

## Enforcement

- `web/src/IncidentDetail.test.tsx` and the "create incident" cases in
  `web/src/routes/IncidentBoardPage.test.tsx`, under `make web-test` — this
  ADR's own claims (legal-transition gating, row_version round-trip, 409
  discipline, error surfacing, create-and-open) checked on every build, not
  by inspection.
- Any future change to `domain.incidentTransitions`
  (`internal/domain/incident.go`) that is not mirrored into
  `web/src/incidents.ts`'s `INCIDENT_TRANSITIONS` will not fail a build —
  there is no generated-from-Go check for this table today. A future E-ACT
  story or a Go-side change to the state machine should update both, and a
  fair follow-up would be generating this table rather than hand-mirroring
  it a second time.

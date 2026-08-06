# ADR-ACT-004 — Maintenance-window create/cancel reuses the E-ACT write-action pattern; no drill-down surface, `DELETE` carries no `row_version` because the endpoint has none

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-ACT-001 (the write-action pattern this story inherits), ADR-ACT-002 (the prior story's own confirmed-gap precedent for a `row_version`-less `DELETE`), `docs/PLATFORM-BUILD-PLAN.md` E-ACT.3, `internal/httpapi/handlers_maintenance_window.go` (the four endpoints this story reuses unchanged), `internal/domain/maintenance_window.go` (`MaintenanceWindow.Validate`, `MaintenanceWindowRepository`), ADR-ALERTING-002 (the half-open `[starts_at, ends_at)` suppression semantics this screen's status computation and validation both restate) |

## Context

E-ACT.1/E-ACT.2 (ADR-ACT-001/ADR-ACT-002) turned the incident board and the
alerts board operational and set the pattern every later E-ACT story
inherits: read once and send the `row_version` back exactly once, confirm
only consequential/destructive moves, refetch (never trust the local
mutation) on success, refetch-and-say-so (never blind-retry) on `409`,
surface every other RFC 7807 detail inline. E-ACT.3 applies that pattern to
`maintenance_window`'s own CRUD surface (E3.3a) — a console screen to see and
manage maintenance windows, reachable from a new top-level "Maintenance" nav
item rather than from the alerts/asset context the brief first proposed
(§2 below explains why).

**The contract, confirmed against the Go before any UI was written**
(`internal/httpapi/handlers_maintenance_window.go`,
`internal/domain/maintenance_window.go`,
`internal/store/postgres/maintenance_window_store.go`):

- `GET /v1/admin/maintenance-windows?limit=&after=`: keyset-paginated over
  `window_id` (`MaintenanceWindowStore.List`, default page 50, max 500 —
  `clampMaintenanceWindowPage`), returns `{items:[...]}` with **no
  next-page cursor in the response body** — the same "one bounded page, no
  keyset chasing" shape `listAlertRules`/`listOnCallSchedules` already have,
  restated here as `MAINTENANCE_WINDOW_LIST_CAP = 100`
  (`maintenanceWindows.ts`).
- `GET /v1/admin/maintenance-windows/{id}`: returns one window; **not called
  by this console** — see §3 below for why no drill-down surface exists to
  call it from.
- `POST /v1/admin/maintenance-windows` (`createMaintenanceWindowRequest`):
  `asset_id` (required, re-verified against the caller's tenant
  server-side — a cross-tenant or nonexistent `asset_id` is `404`),
  `starts_at`/`ends_at` (required, RFC3339 timestamps — Go's
  `encoding/json` decodes `time.Time` from any RFC3339 string, so the
  console does not need to match the server's own serialization format,
  only produce a valid one), `reason` (optional, free text, ≤500 chars —
  `domain.MaxMaintenanceWindowReasonLength`). `window_id` is minted
  server-side; `created_by`/`suppressed_count`/`last_suppressed_at` are
  repository/evaluator-owned and never accepted from the caller. Returns
  `201` + the full `maintenanceWindowDTO`, which carries `row_version`
  (confirmed at `handlers_maintenance_window.go:33`).
- **The half-open interval rule is real and enforced server-side**:
  `domain.MaintenanceWindow.Validate` rejects `ends_at <= starts_at` as a
  `422` ("must be strictly after starts_at — the window is half-open
  `[starts_at, ends_at)`"). A firing at exactly `starts_at` IS suppressed; a
  firing at exactly `ends_at` is NOT (ADR-ALERTING-002 §3) — the same
  half-open convention `TelemetryRepository.QueryRange`'s `[from, to)`
  already uses.
- `DELETE /v1/admin/maintenance-windows/{id}`: **takes no request body and
  no `row_version` at all.** `deleteMaintenanceWindow`'s handler calls
  `s.maintenanceWindows.Delete(ctx, id)` directly;
  `domain.MaintenanceWindowRepository`'s `Delete(ctx context.Context,
  windowID string) error` signature has no optimistic-lock parameter to
  pass one through even if the handler wanted to — **the identical
  asymmetry ADR-ACT-002 §1 already found and recorded for
  `deleteAlertRule`**, confirmed independently here rather than assumed by
  analogy. Returns `204` on success, `404` if `windowID` does not name a
  window visible to the caller's tenant.
- `maintenanceWindowDTO` carries no status field at all — "active now" /
  "upcoming" / "expired" is not part of the HTTP contract; it exists only
  as a client-side derivation from `now` against the window's own
  `starts_at`/`ends_at` (§4 below).

## Decision

### 1. Create and Cancel follow ADR-ACT-001's pattern exactly where the contract allows it to

Create (`POST`) round-trips nothing to send back (there is no prior
`row_version` to read — a window does not exist yet), so it is exactly
ADR-ACT-001 §4's create-incident shape: client-validated, `201` on success,
board refetch. Cancel (`DELETE`) cannot round-trip a `row_version` for the
same confirmed reason `deleteAlertRule` cannot (§1 above and ADR-ACT-002
§1) — the console does **not** fabricate one. The cancel confirmation
`Modal` names the gap directly ("Unlike a rule edit, there is no
optimistic-lock check on cancel — the endpoint takes no row_version"), the
same posture the alert-rule delete modal already established;
`maintenanceWindows.ts`'s `deleteMaintenanceWindow` doc comment records the
same fact at the point of use.

**Consequence, stated plainly:** if the window an operator is looking at was
already cancelled by someone else, this operator's own Cancel click still
succeeds trivially (the row is simply gone, no differently than clicking
Cancel a second time on an already-cancelled window would 404) — there is no
way for this endpoint to detect or refuse "the window changed since I loaded
it" the way a `row_version`-guarded `PATCH` could. This is a genuine gap in
the existing HTTP contract, not one introduced by this story.

### 2. Reachable from a new top-level "Maintenance" nav item, not the alerts/asset context

The brief suggested "from the alerts/asset context." Neither exists as a
reachable seam today: the alerts board (`AlertsBoardPage`/
`AlertRuleDetailPanel`, ADR-NOC-005/ADR-ACT-002) has no notion of "this
asset's maintenance windows" anywhere in its DTO or its UI, and there is no
asset-detail screen in this console at all — `AssetID` appears only as a
free-text field on alert-rule/incident/maintenance-window forms, never as
its own routed page. Bolting a maintenance-window sub-list onto the alerts
board's per-rule detail panel would conflate two different scopes (a rule
watches one metric on one asset; a maintenance window suppresses every rule
on one asset) for no reachability gain, and would still need its own list
call regardless. A new top-level `/maintenance` route + nav item
(`Shell.tsx`'s `NAV_ITEMS`) is simpler, matches how On-call
(`/on-call`) is already its own top-level section rather than hanging off
another screen, and gives every maintenance window — not just ones an
operator happens to reach via an alert rule — a single place to be seen and
managed.

### 3. No drill-down/detail surface — the list IS the detail, and Cancel is a per-row action

Unlike the incident board and the alerts board, `MaintenanceBoardPage` opens
no `SplitPanel`. `maintenanceWindowDTO` is small and flat (nine fields, no
nested collections, no timeline) — the list's own `Table` cells already show
everything a `GET .../{id}` would add, so a drill-down would only exist to
host the Cancel button, which fits perfectly well as a `Table` actions
column instead. `GET /v1/admin/maintenance-windows/{id}` is therefore a
**confirmed-but-unused** part of the contract: real and correct, just not
needed by this screen. This differs from ADR-ACT-001/ADR-ACT-002's own
choice (`SplitPanel` detail panels for incidents/alert-rules) because those
DTOs carry either a real timeline (incidents) or enough fields that a table
row would be unreadably wide (alert rules' seven config fields plus
severity/state) — neither pressure exists here.

### 4. Active/upcoming/expired is a pure client-side derivation, never fetched

`maintenanceWindows.ts`'s `computeMaintenanceWindowStatus(win, now)`
implements exactly ADR-ALERTING-002 §3's half-open rule: `now < starts_at`
is upcoming, `now >= ends_at` is expired, otherwise active. This is
deliberately **not** requested from the server — there is no such field on
`maintenanceWindowDTO` to request, and computing it client-side from two
timestamps already on hand is simpler and always current (no risk of a
cached status going stale between the list fetch and when an operator reads
it), at the cost of the console's own clock being the source of truth
rather than the database's — an acceptable trade given the same clock skew
risk already exists implicitly in every "time since" render this console
does elsewhere (e.g. `last_transition_at`/`updated_at` formatting).

### 5. Half-open validation is enforced client-side before the server's own 422

`maintenanceWindows.ts`'s `maintenanceWindowRangeError(startsAt, endsAt)`
restates `domain.MaintenanceWindow.Validate`'s own rule — `ends_at` strictly
after `starts_at` — as a pure function over two `Date`s, the same
"restate, don't derive" posture `alertRules.ts`'s validators document (no
runtime "what is valid" field exists on any DTO to derive this from
instead). It gates the create modal's submit button directly; the server's
own `422` remains the actual source of truth, exercised by a dedicated test
(`create with end≤start is blocked client-side` covers the client gate,
`surfaces a validation error instead of closing the dialog` covers the
server round-trip when a bad value somehow still reaches it).

### 6. Date+time entry is a Cloudscape `DateInput` + `TimeInput` pair per bound, combined browser-locally, sent as UTC

Each of `starts_at`/`ends_at` is entered as a `DateInput` (`YYYY-MM-DD`) and
a `TimeInput` (`hh:mm`, 24-hour) side by side under one `FormField` label —
not a `DateRangePicker` (which models one *relative* range, not two
independently-labelled absolute instants an operator reasons about as
"starts" and "ends") and not a bare text `Input` (which would accept
anything, including a string `Validate` would reject with no client-side
warning at all). `maintenanceWindows.ts`'s `combineDateAndTime(date, time)`
parses the pair as **browser-local wall-clock time**
(`new Date(`${date}T${time}:00`)`, no offset suffix, which the JS `Date`
constructor already interprets as local time) and `.toISOString()` on the
result is what is actually sent — UTC, RFC3339, exactly what
`createMaintenanceWindowRequest` expects. An operator therefore enters "9am
my time," not "9am UTC," which is the natural way to declare a maintenance
window and matches how every other timestamp this console renders
(`toLocaleString()`) is already presented back to them.

## What this story explicitly does not do

- No Go, migration, or `openapi.yaml` change. `listMaintenanceWindows`/
  `createMaintenanceWindow`/`deleteMaintenanceWindow` call the endpoints
  exactly as `handlers_maintenance_window.go` already defines them —
  field-for-field, verified before any TypeScript was written.
- No fix for the `DELETE`-has-no-`row_version` gap (§1) — the same class of
  confirmed, real contract asymmetry ADR-ACT-002 already left as a
  follow-up for `deleteAlertRule`; a correctly-scoped fix for both would add
  an optional `row_version` parameter to each `Delete` signature.
- No asset picker. `asset_id` is free text, the same precedent ADR-ACT-002
  set for `alert_rule`'s own `asset_id` field — no asset directory/picker
  exists anywhere in this console (`GET /v1/admin/assets/graph` returns
  graph primitives, not a name-searchable list, and was not wired up here
  for the same "frontend-only, don't invent a new read pattern for one
  field" reason ADR-ACT-001 §2 gives for the assignee picker's own
  fallback).
- No recurrence, no dependency-aware/CI-graph suppression — both are
  explicitly out of scope in `domain.MaintenanceWindow`'s own doc comment
  (E3.3b and future work respectively); this story adds no UI implying
  either exists.
- No edit. `MaintenanceWindowRepository` has no `Update`/`Patch` method at
  all — a window is created or cancelled, nothing in between
  (`domain.MaintenanceWindowRepository.Delete`'s own doc comment: "there is
  no other lifecycle"). This is not a gap this story left out; there is
  nothing to wire up.
- No `Idempotency-Key` on either write call — confirmed against
  `handlers_maintenance_window.go` that neither reads that header, the same
  finding ADR-ACT-001/ADR-ACT-002 already made for their own endpoints.
- No live-ticking clock. Active/upcoming/expired is computed once per
  render from `new Date()`; the board does not re-render on a timer to
  advance a window from upcoming to active while it sits open unattended —
  a manual refresh (any action, or navigating away and back) recomputes it.
  A future story could add a periodic refetch/recompute if this proves to
  matter operationally; not added speculatively here.

## Consequences

**What is now guaranteed.** An operator can declare a new maintenance window
over one asset with a validated half-open time range and an optional
reason, see every window for their tenant with a computed active/upcoming/
expired status and a running suppressed-count, and cancel any window at any
time, all from the console. Create is validated client-side against the
same half-open rule the server enforces, proven by test both for the
client-side block and the server-side round-trip. Both write actions
disable-while-pending, never double-submit, and refetch the board on
success; a failed create keeps its dialog open with the server's own detail
shown, a failed cancel leaves the window in the table with the error
surfaced inline rather than either silently vanishing or silently failing.

**What is not claimed.** Cancel has no concurrent-edit protection — the
same confirmed, real gap ADR-ACT-002 already recorded for delete, not a
gap unique to or hidden by this story. There is no detail/drill-down
surface (§3) and no edit capability (there is nothing to edit). Status is a
snapshot at render time, not live-updating. `asset_id` is free text with no
existence check until submit (a typo surfaces as this story's own `404`
from `createMaintenanceWindow`'s asset re-verification, in the same inline
`Alert` every other create-time validation error uses).

## Evidence

- `web/src/maintenanceWindows.ts` — `MaintenanceWindowDTO`,
  `listMaintenanceWindows`/`createMaintenanceWindow`/
  `deleteMaintenanceWindow` (with `deleteMaintenanceWindow`'s doc comment
  recording the no-`row_version` contract fact in full),
  `maintenanceWindowAssetIdError`/`maintenanceWindowReasonError`/
  `maintenanceWindowRangeError`, `combineDateAndTime`,
  `computeMaintenanceWindowStatus`.
- `web/src/routes/MaintenanceBoardPage.tsx` — the board `Table` (asset,
  window range, reason, computed status, suppressed count, actions
  columns), `CreateMaintenanceWindowModal`, the per-row Cancel confirm
  `Modal`.
- `web/src/Shell.tsx` — the "Maintenance" nav item and its
  `activeHref`/breadcrumb wiring; `web/src/App.tsx` — the `/maintenance`
  route.
- `web/src/routes/MaintenanceBoardPage.test.tsx` (9 tests) — list renders
  with active/upcoming/expired computed from a fixed `now`
  (`vi.useFakeTimers({ toFake: ['Date'] })`, faking only `Date` so
  `waitFor`/`findBy*`'s own real-timer polling is unaffected), empty state,
  error-and-retry, create disabled until asset id + a valid range are
  filled, create posts the exact RFC3339-UTC body and refetches, create
  blocked client-side when end is not strictly after start (including the
  equal-bounds case, proving the half-open rule specifically, not just
  "end < start"), create surfaces a server-side validation error without
  closing the dialog, cancel confirms-then-sends-no-row_version-then-
  refetches, cancel error surfaced without removing the row.
- `pnpm --dir web exec tsc -b --noEmit` — clean.
- `pnpm --dir web exec vitest run` (`make web-test`) — 120 tests green (111
  pre-existing + 9 new), no existing assertion weakened; confirmed stable
  across repeated runs (a single unrelated `NOCOverviewPage.test.tsx`
  flake was observed once in a full-suite run and did not reproduce across
  four subsequent full-suite runs, including one against the pre-existing
  `master` baseline with this story's changes stashed — not attributable to
  this story).
- `make web` — builds cleanly (~1,254 kB JS/~363 kB gzip, ~1,177 kB CSS/
  ~239 kB gzip — a modest increase from ADR-ACT-002's own baseline); the
  same `grep -Eo 'https?://...'` sweep every prior ADR has run returns only
  the same inert strings (XML/SVG namespace URIs, a date-fns/React dev-mode
  doc link, a Google Fonts license comment over an embedded `data:` font) —
  no new runtime CDN reference.
- `go build ./...` and `make test` (full suite, `-race`) — green, unaffected;
  this story touches no Go source (`git diff --stat` shows only `web/`).

## Enforcement

- `web/src/routes/MaintenanceBoardPage.test.tsx`, under `make web-test` —
  this ADR's own claims (half-open validation client- and server-side,
  no-row_version-on-cancel, status computed correctly per bucket, error
  surfacing) checked on every build, not by inspection.
- Any future change to `domain.MaintenanceWindow.Validate`'s half-open rule
  or to `MaintenanceWindowRepository.Delete`'s signature (e.g. adding an
  optimistic-lock parameter to close the §1 gap) should be mirrored into
  `maintenanceWindows.ts` and this ADR updated — there is no
  generated-from-Go check today, the same limitation ADR-ACT-001/
  ADR-ACT-002's own Enforcement sections name for their own hand-mirrored
  tables.

# ADR-NOC-005 — The alerts and on-call boards reuse the existing admin endpoints unchanged; no "linked incident" column exists because the field is not on the HTTP contract

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-UI-001 (Cloudscape console foundation both boards live inside), ADR-NOC-004 (the incident board pattern both boards mirror: typed data module, `Table`, shared `SplitPanel`), `docs/PLATFORM-BUILD-PLAN.md` E7.3c, `internal/httpapi/handlers_alert_rules.go` / `handlers_oncall.go` (the four endpoints this story reuses unchanged) |

## Context

E7-UI.2 (ADR-NOC-004) gave the console a real incident board. E7.3c is the
next per-domain drill-down: an alerts board over `GET /v1/admin/alert-rules`
and an on-call board over `GET /v1/admin/on-call-schedules` (+ `/{id}/on-call`
and `/{id}/participants`), completing the NOC overview's three domains
(incidents, alerts, on-call) with their own screens. Both are read-only views
over administration endpoints that already exist in full (E3.1/E5.2a); the
brief was explicit that no Go may be touched.

Confirming the exact DTOs before building (the same discipline ADR-NOC-004
applied) surfaced one real gap: `domain.AlertRule` carries a
`CurrentIncidentID *string` field (E4.1, "the open, alert-sourced Incident
this rule's firing is currently linked to"), but `toAlertRuleDTO`
(`internal/httpapi/handlers_alert_rules.go`) does not serialize it, and
neither does `openapi.yaml`'s `AlertRule` schema. The brief asked for a
"linked incident" column driven by `current_incident_id`. That field is not
part of the actual HTTP contract this story is constrained to reuse
unchanged, so the column does not exist on the board built here — adding it
would mean patching `toAlertRuleDTO` and the OpenAPI schema, which is exactly
the "no Go touched" constraint this story operates under. This ADR records
that gap explicitly rather than fabricating the column from data the API does
not return, or silently dropping the requirement without a paper trail.
Similarly, `AlertRule` has no `name` field (only `asset_id` + `metric`
identify it) — the "Rule (name/metric)" column brief language is read as
"identify the rule by its asset/metric pair," which is what the DTO actually
carries.

## Decision

### 1. Two more `Table`/`Cards` boards, same shape as the incident board

`web/src/alertRules.ts` and `web/src/onCall.ts` are typed data modules in the
same style as `incidents.ts`/`noc.ts`: hand-written interfaces verified
field-for-field against the Go DTOs, one bounded list fetch per board
(`ALERT_RULE_LIST_CAP` / `ON_CALL_SCHEDULE_LIST_CAP`, both 100 — the same "one
page, no keyset chasing" posture `INCIDENT_LIST_CAP` documents, for the
identical reason: neither list endpoint returns a next-page cursor).
`web/src/alertPresentation.ts` and `web/src/onCallPresentation.ts` carry the
severity/state `StatusIndicator` palettes and rank tables, the same pattern
`incidentPresentation.ts` established, extended rather than overloaded (alert
severity is a different three-value enum than incident severity, so it gets
its own map rather than being shoehorned into `SEVERITY_TYPE`). Both boards
reuse `incidentPresentation.ts`'s `humanise` helper directly — it is fully
generic (`(v: string) => string`), not incident-typed, so this is reuse of an
existing shared utility, not a new dependency between domains.

**Alerts board** (`web/src/routes/AlertsBoardPage.tsx`): a `Table` over
`listAlertRules()`, columns Rule (`asset_id · metric`, `rule_id` beneath),
Severity, State (`last_state`: firing→`error`, ok→`success`), Symptom class,
Threshold (`comparator` symbol + value), Enabled — filterable by state and
severity (two `Select`s, matching `IncidentBoardPage`'s status `Select`, not
`PropertyFilter` for the same reason ADR-NOC-004 gave: two single-enum
filters do not need free-text property/operator grammar), sortable
(hand-rolled comparator table, no `collection-hooks`, same as
`IncidentBoardPage`), client-side `Pagination` (20/page). Clicking a rule
opens `components/AlertRuleDetail.tsx`'s `AlertRuleDetailPanel` in the
shell's `SplitPanel` (`GET /v1/admin/alert-rules/{id}`, reused unchanged) —
the same drill-down mechanism ADR-NOC-004 built, now proven reusable by a
second screen exactly as that ADR intended ("any future board can reuse the
same context without Shell knowing anything incident-specific").

**On-call board** (`web/src/routes/OnCallBoardPage.tsx`): `Cards`, not
`Table` — chosen because each schedule's content (current on-call, handoff
interval, an ordered participant list) is multi-section per item rather than
a single-row-per-field shape, which is what `Cards`' `cardDefinition.sections`
is built for; `Table` would need a nested-table-in-a-cell workaround for the
participant roster that `Cards` renders natively. Only `status: active`
schedules are shown (per the brief: "for each active schedule"); an archived
schedule is excluded, not rendered greyed-out, mirroring the on-call
overview widget's own "who is on call now" framing (ADR-NOC-001/003) rather
than introducing a status filter this board does not otherwise need.

### 2. The on-call board's supplementary fetches are a bounded N+1, not a second projection endpoint

"Who is on call now" and "the ordered roster" are not on `onCallScheduleDTO`
— they are separate endpoints (`GET .../on-call`, `GET .../participants`) by
design (E5.2a keeps a schedule's static config distinct from its
point-in-time resolution). After the schedule list loads, the board issues
both calls, in parallel via `Promise.allSettled`-shaped independent promise
chains, for up to `ON_CALL_DETAIL_FETCH_CAP` (20) active schedules — the same
"cap an N+1, don't chase it unbounded" pattern `api.ts`'s
`getRelations`/`RESOLVE_LIMIT` already established for resolving related
artifact names, sized smaller here because it is two round trips per
schedule instead of one. A schedule beyond the cap still renders (name,
handoff interval) with an explicit "Not loaded (see the fetch bound below)"
state rather than the board firing an unbounded number of concurrent
requests. A per-schedule fetch failure degrades that one section to "Could
not load" without blanking the card or the rest of the board — the same
"supplementary data must not fail the primary view" rule
`IncidentDetailPanel`'s timeline fetch already follows.

A dedicated `GET /v1/admin/noc/on-call-board` projection (mirroring
`GET /v1/admin/noc/overview`'s own aggregation) was considered and declined
for the same three reasons ADR-NOC-004 gave for the incident board: no Go
may be touched by this story; the existing per-schedule endpoints already
serve every field needed; and the tenant scale here is smaller than the
incident case by construction — E7.1's overview endpoint itself already caps
active schedules at 100 and resolves `OnCall(now)` for each server-side, so a
tenant hitting this board's own smaller cap is already visible from the
overview first.

### 3. No "linked incident" column on the alerts board — the field is not on the contract

`current_incident_id` is not serialized by `toAlertRuleDTO` today (see
Context). Rather than adding it — which would mean editing a Go handler and
`openapi.yaml`, both out of this read-only, no-Go-touched story — the alerts
board simply does not have that column, and `AlertRuleDetailPanel` states
this explicitly ("This rule's linked incident (if any) is not part of the
current alert-rule contract — see ADR-NOC-005") rather than presenting a
column that would always read empty. Exposing `current_incident_id` as an
additive DTO/OpenAPI field is a small, well-scoped follow-up story, not a
decision this ADR makes on its own — see Consequences.

## What this story explicitly does not do

- No new table, entity, migration, or endpoint. All four endpoints
  (`GET /admin/alert-rules`, `GET /admin/alert-rules/{id}`,
  `GET /admin/on-call-schedules`, `GET /admin/on-call-schedules/{id}/on-call`,
  `GET /admin/on-call-schedules/{id}/participants`) are read exactly as
  E3.1/E5.2a defined them — verified field-for-field against
  `alertRuleDTO`/`onCallScheduleDTO`/`onCallParticipantDTO`/`onCallNowDTO`
  before reuse.
- No `current_incident_id` exposure. `domain.AlertRule.CurrentIncidentID`
  exists but is not on the HTTP contract; the alerts board has no "linked
  incident" column as a direct consequence (see Decision §3).
- No archived-schedule view, no schedule create/edit/participant-reorder UI
  — this is a read-only board over the existing administration surface, the
  same scope line ADR-NOC-004 drew for incidents (view + drill-down, no
  bulk action).
- No `PropertyFilter`, no `collection-hooks` — same reasoning ADR-NOC-004
  gave for the incident board.
- No OpenAPI change — no Go code was touched by this story at all.

## Consequences

**What is now guaranteed.** All three NOC-overview domains (incidents,
alerts, on-call) now have their own drill-down board inside the Cloudscape
console, proven by `web/src/routes/AlertsBoardPage.test.tsx` (renders a
mocked list, filters by state, opens a rule's detail in the split panel, an
explicit empty state, an error banner with retry) and
`web/src/routes/OnCallBoardPage.test.tsx` (renders current-on-call and the
ordered roster, excludes archived schedules, degrades a supplementary-fetch
failure without blanking the card, two distinct empty states — no schedules
at all vs. every schedule archived — and an error banner with retry) rather
than by inspection. `ShellSplitPanelContext` (ADR-NOC-004) is now used by a
second, unrelated screen exactly as designed.

**What is not claimed.** The alerts board has no way to see which incident a
firing rule produced — an honest, named gap (Decision §3), not a silent one.
The on-call board's on-call/roster data is a snapshot at fetch time, not
live (no polling was added here, the same posture ADR-NOC-004 chose for the
incident board — an operator actively looking at a roster benefits less from
rows reordering under them). A tenant with more than
`ON_CALL_DETAIL_FETCH_CAP` (20) simultaneously active schedules will see the
excess schedules with no on-call/roster data loaded — a documented, visible
bound, not a silent one. The natural follow-up this story leaves for a future
story: an additive `current_incident_id` field on `alertRuleDTO` (and
`AlertRule` in `openapi.yaml`) would let the alerts board add the linked-
incident column the original brief wanted.

## Evidence

- `web/src/alertRules.ts`, `web/src/onCall.ts` — the typed data modules,
  verified field-for-field against `handlers_alert_rules.go`/
  `handlers_oncall.go`'s DTOs; `ALERT_RULE_LIST_CAP`,
  `ON_CALL_SCHEDULE_LIST_CAP`, `ON_CALL_DETAIL_FETCH_CAP` and their doc
  comments.
- `web/src/alertPresentation.ts`, `web/src/onCallPresentation.ts` — the
  severity/state `StatusIndicator` palettes and `formatDurationSeconds`.
- `web/src/routes/AlertsBoardPage.tsx`, `web/src/components/AlertRuleDetail.tsx`
  — the alerts board and its split-panel detail.
- `web/src/routes/OnCallBoardPage.tsx` — the on-call board (`Cards`,
  bounded supplementary fetches, degrade-on-failure).
- `web/src/Shell.tsx` — `Alerts` (`/alerts`) and `On-call` (`/on-call`) added
  to `SideNavigation`/`activeHrefFor`/breadcrumbs.
- `web/src/App.tsx` — `/alerts` routes to `AlertsBoardPage`, `/on-call`
  routes to `OnCallBoardPage`.
- `web/src/routes/AlertsBoardPage.test.tsx`,
  `web/src/routes/OnCallBoardPage.test.tsx` — the claims above.
- `pnpm --dir web exec tsc -b --noEmit` && `pnpm --dir web exec vitest run`
  (`make web-test`) — clean; 61 tests (50 pre-existing + 11 new).
- `make web` — builds `webdist/` cleanly (~1,046.67 kB JS / ~296.66 kB gzip,
  ~1,127.56 kB CSS / ~233.65 kB gzip — a modest increase from ADR-NOC-004's
  own baseline for two more screens); `grep -o 'https\?://...'` across the
  built bundle returns only the same inert strings ADR-UI-001/ADR-NOC-003/
  ADR-NOC-004 already documented (XML/SVG namespace URIs, an embedded
  `data:` font, a React dev-mode error-decoder doc link never fetched, a CSS
  license comment) — no runtime CDN reference.
- `go build ./...` and `make test` (full suite, `-race`) — green, unaffected;
  this story touches no Go source.
- `make lint` — 0 issues.

## Enforcement

- `web/src/routes/AlertsBoardPage.test.tsx` / `OnCallBoardPage.test.tsx`
  under `make web-test` — this ADR's own claims about rendering, filtering,
  drill-down, the two on-call empty states, and degrade-on-failure, checked
  on every build.
- `internal/httpapi`'s existing `alertRuleDTO`/`onCallScheduleDTO` tests —
  unchanged; this story adds no Go surface for them to regress against.

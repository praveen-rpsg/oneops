# ADR-NOC-004 — The incident board groups root/collateral client-side from a bounded fetch, sorts and paginates locally, and drills in through one shell-owned `SplitPanel`

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-UI-001 (Cloudscape console foundation this story's screen lives inside), ADR-NOC-003 (the overview screen this board is drilled into from — `NOCIncidentGrouping`'s `root_count`/`collateral_count` summary), ADR-ALERTING-004 (`incident.root_incident_id` — the grouping link this story reads, never writes), `docs/PLATFORM-BUILD-PLAN.md` E7-UI.2, `internal/httpapi/handlers_incidents.go` (the three endpoints this story reuses unchanged: `GET /admin/incidents`, `GET /admin/incidents/{id}`, `GET /admin/incidents/{id}/timeline`) |

## Context

E7-UI.1 (ADR-NOC-003) gave the console a live overview with a `grouped`
summary (`root_count`/`collateral_count`) but no way to see the incidents
themselves. `Incidents` in `SideNavigation` routed to `ComingSoon`. This story
fills it: a real board an operator drills from the overview into, sees open
incidents grouped root-cause to collateral, and clicks into for detail plus
timeline — "see it AND act on it," per the story brief.

The incident HTTP surface already exists in full (E5.1/E4.2, unchanged by
this story): `GET /v1/admin/incidents` (keyset-paginated list, optional
`status` filter, no server-side grouping), `GET /v1/admin/incidents/{id}`,
`GET /v1/admin/incidents/{id}/timeline`. `incident.root_incident_id` (E4.2,
ADR-ALERTING-004) is the only grouping signal, and it is read-only over HTTP
by design — set exclusively by `internal/grouping`'s reconciler, never by a
caller. This story's only real decision is how to render that link, not how
to produce it.

## Decision

### 1. Client-side grouping from one bounded fetch — no new projection endpoint

`GET /v1/admin/incidents` returns a flat, keyset-paginated list and carries
**no next-page cursor in its response body** (unlike `GET /v1/artifacts`,
whose `Page.next_cursor` `EstatePage` already chases with a "Load more"
button). Chasing keyset pages here would mean re-deriving a cursor from the
last item's `incident_id` with no server signal for "is there more," for a
feature (grouping) that specifically needs the *whole* relevant set in memory
at once to link roots to collateral correctly.

Instead, `web/src/incidents.ts` fetches **one page, capped at
`INCIDENT_LIST_CAP = 100`**, and `web/src/incidentGrouping.ts` builds the
root/collateral tree from exactly that set, purely client-side:

- An incident whose `root_incident_id` names another incident **present in
  the same fetched set** renders nested under that root (`Table`
  `expandableRows`, see §2).
- An incident with no `root_incident_id`, or whose `root_incident_id` names
  an incident **not present** in the fetched set (the root fell outside the
  cap, or was excluded by the active status filter), renders as its own
  top-level row — a true root/standalone in the first case, a **degraded
  flat rendering of an orphaned collateral** in the second. Nothing is ever
  dropped or guessed at.

**The honest bound.** A tenant with more than 100 incidents matching the
active filter can have a root and its collateral split across this cap, and
grouping degrades to flat for the one on the wrong side. The board makes this
visible rather than silent: when the fetched count reaches the cap, a warning
line names the bound and suggests narrowing the status filter. This is the
same "state the bound, don't hide it" posture ADR-NOC-001 took for `on_call`
(capped at 100, documented in the handler).

**Why not add a `GET /v1/admin/noc/incident-board` projection instead.** The
brief allowed one (read-only, RLS-scoped `appPool`, `PermAdmin`, additive
openapi, tenant-isolation test) if the client-side approach had "a real
correctness gap, not just theoretical." It doesn't, for three reasons this
story checked before deciding: (1) `internal/grouping`'s reconciler only
links **open, alert-sourced** incidents within a topology-derived dependency
closure (ADR-ALERTING-004 §2) — the population that can be grouped at all is
already a fraction of "every incident," and the board's own default filter
(`status=open`) is exactly that population; (2) a tenant with over 100
*simultaneously open* incidents is already deep in an operational-crisis
regime this screen cannot make worse — the degraded flat rendering still
shows every incident, just not nested; (3) the same reduced-concept
discipline this codebase applies throughout (Vol III §3.4 — no
Dashboard/Report/Overview reification) argues against manufacturing a second
read surface for data three existing, already-tested endpoints already
serve, when the client-side alternative is a same-size problem (bounded,
documented) rather than a smaller one. If a real tenant hits the cap in
practice, the fix is raising `INCIDENT_LIST_CAP` or adding server-side
grouping then, with evidence in hand instead of speculation now.

### 2. `Table` `expandableRows`, not a nested/grouped list component

Cloudscape's `Table` supports `expandableRows` natively (`getItemChildren`,
`isItemExpandable`, `expandedItems`, `onExpandableItemToggle`) — the same
`Table` primitive `EstateTable` already uses, extended rather than replaced.
Groups with collateral default to **expanded** (computed once per fetch from
`groupIncidents`'s own output), so the grouping ADR-NOC-003's KPI card
summarized as a count is immediately visible as rows, not one more click
away. `Pagination`, `sortingColumn`/`sortingDescending`, and a status
`Select` (not `PropertyFilter` — a single enum-valued filter does not need
`PropertyFilter`'s free-text property/operator grammar) all operate the same
way `EstatePage`/`EstateTable` already establish for filters and cursors,
just against the grouped, in-memory set instead of a server round-trip per
page: sorting and the 20-row `Pagination` page size apply to **top-level
rows only** (Cloudscape's own documented behavior for expandable tables — a
root's collateral moves with it, never split across a page boundary), and
the default sort is severity (critical → high → medium → low), then age
(oldest first) as the tie-break — the same fixed severity palette
ADR-NOC-002/003 already established, carried into `StatusIndicator` colors
for both severity and status columns.

### 3. Drill-down via the `AppLayout` `SplitPanel`, driven through a new `Shell`-owned context

The brief allowed `SplitPanel` or a detail route/`Modal`; `SplitPanel` was
chosen because the board stays visible and scannable while one incident's
detail is open — closer to how an operator actually triages a list. Cloudscape's
`SplitPanel` is a slot of the single `AppLayout` `Shell` owns (ADR-UI-001),
not a per-page component, so `Shell.tsx` now exposes
`ShellSplitPanelContext` (`openSplitPanel(header, content)` /
`closeSplitPanel()`) through `useOutletContext` — the same "one shell, many
screens" seam ADR-UI-001 established for navigation and theming, extended
here for the first screen that needs a split panel. A route change clears
the panel automatically (`Shell` resets it on `location.pathname` change), so
leaving `/incidents` never leaves a stale drill-down mounted for the next
screen; any future board (E7.3c's alerts/on-call boards) can reuse the same
context without Shell knowing anything incident-specific.

The panel's content, `components/IncidentDetail.tsx`'s `IncidentDetailPanel`,
fetches `GET /admin/incidents/{id}` and `GET /admin/incidents/{id}/timeline`
— the same two endpoints `getIncident`/`getIncidentTimeline` already serve,
reusing `AuditTimeline`'s established shape (a small, per-event list with
actor + relative/absolute time) rather than inventing a second timeline
renderer. A timeline fetch failure never blanks the detail already loaded —
the same "supplementary data must not fail the primary view" rule
`ArtifactDetail` already applies to its dependency/dependent lists.

## What this story explicitly does not do

- No new table, entity, migration, or write path. `root_incident_id` stays
  exclusively `internal/grouping`'s to set; this screen only ever reads it.
- No new endpoint. All three incident endpoints are read exactly as E5.1/E4.2
  defined them — verified field-for-field against `incidentDTO`/
  `incidentEventDTO` (`internal/httpapi/handlers_incidents.go`) before reuse.
- No `@cloudscape-design/collection-hooks` dependency — sorting is a plain
  comparator table + `useState`, matching `EstateTable`'s existing
  hand-rolled approach rather than introducing a second sorting mechanism.
- No `PropertyFilter` — a single status enum does not need it; a future
  multi-dimension filter (severity + assignee + asset) would be the trigger
  to revisit this.
- No server-side keyset chasing past the first page — see the Honest Bound
  above. A "Load more" affordance (`EstatePage`'s own pattern) was considered
  and rejected here specifically because it would let a root and its
  collateral land on different client-fetched pages, which is worse for
  grouping correctness than one bounded fetch with a visible cap.
- No OpenAPI change — no Go code was touched by this story at all.

## Consequences

**What is now guaranteed.** An operator can go from the NOC overview's
`grouped` summary to the actual incidents in two clicks, see root-cause and
collateral as a real tree instead of two independent counts, and drill into
any incident's detail and timeline without losing the list — proven by
`web/src/routes/IncidentBoardPage.test.tsx` (a root with nested collateral
plus a standalone incident rendering correctly, the default `status=open`
request bounded at the documented cap, detail+timeline opening in the split
panel on click, an explicit empty state with a way to widen the filter, and
an error banner with working retry) rather than by inspection.

**What is not claimed.** Grouping is exact only up to `INCIDENT_LIST_CAP`
incidents matching the active filter — see the Honest Bound above; this is a
documented, visible limit, not a silent one. There is no real-time update
(same polling-only posture as ADR-NOC-003; a manual filter change or page
reload is required to see new incidents — no auto-refresh was added for this
screen, unlike the overview's 15s poll, since an operator actively working a
board benefits less from rows reordering under them mid-triage). No bulk
action (acknowledge/assign/transition) exists from this screen — the
existing incident endpoints support them, but wiring board actions is out of
this story's scope.

## Evidence

- `web/src/incidents.ts` — the typed data module (`IncidentDTO`,
  `IncidentEventDTO`, `listIncidents`/`getIncident`/`getIncidentTimeline`),
  verified field-for-field against `handlers_incidents.go`'s
  `incidentDTO`/`incidentEventDTO`; `INCIDENT_LIST_CAP` and its doc comment.
- `web/src/incidentGrouping.ts` — `groupIncidents`, the pure client-side
  root/collateral builder this ADR's §1 describes, including the orphaned-
  collateral degrade case.
- `web/src/incidentPresentation.ts` — the shared severity/status
  `StatusIndicator` palette and rank, used by both the board and the detail
  panel (one vocabulary, not two).
- `web/src/routes/IncidentBoardPage.tsx` — the board itself (`Table` with
  `expandableRows`, status `Select`, sort, `Pagination`, the cap warning).
- `web/src/components/IncidentDetail.tsx` — the `SplitPanel` content
  (`IncidentDetailPanel`): detail fields plus timeline.
- `web/src/Shell.tsx` — `ShellSplitPanelContext`, the `AppLayout`
  `splitPanel`/`splitPanelOpen`/`onSplitPanelToggle` wiring, and the
  route-change reset.
- `web/src/App.tsx` — `/incidents` now routes to `IncidentBoardPage`, not
  `ComingSoon` (deleted — both nav placeholders it existed for, NOC and
  Incidents, are now live).
- `web/src/routes/IncidentBoardPage.test.tsx` — root+collateral+standalone
  grouping, the default `status=open`/`limit=100` request, split-panel
  detail+timeline on click, empty state with "Show all statuses", error
  banner with retry.
- `pnpm --dir web exec tsc -b --noEmit` && `pnpm --dir web exec vitest run` —
  clean; 50 tests (45 pre-existing + 5 new).
- `make web` — builds `webdist/` cleanly (~1,023 kB JS / ~292 kB gzip,
  ~1,106 kB CSS / ~232 kB gzip); `grep -o 'https\?://...'` across the built
  bundle returns only the same inert strings ADR-UI-001/ADR-NOC-003 already
  documented (XML/SVG namespace URIs, an embedded `data:` font, a React
  dev-mode error-decoder doc link never fetched, a CSS license comment) — no
  runtime CDN reference.
- `go build ./...` and `make test` (full suite, `-race`) — green, unaffected;
  this story touches no Go source.
- `go test ./internal/httpapi/... -run TestOpenAPIContract -v` — both
  contract tests pass unchanged (no `openapi.yaml` edit).

## Enforcement

- `web/src/routes/IncidentBoardPage.test.tsx` under `make web-test` — this
  ADR's own claims about grouping, the default filter/cap, drill-down, empty
  state, and error handling, checked on every build.
- `internal/httpapi`'s existing `incidentDTO`/`IncidentRepository` tests —
  unchanged; this story adds no Go surface for them to regress against.

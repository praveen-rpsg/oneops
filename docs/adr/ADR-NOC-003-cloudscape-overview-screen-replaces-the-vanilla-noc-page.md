# ADR-NOC-003 — The NOC overview lives in the Cloudscape console as a KPI-card `Grid`, polled on a fixed cadence; the vanilla `/noc` page is retired

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-NOC-001 (`GET /v1/admin/noc/overview` — the sole data source, unchanged), **supersedes ADR-NOC-002** (the vanilla `/noc` stopgap this story retires), ADR-UI-001 (Cloudscape console foundation this story builds its first real screen on), `docs/PLATFORM-BUILD-PLAN.md` E7-UI.1 |

## Context

ADR-UI-001 (E7-UI.0) built the shell — `AppLayout` + `TopNavigation` +
`SideNavigation` + router — with `NOC / Overview` already in the nav, routed
to a `ComingSoon` placeholder pending this story. ADR-NOC-002's own
Consequences section named its own retirement condition explicitly: "It does
not retire `/noc` — that happens in E7-UI.1 when a real Cloudscape NOC screen
exists to replace it." E7-UI.1 is that story.

The data contract does not change: `GET /v1/admin/noc/overview` (E7.1,
ADR-NOC-001) is read as-is by a new typed client module, `web/src/noc.ts`
(written in the stalled first pass of this story, verified field-for-field
against `internal/httpapi/handlers_noc.go`'s `nocOverviewDTO` before reuse —
no drift found). This ADR is about the render side only: what replaces
`nocweb/noc.html` inside the shell, and how it stays honest about staleness
and failure the way the vanilla page already was.

## Decision

### 1. A KPI-card `Grid`, not `@cloudscape-design/board-components`

Five `Container`+`Header` widgets (Incidents, Alerts firing, Asset health, On
call now, Escalations) laid out with Cloudscape's `Grid`
(`gridDefinition: [{colspan:{default:12,xs:6,m:4}}, ...]` — one responsive
definition shared by all five cells, 1 column on phones, 2 on tablets, 3 from
"medium" viewports up). `board-components`' `Board`/`BoardItem` (reserved by
ADR-UI-001 "for later dashboards") was considered and declined here: it adds
drag-to-reorder/resize persistence, a `localStorage`/`sessionStorage` layout
key, and a second data model (item ids, layout state) for a screen that has a
fixed, small, non-reorderable set of widgets with no per-user customization
requirement yet. A `Grid` of `Container`s is the same pattern
`ArtifactPage`/`EstatePage` already use for multi-panel layouts
(`ColumnLayout`, `KeyValuePairs` inside `Container`) — simpler, and one fewer
dependency to wire correctly under test. `Board` remains available,
unconsumed, for E9 (time-series dashboards), where reorderable widgets are an
actual product requirement.

### 2. Every count renders through `StatusIndicator`, never an ad-hoc color

A shared `Count` helper wraps every numeric field in Cloudscape's semantic
`StatusIndicator`: `type={count === 0 ? 'success' : <caller-supplied type>}`.
The caller-supplied type reproduces the exact fixed, small palette
ADR-NOC-002 established for the vanilla page it replaces — critical/high
severity and the open-incident total are `error`, warning/medium are
`warning`, info/low are `info` — so a zero count is always green ("healthy")
regardless of category, and a non-zero count keeps the same reading a NOC
operator already had. This is a deliberate carry-forward, not a redesign: the
color vocabulary does not change, only the component that renders it (a
themed, accessible `StatusIndicator` instead of hard-coded CSS classes
`crit`/`warn`/`ok`/`info` in `nocweb/noc.html`), so it now inherits dark/light
mode automatically instead of needing its own palette per mode.

### 3. Polling, not streaming — same cadence, same non-overlap guarantee

`NOCOverviewPage` polls on a 15-second cadence, matching ADR-NOC-002's own
interval and `docs/PLATFORM-BUILD-PLAN.md` E7's header ("Polling v1;
real-time streaming = E11, D2 open"). The scheduling is a self-rescheduling
`runCycle` (fetch, then `setTimeout(runCycle, 15_000)` only after the fetch
settles) rather than `setInterval`, for the same reason ADR-NOC-002 gave: a
slow response cannot pile up overlapping in-flight requests. A manual
"Refresh" button clears the pending timer and re-enters the same `runCycle`,
so a manual refresh and the next scheduled tick can never race each other.
This story does not open D2 (real-time transport stays deferred to E11).

### 4. Failure and staleness are visible, reusing the console's existing `ErrorState`

A fetch failure renders through the console's existing
`components/States.tsx` `ErrorState` (the same `Alert`-in-`role="alert"`
wrapper `EstatePage`/`ArtifactDetail` already use), carrying the server's RFC
7807 `problem.title`/`detail` verbatim — `ApiError`'s 401/403 messages read
as "not authenticated," 5xx as a retryable server error, exactly as
ADR-NOC-002 described for the vanilla page, but now through one shared
component instead of a bespoke banner. "Last updated {generated_at}" sits in
the page header next to the Refresh button, so a viewer always knows how
stale the visible data might be — the same honesty ADR-NOC-001 and
ADR-NOC-002 both state about the projection: a snapshot, not a live feed.

### 5. Empty states are explicit, not blank

Every widget was written against an all-zero fixture, not just a populated
one: `AssetsWidget` renders "All clear" (a `StatusIndicator` success) instead
of four zeroed rows; `OnCallWidget` renders "No active on-call schedules."
instead of an empty `KeyValuePairs`; every `Count` at zero is green rather
than an unstyled `0`. None of this is new policy — it mirrors ADR-NOC-002's
own "an all-zero section renders 'all clear'" / "an empty list renders 'no
active on-call schedules'" — carried forward, not reinvented.

### 6. The vanilla `/noc` page is deleted, not deprecated in place

`internal/httpapi/noc_page.go`, `internal/httpapi/nocweb/noc.html`,
`internal/httpapi/noc_page_test.go` are removed; the `r.Get("/noc",
serveNOCPage)` registration in `internal/httpapi/server.go` is removed;
`contract_test.go`'s scope comment no longer lists `/noc` among the
non-`/v1` operational surfaces, and instead notes this story retired it. No
replacement Go route is added — `/noc` now 404s; the equivalent screen lives
at the console's `/noc` client-side route (a different origin path
under the same server, served by the SPA's catch-all, not a new Go handler).

## What this story explicitly does not do

- No new table, entity, migration, or endpoint. `GET /v1/admin/noc/overview`
  is read exactly as ADR-NOC-001 defined it; `web/src/noc.ts`'s types are
  verified against `nocOverviewDTO`, not changed.
- No drill-down. This is the overview board only — per-domain boards
  (incident list/detail, alerts, on-call) are E7-UI.2 and E7.3c.
- No streaming layer (D2 stays open, deferred to E11).
- No `board-components` adoption — reserved, still unused, for E9.
- No OpenAPI change — `make contract-breaking` against `master` reports no
  breaking diff, because none of `internal/httpapi/openapi.yaml` changed.

## Consequences

**What is now guaranteed.** The NOC overview lives inside the same shell,
router, design system, and theming seam as every other console screen
(ADR-UI-001's own stated goal for exactly this transition), reachable at
`/noc` under `SideNavigation`'s existing `NOC / Overview` entry. Every
`nocOverviewDTO` field the vanilla page rendered is still rendered here,
proven by `web/src/routes/NOCOverviewPage.test.tsx` (data rendering, all-zero
empty states, error banner + retry, manual refresh without a duplicated poll
timer) rather than by inspection. The vanilla page's second auth mechanism
(the `sessionStorage` token-paste fallback ADR-NOC-002 accepted as a
deviation) is gone entirely — the Cloudscape screen inherits the console's
one OIDC session, so there is now exactly one browser-side auth posture in
this codebase, not two.

**What is not claimed.** This is still not a live feed — up to 15 seconds
stale between polls, identical to ADR-NOC-001/002's own honesty about the
projection. `Board`/drag-reorder is not offered; a fixed five-widget layout
is the only one that exists. No topology/CMDB visual (E7.3b), no telemetry
summary (ADR-NOC-001's own deferral, unchanged), no per-domain drill-down
(E7-UI.2/E7.3c).

## Evidence

- `web/src/routes/NOCOverviewPage.tsx` — the screen itself (widgets, polling,
  refresh, error/empty states).
- `web/src/noc.ts` — the typed data module, verified field-for-field against
  `internal/httpapi/handlers_noc.go`'s `nocOverviewDTO`.
- `web/src/routes/NOCOverviewPage.test.tsx` — renders every widget from a
  mocked response, renders every widget cleanly at zero, shows an error
  banner with a working retry, and refetches on manual refresh without
  duplicating the poll timer.
- `web/src/App.tsx` — `/noc` now routes to `NOCOverviewPage`, not
  `ComingSoon`.
- `pnpm --dir web exec tsc -b --noEmit` && `pnpm --dir web exec vitest run` —
  clean; 45 tests (41 pre-existing + 4 new).
- `make web` — builds `webdist/` cleanly (~970 kB JS / ~279 kB gzip, ~1,069 kB
  CSS / ~228 kB gzip — materially unchanged from ADR-UI-001's own baseline);
  `grep -o 'https\?://...'` across the built bundle returns only the same
  inert strings ADR-UI-001 already documented (XML/SVG namespace URIs, an
  embedded `data:` font, a React dev-mode error-decoder doc link never
  fetched) — no runtime CDN reference.
- Deleted: `internal/httpapi/noc_page.go`, `internal/httpapi/nocweb/noc.html`,
  `internal/httpapi/noc_page_test.go`; `internal/httpapi/server.go`'s `/noc`
  route registration removed; `internal/httpapi/contract_test.go`'s scope
  comment updated.
- `go build ./...` and `make test` (full suite, `-race`) — green after the
  deletion, including `internal/httpapi` and `internal/arch`.
- `go test ./internal/httpapi/... -run TestOpenAPIContract -v` —
  `TestOpenAPIContract_CoversEveryServedRoute` and
  `..._PromisesNothingItDoesNotServe` both pass without `/noc`.
- `make contract-breaking BASE_REF=master` — no breaking diff (this story
  makes no OpenAPI change).

## Enforcement

- `web/src/routes/NOCOverviewPage.test.tsx` under `make web-test` — this
  ADR's own claims about widget rendering, empty states, error handling, and
  non-overlapping poll/refresh, checked on every build.
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — unchanged in intent, now passing
  without `/noc`, confirming its removal introduced no `/v1` drift.
- `internal/kg/extract/schema.TestCorpusCensus` — unchanged; this story adds
  no domain entity for the census to see.

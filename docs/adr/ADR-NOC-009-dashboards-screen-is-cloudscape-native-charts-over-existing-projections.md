# ADR-NOC-009 — Dashboards is a Cloudscape-native-chart rendering layer over two existing, unchanged projections; no new Go endpoint, no reified `Dashboard`

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-NOC-008 (E9.1 incident-trends — the projection this screen's primary chart consumes, and the source of its `opened_by_source.alert` honest bound), ADR-UI-001 (Cloudscape + Vite console foundation, the `Shell`/router/theming this screen mounts into), ADR-NOC-004/006/007 (the precedent this story follows: a Cloudscape screen over an EXISTING, unchanged endpoint, no server round-trip added), `docs/PLATFORM-BUILD-PLAN.md` §4 (reduced-concept discipline — `Dashboard`/`Report` are ratified false-nouns; this ADR's title says "screen", never "entity") |

## Context

E9 was split data-then-viz (2026-08-06, recorded ahead of ADR-NOC-008): E9.1
built the one new read this screen needed (`GET
/admin/dashboards/incident-trends`); E9.2, this story, is the render-only
half. Two existing, already-shipped endpoints are available and unchanged by
this story: the trends projection itself, and `GET /admin/telemetry` (E2.1b,
`resolution=rollup_5m`) — the platform's only other genuinely
time-series-shaped read. `@cloudscape-design/components` already ships
`LineChart`/`BarChart`/`AreaChart`/`MixedLineBarChart`
(`docs/PLATFORM-BUILD-PLAN.md` E9's own framing), so the only decisions left
are: which chart type per series, how the time-range control drives
`from`/`to`/`bucket`, how the alert-volume proxy is presented without letting
a reader mistake it for a firing log, and how a chart with genuinely no data
(telemetry, on a fresh tenant) renders without either crashing or lying about
having none.

## Decision

### 1. `MixedLineBarChart` for incident volume, a plain `BarChart` for the alert-source proxy, `LineChart` for telemetry — no `AreaChart`, no new dependency

The primary chart (`IncidentVolumeChart`, `web/src/routes/DashboardsPage.tsx`)
stacks four bar series — one per severity, colored via a new
`SEVERITY_CHART_COLOR` palette in `web/src/incidentPresentation.ts` that
carries forward the same red/red-amber/blue reading `SEVERITY_TYPE` already
establishes for every `StatusIndicator` in this console — plus one `Resolved`
line series overlaid on the same categorical x-axis. `MixedLineBarChart` is
the only Cloudscape chart that mixes bar and line series in one plot; a
second, separate `BarChart` for `opened_by_source` (manual vs the
alert-sourced-incident proxy) keeps that honest, secondary breakdown out of
the primary chart's legend rather than cramming a sixth series onto it.
Telemetry (`TelemetryChart`) is a plain `LineChart` — one continuous series,
no stacking, no categorical bucketing, because raw `rollup_5m` timestamps are
genuinely continuous, unlike the incident chart's discrete buckets.
`AreaChart` was considered and not used: nothing here needs a filled area's
specific reading (cumulative/volume-under-curve), and introducing a third
chart *shape* for four chart instances would be needless variety. All three
components are already bundled in `@cloudscape-design/components` (verified:
`node_modules/@cloudscape-design/components/{line,bar,mixed-line-bar}-chart`
exist pre-install) — `make web`'s existing no-runtime-URL grep (ADR-UI-001 §7)
re-verified clean after this story, so the self-contained-bundle bar holds.

### 2. `xScaleType="categorical"` for the two incident charts, `"time"` for telemetry — `x` is a formatted label, never the raw ISO string

Cloudscape's own component-level guidance (`bar-chart/interfaces.d.ts`: "Use
`categorical` for bar charts") drives this split. Each incident-trends bucket
becomes a formatted label (`bucketLabel`: `"Aug 06, 14:00"` for an hour
bucket, `"Aug 06"` for a day bucket) computed once per point and shared
identically across every series drawn against it, so bars/line stay aligned
on the same discrete axis. Telemetry keeps `x` as a real `Date` on a `"time"`
scale — it is a genuinely continuous series (5-minute rollup buckets over a
rolling 24h window), and collapsing it to a categorical axis would lose the
even-spacing Cloudscape's time scale renders for free.

### 3. Time-range control: a `SegmentedControl`, not a `Select` — three fixed windows, no custom date picker

`SegmentedControl` (`{id: '24h'|'7d'|'30d', text}`) sits in the primary
chart's `Header` `actions` slot. Each option is a `{days, bucket}` pair
(`windowFor`): 24h → 1 day, hourly buckets (24 buckets); 7d/30d → 7/30 days,
daily buckets (7/30 buckets) — all three are far under
`domain.MaxIncidentTrendBuckets` (744, a month of hourly buckets), so none of
them can ever be refused by E9.1's own cap. `from`/`to` are computed fresh
against `new Date()` on every poll tick (not memoized against a fixed
instant), so the window is a rolling one — "last 24h" means the 24 hours
ending now, each time the poll fires, the same property every other
NOC-projection screen in this console already has (ADR-NOC-001 §1: "calling
it twice a millisecond apart can return two different answers"). A custom
date-range picker (`DateRangePicker`) was considered and rejected as
over-scoped for a v1 dashboards screen with exactly one bounded, well-known
set of useful windows — the CTO-locked design brief names exactly these
three, and `docs/PLATFORM-BUILD-PLAN.md`'s "reduced concept" discipline
argues against building generality nothing has asked for yet.

### 4. The alert-volume proxy is a separate, honestly-labeled chart, not a silently-merged series

`AlertSourceChart`'s `Container` header reads "An incident-source proxy — NOT
a firing log (ADR-NOC-008 §5)" and its second series is titled "Alert-sourced
(proxy)", not "Alerts" or "Firing" — repeating, at the UI layer, the same
caution ADR-NOC-008 §5 states three times at the data layer. A reader
glancing at chart titles alone (never opening a tooltip or reading this ADR)
still sees the word "proxy" before any number. This is the "you MAY add a
second chart" option from the E9.2 brief, chosen over folding it into the
primary chart's legend specifically so the honest label has its own visible
home rather than being one line of six in a denser legend.

### 5. Telemetry's asset+metric picker is a `Select` (populated from the existing asset graph) + free-text `Input`, not a new endpoint

There is no "list distinct metric names for an asset" endpoint, and adding
one is out of scope for a rendering-layer story (the brief's own "NO Go
touched" constraint). The asset picker reuses `getAssetGraph` (E7.3b-1,
already fetched for the topology screen) purely for its `{asset_id, name}`
pairs — a second, independent consumer of that endpoint, proving it is a
generic bounded projection rather than a topology-only concern. The metric
`Input` free-types a snake_case name (`domain.Sample.Validate`'s own pattern);
its **default value, `cpu_utilization`, is lifted verbatim from
`domain.Sample.Validate`'s own validation-error example string** ("must be
lower-case snake_case, e.g. cpu_utilization, disk_free_bytes") — a real,
already-documented example rather than an invented placeholder. Committing a
typed value requires an explicit `Enter` or the `Load` button, not
onChange-per-keystroke, so a caller typing a longer metric name does not fire
a request per character.

### 6. Telemetry's `empty` no-data state — not an error, not a synthetic zero series

`TelemetryChart` passes `series={[]}` (rather than one series with an empty
`data` array) whenever `items.length === 0`, which is what makes Cloudscape's
own `empty` slot render — a `Box` reading "No telemetry samples" / "No
rollup_5m data for this asset and metric in the last 24h.". This is the
expected first-run state on a fresh dev database (no telemetry ever
ingested): the screen must render this cleanly, per the brief's explicit
"do NOT error" requirement, and does — proven directly
(`DashboardsPage.test.tsx`'s "renders a clean no-data state for empty
telemetry, never a crash"). By contrast, the incident-trends chart can never
hit this path: ADR-NOC-008 §4 guarantees a full, contiguous, zero-filled
`points` array for any valid window, even on a brand-new empty tenant — so
its "no incidents" reading is an all-zero bar chart, not Cloudscape's `empty`
slot, and the `IncidentVolumeChart`'s own `empty` prop is unreachable in
practice but supplied anyway as a defensive fallback (mirrors
`buildIncidentTrendsResponse`'s own "drop rather than panic" discipline for a
row that lands outside the generated shape).

### 7. Poll + manual refresh, mirroring `NOCOverviewPage`'s exact cadence and cycle shape — two independent cycles, one shared `Refresh` button

Both the trends fetch and the telemetry fetch run their own
`runCycle`/`timerRef`/`abortRef` self-rescheduling loop at
`POLL_INTERVAL_MS = 15_000`, the identical shape `NOCOverviewPage.tsx`
established (ADR-NOC-002's own cadence, carried forward — v1 is polling, not
streaming, D2 stays open). They are two independent cycles, not one, because
they are keyed on different, independently-changing inputs
(`rangeId` for trends; `selectedAssetId`/`metric` for telemetry) — coupling
them into one cycle would force a telemetry refetch on every time-range
change and vice versa, for no benefit. The page-level `Refresh` button clears
and immediately restarts both cycles, the same "manual refresh reuses the
same cycle so it never runs concurrently with the timer" property
`NOCOverviewPage` already proves.

## Alternatives considered

- **A `board-components` (`@cloudscape-design/board-components`) reorderable
  widget grid**, as ADR-NOC-003/E7-UI.1 explicitly reserved it for E9. Not
  used here either: this screen's three cards are fixed and few enough that
  a plain `Grid` (the same primitive `NOCOverviewPage` uses) is simpler, and
  reorderable/resizable widgets are a feature nothing in the E9.2 brief asks
  for. `board-components` remains reserved, unused, for a future screen that
  actually needs user-configurable widget layout.
- **One combined chart for severity AND source breakdown.** Rejected (§4):
  both are breakdowns of the SAME `opened_total`, and stacking both
  dimensions on one chart would either double-count or force an artificial
  choice of which dimension "wins" the stack — two charts read cleanly,
  one would not.
- **A live metric-name-suggestion endpoint** (distinct metrics an asset has
  ever reported). Deferred: genuinely useful, but a new Go read this
  rendering-layer story's own scope excludes; the free-text `Input` with a
  real, documented example value is judged sufficient for v1.

## Consequences

**What is now guaranteed.** `/dashboards` renders the incident-volume chart
from E9.1's own contiguous, zero-filled series — proven against a mocked
response asserting the full severity+resolved legend renders
(`DashboardsPage.test.tsx`). The time-range control refetches with the
correct `bucket` (`hour` for 24h, `day` for 7d/30d) — proven by asserting the
literal query string of the fetch call before and after switching segments.
Telemetry renders Cloudscape's own `empty` state on zero samples, and a real
line series when samples exist — both paths proven directly, neither ever
throws. A trends-fetch failure renders the console's existing `ErrorState`
banner with a working retry, the same contract every other screen in this
package already has. No Go file, migration, or `openapi.yaml` entry changed
— `go build ./...`, `make test`, and contract bijection are all unaffected by
construction (nothing new to break).

**What is not claimed.** This is not a customizable/reorderable dashboard
(`board-components` stays unused, reserved) and not a general query builder —
three fixed time windows, one telemetry asset+metric at a time. The
alert-sourced series is, again, a proxy — ADR-NOC-008 §5's bound is repeated,
not re-litigated, at the UI layer. `make web`'s bundle grew by roughly 126 kB
raw / 44 kB gzip JS (previously-unused Cloudscape chart modules now imported)
— an accepted, expected cost of adding native charts, not a regression to
chase down.

## Enforcement

- `web/src/routes/DashboardsPage.test.tsx` (5 tests): incident-chart legend
  renders every severity + Resolved from a mocked trends response; the
  time-range `SegmentedControl` refetches with `bucket=day` on a wider
  window; telemetry renders Cloudscape's `empty` slot on zero items and a
  real line series when samples exist; a trends failure renders the error
  banner with a working retry.
- `make web-test` (`tsc -b --noEmit` + `vitest run`) — 13 files / 89 tests
  green (84 pre-existing + 5 new).
- `make web`'s existing no-runtime-URL grep (ADR-UI-001 §7) — re-run clean
  after this story; the only matches are a CSS license comment
  (`fonts.google.com`) and a bundled library's own error-message string
  (`github.com`/`reactjs.org`), neither a runtime fetch.
- `go build ./...` / `make test` — unaffected; no Go file in this story's
  diff.

# ADR-NOC-002 — The live NOC screen is a self-contained vanilla-HTML page polling the projection endpoint, not a React-console route

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-NOC-001 (`GET /v1/admin/noc/overview` — the sole data source this screen reads), `docs/PLATFORM-BUILD-PLAN.md` E7.2 / D2 (real-time transport, deferred to E11), `web/src/auth.ts` (the console's OIDC PKCE + in-memory-token session, the pattern this story could not literally reuse and why) |

## Context

`docs/PLATFORM-BUILD-PLAN.md` E7.2 asks for the visible payoff of E7.1: a
human opens a URL and *sees* the NOC's current operational state, refreshing
on its own. E7.1 built the data (`nocOverviewDTO`); nothing renders it yet.

Two render surfaces already exist in this codebase, and neither fits without
a real cost:

- **The React console** (`web/`, embedded via `internal/httpapi/web.go`'s
  `go:embed all:webdist`) is a Vite-built SPA with its own build pipeline
  (`pnpm install && pnpm build`), its own router-less single screen (the
  console's own comment: "the console is a single screen today"), and its
  own OIDC Authorization-Code-with-PKCE session held **in a JS module
  variable, never localStorage** (`web/src/auth.ts`'s own doc comment: "so it
  cannot be read by injected script or survive a tab close"). Adding a NOC
  route inside it means either standing up client-side routing this SPA does
  not have yet (a scope increase this story does not need) or growing the
  single screen into a second concern with a second polling loop, a second
  set of components, and a second `pnpm build` dependency for a feature that
  is one `fetch` and five cards.
- **The Redoc `/docs` page** (`internal/httpapi/openapi.go`) is a static
  `serveDocs` handler with an inline HTML constant — the closest existing
  precedent for "a small standalone HTML surface served from this origin,"
  but it pulls its renderer from a CDN (`cdn.redoc.ly`), which this story's
  hard constraint (no external network assets) rules out as a pattern to
  copy directly.

## Decision

### 1. A second, independent `go:embed`, served at `GET /noc`

`internal/httpapi/nocweb/noc.html` is one self-contained file: inline
`<style>`, inline `<script>`, no `<link>`/`<script src>` to anything outside
itself. `internal/httpapi/noc_page.go` embeds it
(`//go:embed nocweb/noc.html`) and serves it unconditionally at `GET /noc`
(`internal/httpapi/server.go`) — unlike `r.Get("/", ...)`, this route does
**not** fall back to `s.serveRoot`'s JSON descriptor when `webdist/` is
unbuilt, because it has no dependency on `webdist/` at all. `webdist/` and
`web/` are untouched by this story.

This keeps the reduced-concept discipline `docs/PLATFORM-BUILD-PLAN.md` §4
already applies to the data side (ADR-NOC-001: no `Dashboard`/`Report`
entity) applied to the render side too: no new persisted state, no build
tool, no framework — a plain document that asks the existing endpoint a
question every 15 seconds and paints the answer.

`contract_test.go`'s existing scope comment ("Scope is /v1 only. /healthz,
/readyz, /metrics, /openapi.yaml, /docs, / ... are operational or discovery
surfaces, deliberately outside the versioned API contract") now names `/noc`
alongside them. `routesFromRouter` only walks routes under the `/v1/` prefix,
so `/noc` was never going to appear there — `TestServeNOCPage_IsNotUnderV1`
makes that a checked property rather than an assumption, and separately pins
that `GET /v1/admin/noc/overview` (the route this page's own JavaScript
calls) stays present on the `/v1` surface it belongs to.

### 2. Auth in the browser: reuse the server's existing dev/production split; add a minimal, clearly-scoped fallback for the case the console's own mechanism cannot transfer

The console's session (an in-memory bearer token, deliberately never
persisted) exists only inside that SPA's own JavaScript runtime. `/noc` is a
**separate page load** — a fresh browsing context with no access to another
origin-scoped page's in-memory variables, tab or no tab. There is no cookie
session anywhere in this codebase to fall back on either: `internal/httpapi`
authenticates exclusively via `Authorization: Bearer <token>`
(`s.authenticate`, `internal/httpapi/middleware.go`) or, in local
development, not at all.

- **When `ONEOPS_AUTH_ENABLED=false`** (the codebase's own documented local
  development posture — `s.authenticate`'s own comment: "config validation
  hard-fails when it is set in production"), every request synthesises
  `oneops-platform-admin` claims, which satisfy `PermAdmin`. `/noc` needs
  nothing extra here: `fetch('/v1/admin/noc/overview')` with no
  `Authorization` header at all just works, identically to every other admin
  route in dev mode today.
- **When auth is enabled**, `/noc` cannot inherit the console's session (see
  above), so it exposes the smallest possible fallback: a password-style
  input, "Save," writes the pasted token to **`sessionStorage`** (not
  `localStorage`) under `oneops.noc.token`, and every subsequent `fetch` adds
  `Authorization: Bearer <token>` when one is present. This is a real,
  acknowledged deviation from the console's "never touch persistent browser
  storage" posture — the trade-off documented here rather than silently
  taken: `sessionStorage` is still readable by any script that can execute
  in the page (the same XSS exposure `web/src/auth.ts` explicitly designed
  around), but it is cleared when the tab closes, unlike `localStorage`,
  which is why this story uses the narrower of the two the CTO brief
  suggested rather than the one named literally. No new auth scheme is
  invented — the token is verified server-side by the same
  `internal/auth.Verifier` every other bearer-token request goes through;
  this page only carries it.
- **A `401`/`403` response** renders as "not authenticated" in the error
  banner rather than a generic failure, so an operator who has not pasted a
  token knows exactly what to do next; a `5xx` renders as a retryable
  server error and the page's own 15-second poll naturally retries it.

### 3. Polling, not streaming (D2 stays open, deliberately)

The page calls `GET /v1/admin/noc/overview` once on load and once every 15
seconds via `setTimeout`-scheduled re-fetch (not `setInterval`, so a slow
response cannot pile up overlapping in-flight requests). `docs/
PLATFORM-BUILD-PLAN.md`'s own E7 header already states this: "Polling v1;
real-time streaming = E11 (deferred, D2 open)." This story does not open D2.
A client wanting fresher data refreshes manually (`#refresh-btn`) or waits
for the next tick; nothing here holds a persistent connection, and the
server gains no new stateful resource per open browser tab.

### 4. What the page renders, traced to `nocOverviewDTO`'s own fields

Every element id below is asserted present by `TestServeNOCPage_
ServesTheEmbeddedScreen` (`internal/httpapi/noc_page_test.go`), so a future
rename of any field on either side (the DTO or the page's own JS) is caught
by a build-failing test rather than a silently blank card:

- **Incidents** — `open_total`, `by_status` (open/acknowledged/
  investigating), `by_severity` (critical/high/medium/low), and `grouped`
  (E4.2's root/collateral summary).
- **Alerts** — `firing_total`, `by_severity` (critical/warning/info).
- **Assets** — the four `AssetHealthReport` category counts (stale,
  orphaned assets, orphaned business services, incomplete); an all-zero
  section renders "all clear."
- **On-call** — one row per `on_call` entry; `display_name` present renders
  the name, an empty roster renders "unassigned," an empty list renders "no
  active on-call schedules" — never a blank card indistinguishable from a
  failed fetch.
- **Escalations** — `active_total`.
- Severity/status color coding: red (`crit`) for critical/high severity and
  the open-incident count, amber (`warn`) for warning/medium and any
  non-zero asset-health category, green (`ok`) for zeroed/healthy states,
  blue (`info`) for informational/low tiers — a fixed, small palette rather
  than a per-value color function that could silently omit a case.

### 5. What this story explicitly does not do

- No new table, entity, migration, or store method. The page issues exactly
  one kind of request (`GET /v1/admin/noc/overview`) and persists nothing
  server-side.
- No change to `webdist/`, `web/`, or any existing handler's behavior —
  `git diff` against `master` for this story touches only
  `internal/httpapi/noc_page.go`, `internal/httpapi/nocweb/noc.html`,
  `internal/httpapi/noc_page_test.go`, `internal/httpapi/server.go` (the one
  new route registration), and `internal/httpapi/contract_test.go` (a
  documentation-only comment update).
- No streaming layer, no WebSocket/SSE — that is E11, D2, and stays
  deferred.
- No drill-down: this is the E7.2 overview board only. Per-domain boards
  (incidents/alerts/on-call lists with detail) are E7.3, which overlaps E9.

## Consequences

**What is now guaranteed.** `GET /noc` returns `200 text/html` unconditionally
(no dependency on `webdist/` having been built), the page is legible and
functions with zero configuration in the codebase's own dev-mode posture
(`ONEOPS_AUTH_ENABLED=false`), and every card is traced to a specific
`nocOverviewDTO` field, proven by a serving test that fails the build if the
embedded asset's structural anchors ever drift from what the JavaScript
expects. `/noc` cannot appear as an undocumented `/v1` operation, and cannot
cause `TestOpenAPIContract_PromisesNothingItDoesNotServe` to demand an
OpenAPI entry it was never meant to have — both are now checked, not merely
argued.

**What is not claimed.** This is not a live feed: a viewer sees whatever was
true at the last poll, up to 15 seconds stale, identical to the honesty
`ADR-NOC-001` already states about its own endpoint. The `sessionStorage`
token fallback is a real (if narrower-than-`localStorage`) departure from the
console's stricter in-memory-only posture, accepted here only because a
separate static page has no other way to carry a credential across its own
page load — this is not a general precedent for any other page in this
codebase to persist a token to browser storage. No drill-down, no
topology/CMDB visual (E7.3), no telemetry summary (ADR-NOC-001's own
deferral, unchanged).

## Evidence

- `internal/httpapi/nocweb/noc.html` — the page itself.
- `internal/httpapi/noc_page.go` — the embed and the `GET /noc` handler.
- `internal/httpapi/server.go` — the one new route registration, placed
  beside the console's own `r.Get("/", ...)` block.
- `internal/httpapi/noc_page_test.go` —
  `TestServeNOCPage_ServesTheEmbeddedScreen` (200, `text/html`, every
  structural anchor id present, no external network reference in the body)
  and `TestServeNOCPage_IsNotUnderV1` (pins `/noc` off the `/v1` surface and
  `/v1/admin/noc/overview` on it).
- `internal/httpapi/contract_test.go` — scope comment names `/noc`
  alongside `/`, `/docs`, etc.
- `make contract-breaking` against `origin/master`: no diff to
  `openapi.yaml`, so no breaking change — this story adds no API surface.

## Enforcement

- `httpapi.TestServeNOCPage_ServesTheEmbeddedScreen` /
  `..._IsNotUnderV1` — this ADR's own claims, checked on every build.
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — unchanged by this story; still pass,
  confirming `/noc` introduced no `/v1` drift.
- `internal/kg/extract/schema.TestCorpusCensus` — unchanged; this story adds
  no domain entity for the census to see.

# ADR-UI-001 — AWS Cloudscape on the existing React + Vite console, with a real router and a shared `AppLayout` shell

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO (design system + Next.js decision: founder, 2026-08-06) |
| **Related** | `docs/PLATFORM-BUILD-PLAN.md` E7 "⭑ UI DESIGN-SYSTEM DECISION" block and E7-UI.0/E7-UI.1/E7-UI.2 (this ADR implements E7-UI.0), ADR-NOC-002 (the vanilla `/noc` stopgap this foundation exists to retire), `web/src/api.ts` + `web/src/auth.ts` + `web/src/governance.ts` + `web/src/audit.ts` (the contracts this story reuses unchanged) |

## Context

The console (`web/`) was one query-param-routed screen: an estate list, an
artifact detail view opened via `?artifact=`, filters in `?role=&lifecycle=
&authority=&q=`, a Ratify flow, an audit timeline, and OIDC sign-in — all
hand-rolled HTML/CSS (`web/src/styles.css`), built by Vite into
`internal/httpapi/webdist/` and served same-origin via `//go:embed
all:webdist` (`internal/httpapi/web.go`). ADR-NOC-002 deliberately kept the
first NOC screen (`GET /noc`) *outside* this console — a standalone
`nocweb/noc.html` — because the console "does not have client-side routing
yet" and growing a single screen into a second concern for one board was not
worth it at the time.

That trade-off runs out now. `docs/PLATFORM-BUILD-PLAN.md` E7-UI.1
(Cloudscape NOC board) and E7-UI.2 (incident board) are the next two stories,
and E8/E9/E14 all eventually need a console to live in. Continuing to bolt
each new screen on as another standalone static page multiplies the
NOC-page's own accepted deviations (a second auth mechanism, a second
`sessionStorage` token fallback, no shared navigation) once per screen instead
of once. The founder separately judged the hand-rolled CSS of E7.2/E7.3a as
dated and directed a modern, responsive, industry-standard design system
rather than another bespoke stylesheet.

## Decision

### 1. AWS Cloudscape, not a hand-rolled system or a generic component library

`@cloudscape-design/components` + `@cloudscape-design/global-styles` (theming
primitives: `applyMode`/`Mode`) + `@cloudscape-design/board-components`
(reserved for E9/E7.3c dashboard widgets; unused by this story). Cloudscape is
the open-source design system behind the AWS Console: React, Apache-2.0,
WCAG 2.1 AA, dark mode built in, and — the deciding factor over a generic
library (MUI, Chakra, Ant) — it ships the specific vocabulary an ops console
needs out of the box: `AppLayout` (nav shell), `Table` with `PropertyFilter`/
sorting/pagination, `SplitPanel`, `StatusIndicator`, `KeyValuePairs`,
`Board`/`BoardItem`. Building those primitives by hand, or bending a
general-purpose library's `Card`/`Grid` toward them, is exactly the kind of
one-off construction this story exists to stop repeating. "Designer-approved
without a designer on staff" is a real constraint here — Cloudscape is a
battle-tested, publicly-documented system rather than this codebase's own
taste. `global-styles`' `applyTheme`/`Theme` API is the intended seam if
OneOps brand theming is wanted later; nothing here uses it yet.

### 2. React + Vite stays; Next.js was considered and declined

The founder explicitly considered Next.js for this migration and declined it
(2026-08-06): OneOps ships as a **single Go binary** — `internal/httpapi`
embeds the built SPA via `//go:embed all:webdist` and serves it same-origin,
with zero external runtime dependency (`internal/httpapi/server.go`'s comment:
same-origin by construction, no CORS layer). Next.js's value proposition is
server-side rendering and a Node server process; Cloudscape's components are
client-rendered React with no server data-fetching story that SSR would help,
and introducing a Node runtime into the deploy would break the one-artifact,
one-process posture the whole control plane is built around for no offsetting
benefit. Vite already builds exactly what this architecture needs — a static
bundle — so the stack stays **React 18 + TypeScript 5 + Vite 5**, unchanged.

### 3. `react-router-dom` v6, not v7 and not a state-machine router

v6 is the last major version whose API is a straightforward `<Routes>`/
`<Route>`/`useNavigate`/`useSearchParams` tree with no required data-loader
migration; v7 changes the recommended project shape (loaders/actions) for
benefits (streaming SSR, deferred data) this SSR-less, embed-only console does
not use. TanStack Router was the other option on the table
(`docs/PLATFORM-BUILD-PLAN.md` E7-UI.0's own framing: "react-router or
TanStack") — react-router-dom wins on being the de facto standard with the
larger Cloudscape-adjacent ecosystem precedent, and because this story's
routing needs (three top-level routes, one dynamic segment, URL-held filter
state) do not need TanStack's type-safe-search-params machinery to justify
its steeper adoption cost.

### 4. One shared `Shell` (`web/src/Shell.tsx`): `AppLayout` + `TopNavigation` + `SideNavigation` + `BreadcrumbGroup`, routed content in the `content` slot

`TopNavigation` (rendered outside `AppLayout`, referenced back in via
`headerSelector="#top-nav"` — Cloudscape's documented "full page app layout"
pattern) carries product identity ("OneOps"), a dark/light toggle, and the
signed-in identity + sign-out, reading `getSubject()`/`signOut()` from the
**unchanged** `web/src/auth.ts`. `SideNavigation` has three entries today:
**Estate/Governance** (the migrated screen, `/`), **NOC / Overview** (`/noc`)
and **Incidents** (`/incidents`) — both placeholders rendering a `ComingSoon`
`Container` until E7-UI.1/E7-UI.2 land, so the nav structure for the whole
epic exists now rather than being retrofitted per screen.
`react-router-dom`'s `<Outlet/>` is the `content` slot, so `Shell` is a layout
route every future section mounts under, not a per-screen copy-paste.

Dark mode **follows the OS by default** (`window.matchMedia
('(prefers-color-scheme: dark)')`, re-checked on system change) and an
explicit user toggle **persists in `sessionStorage`** for the tab's lifetime
only (`web/src/theme.ts`) — deliberately the same storage tier `auth.ts`
already uses for PKCE state, not `localStorage`, keeping one storage posture
across the console rather than introducing a second one for a cosmetic
preference.

### 5. Routing: `/` (estate, filters in the query string) and `/artifacts/:id` (path segment, not `?artifact=`)

The story brief allowed either; a path segment was chosen because it is the
more conventional shareable-deep-link shape for a resource and keeps the
list's filter query string free of an unrelated concern. "Back to estate"
does **not** rely on browser history depth (a user can deep-link straight
into `/artifacts/:id` with no prior list visit in the session, which the
existing test suite exercises directly via `window.history.replaceState`).
Instead, `EstatePage` passes the list URL it navigated from as router
`state: { from }`, and `ArtifactPage` threads that same `from` forward through
every subsequent related-artifact navigation (not just the immediately
preceding artifact) so "Back to estate" always lands on the original list
view — reproducing the single-level-back behavior the console has always had,
without depending on the number of `history` entries a session happens to
have. Filter state (`role`/`lifecycle`/`authority`/`q`) stays exactly where it
was: written to the URL query string via `history.replaceState` (unchanged
mechanism, now living in `web/src/routes/EstatePage.tsx`), so a filtered view
is still one URL to paste into Slack.

### 6. `api.ts` / `auth.ts` / `governance.ts` / `audit.ts` are untouched

Every data, auth, and governance-decision contract is imported as-is; this
story is a rendering-layer migration only. `executeGovernance`'s
`Idempotency-Key`/`If-Match` contract, `explainFailure`'s per-status-code
messages, and `RATIFY`'s availability predicate are called from the new
Cloudscape `ConfirmOperation` (a `Modal`) exactly as they were from the old
hand-rolled dialog — verified by the existing governance test suite, updated
only where the DOM shape changed (see Evidence), never where the behavior
did.

### 7. Self-contained bundle: no runtime CDN

Cloudscape's global stylesheet embeds its fonts as base64 `data:` URIs (no
`@import url(https://fonts...)`), and every Cloudscape component ships as a
plain npm package bundled by Vite like any other dependency — nothing in this
stack fetches a script, stylesheet, or font from a third-party host at
runtime. This matches the existing `/docs` page's *un*-met bar (Redoc there
still pulls from `cdn.redoc.ly`, a pre-existing gap this story does not
touch) and keeps the single-binary, same-origin deploy posture intact.

## Consequences

**What is now guaranteed.** Every screen the platform builds from here
(NOC, incidents, SOC, dashboards) has one navigation shell, one router, one
design system, and one theming seam to inherit, instead of a new standalone
page per screen. The estate/governance flow — list, filters, pagination,
deep-linked detail, dependency/dependent resolution, Ratify (with idempotency
key reuse across retries and full 400/401/403/404/409/412/422/428/5xx
handling), audit timeline (hash-chain badge, expandable per-event chain
record, older-page cursor) — is byte-for-byte behavior-equivalent to before,
proven by the pre-existing test suite (updated only for the new DOM shape,
never for weakened assertions).

**What this does not do.** It does not retire `/noc`
(`internal/httpapi/nocweb/noc.html`, ADR-NOC-002) — that happens in E7-UI.1
when a real Cloudscape NOC screen exists to replace it; both are left running
side by side deliberately. It does not touch any Go code, migration, or
`internal/arch` guard — this is a `web/`-only change, and `go build ./...`
and the full Go suite are unaffected. It does not theme Cloudscape to an
OneOps brand — `applyTheme`/`Theme` exists for that later, unused today. It
does not adopt `@cloudscape-design/board-components` yet — installed now
(per the brief, "for later dashboards") but not wired into any screen; E7-UI.1
and E9 are its first consumers. The bundle is materially heavier than the
hand-rolled version (uncompressed JS ~965 kB / gzip ~277 kB, CSS ~1,069 kB /
gzip ~228 kB, driven mostly by Cloudscape's own component styles and embedded
font weights) — an accepted, explicitly-flagged cost of a real design system
over bespoke CSS, not yet code-split (Vite's own build warns on chunk size;
splitting is a fair follow-up once more screens exist to split along).

## Evidence

- `web/package.json` — `@cloudscape-design/components@3.0.1341`,
  `@cloudscape-design/global-styles@1.0.65`,
  `@cloudscape-design/board-components@3.0.212`, `react-router-dom@6.30.4`.
- `web/src/Shell.tsx`, `web/src/theme.ts`, `web/src/App.tsx`,
  `web/src/routes/{EstatePage,ArtifactPage,ComingSoon}.tsx` — the shell,
  router, and theme toggle.
- `web/src/components/*` — every existing component (`EstateTable`,
  `FilterBar`, `ArtifactDetail`, `AuditTimeline`, `ConfirmOperation`,
  `SignIn`, `AuthorityPill`, `States`) reimplemented on Cloudscape primitives.
- `web/src/*.test.tsx` (41 tests, `pnpm --dir web exec vitest run`) — the
  full pre-existing behavior suite, passing against the new implementation;
  `web/src/test-render.tsx` adds the `BrowserRouter` wrapper every test now
  needs; only URL-shape assertions changed (`?artifact=` → `/artifacts/:id`),
  no assertion was removed or weakened.
- `pnpm --dir web exec tsc -b --noEmit` — clean.
- `make web` — builds `internal/httpapi/webdist/` cleanly; `grep -o
  'https\?://[^"'"'"']*'` across the built `index.html`/`assets/*` returns
  only inert strings (XML/SVG namespace URIs, a React dev-mode
  error-decoder doc link never fetched, and a CSS license comment) — no
  runtime CDN reference.
- `go build ./...` and `make test` (full suite, `-race`) — unaffected, both
  green; this story touches no Go source.

## Enforcement

- `web/src/*.test.tsx` under `make web-test` is the regression suite for
  every behavior this ADR claims is preserved (Ratify's idempotency-key
  reuse and per-status error text, filter-to-URL round-tripping, deep-linked
  artifact detail, audit timeline chain-verification badge and hash
  expansion, OIDC sign-in/out and CSRF-state rejection). A future change that
  breaks any of these fails a named test, not a visual inspection.
- `make web` is the self-containment check: any future dependency that pulls
  from a runtime CDN would need to be caught by re-running the `grep` above
  against the built bundle (not yet a standing automated test — a fair
  follow-up once a second external-URL-prone dependency is added).

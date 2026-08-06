# ADR-UI-002 — The console is route-code-split; Cloudscape is an isolated, cacheable vendor chunk whose >500 kB size is intentional

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-UI-001 (Cloudscape + the self-contained `go:embed` bundle this preserves), ADR-NOC-003 (SPA fallback the router relies on), `docs/PLATFORM-BUILD-PLAN.md` E-HARD.4, `web/src/App.tsx`, `web/src/Shell.tsx`, `web/vite.config.ts`, `.github/workflows/ci.yml` (the `web` job's self-contained check) |

## Context

The console shipped as a single ~1.28 MB JS chunk (~369 kB gzip) that tripped
Vite's >500 kB chunk warning and put every screen's code — Dashboards charts,
Topology SVG, all nine boards — into the download that must complete before
the router resolves. The bundle is embedded in the Go binary via `go:embed`
and must remain fully self-contained (no runtime CDN — ADR-UI-001).

## Decision

Two changes, together:

1. **Route pages are `React.lazy`-loaded** (`web/src/App.tsx`), behind a single
   `<Suspense>` boundary wrapping the Shell's `<Outlet/>` (`web/src/Shell.tsx`)
   so the AppLayout/nav chrome stays mounted and visible while a route chunk
   loads. Pages keep their **named exports** (their own tests import them by
   name); `React.lazy`, which needs a default export, is adapted per-import
   (`import('./routes/X').then(m => ({ default: m.X }))`) rather than changing
   the pages.

2. **Cloudscape is pinned to its own `manualChunks` vendor chunk**
   (`web/vite.config.ts`: `cloudscape: ['@cloudscape-design/components',
   '@cloudscape-design/global-styles']`). Route-lazy alone was insufficient —
   because the always-loaded `Shell` uses Cloudscape, Rollup hoisted the whole
   library into the main entry, which stayed >500 kB. Isolating it drops the
   main entry to ~32 kB (~12 kB gzip) and gives the library a stable,
   independently-cacheable chunk.

**Result:** main entry **1.28 MB → 32 kB** JS; each route is a 5–16 kB
on-navigation chunk; Cloudscape is a single ~1.14 MB (~327 kB gzip) chunk,
`modulepreload`-ed because the Shell needs it immediately, then cached across
every subsequent deploy that does not bump the Cloudscape version.

### The remaining >500 kB warning is intentional — do not "fix" it

Vite still prints `Some chunks are larger than 500 kB` for the `cloudscape`
chunk **and only that chunk**. That is the component library itself; it cannot
be made smaller without dropping Cloudscape (rejected — ADR-UI-001) or
route-splitting a library the Shell needs on first paint (impossible — it's
needed immediately). The warning is therefore accepted, on purpose:

- **Do not raise `build.chunkSizeWarningLimit`** to silence it — that also
  hides a future *app-code* chunk crossing the threshold, which we would want
  to see.
- **Do not re-merge the vendor chunk** into the entry to "reduce chunk count" —
  that reverts the 32 kB entry and the independent cacheability.
- The warning naming the `cloudscape` chunk specifically is the expected steady
  state. A warning naming any *other* chunk is a real signal.

## Consequences

**Guaranteed.** First load downloads ~32 kB of app entry + the Cloudscape
vendor chunk (cached thereafter); each screen's code arrives on navigation
behind a nav-visible spinner. The bundle stays self-contained — the lazy
chunks are local `assets/*.js`, and the CI `web` job's self-contained-bundle
grep (no external host beyond the documented-inert allowlist) passes against
the split build. `go:embed all:webdist` still resolves (the tracked
`.gitkeep` placeholder survives `emptyOutDir`, restored if a local build wipes
it).

**Not claimed.** The total bytes shipped are not reduced — they are
re-partitioned so the initial critical path is small and the heavy, stable
part caches. This is a load-time and cache-efficiency change, not a
payload-reduction one.

## Enforcement

- `make web-test` (tsc + vitest, 143 tests) covers the lazy/Suspense wiring;
  `App.test.tsx` renders the router across the Suspense boundary via
  `await findBy*` and passes.
- The CI `web` job builds the split bundle and runs the self-contained grep on
  it every push — a lazy chunk that somehow pulled in an external host would
  fail there.
- This ADR is the record that the lone remaining >500 kB warning is the
  Cloudscape vendor chunk by design; a change that silences it by raising the
  limit or re-merging the chunk contradicts this decision and must supersede
  it, not slip in as a cleanup.

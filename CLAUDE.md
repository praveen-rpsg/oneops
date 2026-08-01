# OneOps Control Plane — Engineering Context

Go control plane for constitutional document governance: configuration objects,
append-only audit chains, policy automation, webhook delivery, multi-tenant
row-level isolation, and a derived Platform Knowledge Graph (PKG).

**Rule zero: this file points, it never restates.** If a procedure is
executable, it lives in the Makefile or a guard — read those, don't trust prose.

## Commands

- `make test` — full suite, race detector + coverage. **Run before claiming anything works.**
- `make lint` / `make vet` / `make fmt`
- `make up` — local deps. **Postgres is on host port 5435** (5432–5434 are taken by other projects).
- `make test-integration` — needs `TEST_DATABASE_URL` (from `.env` if present)
- `make graph` — regenerate `pkg.json` (generated, **never committed**)
- `make migrate-hash` / `make migrate-validate` — after any migration change (Atlas)

## START HERE — the canonical build plan

**`docs/PLATFORM-BUILD-PLAN.md` is the single source of truth for what to build
next.** Read it before any product work. Find the `▶ CURRENT` marker, do that,
and update the marker + checkboxes in the SAME change that lands the work. It
exists so no session/agent ever re-plans from scratch or loses track. OneOps is
one unified platform (governance + ITSM + NOC + SOC + asset + AI); build it all
here (the separate AINOC project is retired).

## Authoritative documents (in order of consultation)

1. `docs/PLATFORM-BUILD-PLAN.md` — **what to build next + status (read first)**
2. `docs/ENGINEERING-OPERATING-SYSTEM.md` — how work flows; what lives where
2. `docs/TRUST-REGISTER.md` — eliminated defect classes + the 5-condition rule for adding entries
3. `docs/adr/` — 37+ ADRs; find the governing ADR **before** touching its area
4. `docs/architecture-guards.md` + `internal/arch/` — ~56 build-failing guards
5. `docs/PLATFORM-KNOWLEDGE-GRAPH.md`, `docs/PKG-IMPLEMENTATION-SPEC.md`, `docs/PKG-ENGINEERING-BACKLOG.md` — the active track

## Non-negotiable invariants

- **Never weaken, skip, or delete a guard in `internal/arch/` to make a change pass.** A failing guard means the change is wrong until an ADR says otherwise.
- **Audit chains are append-only.** Per-chain `seq` is commit-ordered by the chain-head `FOR UPDATE` lock. New audit-adjacent triggers must be `ENABLE ALWAYS`; write privileges must be explicitly `REVOKE`d (the default posture is open — `20260729000001_rls_policies.sql` auto-grants writes on every new table).
- **Tenancy:** any new tenant-owned table goes into `postgres.TenantOwnedTables` and gets RLS in its migration, or CI fails. Never trust a queue row's tenant label — ownership is re-derived via `domain.ResolveAndAuthorize` (ADR-TENANCY-003).
- **PKG determinism:** extractors may not consult network, clock, or environment; sort every collection before emit; a `Medium`/`Declared` fact may never render as `Certain`. Route inventory is derived inside `internal/httpapi` and exported — **no AST route inventory may exist** (backlog Amendment A2).
- **Ratified vocabulary only:** no "kernel", no AI service, no Workspace/Search/Task entities. Prose is the sole source of obligation; never cite a diagram as authority (CI-6).
- **Security claims follow the Trust-Register discipline:** live exploit → class-eliminating fix → live re-attack fails → build-failing test → honest ADR. No entry until all five hold.

## Definition of done (every change)

1. `make test` green — paste the tail of the output as evidence.
2. `make lint` clean on touched packages.
3. Migration touched → `make migrate-hash` + `make migrate-validate`.
4. The governing ADR is cited in the commit message; new decisions get a new ADR.
5. Commit style: `OPS-S<story>: <what changed>` for backlog stories, matching `git log`.
6. No claim of completion without command output to back it. "Should work" is not done.

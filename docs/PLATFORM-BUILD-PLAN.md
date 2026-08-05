# OneOps — Platform Build Plan (Canonical Execution Tracker)

> **This is the single source of truth for what OneOps is becoming, what is
> done, what is in progress, and what to build next.** Every session and every
> agent reads this FIRST and updates it as work lands. If you are a new
> session/agent: start here, find the `▶ CURRENT` marker, do that, then move the
> marker. Do not re-plan from scratch — refine this in place.

**Owner:** CTO (top-level session). **Status convention:** `[x]` done & merged ·
`[~]` in progress (branch open) · `[ ]` not started · `▶` the current front.
**Last updated:** 2026-08-05.

---

## 0. How to use this file (read this every time)

1. **Before building anything**, read §3 (cross-cutting standards) and the epic
   you're touching. Every increment honors §3 — no exceptions.
2. **Find `▶ CURRENT`.** That is the next thing to build. One front at a time per
   dependency chain; independent epics may run in parallel on separate branches.
3. **Build → review → CTO commit → merge.** Nothing merges on an implementer's
   own claim. Commit to the branch BEFORE review (a reviewer's mutation-cleanup
   `git restore` must never wipe uncommitted work — this bit us once).
   **Calibrated review depth (2026-08-03):** a FULL independent mutation-testing
   review agent for any increment touching security, tenant-isolation,
   concurrency, data-integrity, or hardened migrations (where the gate has caught
   every real breach). A FAST CTO self-review (verify build + tests + RLS +
   contract + guards directly, no separate agent) for genuinely low-risk additive
   increments (CRUD, DTOs, read-only projections, docs). Safe parallelism via
   worktrees is unavailable here (repo is the `oneops/` subdir, not the workspace
   root), so increments run sequentially — calibrated depth is the speed lever.
4. **When an item lands, update its checkbox here and move `▶`** in the same
   change. A stale plan is the failure this file exists to prevent.
5. **Scope discipline:** an increment is one coherent, reviewable story. If a
   brief would exceed that, split it and list the splits here.

---

## 1. Vision & scope

OneOps is **one unified, AI-native enterprise operations platform** — a single
operating model spanning governance, IT service management (ITSM), network
operations (NOC), security operations (SOC), asset/configuration management,
observability, automation, and executive decision support. It is the real
product; the earlier `AINOC` effort is retired and out of scope.

Bar: **best-in-the-world, not a lollipop product.** Every module is
multi-tenant, secure, scalable, observable, evidence-backed, and honors the
constitution. We build depth (edge cases, failure modes, operability), not demos.

## 2. Foundations already in place (do not rebuild — extend)

The control plane on `master` already provides the substrate every module reuses:

- **Identity:** JWT + multi-IdP SSO (OIDC/JWKS) with issuer→tenant binding; RBAC;
  Organizations, Tenants, Teams, Memberships, Users, Invitations.
- **Tenancy:** row-level security on every tenant-owned table (`TenantOwnedTables`
  + FORCE RLS), re-derived ownership, startup + sentinel schema validation.
- **Data & governance:** Configuration objects + a **dependency-graph engine**
  (`internal/graph`: WalkDependencies/Dependents/DetectCycles) — the CMDB reuses
  this. Governance state machine (lifecycle + transitions). Append-only,
  tamper-evident **audit** chains. Optimistic-locking/versioning everywhere.
- **Work & automation:** Policy automation (event→Action), **Workflow as
  action-decision composition** (multi-step, gated, self-healing, crash-durable),
  **multi-approver approval quorum**, **Notification service** (at-least-once,
  leader-gated), Webhooks (SSRF-guarded, at-least-once).
- **Platform:** Settings (per-tenant), per-tenant rate limiting, observability
  (metrics/tracing/health), PrometheusRule alerting/SLOs, Helm (HPA/PDB/
  NetworkPolicy/TLS/ServiceMonitor), DR/backup, Go SDK, embedded web console,
  **Platform Knowledge Graph (PKG)** (derived architectural truth).

These are the Lego bricks. **Compose them; don't re-found them.**

---

## 3. Cross-cutting standards — EVERY increment honors these

These are the "all angles" that make it world-class rather than a demo. A review
checks them on every module.

**Correctness & governance**
- Multi-tenant isolation: every tenant-owned table in `TenantOwnedTables` + FORCE
  RLS + fail-closed policy; a two-tenant negative test that BITES.
- Audit: every meaningful mutation is attributable and, where constitutional,
  append-only. Optimistic locking (RowVersion) on mutations.
- Ratified vocabulary + reduced-concept discipline (see §4). No guard weakened;
  new decisions get an ADR.

**Scale & performance**
- Designed for the data volumes each domain implies (telemetry is the extreme —
  see E2). Pagination, filtering, and bounded queries on every list endpoint.
  Index every access path; no unbounded scans on hot paths. Load/latency budget
  stated per module; regressions caught by the load harness (`make loadtest`).
- Backpressure, retries with capped backoff, idempotency (deterministic ids +
  ON CONFLICT), leader-gated singletons, at-least-once where delivery matters.

**Reliability & operability**
- Crash-durable (self-healing sweeps, not permanent stalls). Graceful shutdown.
- Every module emits its own metrics + health; every alertable failure has a
  runbook. DR/backup covers new stores.

**API & UX consistency**
- Uniform REST under `/v1`, exact OpenAPI contract (bijection test), consistent
  errors (RFC 7807), pagination, filtering, sorting. SDK + console coverage per
  module. Real-time surfaces (NOC, dashboards) use a defined streaming layer (E11).

**Security**
- AuthZ on every resource (RBAC + tenant scope). Secrets never inline. Outbound
  calls SSRF-guarded. Security-relevant classes follow the Trust-Register
  discipline (live exploit → fix → re-attack → guard → ADR).

**Extensibility**
- Ingestion/integration is connector-based (E12), not hard-coded per vendor.
  Import/export and bulk ops on core entities.

**Edge cases are first-class.** Each epic below lists the non-obvious ones. "It
works on the happy path" is not done.

## 4. Reduced-concept handling (constitutional — do not reify)

Vol II §5.3 / Vol III §3.4 ratify these as **derived/projections, never stored
domain types**: `Alert`, `Event`, `Signal`, `Dashboard`, `Report`, `Ticket`,
`Metric`, `Runbook`, `State`, `Workflow`. Deliver the CAPABILITY over primitives:
- **Alert/Event/Signal** → derived from telemetry crossing rules; correlated into
  incidents. Not a stored `Alert` table-as-noun; a rule + a derivation + a
  notification/incident.
- **Ticket/Incident/Problem/Change** → stateful work items on the governance
  state-machine + workflow (lifecycle, assignee, transitions, approvals).
- **Dashboard/Report** → queries/projections over audit, timeline, telemetry,
  and the domain stores.
- **Workflow** → already delivered as action-decision composition.
If a capability truly cannot be delivered without reifying a reduced concept,
that is a founder-level constitutional override + ADR, not a story decision.

---

## 5. THE BUILD PLAN (sequenced, foundation → operations → intelligence)

Dependency order matters: you cannot monitor, alert on, ticket against, or secure
what you cannot model. Assets first; intelligence last.

### E1 — Asset / CMDB foundation  `[x]`
The typed configuration-item model + relationship graph everything references.
- [x] E1.1 CI model + relationships + CRUD/graph API (reuses `internal/graph`) — **merged to master** (ADR-ASSET-001; cross-tenant edge defense mutation-proven)
- [x] E1.2 Business-service mapping (service → supporting CIs), criticality, environment, ownership — **merged** (owner refs tenant-verified; service-map = typed graph projection)
- [x] E1.3 CI lifecycle (planned→active→retired), change history, soft-retire — **merged** (4-state machine per amended ADR-ASSET-001 §5; append-only history hardened ENABLE ALWAYS+REVOKE)
- [x] E1.4 Bulk import/export + duplicate **detection** (source+external_ref, idempotent upsert, DB uniqueness) — **merged**
- [ ] E1.4b Controlled duplicate **merge/resolution** (reassign relationships+history to a survivor, retire duplicate) — deferred follow-on, does not block epic completion
- [x] E1.5 CMDB health: staleness, orphans, data-quality (`last_seen` freshness field + GET /admin/assets/health) — bounded, index-backed, RLS-isolated; true drift-vs-reality deferred to post-E2 discovery. **E1 (CMDB) is now complete** — E1.4b remains a separately-gated later increment.
- [ ] FOLLOW-UP (tech-debt): harden `TestEveryAuditAppendPath_SerialisesOnItsChainHead` — `reachesForUpdate` keys on unqualified method name, so a `Create` can pass via cross-type name-collision (pre-existing; not introduced by E1.x). Also: relationship export (E1.4 exported assets only).
- **Edge cases:** circular relationships, cross-tenant edges (forbidden), orphaned/duplicate CIs, high-fan-out services, historical point-in-time queries.

### E2 — Telemetry & Monitoring ingestion  `[ ]`
Signals about assets: metrics, logs, traces, synthetics. The scale extreme.
- [x] E2.1 Ingestion data model + API (metrics/time-series tied to CIs), spine only — **merged** (ADR-TELEMETRY-001: Postgres + TimescaleDB hypertables behind `domain.TelemetryRepository`, D1 resolved; `telemetry_sample` tenant-owned + RLS + FORCE; `asset_id` tenant-re-verified on write per-sample, same defense as ADR-ASSET-001 §6; bounded ingest batch (1000) + bounded/paginated range query (5000, keyset over timestamp); infra-gate proved the Timescale image swap regresses nothing in the pre-existing suite)
- [x] E2.1b Retention + downsampling over telemetry_sample — **merged (`1757dc6`)**. FINDING: TimescaleDB's `add_retention_policy`/continuous-aggregate features require the Timescale License; the pinned image (`*-oss`, ADR-TELEMETRY-001) carries only Apache — verified live. ADR-TELEMETRY-002 records the (in-scope) alternative actually shipped: `TelemetryRetentionWorker`/`TelemetryRollupWorker` drive Apache-licensed `drop_chunks`/`time_bucket` directly; downsampling is a second platform-owned hypertable (`telemetry_rollup_5m`) carrying the SAME RLS as every tenant-owned table (stronger tenant-isolation position than a native continuous aggregate, whose backing object is unproven under RLS — mutation-tested live). Raw retention horizon is an operator-tunable Setting (`telemetry_raw_retention_days`, platform-scoped — per-tenant retention deferred, documented, not faked). `QueryRange` gained a `resolution` param (raw/rollup_5m/auto) with correct wide→rollup, narrow→raw selection. Also folds the E2.1 review nits (first-write-wins documented; pagination/natural-key comment). Relicensing to the Timescale-licensed image for native policies is a separate, undecided ADR-level choice.  **DONE + merged (`1757dc6`, OPS-TELE-002).**
- [x] E2.1b Retention + downsampling — **merged** (ADR-TELEMETRY-002: Apache-primitive workers; rollup is a 2nd owned RLS hypertable; OneOps stays Apache-only for telemetry — standing decision). Follow-ups: PKG extraction-budget guard tightening (217/200ms under load); retention-error metric/alert; setting-decode diagnostic log.
- E2.2 Agentless collectors — SPLIT:
  - [x] E2.2a Collector framework + HTTP uptime check — **merged** (leader-gated scheduler, SSRF-guarded via safehttp at DIAL time [DNS-rebinding refused], tenant-verified asset, RLS; writes up/latency/status telemetry). SECURITY-GUARD FOLLOW-UP (tracked): the SSRF arch guard `TestOutboundClients_AreSSRFGuarded` gives illusory coverage — it only catches a bare `&http.Client{}` LITERAL and doesn't sweep `cmd/` (the composition root), so it can't catch an SSRF regression for ANY of the 3 outbound clients (webhook/registry/collector). Shipped code is safe; harden the sweep to include cmd/. Also: up=any-status masks 5xx (future health-semantics); due-scan starvation at very high check counts.
  - [ ] E2.2b SNMP collector (network devices) — protocol dep + credential (community/v3) storage
  - [ ] E2.2c Cloud/API pollers (AWS/etc.) — credential storage + connector pattern (see E11 connector framework)
- [ ] E2.3 Agent/push ingestion (app/APM, custom metrics), log ingestion
- [ ] E2.4 Distributed tracing ingestion; correlation to CIs
- **Edge cases:** high cardinality, ingestion backpressure/quotas per tenant, out-of-order & late data, clock skew, collector disconnection, storage growth — none of these are addressed by E2.1's spine and remain open for E2.1b/E2.2/E2.3.

### E3 — Alerting (derived) + suppression  `[x]`
- [x] E3.1 Alert **rules** (threshold first; rate-of-change/absence/anomaly later) over telemetry + leader-gated evaluator that fires on transition → Notification. `AlertRule` is config; a firing is DERIVED (no reified `Alert` type — §4). Cross-tenant telemetry isolation enforced by an explicit tenant_id predicate (`TelemetryRepository.QueryRangeForTenant`), mutation-verified.
- [x] E3.2 Flap suppression: a candidate transition must hold continuously for the rule's own `flap_dwell_seconds` (default 60s, rule-level config, patchable) before it is committed/notified — hysteresis DWELL, not a blind cooldown (ADR-ALERTING-001). Oscillation collapses to at most one eventual transition; a genuinely sustained change still transitions promptly. Severity is unaffected — still `AlertRule.Severity`, attached at commit exactly as E3.1/E4.1 already do. `pending_state`/`pending_since` (evaluator-only bookkeeping, not a new entity, not in the HTTP contract) persist the in-flight candidate on the SAME `alert_rule` row so the dwell clock survives a restart/leader failover; any admin edit clears it. Mutation-verified (short-circuiting the dwell check fails 3/4 new tests).
- [x] E3.3a Maintenance windows / blackout: operator-declared, one-shot `[starts_at, ends_at)` window over a single asset during which an `ok→firing` transition's incident-create/link and notify are suppressed — the underlying firing derivation and E3.2's dwell/pending bookkeeping are untouched, and recovery is never suppressed (ADR-ALERTING-002). Tenant-owned `maintenance_window`; the evaluator's privileged active-window check carries an explicit `tenant_id` predicate (ADR-TENANCY-012); suppression is recorded (`suppressed_count`/`last_suppressed_at`), never silently lost. A still-firing condition pages on the next tick after its window ends. Split out of the old bundled E3.3 — lower-risk, universally-needed, shipped first. Mutation-verified (defeating the check flips 6 tests to FAIL). Reviewer APPROVE-WITH-NITS; nits folded (ADR count, `suppressed_count` per-(rule,tick) semantics, guard blind-spot note below).
- [x] E3.3b Dependency-aware suppression (ADR-ALERTING-003): when a CI X would fire and a CI **X depends on** (`depends_on`/`runs_on`, cycle-safe recursive-CTE traversal, max-depth 10) is itself "down" (≥1 enabled+firing `alert_rule` on it), X's `ok→firing` side-effect is suppressed — mirroring E3.3a exactly (skip RecordTransition+incident+notify; state untouched ⇒ **self-clearing** when the root recovers). Down-check is a plain privileged SELECT with an explicit `tenant_id` predicate (now actively covered by the E4.1-GUARD read guard); traversal stays RLS-scoped. Recorded in tenant-owned `dependency_suppression` (names the root, atomic upsert). Precedence: maintenance-window (E3.3a) checked first. **HONEST BOUND:** no rule symptom-class exists ⇒ all-or-nothing per asset (can mask a genuinely independent failure on X while a dependency is down); mitigated by visible recording + self-clearing; symptom-class scoping named as future refinement. Direction-correctness + both-tenant-isolation + cycle + edge-type-exclusion mutation-verified (store, real PG) and evaluator hook mutation-verified (defeating the gate flips 4 tests). **VERIFICATION (honest record):** 3 implementer-agent API drops during build + 2 reviewer-agent drops during review (session-wide API instability), recovered via WIP-to-branch each time. Evaluator unit tests + ADR authored by CTO after the 3rd build drop. The independent reviewer confirmed baseline+contract+vet+lint green before it dropped; the CTO then completed the adversarial gate directly: all 4 mutation proofs re-run with a REAL integration DB (direction flip → dir test fails; tenant predicate break → store isolation fails; guard tenant-strip → E4.1-GUARD fails naming this store; evaluator gate defeat → 4/9 flip), plus an adversarial read of the agent-written store logic. **This caught 2 real integration failures an earlier skip-run had hidden** (dependency_suppression_pkey unregistered in the tenant-key-scope + uniqueness guards — fixed: server-minted suppression_id justified, mirroring maintenance_window). Full real integration suite (race) now 0 FAIL / 30 pkgs.  [merged — see below]
- [x] E3.4 **Alert-rule symptom-class taxonomy** (ADR-ALERTING-005) — `alert_rule.symptom_class` enum = **`availability | resource | unspecified`** (minimal — the one distinction suppression/correlation need: does the symptom cascade through dependencies? availability does, resource doesn't). **Explicit operator-set field, default `unspecified`** (CTO call on founder's "follow best"): existing rules backfill to `unspecified` via the migration's `DEFAULT` and behave EXACTLY as today — no metric-name inference (too fragile / silently mis-suppressing). Optional on create, patchable via `AlertRulePatch.SymptomClass`. **E3.4 = the PRIMITIVE ONLY** — field + create/patch + backfill + validation (domain `Validate` + DB `CHECK ck_alert_rule_symptom_class`, both mutation-verified) + DTO/openapi (additive, `contract-breaking` clean) + PKG census bump. **NO behavior change**: `internal/alerting/evaluator.go`, `internal/grouping`, dependency_suppression and maintenance_window were not touched, and their full pre-existing suites pass unmodified — nothing reads this column to decide anything yet. The class-scoped refinements of E3.3b dependency suppression and E4.2 root-cause ranking are SEPARATE follow-on stories that consume this primitive. NOT a reified entity — a column on `alert_rule`. Round-trip (each class + omitted⇒unspecified) + backfill + DB-CHECK-rejection mutation-verified against real Postgres. Full unit suite 1145 PASS/0 FAIL; full integration suite (race) 1518 PASS/0 FAIL/3 pre-existing unrelated SKIPs.
- **Edge cases:** alert storms, self-referential suppression, rule changes mid-fire, per-tenant rule isolation.

### E4 — Event Correlation & AIOps-lite  `[x]`
- [x] E4.1 Correlate an alert firing into an incident: create-or-link by affected CI (connects E3→E5). `Incident.Source` (manual|alert) gates correlation so an operator's own incident is never auto-annexed; `alert_rule.current_incident_id` is the LINK (a column, not a reified Alert/Event). Wired into the evaluator's ok→firing (find-or-create, DB-uniqueness-backed — no duplicate even under true concurrency) / firing→ok (append recovery note, clear link, never auto-resolve/close) transitions. Every privileged correlation read/write carries an explicit `tenant_id` predicate (ADR-TENANCY-012), mutation-verified (`AppendAlertNote`'s tenant check bites; the no-duplicate unique index bites). READ-SIDE GUARD still NOT built (tracked below, ADR-TENANCY-012's own follow-up) — flagged rather than forced: a general sweep risks false positives against every dual-role store (AlertRuleStore/CollectorCheckStore/IncidentStore) whose SAME methods serve both the tenant-scoped and privileged pools depending only on which pool constructs them, which the type system cannot currently distinguish.
- [x] E4.1-GUARD (carried over) Build the failing arch guard sweeping privileged `SELECT`s on `TenantOwnedTables` for an explicit tenant predicate (ADR-TENANCY-012's read analogue of `TestPrivilegedMutations_AreScopedToAnOwner`). **DONE + merged (`2a8f193`)** as `arch.TestPrivilegedReads_AreScopedToATenant`: derives privileged store types from the composition root (wiring-level, like `TestServerWiringUsesTenantScopedPool`, not a per-method text sweep), flags any `asset_id`-scoped privileged read lacking a `tenant_id` predicate; mutation-proven to bite (incl. concat-column style); canary-guarded against vacuity; 6 justified exemptions. ADR-TENANCY-012 follow-up discharged.
  - **DEBT (arch guard — CTE-with-UPDATE tenant-read coverage; found E3.3a review 2026-08-05):** neither static guard enforces the `tenant_id` predicate on a read embedded in a write-CTE (a statement containing `UPDATE <table>`) — the read guard skips it as the mutation guard's subject, and the mutation guard only fires on the `id = ANY($)` shape such methods lack. Affects `MaintenanceWindowStore.Suppress` and `IncidentStore.FindOrCreateOpenAlertIncident`; both are correctly predicated and covered by live cross-tenant integration tests, so this is a static-coverage gap, not a live hole. Honest bound recorded in `privileged_read_test.go`'s doc comment. Closing it = teach the sweep to parse the SELECT portion of a write-CTE. Do before the next privileged write-CTE reader lands.
- [x] E4.2 Topology-aware incident grouping (ADR-ALERTING-004): a nullable self-FK `incident.root_incident_id` — NOT a reified Group/Correlation noun (§4) — set/cleared exclusively by a leader-gated `internal/grouping.Reconciler` periodic pass (mirrors `alerting.Evaluator`'s own Config/Store/Run/RunOnce shape), never an HTTP write path. Heuristic (ML later in E13): for an open alert-incident on asset X, walk X's transitive `depends_on`/`runs_on` closure (identical edge-type filter + direction discipline to E3.3b) for nodes that are themselves "down" (their own open alert-incident); the DEEPEST such node's incident wins (ties broken lexicographically by asset_id), producing FLAT grouping directly since the closure is already fully transitive. No down dependency ⇒ NULL. Self-healing + idempotent by recomputation, not locking: a resolved root's children re-root or clear on the very next pass; an unchanged pass writes nothing. A genuine CMDB dependency cycle (not a stored-pointer bug) is broken deterministically (lexicographically-smallest incidentID anchored to NULL), never persisted as a pointer cycle; a same-row self-pointer is additionally refused at the schema level. Grouping never mutates incident STATE — the only write, `SetRootIncidentID`, cannot express one. Privileged store (`IncidentGroupingStore`) carries an explicit `tenant_id` predicate on every read/write, mutation-verified on both; the dependency walk stays RLS-scoped (tenant-scoped pool, `domain.WithTenant` re-bound per tenant per pass) — proven independent even when two tenants share identical `asset_id` strings with different topologies. Additive read-only DTO projection (`root_incident_id` omitempty + `is_root`); no new group-management API. **HONEST BOUND:** single-root pick (not ranked/multi-root); heuristic, not ML; a silently-dead root with no open incident of its own is invisible to it (same limitation class as E3.3b's down-check); no symptom-class exists yet (E3.4, founder-approved but not built) so grouping cannot distinguish a genuinely independent coincident failure from true collateral — named future refinement, not built here; eventually-consistent (default 60s reconciliation interval, not instant). Complementary to, and does not replace, E3.3b: suppression prevents a NEW collateral page at transition time when a root is already firing; grouping organizes the incidents that get created anyway (a root detected later, a suppression window that did not apply).
- **Edge cases:** correlation never crosses tenants (E4.1 done); window tuning; late-arriving correlated signals; grouping tenant isolation + no-state-mutation + cycle-safety all mutation-verified (E4.2 done).

### E5 — ITSM core: Incident / Problem / Change  `[ ]`
Stateful work items on the workflow + state-machine primitives.
- [x] E5.1 Incident lifecycle (manual and, since E4.1, alert-correlated), assignment, append-only timeline *(sequenced ahead of E3.2/E4: after "notify", operators need to TRACK work — incidents are the NOC's operational heart; independent of alerting refinements)*
- [x] E5.2 On-call, scheduling & paging — **SPLIT (CTO, 2026-08-05) into E5.2a + E5.2b** (founder sequence: E3.4 → E5.2):
  - [x] E5.2a **On-call schedules & rotations (who's on call now).** Tenant-owned `on_call_schedule` (name, `handoff_interval_seconds`, `rotation_start_at`, status) + `on_call_participant` (ordered users; FK `app_user` re-verified via ACTIVE membership per ADR-IDENTITY-002, like incident-assignee). Who's-on-call(T) = **deterministic computed rotation** `index = floor((T − rotation_start)/interval) mod participants` — NO JSON rules engine, NO per-shift rows (that's a framework). CRUD + reorder + a "who's on call now" endpoint (`GET .../on-call-schedules/{id}/on-call?at=`). Reduced-concept safe (legitimate operational objects, not false-nouns). No privileged pool / background worker — request-time resolution only (ADR-ONCALL-001). `AddParticipant` re-verifies ACTIVE membership (mirrors `IncidentStore.verifyAssigneeExists`, mutation-verified to bite); reorder is atomic and set-checked (`uq_on_call_participant_schedule_position` DEFERRABLE INITIALLY DEFERRED to allow a genuine position swap); positions stay contiguous 0..N-1 across add/remove/reorder. Rotation math is a pure, DB-free function (`domain.OnCallRotationIndex`) covering N=0/1/3, the half-open handoff boundary, before-rotation-start, and far-future inputs; the positive-modulo formula is separately, directly mutation-tested (`positiveModulo`/`TestPositiveModulo`) since the public function's own guard keeps every live input non-negative. **Deferred (honest bound):** overrides/coverage-swaps, multi-layer follow-the-sun, calendar-aware (non-UTC-seconds) cadences. Self-contained — no escalation dependency. Full unit suite (race) 1183 PASS/0 FAIL; full integration suite (race) 1562 PASS/0 FAIL/3 pre-existing unrelated SKIPs.
  - [x] E5.2b **Escalation policies & paging** — **SPLIT (CTO, 2026-08-05) into config + engine** (config is low-risk additive CRUD; the engine is a concurrency-heavy state machine deserving full independent review):
    - [x] E5.2b-1 **Escalation policy + tier config (CRUD).** Tenant-owned `escalation_policy` (name, status; MVP = one tenant-default) + ordered `escalation_tier` (position, `on_call_schedule_id` FK = which schedule to page, `wait_seconds` before advancing). CRUD + tier reorder (reused E5.2a's deferred-position-unique pattern exactly: `uq_escalation_tier_policy_position` DEFERRABLE INITIALLY DEFERRED, `ReorderTiers` mirrors `ReorderParticipants` line for line). `escalation_tier.on_call_schedule_id` re-verified same-tenant on the writer's own RLS-scoped connection before insert — the FK alone bypasses row-level security via the FK trigger's own privileges (ADR-ASSET-001 §6), mutation-verified to bite. NO engine/worker/paging/incident/notification wiring — that is E5.2b-2 entirely. ADR-ONCALL-002. Full unit suite (race) green; full integration suite (race, real Postgres) green, including 9 escalation-specific integration tests (CRUD, two-tenant RLS isolation, cross-tenant-schedule rejection mutation-verified, append ordering, removal compaction, atomic reorder with a genuine swap, mismatched-reorder rejection, cascade delete, `wait_seconds>0` DB CHECK backstop). Schema census/key-scope/uniqueness/wiring/contract guards all green; `make contract-breaking` additive-only.
    - [x] E5.2b-2 **Escalation engine (seed → page → escalate → cancel).** Tenant-owned `escalation_state` work-queue (at most one row per incident: `UNIQUE (tenant_id, incident_id)`, `current_tier_index`, `next_attempt_at`, `claimed_at` fence, status active|acked|resolved|exhausted — claim/fence shape mirrors `notification` exactly, ADR-CONCURRENCY-002/005/006/007). A leader-gated **Seeder** enrols open, unacked, alert-sourced incidents at the tenant's default policy (single active policy, or earliest-by-`created_at` if several; no active policy ⇒ nothing seeded, not an error) — a SEPARATE reconciliation pass, alerting hot path untouched (mirrors E4.2's grouping-reconciler precedent). A leader-gated **Worker** mirroring `notification.Worker` (ClaimDue → re-load incident, still open? if acked/resolved-class mark terminal & stop (this is how ack/resolve cancels, re-checked every cycle) → resolve who's-on-call for the tier's schedule → **PAGE-TIME ACTIVE-MEMBERSHIP RE-CHECK (E5.2a review nit 1, new public `MembershipStore.ActiveMember`): do NOT page a revoked/absent user — skip and advance anyway, recorded via `IncSkippedRevoked`, mutation-verified to bite live against Postgres** → enqueue an **email** Notification to the on-call user's resolved address (no `internal/notification` change) → advance tier or mark exhausted). Every tenant-scoped read bound to the claimed row's own tenant via `domain.WithTenant`, reusing the EXISTING tenant-scoped incident/policy/on-call/membership/user stores unchanged rather than a privileged reimplementation (ADR-TENANCY-003). No double-page under concurrent claims (`FOR UPDATE SKIP LOCKED` + fenced `Advance`/`MarkTerminal`, mutation-verified with a forced two-worker race and a forced stale-claim fence). Two-tenant RLS isolation mutation-verified live (a connection bound to tenant B cannot read tenant A's real `escalation_state` row). **Deferred (honest bound):** per-asset/service policy routing (MVP tenant-default), SMS/push, re-arming escalation on a reopened incident that already ran to a terminal state. ADR-ONCALL-003. Full unit suite (race) 1718 PASS/0 FAIL/6 pre-existing unrelated SKIPs; full integration suite (race, real Postgres) 439 PASS/0 FAIL/2 pre-existing unrelated SKIPs (perf tests excluded under `-race` by design). Schema census (43 tables/402 columns/90 indexes/140 constraints, 37 tenant-scoped)/key-scope/uniqueness/wiring/contract guards all green; `make contract-breaking` reports no breaking change.
- [ ] E5.3 SLA/SLO tracking on work items (business hours, pause-on-hold, breach escalation) ▶ CURRENT
- [ ] E5.4 Problem management (root cause, known errors) linked to incidents/CIs
- [ ] E5.5 Change management (change request → CAB approval via approval quorum, change calendar, conflict/collision detection, risk)
- [ ] E5.6 Major-incident handling, timeline, post-incident review
- **Edge cases:** timezones/business-hours, SLA pause semantics, escalation loops, linked/duplicate incidents, reassignment races.

### E6 — Service Catalog & Request Management  `[ ]`
- [ ] E6.1 Catalog (offerings, request items, forms)
- [ ] E6.2 Request → approval (quorum) → fulfillment (workflow) → asset provisioning link
- **Edge cases:** multi-step approvals, request SLAs, catalog per-tenant/per-role visibility.

### E7 — NOC Command Center  `[ ]` (projections; needs E11 streaming)
- [ ] E7.1 Real-time operational views: service health rollups, CI health, active incidents/alerts
- [ ] E7.2 Topology maps (CMDB graph visual), health propagation
- [ ] E7.3 Shift handover, operational runbooks (link E10 knowledge)
- **Edge cases:** real-time at scale, per-role/per-tenant views, rollup correctness when data is partial.

### E8 — SOC / Security Platform  `[ ]`
- [ ] E8.1 Security event ingestion + SIEM correlation (reuses E2/E4 patterns, security-scoped)
- [ ] E8.2 Threat detection (rules, IOCs, behavioral hooks)
- [ ] E8.3 Vulnerability management (scan ingestion, prioritization by CI criticality, remediation → E5 tickets)
- [ ] E8.4 Compliance (frameworks, controls, evidence, continuous audit) + Risk register/scoring
- [ ] E8.5 Security automation (SOAR playbooks on the workflow engine)
- **Edge cases:** security-data isolation & retention/legal hold, chain of custody, high-severity fast-path.

### E9 — Reports & Dashboards (projections)  `[ ]`
- [ ] E9.1 Query/aggregation engine over the domain + telemetry (bounded, cached)
- [ ] E9.2 Dashboards (composable projections), scheduled reports, exports
- [ ] E9.3 SLO/KPI reporting
- **Edge cases:** large aggregations, per-tenant data, caching/staleness, export volume.

### E10 — Knowledge Management  `[ ]` (extends PKG)
- [ ] E10.1 Knowledge articles / runbooks / known-errors, linked to CIs/incidents/problems
- [ ] E10.2 Versioning, review workflow (reuse governance), lifecycle
- (Semantic search arrives in E13.)

### E11 — Real-time & Integration platform (cross-cutting enablers)  `[ ]`
Pull forward as soon as E7/dashboards need it.
- [ ] E11.1 Streaming/real-time layer (WebSocket/SSE) with tenant-scoped subscriptions
- [ ] E12.x Connector framework: inbound (monitoring tools, cloud providers, SNMP, syslog) + outbound (ITSM/chat/paging) — versioned, sandboxed, SSRF-guarded
- [ ] Search: cross-entity search service (keyword first; semantic in E13)
- **Edge cases:** subscription fan-out at scale, connector failure isolation, backfill.

### E12 — Project & Portfolio Management  `[ ]`
- [ ] E12.1 Projects, tasks, dependencies, timelines, resourcing
- [ ] E12.2 Portfolio rollups (feeds E14 executive)
- **Edge cases:** cross-project dependencies, capacity conflicts.

### E13 — AI & Automation (AIOps / SecOps / Copilot)  `[ ]` (needs an AI-architecture ADR first)
- [ ] E13.0 **AI architecture decision (ADR):** model strategy, RAG over platform data, guardrails, human-in-the-loop, cost/latency, data-privacy/tenant-isolation of prompts. **Blocks all of E13.**
- [ ] E13.1 Semantic search over knowledge + CMDB + incidents
- [ ] E13.2 Copilot (operator assist: summarize incident, suggest next action, draft RCA) — human-in-the-loop
- [ ] E13.3 Predictive analytics (capacity forecasting, anomaly/failure prediction)
- [ ] E13.4 Auto-remediation / SOAR (AI-proposed, policy/approval-gated actions via workflow)
- **Edge cases:** hallucination guards, never auto-act without a gate, prompt/tenant data isolation, cost controls, audit of AI actions.

### E14 — Executive Command Center  `[ ]` (projections over everything)
- [ ] E14.1 Executive dashboards, KPI monitoring, portfolio view
- [ ] E14.2 Cross-business insights, enterprise analytics, decision intelligence
- **Edge cases:** cross-tenant aggregation only where authorized; data freshness; drill-down authorization.

### Capacity Management (spans E2/E9/E13)
- [ ] Capacity/utilization tracking, forecasting, right-sizing recommendations. Sequenced with E9/E13.

---

## 6. Global decisions still OPEN (resolve via ADR before the dependent epic)
- ~~**D1 — Time-series storage** (E2): Postgres partitioning vs dedicated TSDB.~~ **RESOLVED** — ADR-TELEMETRY-001: Postgres + TimescaleDB hypertables behind a pluggable interface (RLS preserved, one datastore/DR, swap-later hedge).
- **D2 — Real-time transport** (E7/E11): WebSocket vs SSE; subscription model.
- **D3 — AI architecture** (E13): model, RAG, guardrails, isolation, cost.
- **D4 — Connector isolation/runtime** (E11/E12): in-process vs sandboxed/out-of-process.
- (Record each as an ADR when reached; link here.)

## 7. Working agreement (the anti-lost-track rules)
- One `▶ CURRENT` per active chain; update it and the checkboxes when work lands.
- Every increment: ADR if it's a new decision; two-tenant isolation test; contract
  bijection; load/latency note; runbook if alertable; PKG census updated.
- Branch naming: `feat/<epic>-<slug>` (e.g. `feat/asset-cmdb`). Stack within an
  epic; consolidate to `master` per epic once reviewed.
- This file is updated in the SAME change that lands the work. No exceptions.

## 8. Standing note (CTO → founder)
The platform capability is real and compounding. The remaining constraint is not
engineering but a paying design partner — the highest-leverage move, and the one
outside the team's lane. Everything above is buildable; sequence and depth are
tuned so early customer feedback can reprioritize without rework (foundation is
customer-agnostic; intelligence/executive layers are where opinions will differ).

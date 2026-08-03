# OneOps — Platform Build Plan (Canonical Execution Tracker)

> **This is the single source of truth for what OneOps is becoming, what is
> done, what is in progress, and what to build next.** Every session and every
> agent reads this FIRST and updates it as work lands. If you are a new
> session/agent: start here, find the `▶ CURRENT` marker, do that, then move the
> marker. Do not re-plan from scratch — refine this in place.

**Owner:** CTO (top-level session). **Status convention:** `[x]` done & merged ·
`[~]` in progress (branch open) · `[ ]` not started · `▶` the current front.
**Last updated:** 2026-08-01.

---

## 0. How to use this file (read this every time)

1. **Before building anything**, read §3 (cross-cutting standards) and the epic
   you're touching. Every increment honors §3 — no exceptions.
2. **Find `▶ CURRENT`.** That is the next thing to build. One front at a time per
   dependency chain; independent epics may run in parallel on separate branches.
3. **Build → independent review (mutation-tested) → CTO commit → merge.** Nothing
   merges on an implementer's own claim. Commit to the branch BEFORE review (a
   reviewer's mutation-cleanup `git restore` must never wipe uncommitted work —
   this bit us once).
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
- [~] E2.1b Retention policy + downsampling/continuous aggregates over telemetry_sample (TimescaleDB `add_retention_policy`/`add_continuous_aggregate_policy`); also folds the E2.1 review nits (document first-write-wins on duplicate-ts; pagination/natural-key comment)  ▶ CURRENT
- [ ] E2.2 Agentless collectors: SNMP (network), API/cloud pollers, uptime/synthetic checks — **large; SPLIT when reached** (E2.2a collector framework + HTTP/uptime check; E2.2b SNMP; E2.2c cloud/API pollers — each needs credential storage + leader-gated scheduling + target config)
- [ ] E2.3 Agent/push ingestion (app/APM, custom metrics), log ingestion
- [ ] E2.4 Distributed tracing ingestion; correlation to CIs
- **Edge cases:** high cardinality, ingestion backpressure/quotas per tenant, out-of-order & late data, clock skew, collector disconnection, storage growth — none of these are addressed by E2.1's spine and remain open for E2.1b/E2.2/E2.3.

### E3 — Alerting (derived) + suppression  `[ ]`
- [ ] E3.1 Alert rules (threshold, rate-of-change, absence-of-data, anomaly hook) over telemetry
- [ ] E3.2 Derivation engine → notifications/incidents; severity; dedup; flap suppression
- [ ] E3.3 Maintenance windows / blackout; dependency-aware suppression (suppress downstream when a root CI is down — uses the CMDB graph)
- **Edge cases:** alert storms, self-referential suppression, rule changes mid-fire, per-tenant rule isolation.

### E4 — Event Correlation & AIOps-lite  `[ ]`
- [ ] E4.1 Correlate alerts/events into incidents (time + topology window via CMDB)
- [ ] E4.2 Noise reduction, grouping, root-cause candidate suggestion (heuristic first; ML later in E13)
- **Edge cases:** correlation never crosses tenants; window tuning; late-arriving correlated signals.

### E5 — ITSM core: Incident / Problem / Change  `[ ]`
Stateful work items on the workflow + state-machine primitives.
- [ ] E5.1 Incident lifecycle (from correlated alerts or manual), assignment, queues, escalation
- [ ] E5.2 On-call, scheduling & paging (rotations, escalation policies, notify via E-notification)
- [ ] E5.3 SLA/SLO tracking on work items (business hours, pause-on-hold, breach escalation)
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

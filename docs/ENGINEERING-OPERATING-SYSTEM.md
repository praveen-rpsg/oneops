# OneOps Engineering Operating System (EOS)

**Not constitutional.** All authority derives from the Execution Constitution v1.0
(Parts 1–10) and Amendment I (Parts 11–15). Where law is needed, this document
**points**; it never restates.

**This document lives in the repository, not the corpus**, because engineers work
here and because most of what an operating system would say is already enforced
by artifacts a few directories away.

## What this document deliberately does NOT contain

The following already exist in **executable** form. Duplicating them in prose
would create a stale copy of something currently self-enforcing — the failure
mode Constitution Law 14.1 exists to prevent.

| Requested | Where it actually lives | Status |
|---|---|---|
| Automation / CI model | `.github/workflows/ci.yml` — 9 jobs | Executable |
| Tool surface | `Makefile` — 22 documented targets | Executable |
| Architecture validation | `internal/arch/` — 56 guards | Executable |
| Contract validation | `internal/httpapi/contract_test.go` | Executable |
| Release checklist | `docs/RELEASE-CHECKLIST.md` | Executable |
| Rollback procedure | `docs/rollback.md` | Documented |
| DR / restore | `docs/disaster-recovery.md`, `make dr-drill` | Executable |
| Upgrade / deployment | `docs/upgrade.md`, `docs/deployment.md` | Documented |
| Incident playbooks | `docs/runbooks/` — audit-integrity, event-delivery, policy-automation, timeline-and-compliance | Documented |
| ADR convention | `docs/adr/` — 37 ADRs | By example |
| Guard inventory | `docs/architecture-guards.md` | Documented |
| Eliminated-defect ledger | `docs/TRUST-REGISTER.md` | Documented |
| Engineering law | Execution Constitution Parts 11–15 | Ratified |

**Rule: if a procedure can be executed, it belongs in the Makefile or a guard —
not here.** This document shrinks as automation grows. A growing EOS is a
regression.

---

# Part I — Capability Workflow

The sixteen stages, their law, and their exit criteria are **Constitution Part 11**.
This adds only what the Constitution does not define: owner, tooling, and handoff.

| Stage | Owner | Command / Tool | Handoff artifact |
|---|---|---|---|
| 0 Inventory | Any engineer | `make inventory` *(see Part V — not yet automated)* | Inventory table |
| 1 Discovery | Proposing engineer | — | Capability Proposal (Part VII) |
| 2 Investigation | Proposing engineer | `grep`/`psql`, cited file:line | Evidence Matrix |
| 3 Dependency analysis | Architect | — | Graph with VERIFIED/HYPOTHESISED labels |
| 4 Blueprint | Architect | Template, Part VII | Blueprint |
| 5 Implementation plan | Tech lead | — | Milestones, each deployable |
| 6 Adversarial review | **An engineer who did not author** | — | Attack log incl. failed attacks |
| 7 Gates | CTO or delegate | — | Gate verdicts |
| 8 Authorization | CTO | — | Certificate + selection ADR |
| 9 Implementation | Implementing engineer | `make test lint vet` | Commits tracing to milestones |
| 10 Verification | Implementing engineer | `make test-integration`, mutation harness | Mutation evidence |
| 11 Production validation | On-call | `make dr-drill`, SLO check | Rollback **executed** |
| 12 Post-implementation audit | Tech lead | — | PIA (Part VII) |
| 13 Re-inventory | Any engineer | Stage 0 repeated | Updated inventory |
| 14 Graph refresh | Architect | — | Recalculated centrality |
| 15 Next selection | CTO | — | Selection ADR |

**Handoff rule.** A stage is complete when its artifact exists and the next
owner has accepted it. Verbal handoff is not handoff.

---

# Part II — Playbooks

**Existing playbooks are authoritative.** Only the gaps are written here, and
each is compressed to the decisions that are easy to get wrong.

| Activity | Playbook |
|---|---|
| Incident: audit integrity / event delivery / policy / timeline | `docs/runbooks/` |
| Release · Rollback · DR · Upgrade | `docs/` (see table above) |

### New: Schema evolution
Additive only. Never edit an applied migration — `atlas.sum` is checksummed and
CI fails. New table → decide tenant-owned vs global **before** writing DDL;
tenant-owned enters `TenantOwnedTables`, global enters `globalRegistryPrefix`.
Pair a rollback. Run `make migrate-hash` in the same commit.
**Only one workstream may add a migration at a time** — `atlas.sum` is a single
append point.

### New: API evolution
Route and OpenAPI change in the same commit or both contract guards fail.
Breaking changes are caught by `make contract-breaking` **on PRs only** — a
direct push to master bypasses it. Every route declares its authorization.

### New: Background worker
Claim atomically; hold a lease; charge the attempt on claim; write outcomes
under a detached context; observe `ctx.Done()`. Six guards enforce this
(`internal/arch/`). Prefer extending an existing queue to adding one.

### New: Architecture guard creation
Derive the subject set from the tree — never a list (Law 14.1). Anti-vacuity is
per subject (Law 14.2). Mutation-test both directions before merging: break the
thing, and break the detector.

### New: Mutation testing
Mutate **semantics, not identifiers** (Law 14.3). A survivor is a defect until
another control is shown to catch it (Law 14.4). Record survivors and their
disposition in the commit message.

### New: Security change
Threat model per Constitution Part 3. Least privilege at the database: grant
what the writer needs, revoke the rest, assert both in an integration test.
Privileges do not survive `pg_restore` unless explicitly restored.

### New: Performance change
Measure before and after with `EXPLAIN` inside a transaction with
`SET LOCAL enable_seqscan = off` — a pooled `Exec` then `Query` takes two
connections and the setting silently will not apply. Assert `Index Cond`, and
assert the absence of `Sort` where ordering matters.

### New: Observability change
Follow existing naming: `oneops_` prefix, `_total`/`_seconds` suffix,
`{operation,outcome}` labels, no per-entity labels. Register into the existing
registry.

---

# Part III — Rhythm

Cadences are **Constitution Part 8**. This adds only the capability artifact
each cadence produces.

| Cadence | Capability artifact | Evidence required |
|---|---|---|
| Daily | — | Blockers only |
| Weekly | Stage progress | The artifact of the current stage |
| Sprint | Milestone demo | Gates green; mutation evidence |
| Release | Certificate | Rollback executed, not written |
| Quarterly | Re-inventory + graph refresh | Freshness metrics (Part VI) |
| Annual | Constitution review | Part 8 |

---

# Part IV — Checklists

`docs/RELEASE-CHECKLIST.md` is authoritative for release. Three checklists do
not exist elsewhere:

**Inventory (Stage 0/13)** — enumerate by artifact, not concept: every package,
table, route, migration. Classify each. Reconcile route count against
`server.go`. *(A prior inventory omitted a 1,755-LOC package with 24 routes
because it enumerated concepts.)*

**Blueprint (Stage 4)** — every section answered; no "TBD"; facts and hypotheses
separated; rollback stated; guards and mutations named before code exists.

**Post-implementation audit (Stage 12)** — what the plan got wrong; which
estimates were off and by how much; which hypotheses resolved; which assumptions
expired; what the next capability inherits.

---

# Part V — Automation Model

| Activity | State | Command |
|---|---|---|
| Build · vet · lint · unit · race | **Fully automated** | CI + `make test lint vet` |
| Integration tests | **Fully automated** | `make test-integration` |
| Architecture + contract guards | **Fully automated** | included in `go test ./...` |
| Migration validation | **Fully automated** | CI `migrations` job |
| Security scan | **Fully automated** | CI gitleaks + trivy |
| Breaking-change check | **Semi** — PR only | `make contract-breaking` |
| DR drill | **Semi** — on demand | `make dr-drill` |
| Mutation testing | **Manual** | ad-hoc harness |
| Repository inventory | **Manual** | — |
| Dependency graph | **Manual** | — |
| ADR validation | **Manual** | — |

**The three manual rows are the EOS's own backlog.** Each should become a
Makefile target; when it does, delete its row from this table and its checklist
from Part IV.

---

# Part VI — Metrics

Computable from the repository. No metric is listed that cannot be measured.

| Metric | Definition | How |
|---|---|---|
| Guard health | Guards passing / total | `go test ./internal/arch/ -v` |
| Guard growth | Guards added per capability | `git log` on `internal/arch/` |
| Mutation score | Killed / (total − proven equivalent) | Harness output |
| Inventory freshness | Days since last Stage 13 | PIA date |
| Graph freshness | Days since last Stage 14 | Selection ADR date |
| ADR freshness | Capabilities shipped without a selection ADR | Should be **0** |
| Rollback success | Rollbacks executed / releases | Release record |
| Test:prod ratio | Test LOC / production LOC | `wc -l` |
| Debt trend | Open register items by class | Debt register |
| Lead time · deploy frequency | Commit → production | Git + deploy log |

**Targets are set per capability, not globally.** The only fixed target is ADR
freshness = 0.

---

# Part VII — Templates

**Capability Proposal** — Gap (measured, with command) · Why now · Reverse
dependencies · Prerequisites · Falsifying evidence.

**Blueprint** — Purpose · Responsibilities · Non-responsibilities · APIs ·
Domain · Persistence · Events · Security · Authorization · Tenancy ·
Extension · Config · Failure modes · Recovery · Performance · Scale · HA ·
Observability · Testing · Guards · Mutations · Migration · Rollback ·
Acceptance · DoD. **Facts and hypotheses in separate sections.**

**ADR** — follow `docs/adr/` by example. Cite repository evidence. Immutable;
superseded, never edited.

**Implementation plan** — per milestone: Objective · Deliverables · Schema ·
API · Testing · Migration · Rollback · Risk · Exit criteria. Each independently
deployable.

**Rollback plan** — Trigger · Steps · Point of no return · Window · **Date last
executed**.

**Post-implementation audit** — Planned vs actual · Estimate error ·
Hypotheses resolved · Assumptions expired · What the next capability inherits.

---

# Part IX — Standing Manual Gates

Manual gates are recorded here because they cannot be automated. Each carries an
owner, a pass criterion and a termination condition. **A gate without a
termination condition is not a gate; it is a permanent tax.**

## E9 — ADR citation extraction (PKG)

| | |
|---|---|
| **Owner** | `<placeholder — assign before S5.3 starts>` |
| **Authority** | May remove E9 entirely. |
| **Pass criterion** | At least 8 of 10 sampled ADR references resolve correctly. |
| **Failure** | Delete E9. Do not weaken confidence guarantees. |
| **Frequency** | Once, before S5.3 completes. |
| **Termination** | Successful validation permanently limits E9 findings to advisory. |

---

# Part VIII — Operating Principles

Engineering law is **Constitution Part 14**. These are habits, not laws.

1. **Read the migration before inferring the schema.** Two planning cycles were
   lost to inferring a missing capability that a migration had already delivered.
2. **Check the fixture before believing the failure** (Law 14.5).
3. **Verify the premise you were handed.** Briefs have twice asserted work that
   did not exist; each cost one command to disprove.
4. **When a guard and a test disagree, the semantics decide** — not the name.
5. **Prefer deleting this document to extending it.** Every row that becomes a
   Makefile target is a row that can no longer go stale.

---

*Operational handbook. Derives all authority from the Execution Constitution.
Amend freely — no ratification required.*

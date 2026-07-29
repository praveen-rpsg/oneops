# EVR-005 — Cross-tenant key confusion (entry 1)

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Auditor** | Acting CTO / Evidence Authority |
| **Trust Register entry** | 1 |
| **Associated authority** | none — commit `8ed1e46` only (the weakest provenance in the register) |
| **Confidence** | **VALIDATED** — class confirmed closed. ECL-2 → **ECL-5**. Enforcement maturity **1 → 5 (discovery) / 4 (justification)**. |

## Weighted ranking (programme evidence)

Scored per the weighted model — enforcement maturity 40%, architectural
authority 25%, evidence quality 15%, blast radius 10%, hidden siblings 10%
(higher = more urgent):

| Entry | Maturity | Authority | Evidence | Blast | Siblings | **Score** |
|---|---|---|---|---|---|---|
| **1** — cross-tenant idempotency key | L1 (int only) 9 | 9 | **9 (no ADR)** | 9 | 8 | **8.90** |
| 10 — operator audit-guard removal | L1 9 | 8 | 4 | 8 | 5 | 7.50 |
| 16 — demoted leader keeps workers | L1 9 | 7 | 4 | 7 | 4 | 7.05 |
| 8 — restore inconsistency | L1 9 | 6 | 4 | 7 | 5 | 6.90 |
| 13 — concurrent-boot migration race | L1 9 | 6 | 4 | 4 | 3 | 6.40 |
| 23 — unaudited destruction | L2/3 4 | 8 | 4 | 8 | 3 | 5.30 |
| 3 — metrics listener | L2 5 | 4 | 9 (no ADR) | 5 | 3 | 5.15 |
| 9 — schema weakening | L4 3 | 9 | 4 | 8 | 3 | 5.15 |
| 12 — multi-replica execution | L2/3 4 | 7 | 4 | 7 | 4 | 5.05 |
| 24 — §9.1 constitutional inputs | L3 3 | 8 | 4 | 8 | 3 | 4.90 |

Entry 1 ranks first on three of five axes: the weakest enforcement tier
(integration test only), the weakest provenance in the entire register (**no ADR
— a commit message is the only record of the decision**), and a cross-tenant
authority boundary.

## Original claim and evidence

`idempotency_key.id` was the client's `Idempotency-Key` header used as a
**global** primary key on a tenant-scoped table, so one tenant's key resolved
another tenant's row — and one tenant could disable another's idempotency by
claiming a key first. Row-level security does not help: it filters which rows are
*visible*, not which key space they *occupy*. Fixed by making the key
`(tenant_id, id)` and the conflict target the full primary key.

## Fresh investigation

**Class:** *a key whose value a client supplies, used as a global key on a
tenant-scoped table.*

**Sibling search, schema-derived.** Every unique index on every table in
`TenantOwnedTables` was enumerated from the live catalogue — 16 keys. Two contain
`tenant_id`. The remaining fourteen are safe for one of two reasons: the identity
is platform-generated (`cfg_id`, `wh_`/`pol_`/`rpl_` ids, content-derived
delivery/execution ids), or the key is transitively scoped through a leading
column that identifies an already-scoped row (`cfg_id`, `chain_id`).

**No sibling instance.** Notably `audit_event.uq_audit_event_id (chain_id,
event_id)` derives `event_id` from the client's `Idempotency-Key` — but it is
confined to a chain, and a chain is a `cfg_id`, so two tenants cannot collide.

**The load-bearing assumption was tested live.** Several justifications rest on
"the platform generates the identity". A `POST /v1/artifacts` carrying
`"cfg_id":"ATTACKER-CHOSEN-ID"` was ignored and a server ULID assigned; zero rows
bear the chosen id. But the repository *does* honour a pre-set id by design (the
governance engine and replay construct rows with known ids), so the guarantee
lives entirely at the transport boundary — which is now pinned by its own guard.

## Root cause

None in the implementation. The defect was in the evidence: a cross-tenant
authority boundary held at maturity Level 1 with no architectural record.

## Authority produced — **EVR-005** only

No ADR: no implementation defect. No AR: no competing design. No CMR: no
constitutional question.

## Class status — **CLASS CLOSED**

## Evidence confidence — **ECL-5** (from ECL-2)

Independent reproduction (live attempt to supply an entity identity), whole-tree
and schema-derived completeness, structural enforcement, mutation verified,
self-validation verified.

## Enforcement maturity — **Level 1 → Level 5 discovery / Level 4 justification**

An honest split, and worth stating precisely: **a purely schema-derived proof of
this class is impossible.** Whether a key is safe depends on whether its value is
client-supplied, which is a property of the code, not the catalogue.
`idempotency_key.id` and `webhook.id` are both single-column keys on tenant-owned
tables; one was a defect and the other is not, and nothing in the schema
distinguishes them.

So the guard does what the schema *can* do — **discover** every unique key on
every tenant-owned table automatically (Level 5) — and forces anything not keyed
by `tenant_id` to carry a written justification (Level 4). A new table or a new
unique key fails the build until someone records why it cannot collide. Stale
justifications for keys that no longer exist also fail, so the list cannot drift
away from the schema.

## Mutation verification and self-audit

| Control | Result |
|---|---|
| Expose `cfg_id` on the create DTO (false negative) | fails, naming `createRequest` |
| Remove a justification, simulating a new unjustified key | fails: *"UNJUSTIFIED KEY SCOPE: webhook.webhook_pkey over (id)…"* |
| Add a justification for a non-existent key (drift) | fails: *"stale justification for ghost_table.ghost_idx"* |
| Vacuity | both guards fail explicitly if they find no keys / no DTOs |
| Determinism | both are catalogue and source reads; no concurrency, no timing |

A first attempt at this sweep tried to classify safety purely from schema
metadata (surrogate-PK and foreign-key routes) and produced **eight false
positives** — it flagged every correct key on `configuration_object`,
`audit_event`, `configuration_metadata`, `artifact_version` and
`dependency_edge`. That failure is what established the limit above and is
recorded rather than quietly fixed: a guard tuned until it passes teaches nothing
about why it passes.

## Residual risk

- The justification list is a human artefact. The sweep forces one to exist and
  to correspond to a live key; it cannot check that the stated reason is *true*.
- Entry 1 still has **no ADR**. This EVR is now the architectural record of the
  decision; the register's provenance column remains a bare commit hash, and that
  is a documentation gap rather than an engineering one.
- The transport guard covers `createRequest` and `patchRequest`. A future
  client-facing DTO that writes a tenant-owned table would need adding — the same
  residual enumeration acknowledged in EVR-003.

# ADR-GOV-001: Extension records its edge inside the constitutional transaction

- **Status:** Accepted
- **Date:** 2026-07-23
- **Constitutional authority:** Configuration State Model §8 (Extension — *"base **Extended By** += successor; base Authority UNCHANGED"*; Audit: *"Event row; dependency edge"*)
- **Laws engaged:** Vol II Part 11 — Law 7 (Composition before Duplication), Law 12 (append-only history)
- **Relates to:** `ADR-AUDIT-005` (the single atomic constitutional mutation); M4 WP-2
- **Evidence:** `docs/milestones/M4-validation-engine-plan.md`

## Context

`ADR-AUDIT-005` establishes that the governance engine owns exactly one atomic mutation per operation: the Configuration Object's dimensional change and its audit append commit in a single transaction, or neither does.

Extension is the first §8 operation whose constitutional effect is **not dimensional**. §8 is explicit that the base's dimensions are unchanged — Authority in particular stays exactly what it was. The operation's actual effect is relational: *base Extended By += successor*, recorded as an `extends` edge, and §8's Audit column names both outputs together — *"Event row; dependency edge."*

This raised the question the M4 plan flagged as risk R3: does writing the edge inside the governance transaction *widen* the ADR-AUDIT-005 boundary?

## Decision

**The edge is written inside the operation's single transaction.** The `governance.Store` port gains `RecordEdge(ctx, tx, from, to, kind)`, called by the engine between the dimensional apply and the audit append, on the engine's own `pgx.Tx`.

This is **not** a widening. ADR-AUDIT-005's guarantee is that *the operation's mutation and its audit event commit atomically*. For Extension the mutation **is** the edge. Writing it outside the transaction would permit exactly the two states ADR-AUDIT-005 exists to forbid: a committed audit event asserting an extension that has no edge, or an edge with no audit event. Applying the same principle to an operation whose mutation happens to be relational preserves the guarantee rather than stretching it.

Three consequential sub-decisions:

### 1. No existence check — the schema enforces referential integrity

`dependency_edge.from_cfg` and `to_cfg` are foreign keys to `configuration_object(cfg_id)`, and `uq_edge_from_to_kind` is unique. A missing endpoint therefore surfaces as `domain.ErrNotFound` and a repeated extension as `domain.ErrConflict`, reusing the existing `isForeignKeyViolation` / `isUniqueViolation` mappers. The engine performs no separate existence query — a check that the database already performs atomically, and that a separate query could not perform without a race.

### 2. `successor_id` is `omitempty` in the audit payload

The audit payload gains `successor_id`, emitted only by Extension. It is `omitempty` so all seven pre-existing operations marshal **byte-identically** to before the field existed. The hash chain over already-committed events is therefore untouched, satisfying Law 12 (audit is append-only and never rewritten). `TestNonExtensionPayloadOmitsSuccessor` pins this.

### 3. The "responsibilities not re-owned" precondition is deferred

§8's Extension precondition has two parts: the successor depends on / inherits the base, **and** the base's responsibilities are not re-owned. The second is decided by M3.3's `ResponsibilityEvaluator`. Wiring that evaluator into governance is WP-1 (Replacement), where the full four-part Replacement Test is bound.

Duplicating the responsibility comparison here would violate Law 7 (Composition before Duplication). This work package therefore enforces the **structural** preconditions only — successor required, no self-extension, referential integrity — and defers the responsibility conjunct to WP-1. This is the same incremental convergence the ARB accepted for M3.1's F1 finding: a deliberate milestone deferral, not a defect.

**Until WP-1 lands, the engine will record an Extension whose successor re-owns all of the base's responsibilities** — a case that §8 says should be a Replacement. The direction of the residual error matters and is safe: the base remains Active, so the CVP error (wrongly demoting a base to Historical) remains structurally impossible.

## Consequences

**Easier.** Extension is now a first-class operation, and the CVP error is impossible by construction rather than by convention: Extension is dimension-preserving from every starting authority (`TestExtensionNeverProducesHistorical`). The `plan.Edge` mechanism generalizes to Replacement in WP-1 with no further interface change.

**Harder.** `governance.Store` gained a method, so every implementation must supply it — two in this repository, both updated. This is deliberate: an optional, type-asserted port would let a store that cannot record edges silently succeed without one, which for a constitutional operation is a correctness hole. Compile-time enforcement is the right failure mode.

**Known behaviours, recorded rather than fixed:**

- **`row_version` increments on Extension** even though no dimension changed. The object's governed state *did* change relationally, so invalidating the client's ETag is correct.
- **`ON DELETE CASCADE` on `dependency_edge`.** §8 Deletion checks *incoming* edges (dependents); an extending successor's edge is *outgoing*, so deleting the successor is permitted and silently removes the edge. **ARB-reviewed 2026-07-23: NO ACTION — ratified authority already answers this.** A deletable successor is provably Non-Normative, so its `extends` edge conferred no authority and its removal cannot change the base's computed Authority. Proof: §8 Deletion requires `Retention = Working Material` **and** no dependents; the Active set is seeded from `Retention = Current Baseline` roots (§9.1 step 2, §9.3-1), and an artifact has exactly one Retention Class (§4) — so a Working Material artifact is never a baseline root; and the only other route into the Active set is an *incoming* depends/extends edge from an Active artifact, which is a dependent and therefore already bars Deletion. The Extension's audit event survives regardless (Law 12), satisfying Vol III §6.4: *"nothing, including a removal, ever leaves the Timeline without a trace."*
- **The `INSERT` in `RecordEdge` restates the one in `GraphRepo.CreateEdge`.** They cannot share code today: the graph repository is pool-scoped, and the engine requires a transaction-scoped write. Refactoring `GraphRepo` to accept a querier would have broken M2's 0-diff discipline for no functional gain.

## Reversal

`git revert` the WP-2 commit. The change is additive: no migration, no schema, no data. Reverting removes the `extend` endpoint and returns `OpExtension` to `ErrUnsupportedOperation`. Any `extends` edges already written remain valid M2 graph data and are read correctly by the M3 authority resolver, which has always understood `extends` — so a revert leaves no orphaned or unreadable state.

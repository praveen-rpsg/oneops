# ADR-CP6: Governance owns every Lifecycle mutation; "descriptive" classifies meaning, not ownership

- **Status:** Accepted
- **Date:** 2026-07-23
- **Authority:** §1 (four dimensions), §3 (Lifecycle), **§8 header — *"No dimension changes except as an operation specifies"***, CI-5
- **Binding:** ADR-CP5, CI-1…CI-5, RFC-AUTH
- **Does not amend the Constitution. Does not reopen ADR-CP5 or CI-5.**

## Measured Position

| Writer of the Lifecycle column | Values it can produce |
|---|---|
| `governance_store.go:54` (`ApplyDimensions`) | Ratified · Approved · Suspended · Deprecated · Withdrawn |
| Registry create (`ValidateInception`) | Draft · In Progress *(inception only)* |
| Registry PATCH | **none** — CP-5.1 removed the `SET lifecycle` branch |

**Exactly one writer of the column remains.** **Complete and In Review are produced by nothing.** Their only production references are `transition.go:74,81`, where §8 Ratification and Approval *read* In Review as a precondition.

A consequence worth recording: because In Review is unproducible, §8 Ratification's precondition *"Draft/In Review complete"* can only ever be satisfied from **Draft**. Half of that precondition is unreachable.

## Q1 — What differentiates a constitutional from a descriptive lifecycle transition?

**Meaning differs; governance does not.**

A **constitutional** value changes the artifact's *standing* — whether and how it governs (§2). A **descriptive** value records the artifact's *work state* — §3: *"Where is this artifact in its own evolution?"*, and *"Lifecycle **never** implies Authority."* CI-5's test holds: every §8 operation names an *"Authority (who)"*; nobody authorises that a program finished executing.

**But that distinction is semantic, not jurisdictional.** §8's header governs **dimension changes**, not constitutional-meaning changes: *"No dimension changes except as an operation specifies."* Lifecycle is one of §1's four dimensions. **Therefore every write to Lifecycle — whatever the value means — falls under §8.**

CI-5 classified Complete as descriptive. That classifies **the value**, and it does not create an exemption from §8's exhaustiveness clause.

## Q2 — Who owns descriptive lifecycle mutation?

**Governance — the same owner as constitutional lifecycle mutation. There is no separate descriptive owner.**

## Q3 — Should descriptive lifecycle transitions be audited?

**Yes, necessarily.** If Governance owns them, they occur through §8 operations, and §8 assigns an *"Event row"* to every operation except Historical Preservation. There is no mechanism by which a Lifecycle write reaches storage unaudited, and inventing one would reproduce the exact defect CP-0.2 measured.

## Q4 — May repositories expose descriptive lifecycle mutation directly?

**No.** Lifecycle is **one column**. A repository writer plus a governance writer is two owners of one constitutional state — the root cause CP-0.1 identified. ADR-CP5 confines the repository to persistence, and that boundary does not bend for values whose *meaning* is descriptive.

## Q5 — Can descriptive and constitutional values coexist in one dimension?

**They already do, and this ADR cannot change it.**

§3 defines nine values mixing standing states (Ratified, Approved, Suspended, Deprecated, Withdrawn) with work states (Draft, In Review, In Progress, Complete). **Lifecycle is two semantic concerns wearing one name** — which is precisely why ownership looked hard: one column cannot have two owners without recreating the defect.

Decomposing Lifecycle would alter §1's four ratified dimensions. **That is a constitutional amendment and outside this ADR's authority.** Recorded as an amendment candidate, not decided.

## Option Analysis

| Option | Verdict |
|---|---|
| **A — Governance owns both** | **ADOPTED.** Single writer preserved. Justified by §8's exhaustiveness over *dimension* changes, not adopted by default |
| B — Execution/Workflow owns descriptive | **Rejected.** Creates a second writer of one column. It also names an owner that **does not exist** — no workflow engine is present; NATS and Temporal were never adopted (ADR-ARCH-002). Speculative architecture |
| C — Repository owns descriptive | **Rejected.** Contradicts ADR-CP5 and reintroduces multiple ownership |
| D — Alternative | **Rejected.** No architecture assigns a second owner to one column without recreating the measured defect |

**Only Option A avoids recreating multiple ownership.** B and C both fail the single-writer principle on the same column.

## Decision

**Governance is the sole owner of every Lifecycle mutation.**

**Consequence, stated plainly: Complete and In Review remain unreachable.** §8 provides no operation producing either, and this ADR may not invent one. That is not an architectural defect to be routed around — it is the correct behaviour of a system whose dimension changes are exhaustively governed by a closed operation set.

**The gap is constitutional, and it is now symmetrical.** CI-5 routed **In Review** to the Amendment Process. On this reading **Complete joins it**: whatever its value *means*, writing it is a dimension change requiring a §8 operation that does not exist.

## Required Architectural Changes

**None.** The measured position already satisfies this decision: one writer, governance-owned, no descriptive bypass. CP-1.2, CP-1.3, CP-5.1 and CP-5.2 produced it.

This ADR **ratifies the current state rather than changing it**, and refuses the restoration of a PATCH capability on the grounds that it once existed — historical implementation is not architectural authority.

## Migration Impact

**None.** No code, schema, contract or API change.

**Operational impact is real and must not be understated:** an artifact created `in_progress` cannot be recorded as `complete`, and no artifact can be marked `in_review` before Ratification. Until the Amendment Process acts, executable artifacts have no way to record completion. **That is a known, accepted constraint of constitutional conformance — not an oversight.**

## Relationship to CP-1.4

Unchanged and still pending re-scope. Registry DELETE must be removed or delegate to governance; ADR-CP6 reinforces the principle — one owner per constitutional state — without altering CP-1.4's disposition.

## Relationship to CI-5

**Accepted in full and extended.** CI-5's classification of Complete as descriptive and In Review as an omission both stand. ADR-CP6 adds the ownership consequence CI-5 did not reach: **descriptive meaning confers no exemption from §8**, so Complete's unreachability has the same character as In Review's.

CI-5's open question — whether §3's ratified state diagram is normative or illustrative — **remains the governing uncertainty**. If the diagram is normative, §8 omits four transitions (`submit`, `begin execution`, `finish`, `resume`). If illustrative, only the two unreachable states need constitutional action. **That question should be answered before any amendment is drafted.**

## Relationship to RFC-AUTH

**Consistent and mutually reinforcing.** RFC-AUTH established one authoritative representation and one writer for Authority; ADR-CP6 establishes one writer for Lifecycle. Both derive from the same principle CP-0.1 measured. **WP-A2 remains blocked** on CI-2's §8 Approval derivation gap; this ADR does not affect it.

## Consequences

**Positive.** Exactly one owner for every Lifecycle mutation. No speculative execution engine is introduced. The gap is relocated to where it belongs — the Amendment Process — rather than papered over with a second writer.

**Negative.** Two §3-defined states are unwritable, and executable artifacts cannot record completion. This is the cost of a closed operation set, and it is visible rather than hidden.

**Recorded, not resolved.** Lifecycle conflates standing and work states in one dimension. Decomposition is an amendment candidate and the likely long-term answer, but it is not this ADR's to make.

## Reversal

Nothing to reverse — no change is made. Reversing the *decision* would mean admitting a second writer to the Lifecycle column, which requires reopening ADR-CP5 and CP-0.1's measured root cause.

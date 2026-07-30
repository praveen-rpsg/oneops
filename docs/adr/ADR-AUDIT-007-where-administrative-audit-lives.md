# ADR-AUDIT-007 — Where administrative audit lives

| | |
|---|---|
| **Status** | **RATIFIED — 2026-07-30.** One status, here, and nowhere else in this document. |
| **Resolves** | `OPS-S033` (G-6, "AR where administrative audit lives") |
| **Closes** | The question AR-001 placed under *"What is deliberately not decided here"* |
| **Unblocks** | `OPS-S031`, `OPS-S034`, `OPS-S035`, `OPS-S036`, `OPS-S037`, `OPS-S038` |
| **Related** | ADR-AUDIT-003/004/005/006 (the constitutional chain), ADR-TENANCY-001/002/003/004, ADR-IDENTITY-001/002, ADR-AUTHZ-001, AR-001 |
| **Author** | Constitutional Architecture Council |

## 1. Question

Administrative acts — registering a user, suspending an organisation, granting
or revoking a membership, redeeming an invitation — currently produce **no
record at all**. AR-001 measured this ("three admin creations, no record") and
then explicitly declined to resolve it.

May administrative audit reuse `audit_event`? If not, where does it live?

## 2. Repository evidence

Everything below was read from the repository or executed against the running
database. Nothing is inferred.

### 2.1 `audit_event` admits twelve operations, none administrative

`internal/store/migrate/sql/20260723000001_audit.sql:36-39`, confirmed live:

```
ck_audit_operation CHECK (operation = ANY (ARRAY[
  'ratification','approval','extension','replacement','suspension',
  'deprecation','withdrawal','archiving','historical_preservation',
  'deletion','baseline_freeze','amendment']))
```

Every permitted value is a **Configuration Object** operation. `user.suspended`
and `membership.granted` are not merely absent — they are refused by a CHECK
constraint.

### 2.2 A chain is a governed object, not a stream

`internal/domain/audit_chain.go:18` — `AuditChainID(cfgID) = cfgID`. The table is
`PARTITION BY LIST (chain_id)` with `PRIMARY KEY (chain_id, seq)`.

An administrative act has no Configuration Object, so it has no chain. The
mapping's own doc comment anticipates evolution *"to a tenant- or stream-scoped
key"*, but no such key exists today.

### 2.3 `audit_event` has exactly one writer

`INSERT INTO audit_event` occurs once in the tree
(`internal/store/postgres/audit_store.go:40`). Its appender is constructed once
and handed to one consumer — the governance engine
(`cmd/controlplane/main.go:207-208`).

ADR-AUDIT-005 binds that writer: *"a governance mutation can never exist without
its audit event, nor an audit event without its mutation."* An administrative
write to `audit_event` would produce an audit event with no governance mutation
— a direct violation, not a stylistic one.

### 2.4 The fan-out constraint, stated precisely

`internal/events/relay.go:81-101` walks **every** chain (`ListChainIDs`) against
**every** enabled webhook across all tenants (`ListEnabled`).

`internal/events/events.go:62-73`:

```go
if len(w.Operations) > 0 && !contains(w.Operations, ev.Operation) {
    return false
}
```

**An empty `Operations` list matches every operation**, and this is a *published
promise*, not an accident — `openapi.yaml:1684` documents `operations` as
*"Empty means all operations"*, with only `url` required.

So any administrative event written to `audit_event` carrying tenant T's id
becomes deliverable to T's existing catch-all subscriptions immediately, with no
subscription change and no consent step. Honouring that would be *correct*
behaviour under the published contract — which is precisely why the events must
not be there.

Two further delivery and read paths inherit the same property:

- **Replay** (`internal/events/replay.go:141-161`) re-reads all chains from the
  audit log and re-enqueues deliveries verbatim.
- **Read** — `/v1/governance/{id}/audit` and `/audit/events`
  (`server.go:225-226`) require only `PermRead`, the lowest tier.

### 2.5 Correction to AR-001's stated reason

AR-001 gives the constraint as: *"the relay fans out every chain to subscribers,
so administrative events placed there would leak administrative activity to
tenants."*

**The cross-tenant half of that is no longer true.** `domain.FanOut`
(`ownership.go:68-82`) confines every event to `SameOwner` subscribers before
`Matches` is consulted, and returns nothing for an event of unknown ownership.
Tenant A cannot receive tenant B's events.

The real constraint is narrower and firmer: a tenant would be **automatically
notified of platform-administrative acts performed upon it**, would be able to
**read them at `PermRead`**, and the events would be **replayable**. Combined
with §2.1–2.3, reuse is not merely unwise; it is structurally unavailable.

### 2.6 Immutability, retention, isolation

Live: `audit_event` and `audit_chain_head` are both `rls=true force=true`;
`tenant_id` defaults to `'system'`; triggers `trg_audit_event_no_row_mutate` and
`trg_audit_event_no_truncate` are installed. The retention worker prunes
`webhook_delivery` only (`internal/events/retention.go`) — audit history is never
swept.

### 2.7 A record defect, reported not repaired

AR-001 carries **two contradictory statuses**: the header says
*"DECIDED — 2026-07-29"*, and §Status at line 238 says *"**OPEN.**"* The S033
Definition of Done — *"AR merged with exactly one status"* — appears to
anticipate exactly this.

This ADR does not depend on resolving it: the administrative-audit question is
out of AR-001's scope under **either** reading. Repairing AR-001 is a separate
record action and is listed in §11.

## 3. Proven constraints

| # | Constraint | Evidence |
|---|---|---|
| C1 | Administrative operations cannot be written to `audit_event` | §2.1 CHECK |
| C2 | Administrative acts have no chain, and the table is partitioned and keyed by chain | §2.2 |
| C3 | `audit_event` has one writer, bound to governance mutations by ADR-AUDIT-005 | §2.3 |
| C4 | Anything on `audit_event` with a tenant owner is deliverable to that tenant's catch-all subscriptions, by published contract | §2.4 |
| C5 | The same content is replayable and readable at `PermRead` | §2.4 |
| C6 | Cross-tenant leakage is already prevented; the risk is same-tenant disclosure | §2.5 |
| C7 | Audit history is never pruned; immutability is enforced by triggers | §2.6 |

## 4. Options

### Option A — separate administrative audit store

A distinct append-only table with its own chain-head table, reusing the existing
hashing primitives.

- **Advantages** — no CHECK conflict (C1); its own chain namespace (C2); leaves
  `audit_event`'s single-writer invariant intact (C3); invisible to the relay,
  which reads `audit_event` alone (C4); not replayable (C5); not reachable from
  the `PermRead` governance routes.
- **Disadvantages** — a second append-only store. AR-001 Q4 names *"a second
  audit-like store"* as the duplicated-authority smell.
- **Constitutional** — none. The Vol IV §4.6 Audit Contract governs
  Configuration Objects; AR-001 itself classes these as *"platform records, not
  Configuration Objects."*
- **Operational** — one further table to verify and back up; its verification can
  reuse the existing chain verifier.
- **Migration** — additive; a new migration only.
- **Security** — strongest available: isolation becomes a property of *which
  table the row is in*, not of a predicate anyone must remember.

### Option B — a separate chain inside `audit_event`

Rejected. It still fails C1 (the CHECK), C3 (an audit event with no governance
mutation), and C2 (a chain that is not an object breaks `AuditChainID`'s
contract and the timeline's `WHERE chain_id = $1` object assumption). Decisively,
it does **not** solve C4 or C5: `ListChainIDs()` returns *all* chains, so
administrative chains would be walked and fanned out exactly like any other.
Fixing that requires bolting Option D on top.

### Option C — dual audit architecture

Rejected. Every disadvantage of B, plus two records of one fact that can
disagree, plus the loss of ADR-AUDIT-005's atomicity across two stores. This is
the duplicated authority AR-001 Q4 warns against, in its purest form.

### Option D — policy-based visibility

Rejected. It makes isolation a property of a predicate rather than of storage —
the exact failure mode ADR-TENANCY-002 (*"isolation is a property of wiring"*)
and `FanOut` exist to eliminate. Every present and future read path must
remember the filter. It would also require the published contract *"Empty means
all operations"* to silently become *"all operations except some"*. And it still
fails C1 and C3.

## 5. Constitutional review

| Authority | Finding |
|---|---|
| **ADR-AUDIT-003/004/006** | No conflict. Their subject is the constitutional chain, which is untouched. The new store reuses the same hashing, canonicalisation and chain-head-lock discipline. |
| **ADR-AUDIT-005** | **Preserved only by Option A.** B and C would create audit events with no governance mutation. |
| **ADR-TENANCY-001/002** | Satisfied. Isolation is structural, not predicate-based. |
| **ADR-TENANCY-003/004** | Satisfied. Ownership is recorded, never inferred. |
| **ADR-IDENTITY-001/002** | Consistent. §3.1's reasoning for `organization` applies verbatim: a column pointing **at** a boundary is not the row's owner. |
| **ADR-AUTHZ-001** | Satisfied, and self-enforcing — see §6.7. |
| **AR-001** | Closes its open question. Q4 (second history implementation) is answered in §6.10. |
| **Vol IV §4.6** | Not engaged. Administrative acts are platform records, not Configuration Objects. |

**No conflicts remain for Option A.**

## 6. Decision

**Administrative audit lives in its own append-only store, global and
platform-only, never delivered to subscribers.** The following are normative.
Schema, triggers and endpoints belong to `OPS-S034`/`S038`; this ADR fixes the
constraints they must satisfy, not their DDL.

**6.1 Purpose.** To make administrative action attributable and
non-repudiable — who did what, to whom, when — independently of the
constitutional chain.

**6.2 Scope.** Every mutation of `app_user`, `organization`, `membership`,
`invitation` and `tenant`, however performed. Reads are out of scope. Governance
operations remain on `audit_event` and are out of scope.

**6.3 Ownership.** The **platform** owns every row. Not a tenant, not an
organisation.

**6.4 Isolation boundary.** The store is **global**: no `tenant_id` ownership
key, absent from `TenantOwnedTables`, no row-level security. A subject
organisation or tenant is recorded as an **attribute** — the fact acted upon —
and never as an ownership or RLS key. This is ADR-IDENTITY-002 §3.1's reasoning
for `organization`, applied to a record whose subject is a tenant but whose owner
is not.

**6.5 Visibility.** Platform administrators only, through `requirePlatformAdmin`.
No tenant-facing route may read it. **It is not an event source**: the relay
reads `audit_event` alone, so administrative events are undeliverable by
construction rather than by filter — no webhook, no replay, no `PermRead` path.

**6.6 Retention.** Never pruned, matching `audit_event`. The retention worker
touches deliveries only and must continue to.

**6.7 Permissions.** Because the store is global — in the schema and absent from
`TenantOwnedTables` — the existing guard
`TestEveryGlobalRegistryRoute_RequiresPlatformAdmin` will **fail the build** the
moment the table is added, until it is recorded in `globalRegistryPrefix` with
either a platform-admin route prefix or `""`. The permission rule is therefore
enforced mechanically, not by review.

**6.8 Event lifecycle and chaining.** Hash-linked and append-only, reusing
`internal/audit`'s existing primitives. **One chain per organisation**, plus a
platform chain for acts with no organisation. A single global chain is refused:
ADR-AUDIT-006 serialises appends on the chain head under `FOR UPDATE`, so one
chain would serialise every administrative write platform-wide. Per-organisation
chains bound contention to one organisation's administrative rate and give a
natural verification unit.

**6.9 Failure model.** The administrative audit append and the administrative
mutation **commit in one transaction, or neither does** — ADR-AUDIT-005's shape
applied to administration. An unauditable administrative act must fail. This is
what makes `OPS-S035`'s chokepoint enforceable rather than advisory.

**6.10 Second store, not a second implementation.** AR-001 Q4 is answered by
construction: the new store reuses `internal/audit`'s hashing, canonicalisation,
event-id derivation and chain-head-lock discipline. What is new is *where rows
land and who may see them* — which is the entire point — not *how history is
sealed*. Re-deriving the sealing logic is forbidden.

**6.11 Migration strategy.** Additive, in a new migration; no completed migration
is altered. Not added to `TenantOwnedTables`. No backfill: acts before the store
existed were never recorded, and inventing them would be fabricating history.
`atlas.sum` regenerated in the same commit.

**6.12 Operational model.** Verified by the existing chain verifier, extended to
the new chains. Backed up with the primary database. Growth is bounded by
administrative rate, which is orders of magnitude below event traffic.

**6.13 Future extensibility.** New administrative operations are new values in
the store's own vocabulary and require no change here. Should administrative
history ever need to reach a tenant, that is a **new ADR** proposing a deliberate
export path — never a relaxation of §6.5.

## 7. Risks

| Risk | Mitigation |
|---|---|
| Two append-only stores drift in discipline | §6.10 — shared primitives, no re-derivation |
| An administrative path forgets to audit | `OPS-S035` chokepoint + `OPS-S036` derived guard; §6.9 makes omission a failed transaction |
| Per-organisation chains proliferate | Growth is bounded by organisation count; partitioning is available exactly as for `audit_event` |
| The store later grows a tenant-facing reader | §6.5 is normative; §6.13 requires a new ADR |
| `PermRead` drift on a future route | §6.7 — the global-registry guard fails the build |

## 8. Consequences — stories unblocked

| Story | Why it is now executable | Assumption that becomes valid |
|---|---|---|
| **S031** Membership add/remove/list with audit | The audit target its acceptance requires now exists as a decided design | "Every membership change writes an admin audit record" has a defined destination and transaction rule (§6.9) |
| **S034** Admin audit schema + immutability triggers | Placement, ownership, isolation, chaining and retention are fixed (§6.3–6.8) | Triggers may be modelled on `audit_event`'s, which are proven present (§2.6) |
| **S035** Audit interception at the storage chokepoint | §6.9 makes one write path mandatory rather than a preference | "One write path not per-handler calls" is now a constitutional requirement |
| **S036** Arch guard: every admin mutation passes the chokepoint | §6.2 defines the subject set as mutations of five named tables — derivable from the schema | A derived subject set exists; the guard need not enumerate handlers |
| **S037** Admin events do not reach tenant subscribers | Becomes a **structural** assertion, not a filter test: the relay reads `audit_event` alone (§2.4, §6.5) | "Subscriber receives zero admin events" is provable from where rows live |
| **S038** Admin audit query API | §6.5 and §6.7 fix the permission, and the guard enforces it | Platform-admin-only is mechanically enforced, not reviewed |

## 9. Sprint 2 impact

Unchanged and unblocked. `OPS-S024` and `OPS-S051` still require
membership-based tenant resolution; `middleware.go:182` still resolves tenant
from the token claim. This ADR neither helps nor hinders that work — but the
authorisation changes in `OPS-S042`–`S051` mutate `role`, `grant` and
`role_assignment`, and **§6.2 will need extension to cover them**. That extension
is an amendment to this ADR's scope table, not a new decision.

## 10. Non-goals

No code, migration, table, endpoint or guard is created by this document. No
existing architecture is redesigned and no prior ADR is reopened.

## 11. Record action arising

`AR-001` carries two contradictory statuses (§2.7). This should be corrected to
exactly one before Sprint 1 closes. It does not block any story here.

**STATUS: RATIFIED**

# Trust Register

The running ledger of **vulnerability classes eliminated from the running
service**. An entry is admitted only when the class was proven exploitable
against the live system, remediated, re-attacked live to show the exploit now
fails, and locked shut by a test that fails the build if the class returns. Each
entry cites the ADR that carries the full evidence.

The rule this register enforces: eliminate the *class*, not the instance. A fix
that closes one path but leaves the category open is not an entry here.

**Class status is stated explicitly.** Every entry is a *verified class*, and any
entry whose class still has a surviving instance is marked **OPEN** with that
instance named. Two entries were found overstated on 2026-07-29 and are corrected
below — see *Class status* after the table. An entry that claims more than was
swept is a worse defect than the one it records, because it is trusted.

Legend for **Enforced by**: `arch` = build-failing architecture test; `int` =
integration test against a real PostgreSQL; `unit` = unit test; `startup` =
fail-closed startup validator.

| # | Vulnerability class | Eliminated by | Live proof | Enforced by | ADR |
|---|---|---|---|---|---|
| 1 | Cross-tenant idempotency-key confusion (one tenant's key resolves another's request) | Keys scoped to the supplying tenant | ✓ | int | — (`8ed1e46`) |
| 2 | Platform/tenant administration conflated (a tenant reaching platform administration) | Separate platform vs. tenant admin boundary | ✓ | arch, int | ADR-AUTHZ-001 |
| 3 | Metrics listener exposing the app surface | `/metrics` bound to its own listener | ✓ | arch | — (`8596e4b`) |
| 4 | Cross-tenant event relay (relay delivering across tenant boundaries) | Tenant-confined fan-out in the relay | ✓ | int | ADR-TENANCY-003 |
| 5 | Privileged-worker ownership drift (each worker trusting the queued row's own label) | One ownership framework, re-derived from the audit log | ✓ | arch, int | ADR-TENANCY-003/004 |
| 6 | Confused-deputy policy execution (a policy run against another tenant's event) | Ownership re-derived and authorized at execution, not production | ✓ | int | ADR-TENANCY-003 |
| 7 | Replay asserting authority (replay writing outside its lane) | Replay owns no authority; verification-only | ✓ | int | ADR-TENANCY-005 |
| 8 | Restore-inconsistency (an inconsistent backup silently trusted) | Recovery is a verification boundary; refuse inconsistent restores | ✓ | int | ADR-TENANCY-006 |
| 9 | Ownership-model schema weakening (a migration dropping an invariant the model needs) | Schema invariants validated at startup, fail closed | ✓ | startup, int | ADR-TENANCY-007 |
| 10 | Operator audit-guard removal / untrusted operational tooling | Operational tooling brought inside the trust model | ✓ | int | ADR-TENANCY-008 |
| 11 | Split-brain audit authority (chain head ambiguity) | Audit ownership verified against its root; fail closed on ambiguity | ✓ | int | ADR-AUDIT-004 |
| 12 | Multi-replica double-execution (every replica running the singleton workers) | Leader election on a PostgreSQL advisory lock | ✓ | arch, int | ADR-CONCURRENCY-001 |
| 13 | Concurrent-boot migration race (duplicate-key on `schema_migrations`) | Migrations serialised on a dedicated advisory lock | ✓ | int | ADR-CONCURRENCY-001 |
| 14 | Concurrent double-claim of a queue row (plain-SELECT claim; overlap double-send) | Atomic compare-and-set claim with a lease (`FOR UPDATE SKIP LOCKED`) | ✓ | int | ADR-CONCURRENCY-002 |
| 15 | **Non-dedup-able duplicate delivery** (random ids; a re-processed event becomes a new row with a new dedup key) | **Content-derived, idempotent production** (`DeliveryID`/`ExecutionID`); re-production collides on the primary key | ✓ | arch, int, unit | **ADR-CONCURRENCY-003** |
| 16 | **Demoted leader keeps running its workers** (lock-loss only logged; permanent two-leader overlap) | **Leadership context cancelled on lock loss; re-enters the election** | ✓ | int | **ADR-CONCURRENCY-003** |
| 17 | **Non-monotonic cursor** (blind overwrite; a stale/overlapping writer rewinds the watermark) | **Monotonic write (`GREATEST`); the watermark can only rise** | ✓ | arch, int | **ADR-CONCURRENCY-004** |
| 18 | **Unfenced completion of an evicted worker** (lease expires, row reclaimed; the stale worker's `MarkResult` resurrects a delivered row / corrupts the reclaimer's state) | **`MarkResult` fenced on the claim token (`claimed_at`); a stale write is rejected with `ErrStaleClaim`** | ✓ | arch, int | **ADR-CONCURRENCY-005** |
| 19 | **SSRF via tenant-supplied delivery URLs** (webhook / policy-action URLs dialed through a default client; loopback + `169.254.169.254` metadata + private ranges reachable; `last_status_code` an internal scanner oracle) | **`safehttp` dialer refuses non-public IPs at dial time (DNS-rebinding-safe); applied to both outbound clients; secure by default** | ✓ | arch, unit | **ADR-SECURITY-001** |
| 20 | **Unbounded retry of a row whose worker never reports back** (the reclaim path left `retry_count` untouched, so a crash-looping row was re-delivered/re-executed forever; the queue had no terminating state for it) | **The claim charges the attempt and enforces the budget: `ClaimDue` advances `retry_count` and dead-letters a row whose next attempt would exceed `max_retries`, atomically** | ✓ | arch, int | **ADR-CONCURRENCY-006** |
| 21 | **Outcome lost when the worker is stopped** (outcome written through the leadership context; a demotion mid-flight POSTed to the subscriber, recorded nothing, incremented the success metric anyway, and left the row claimed for unbounded re-send) | **Outcomes written on a context detached from the worker's cancellation (`WithoutCancel` + own deadline); a failed outcome write is reported, not swallowed** | ✓ | arch, int | **ADR-CONCURRENCY-006** |
| 22 | **A security invariant weakened after startup goes unnoticed** (schema invariants proven once at boot; RLS disabled post-startup produced a live cross-tenant read while the process reported ready, served traffic, and logged nothing) | **Continuous re-verification (`ops.Sentinel`) re-running the same startup validator; a breach fails closed — `/v1` refused, readiness red, workers stopped — and clears on repair** | ✓ | arch, int, unit | **ADR-SECURITY-002** |
| 23 | **Unaudited destruction of a governed object via a second, non-constitutional door** (`DELETE /v1/artifacts/{id}` issued a bare SQL delete enforcing only the protected-role rule: a ratified `current_baseline` object the engine refuses (409) was destroyed (204) with **zero** audit events, the dependents check skipped and its dependency edges silently cascaded away) | **Destruction has one door: the route executes the engine's §8 deletion, and the destructive method is removed from the repository *and* from the persistence contract — not hardened** | ✓ | arch, int, unit | **ADR-GOV-002** |
| 24 | **§9.1 constitutional inputs writable and erasable through the descriptive-metadata channel** (`responsibilities`/`citations`/`coverage` refused by PATCH but accepted at CREATE/BULK — a successor seeded that way turned a Replacement the engine refused (409) into one it granted (200) on a ratified `current_baseline` artifact; and a PATCH of an unrelated key returned 200 while **erasing** `responsibilities`) | **Enforced at the storage chokepoint both writers pass through, plus the inception boundary; a wholesale metadata clear spares them; one definition of the key set** | ✓ | arch, int | **ADR-GOV-003** |
| 25 | **A delivery record's destination was retroactively rewritable** — *one instance of a wider class; see the scope note below* (`webhook_delivery` stored only `webhook_id`; the destination was derived by joining to the mutable `webhook.url`, so one admin `PATCH` — 200, no audit event — rewrote where every past delivery through that subscription had gone) | **The delivery holds `delivered_to`, captured at attempt time in the same fenced UPDATE as the outcome; an unattempted outcome records nothing and erases nothing; reads never join to the subscription** | ✓ | arch, int | **ADR-GOV-004** |
| 26 | **A platform invariant enforced at only one of its two enforcement points** (`OwnershipValidator` ran at startup only, so a divergent ownership graph served `/v1` with `readyz=200` and 0 breaches while the same binary refused to boot on it — an instance would serve indefinitely but never restart) + **an outbound client outside the SSRF guard** (JWKS used a bare `http.Get`, unguarded and untimed) | **One `ops.Invariant` registry read by both the startup gate and the sentinel, with every validator required to be registered; and a whole-tree sweep for unguarded outbound HTTP instead of named call sites** | ✓ | arch, unit | **ADR-SECURITY-003** |
| 27 | **A work queue with no atomic claim and no fencing token** (`webhook_replay_job` had neither: 8 of 8 pending jobs claimed by two workers at once, and a stale worker overwrote the owner's `completed/42` outcome with `failed/0`) | **The claim is a compare-and-set under `FOR UPDATE … SKIP LOCKED` that stamps the token; the outcome write is fenced on it; and the guards derive the queue and cursor sets from the schema instead of naming them** | ✓ | arch, int | **ADR-CONCURRENCY-007** |

## Class status

**Audit coverage (2026-07-29).** Entries swept under the Class Elimination Law:
14, 17, 18, 19, 22, 25, 26, 27. Entries **1–13, 15, 16, 20, 21, 23, 24 have not
yet been swept** — they are recorded as verified on the evidence in their ADRs,
which is not the same as verified complete. Three of the six classes examined so
far were found overstated, so the unswept remainder should be treated as
*insufficiently verified* rather than closed.

| Class | Entries | Status | Remaining instances |
|---|---|---|---|
| Boundary verified only at boot | 22 | **CLOSED** (2026-07-29) | none — one registry now feeds the startup gate and the sentinel, and every validator must be registered (ADR-SECURITY-003) |
| Unguarded outbound HTTP client | 19 | **CLOSED** (2026-07-29) | none — whole-tree sweep, not named call sites (ADR-SECURITY-003) |
| Historical record derived from mutable state | 25 | **OPEN** | `policy_execution` does not record the action it ran (held under AR-001) |
| Non-exclusive claim on shared work | 14 | **CLOSED** (2026-07-29) | none — queue set derived from the schema, not named (ADR-CONCURRENCY-007) |
| Unfenced completion by a worker that lost its claim | 18 | **CLOSED** (2026-07-29) | none — same schema-derived sweep (ADR-CONCURRENCY-007) |
| Non-monotonic cursor | 17 | **CLOSED** (2026-07-29) | none — cursor set now derived from the schema (ADR-CONCURRENCY-007) |

### Reopened and re-closed on 2026-07-29

**Entry 22 (boundary verified only at boot) was reopened.** ADR-SECURITY-002
stated the principle — *"fail-closed at boot and fail-open at runtime is not a
policy, it is an accident of where the check happened to be called"* — and then
applied it to one of the two startup validators. `OwnershipValidator` stayed
boot-only. Proven live: a database with a divergent ownership graph served `/v1`
with `readyz=200` and zero breaches, while the same binary **refused to start**
on it. Closed by ADR-SECURITY-003: one registry, both enforcement points, and an
architecture test that fails if any validator is unregistered.

**Entry 19 (unguarded outbound client) was reopened.** ADR-SECURITY-001 guarded
"both outbound clients"; the JWKS fetch used a bare `http.Get` with no guard and
no timeout. Closed by ADR-SECURITY-003: the guard is now enforced by a whole-tree
sweep rather than by naming the clients someone remembered.

**Lesson recorded:** both survived because the enforcement named specific call
sites. An architecture test that enumerates known instances cannot close a class;
it can only pin the instances already known.

### Reopened and re-closed on 2026-07-29 (second audit)

**Entries 14 and 18 were reopened.** The atomic claim and claim fencing were
verified on `webhook_delivery` and `policy_execution`; `webhook_replay_job` is a
third claimed resource and had neither — no `claimed_at` at all, a plain
`SELECT … WHERE status='pending'`, and an unconditional outcome write. Proven
live: **8 of 8 pending jobs were handed to both workers simultaneously**, and a
worker that no longer owned a job overwrote the owner's `completed/42` with
`failed/0`. Closed by ADR-CONCURRENCY-007.

**Entry 17 re-verified and its guard replaced.** No third cursor exists, so the
class was in fact closed — but its enforcement named the two known cursors. The
guard now derives the cursor set from the schema, so the *claim* of completeness
is now backed by a completeness check rather than by two examples.

**The same failure mode, twice in two audits.** Enumerated enforcement was the
common cause in both this audit and ADR-SECURITY-003. Guards in this programme
must derive their subject set from the schema or the tree, not from a list.

## Scope correction — entry 25 closed an instance, not a class

Entry 25 was admitted as if it eliminated *"a historical record deriving from
mutable state."* It did not. It eliminated one instance of that class, and a
later sweep (2026-07-29) proved two siblings remain open:

**Status of the siblings (updated 2026-07-29, after AR-001 was decided):**

- **Instance 3 is now CLOSED** — a delivery records `signed_ts`, the timestamp it
  actually signed, and `DeliveryView` renders historical headers from it rather
  than from `now`. An unattempted delivery reports no headers
  (`ErrNotAttempted`) instead of minting a signature that never crossed the wire.
  Enforced by `arch.TestDeliveryView_DoesNotMintATimestamp` and the extended
  destination guards; mutation-verified.
- **Instance 2 remains OPEN** — it is scheduled under AR-001's decision as its own
  investigation (versioned policy revisions), and must not be closed by
  snapshotting an unredacted `action_config`.

Original finding:

- **`policy_execution` does not record what it did.** The action is
  `policy.action_type`/`action_config`, wholesale-replaceable by `PATCH`. Proven
  live: an execution that ran `webhook → /approved-action` reads as
  `http → /attacker-action` after one PATCH returned 200, with 0 audit events.
  Worse than the delivery case, because a policy action actively POSTs governance
  data outbound.
- **A delivery's headers are re-minted at read time.**
  `DeliveryView(d, wh.Secret, time.Now())` builds the timestamp from *now* and
  signs with the *current* secret, so the headers shown for a past delivery were
  never the headers sent.

Both are held under **AR-001**, which must decide how the platform records a
historical fact before either is implemented. This note stands until AR-001 is
decided; entry 25 claims the instance only.

## Guarantees stated, not overstated

The register records what was *eliminated*. Two properties are deliberately
*bounded, not removed*, and are stated as such wherever they are relied upon:

- **Delivery and execution are at-least-once, never exactly-once.** Exactly-once
  delivery of an outbound HTTP request is impossible (two-generals); it was
  disproved live and is not claimed anywhere (ADR-CONCURRENCY-002). The remaining
  duplicate is the crash between the outbound action and persisting its result —
  bounded by the claim lease, and dedup-able because every attempt of a logical
  delivery now carries the *same* stable `X-OneOps-Delivery` (ADR-CONCURRENCY-003
  made that key stable across re-production, which is what turns at-least-once into
  effectively-once for a compliant receiver).
- **Retries are bounded by attempts, not by wall-clock time.** A row is handed to
  a worker at most `max_retries` times in total, counting attempts whose outcome
  was never recorded (ADR-CONCURRENCY-006). Each of those attempts may still hold
  the row for a full claim lease before reclaim, so termination is guaranteed in
  attempts, not within any particular duration.
- **A weakened invariant is detected, not prevented.** Nothing inside the
  application can stop an operator with DDL rights from disabling row-level
  security. The platform's guarantee is that it notices within one sentinel
  interval and stops trusting itself — refusing tenant traffic, leaving the load
  balancer, and stopping its workers (ADR-SECURITY-002). Reads occurring inside
  that detection window are not prevented, and the window is bounded, not zero.
- **Destruction is authorised and recorded, not reversible.** A permitted
  deletion still removes the object; what is guaranteed is that it passed the §8
  preconditions and appended exactly one audit event in the same transaction
  (ADR-GOV-002). The audit chain deliberately outlives the object. An operator
  with direct SQL access can still delete rows — that residual is covered by the
  audit-immutability triggers and the schema sentinel (ADR-SECURITY-002), not by
  this entry.
- **The §9.1 write channel is closed; the §9.1 *verdict* is not fixed.** A client
  can no longer forge or erase the inputs to the Replacement Test
  (ADR-GOV-003). It remains true that `owns(new, ∅)` is vacuously satisfied, so an
  artifact declaring no responsibilities can still be replaced by any successor —
  the ratified clause says so, and changing it is an Amendment. Registered as
  **CMR-A05**, BLOCKED. There is also now no constitutional operation for
  establishing those inputs at all; that gap is part of the same referral. This
  register entry claims the closed channel, nothing more.
- **The delivery record accounts for the destination, not for the administrative
  act.** `delivered_to` makes where an attempt went a fact that repointing a
  subscription cannot alter (ADR-GOV-004). It records only the *most recent*
  attempt, and it does **not** record the administrative change itself:
  creating, patching or deleting a webhook, policy or tenant still writes no
  audit event — measured live, three admin creations produced a delta of 0 rows
  in `audit_event`. Privileged-action accountability is an open gap, stated here
  rather than implied by this entry.
- **The leadership step-down window is bounded, not zero.** Up to the health-watch
  interval, a demoted leader may still run its workers; that overlap is made
  *safe* by idempotent production and the atomic claim, not eliminated
  (ADR-CONCURRENCY-003). True fencing of an in-flight outbound call is unclaimed
  future work.

## Boundary dossiers

The mandated long form for a boundary: property, threat model, root authority,
failure/recovery/operational assumptions, startup and runtime validation,
evidence, residual risks, status. New investigations add their boundary here.

### Cursor completeness — no lost events (ADR-CONCURRENCY-004)

- **Property.** The relay and the policy consumer never silently drop a committed
  event: each per-chain cursor is a monotonic watermark over a gapless committed
  prefix, advanced only past events already durably enqueued.
- **Threat model.** (a) Commit reorder — a lower seq becomes visible after the
  cursor passed a higher one, skipping it. (b) Non-monotonic cursor — a stale or
  overlapping writer (a demoted leader in the bounded step-down window) rewinds
  the watermark. (c) Advance-before-enqueue — the cursor leads the enqueued set,
  so a crash drops the gap. (d) Genesis race — two first events collide on seq.
- **Root authority.** The `audit_event` log, whose per-chain `seq` is assigned
  `last_seq + 1` under the chain-head `SELECT … FOR UPDATE` (ADR-AUDIT-004), with
  a unique `(chain_id, seq)` backstop. This is what makes the committed log a
  gapless prefix per chain. The cursor is derived, never authoritative.
- **Failure assumptions.** A worker can crash between enqueue and cursor advance
  (cursor stays; events re-read — safe with idempotent production,
  ADR-CONCURRENCY-003). Two workers can run during the bounded leadership overlap
  (ADR-CONCURRENCY-003); neither can rewind the watermark now, and neither can
  advance past un-enqueued events.
- **Recovery assumptions.** `webhook_cursor`/`policy_cursor` and `audit_event`
  are restored from one consistent snapshot (ADR-TENANCY-006). A cursor restored
  ahead of its log would skip the missing range; a periodic "no cursor exceeds
  its chain head" check is noted future hardening.
- **Operational assumptions.** The only writers of these cursors are the tail
  loops; replay and administration do not move them. No operator tooling advances
  a cursor.
- **Startup validation.** None specific to cursors (the schema-invariant
  validator, ADR-TENANCY-007, guards the ownership columns the workers read).
- **Runtime validation.** Monotonic write (`GREATEST`) enforces "watermark only
  rises" on every advance. Enqueue-before-advance ordering is preserved in the
  tail loops.
- **Evidence.** Live exploit: cursor regressed 10 → 5 under the blind write.
  Live fix: stale write ignored, holds at 10, advances to 12. Live end-to-end
  across failover: 12 committed ratifications → 12 deliveries → 12 distinct ids;
  every cursor exactly at its chain head (0 ahead, 0 behind); 0 lost, 0 phantom
  by `(chain_id, seq)`. Gapless prefix: `TestAppenderConcurrentSerialization`
  (12 concurrent appends → seqs 1..12 contiguous).
- **Residual risks.** No cross-chain global order (not claimed). Delivery order to
  a receiver is not guaranteed — receivers needing per-object order sort on
  `(chain_id, seq)`. Restore consistency depends on ADR-TENANCY-006. Genesis race
  is fail-closed and retryable.
- **Status.** ✓ Closed. Enforced by `arch.TestCursorWriters_AreMonotonic`,
  `postgres.TestCursor_{Webhook,Policy}WriteIsMonotonic`, and
  `postgres.TestAppenderConcurrentSerialization`.

### Claim fencing — an evicted worker cannot corrupt the row (ADR-CONCURRENCY-005)

- **Property.** A worker records a delivery/execution outcome only while it still
  holds the claim it was granted; an evicted (lease-expired, reclaimed) worker's
  late completion changes nothing.
- **Threat model.** A slow outbound call outlives the lease; another worker
  reclaims the row and acts; the first worker then completes and (a) resurrects a
  terminal row into a retry state, (b) overwrites the reclaimer's outcome, or (c)
  corrupts retry/backoff bookkeeping — amplifying duplicates.
- **Root authority.** The claim state on the row: `claimed_at`, stamped by
  `ClaimDue` and advanced on every reclaim. It is the fencing token.
- **Failure assumptions.** A worker can be paused (GC, SIGSTOP) or simply slow
  past the lease; the row will be reclaimed under it. Its later write must be a
  no-op against the row it no longer owns.
- **Recovery assumptions.** None specific; the fence is per-write. After a crash,
  the lease reclaim (ADR-CONCURRENCY-002) still applies and the new claim carries
  a new token.
- **Operational assumptions.** `MarkResult` is only reached by the workers (and
  the admin test path, which passes a zero token and writes unfenced by design).
- **Startup validation.** None.
- **Runtime validation.** `MarkResult` fences on `claimed_at = $token`; a
  mismatch yields zero rows and `ErrStaleClaim`, which the dispatcher/executor
  observe and discard.
- **Evidence.** Live exploit: an evicted worker resurrected a `delivered` row to
  `failed` with a reschedule. Live fix: the same write returns `ErrStaleClaim`
  and the row keeps the owner's `delivered` state; the reclaim advances the token;
  policy executions behave identically.
- **Residual risks.** The concurrent double-*send* remains (at-least-once ceiling,
  dedup-able on the stable id). `claimed_at` as the token is sound because
  reclaims are ≥ lease apart; a dedicated monotonic counter is future hardening.
  The lease is not yet operator-tunable.
- **Status.** ✓ Closed. Enforced by `arch.TestMarkResult_IsFencedOnTheClaim` and
  `postgres.TestLeaseFencing_{Webhook,Policy}EvictedWorkerIsFenced`.

### Outbound egress — no SSRF to internal addresses (ADR-SECURITY-001)

- **Property.** The platform never opens an outbound connection to a non-public IP
  address on behalf of a tenant (webhook delivery, policy HTTP actions).
- **Threat model.** A tenant registers a delivery URL pointing at loopback, the
  cloud metadata endpoint (`169.254.169.254`), or a private-range host. The
  platform, as a confused deputy, POSTs to it — reaching internal services,
  metadata/credentials, and (via the returned `last_status_code`) scanning and
  fingerprinting the internal network. DNS rebinding defeats string blocklists.
- **Root authority.** The resolved IP at dial time — not the spelling of the URL.
  `safehttp` resolves the host, refuses if any resolved address is non-public, and
  dials the exact validated address.
- **Failure assumptions.** DNS can resolve a public name to a private address
  (rebinding); redirects can point at private addresses; encodings of `127.0.0.1`
  are unbounded. All are handled at the dial, per-hop, on the IP.
- **Recovery assumptions.** None; the guard is stateless per connection.
- **Operational assumptions.** Only the dispatcher and policy registry dial
  operator-supplied URLs; both use `safehttp.Client`. Network egress policy is a
  defence-in-depth complement.
- **Startup validation.** None (the guard is per-dial). Config
  `ONEOPS_WEBHOOK_ALLOW_PRIVATE_TARGETS` (default false) is the only knob.
- **Runtime validation.** `guardedDialContext` refuses non-public addresses on
  every dial; webhook creation rejects non-http(s) schemes.
- **Evidence.** Live exploit: a loopback webhook received a signed POST from the
  platform; the tenant read `last_status_code=200`. Live fix: the same webhook
  reaches nothing (internal service received 0 requests); delivery is
  `status=failed, last_status_code=0`, uniform across all blocked targets.
- **Residual risks.** Create-time accepts literal private IPs (blocked at dial);
  policy-action URLs guarded at dial, not create; a fully hostile DNS resolver is
  outside scope (network egress policy backstops).
- **Status.** ✓ Closed. Enforced by `arch.TestOutboundClients_AreSSRFGuarded`,
  `safehttp.TestIsPublicIP_BlocksNonPublic`, `safehttp.TestClient_RefusesLoopbackDial`,
  `safehttp.TestValidateWebhookURL`.

### Retry liveness — every row terminates (ADR-CONCURRENCY-006)

- **Property.** Every delivery and policy execution reaches a terminal state. A
  row is handed to a worker at most `max_retries` times in total — counting
  attempts whose outcome was never recorded — and then becomes `dead_letter`. An
  outcome the platform produced in the outside world is recorded even when the
  worker producing it is being stopped.
- **Threat model.** (a) Crash-looping worker — the attempt kills the worker (OOM,
  node loss, SIGKILL), the lease reclaims the row, and the budget never depletes.
  (b) Demotion mid-flight — ADR-CONCURRENCY-003 cancels the leadership context,
  the outcome write rides that context and fails, and the recorded state diverges
  from what the subscriber actually received. (c) Orphaned row — the subscriber is
  deleted and the row circulates with no possible success. (d) Budget burned
  without work — a claim charged but never attempted (shutdown) dead-letters a
  healthy row. (e) Evicted worker refunding a row it no longer owns.
- **Root authority.** The claim itself. It is the only event in a row's lifecycle
  that a failing worker cannot skip, so it is where the attempt is charged and
  where the budget is enforced. The budget values live on `webhook.max_retries` /
  `policy.max_retries` and are read by the claim through a join, so the claim and
  the worker enforce one number.
- **Failure assumptions.** A worker may vanish at any point after claiming and
  never write anything. A worker may be cancelled between claiming a batch and
  attempting it. The database may be briefly unavailable for an outcome write
  (bounded by the 5s outcome deadline; the row then falls back to the reclaim
  path, which now counts).
- **Recovery assumptions.** `RequeueDeadLetters` refills the budget
  (`retry_count = 0`) and clears the claim token, so operator recovery is real and
  not a no-op against the claim's budget check. Restore consistency is unchanged
  (ADR-TENANCY-006).
- **Operational assumptions.** Only the workers claim and release. The admin test
  path writes unfenced with a zero token by design and does not participate in
  budget accounting.
- **Startup validation.** None specific.
- **Runtime validation.** The claim's `attempt_no > budget` predicate terminates
  the row; `COALESCE(max_retries, 0)` terminates orphans; `ReleaseClaim` is fenced
  on `claimed_at`; a failed outcome write is logged and returned rather than
  counted as success.
- **Evidence.** Live exploit: `max_retries=3`, six crash cycles →
  `claims_handed_out=6`, `status=inflight`, `retry_count=0` (both queues). Live
  exploit: demotion mid-flight → subscriber received the POST, row `inflight`,
  `retry_count=0`, success metric incremented. Live fix, same tests: six cycles →
  `claims_handed_out=3`, `dead_letter`, `retry_count=3`; demotion → outcome
  recorded `failed`/`retry_count=1`; orphan → 0 claims, `dead_letter`; five
  claim/release cycles → `retry_count=0`, `pending`; stale release changes
  nothing; requeued dead-letter claimable again.
- **Residual risks.** The at-least-once ceiling is unchanged — duplicates still
  occur on a crash between action and outcome, now bounded and dedup-able on the
  stable id. Termination is bounded in attempts, not wall-clock. The claim lease
  and the 5s outcome deadline are still not operator-tunable. `retry_count`'s
  observable meaning changed to "attempts started" (+1 while in flight).
- **Status.** ✓ Closed. Enforced by
  `arch.TestClaimDue_ChargesTheAttemptAndBoundsIt`,
  `arch.TestWorkerOutcomeWrites_AreNotCancelledByDemotion`,
  `arch.TestOutcomeContext_IsDetachedAndBounded`,
  `arch.TestWorkers_ReleaseUnusedClaimsOnStop`,
  `postgres.TestRetryLiveness_*` (5 tests), and
  `postgres.TestOutcomeDurability_ResultSurvivesWorkerCancellation`.

### Continuously-held invariants — verification does not expire (ADR-SECURITY-002)

- **Property.** Every invariant the platform refuses to boot without is
  re-verified for the life of the process, and a breach fails closed the same way
  a breach at boot does: the tenant-data surface is refused, the instance leaves
  the load balancer, and the background workers stop.
- **Threat model.** (a) A migration, operator `ALTER`, restore from an older
  dump, or rollback weakens row-level security, the ownership columns, or the
  audit append-only guards *after* startup. (b) The platform keeps serving on a
  stale verdict — proven live: a tenant read another tenant's rows while the
  process reported ready and logged nothing. (c) HTTP is gated but the workers
  keep fanning out across the broken boundary. (d) A transient database failure
  is mistaken for a breach and takes the whole fleet down. (e) The gate opens
  before the invariant has ever been checked in this process.
- **Root authority.** The live PostgreSQL catalogue, read by the *same*
  `SchemaValidator` the startup sequence uses. One definition of "valid" for both
  boot and runtime; two checks could disagree.
- **Failure assumptions.** The check can fail to run (database unreachable
  mid-restart); the previous verdict is carried and the failure counted, because
  a blip is not evidence of a breach and treating it as one would train operators
  to ignore the signal. A failed check never clears a known breach.
- **Recovery assumptions.** Repair is an operator action on the database; the
  sentinel clears on its own, and readiness, the gate and the workers all resume
  without a redeploy.
- **Operational assumptions.** The sentinel runs on *every* replica, outside the
  leader gate — it is each replica's supervision of itself, not singleton work.
  It performs no tenant work and has no duplicable side effect. The allowance is
  recorded in `arch.perReplicaSupervisors`.
- **Startup validation.** Unchanged (ADR-TENANCY-007): the process still refuses
  to boot on any problem. The sentinel starts only after that check passes, so
  its first verdict confirms a known-good boundary.
- **Runtime validation.** The sentinel re-runs on
  `ONEOPS_SCHEMA_SENTINEL_INTERVAL_SECONDS` (default 30s). Unverified reads as
  unhealthy. `oneops_invariant_breached` is the alerting signal.
- **Evidence.** Live exploit: RLS disabled post-startup → tenant A read 1 of
  tenant B's rows through the tenant-scoped pool (0 before), with the running
  binary at `readyz=200`, `/v1/artifacts=200`, and zero relevant log lines. Live
  fix: `readyz` and `/v1/artifacts` both `503`, `oneops_invariant_breached 1`, one
  `SENTINEL BREACH` error naming the problem, workers stopped; after repair, all
  restored with no redeploy. Negative results recorded: `NO FORCE` is not
  exploitable for the non-owner app role, and PostgreSQL restart recovery already
  worked (leadership re-election observed live).
- **Residual risks.** Detection, not prevention — the weakening itself cannot be
  stopped from inside the application, and reads inside the detection window are
  not prevented. The window is bounded by the interval, not zero. Coverage is the
  set of invariants `SchemaValidator` checks; new invariants must be added to the
  sentinel rather than to a new mechanism. A breach now takes instances out of
  service, which is a real availability trade taken deliberately.
- **Status.** ✓ Closed. Enforced by
  `arch.TestSchemaValidator_IsRunContinuouslyNotOnlyAtStartup`,
  `arch.TestInvariantBreach_FailsClosedOnEveryTenantDataPath`,
  `arch.TestSentinel_TreatsUnverifiedAsUnhealthy`, `ops.TestSentinel_*`,
  `ops.TestRunWhileHealthy_*`, `httpapi.TestInvariantGate_*`, and
  `postgres.TestRuntimeInvariant_*`.

### Constitutional destruction — one door (ADR-GOV-002)

- **Property.** A Configuration Object can be destroyed only by the Governance
  Engine, only when §8 permits it (role not protected, working material, no
  dependents), and never without exactly one audit event committed in the same
  transaction as the removal.
- **Threat model.** (a) A second route reaching storage directly — `DELETE
  /v1/artifacts/{id}` did, enforcing one of the four preconditions. (b) Unaudited
  destruction, making the audit log an incomplete record of state changes.
  (c) The dependents check skipped, so `ON DELETE CASCADE` silently destroys the
  edges an audited Extension created, leaving the log asserting a relationship
  the graph no longer contains. (d) A future handler wired to a destructive
  repository method. (e) Destruction attempted with no engine present.
- **Root authority.** The Governance Engine's §8 transition plan plus the atomic
  audit append (ADR-AUDIT-005). Storage is reached only through
  `GovernanceStore.RemoveObject` inside that transaction.
- **Failure assumptions.** A caller may hold `delete` permission and intend
  destruction of governed content; a future contributor may add a handler without
  reading this ADR; an engine may be absent from a given wiring.
- **Recovery assumptions.** None — destruction is terminal by design. The audit
  chain survives the object deliberately, so the record of its existence and
  removal persists. Orphaned chains from before this change are harmless to
  startup (verified live).
- **Operational assumptions.** Direct SQL access bypasses this entirely; that
  residual belongs to ADR-SECURITY-002's controls.
- **Startup validation.** None specific.
- **Runtime validation.** The engine's §8 preconditions on every destruction; the
  route refuses outright when no engine is wired.
- **Evidence.** Live exploit: ratified `current_baseline` object refused by the
  governance route (409) destroyed by the registry route (204), 0 rows left,
  0 deletion audit events; concurrent variant diverged the graph from the audit
  log. Live fix: 409 and the object survives; a permitted deletion returns 200
  with exactly 1 `deletion` audit event; an object with a dependent is refused and
  its edge survives.
- **Residual risks.** Breaking API change (Idempotency-Key now required; 200 not
  204; 409 where destruction formerly succeeded). `ON DELETE CASCADE` is still the
  removal mechanism and is safe only because the dependents check now precedes it
  on every path — the architecture tests exist to keep that true.
- **Status.** ✓ Closed. Enforced by
  `arch.TestConfigObjectRepository_ExposesNoDestructiveMethod`,
  `arch.TestConfigObjectRepo_HasNoUnguardedDelete`,
  `arch.TestHTTPHandlers_DoNotDestroyObjectsDirectly`,
  `httpapi.TestDestruction_*` and `httpapi.TestCreateGetDelete`.

### Constitutional inputs vs descriptive data (ADR-GOV-003)

- **Property.** The descriptive-metadata channel can neither write nor destroy a
  §9.1 constitutional input (`responsibilities`, `citations`, `coverage`).
- **Threat model.** (a) Seeding a successor at creation so the four-part
  Replacement Test passes. (b) The same via bulk create. (c) Erasing the
  incumbent's inputs through a patch of an unrelated key, which is as powerful as
  forgery because an empty set satisfies the clause. (d) A future surface writing
  metadata without remembering the rule. (e) Two copies of the key set drifting.
- **Root authority.** `domain.ConstitutionalMetadataKeys()` — one definition —
  enforced at the two storage writers every caller passes through, plus the
  inception boundary for a clean 422.
- **Failure assumptions.** A caller may hold ordinary write permission and craft
  metadata deliberately; a future contributor may add a metadata-writing surface
  without reading this ADR.
- **Recovery assumptions.** None needed for the write channel. Inputs already
  written before this change persist and are now immutable through the API.
- **Operational assumptions.** Direct SQL bypasses this, as it bypasses every
  application control (see ADR-SECURITY-002's controls). Because no constitutional
  operation exists to declare these inputs, direct SQL is currently the only way
  they can come to exist — stated, not hidden.
- **Startup validation.** None specific.
- **Runtime validation.** Both metadata writers refuse constitutional keys; the
  wholesale clear excludes them; `ValidateInception` refuses them at ingress.
- **Evidence.** Live exploit: create stored `responsibilities` while patch
  refused it (400); a seeded successor moved a Replacement 409 → 200 against a
  ratified `current_baseline` artifact; a patch of an unrelated key returned 200
  and erased `responsibilities`. Live fix: create and bulk → 422; patch preserves
  the input and still applies the descriptive change; seeded successor refused;
  replacement with a plain successor → 409.
- **Residual risks.** The vacuous-pass property of §9.1 is untouched and is
  registered as **CMR-A05** (BLOCKED, Amendment required). No constitutional path
  exists to establish these inputs. Breaking change: create/bulk return 422 for
  bodies that previously succeeded.
- **Status.** ✓ Closed *for the write channel only*. Enforced by
  `arch.TestMetadataWrites_AreGuardedAtTheStorageChokepoint`,
  `arch.TestMetadataClear_PreservesConstitutionalInputs`,
  `arch.TestInception_RefusesConstitutionalMetadata`,
  `arch.TestConstitutionalMetadataKeys_HaveOneDefinition` and
  `postgres.TestConstitutionalMetadata_*`.

### Delivery destination — a recorded fact, not a pointer (ADR-GOV-004)

- **Property.** For every attempted delivery the platform holds the URL that
  attempt was sent to, captured when it was made; changing the subscription
  cannot alter it.
- **Threat model.** (a) An administrator repoints a subscription and every past
  delivery reads as having gone to the new URL. (b) An actor repoints, collects
  governed events at a destination of their choosing, and repoints back — leaving
  the history attesting to the approved destination throughout. (c) An outcome
  reached without an attempt invents a destination. (d) A later dead-letter
  erases the record of where a real attempt went. (e) A read path re-derives the
  destination from the subscription.
- **Root authority.** The delivery row itself. The destination is written in the
  same fenced `UPDATE` as the outcome (ADR-CONCURRENCY-005), so it is exactly as
  trustworthy as the outcome and an evicted worker can no more rewrite one than
  the other.
- **Failure assumptions.** A subscription's URL may change at any time, by an
  actor holding ordinary platform-admin rights; the signing secret is unchanged
  by a URL patch, so a repointed destination receives validly signed events.
- **Recovery assumptions.** None — the record is the recovery artefact. Rows
  written before this change hold NULL, which honestly says "unknown".
- **Operational assumptions.** Delivery history is retention-bounded
  (`ONEOPS_WEBHOOK_RETENTION_HOURS`, default 720h); the recorded fact expires
  with the row. Direct SQL bypasses this as it bypasses every application
  control.
- **Startup validation.** None specific.
- **Runtime validation.** `COALESCE($8, delivered_to)` — an empty destination
  neither writes nor erases. Reads take the destination from the delivery row.
- **Evidence.** Live exploit: a delivery POSTed to `/approved` reported
  `/attacker-controlled` after one PATCH returned 200, zero audit events. Live
  fix: the recorded destination stays `/approved` while the subscription now
  reads `/attacker-controlled` — the record and the configuration correctly
  disagree.
- **Residual risks.** Only the most recent attempt's destination is retained (no
  per-attempt history). The administrative change itself is still unaudited — a
  stated open gap, not covered by this entry. Retention-bounded.
- **Status.** ✓ Closed. Enforced by `arch.TestDelivery_RecordsItsOwnDestination`,
  `arch.TestDeliveryDestination_IsWrittenWithTheOutcome`,
  `arch.TestDispatcher_RecordsTheURLItPosted`,
  `arch.TestDeliveryReads_DoNotDeriveDestinationFromTheWebhook` and
  `postgres.TestDeliveryDestination_*`.

### Platform invariants and egress — enforced by construction (ADR-SECURITY-003)

- **Verified class.** (a) A boundary enforced at one point but not the other.
  (b) An outbound HTTP request outside the SSRF guard.
- **Remaining instances.** None known. Both are now enforced structurally rather
  than by enumeration: an invariant cannot be registered at one enforcement point
  only, and the egress sweep covers every non-test file in the tree.
- **Scope of elimination.** Every validator the platform defines is in the
  invariant registry, and both the startup gate and the sentinel evaluate that
  one registry in order with first-failure short-circuit. Every outbound request
  outside `internal/safehttp` must use the guarded client.
- **Known exceptions.** `internal/safehttp` itself constructs the client — it is
  the guard. Tests may inject their own client (`NewVerifierWithClient`), which
  is why the guarded default is separately pinned by
  `auth.TestJWKSFetchIsSSRFGuarded`.
- **Residual risk.** Detection, not prevention: an operator with database access
  can still break the ownership graph, and reads inside one sentinel interval are
  not prevented. The registry guarantees that what is in it is enforced at both
  points; it does not claim to enumerate every property worth checking. The JWKS
  guard refuses non-public addresses only — it does not authenticate the
  endpoint. Ownership breaches now take instances out of service, a wider
  availability trade than ADR-SECURITY-002 made, taken deliberately.

### Work-queue exclusivity and fencing — swept over the schema (ADR-CONCURRENCY-007)

- **Verified class.** (a) Two workers holding the same unit of work. (b) A worker
  recording an outcome for work it no longer holds.
- **Remaining instances.** None known. The queue set is derived from the
  migrations (any table whose `status` defaults to `'pending'`), so a fourth
  queue cannot be added without a claim and a fence.
- **Scope of elimination.** All three queues — `webhook_delivery`,
  `policy_execution`, `webhook_replay_job` — claim under
  `FOR UPDATE … SKIP LOCKED`, stamp `claimed_at`, and fence the outcome write on
  it. The cursor sweep (entry 17) is derived the same way.
- **Known exceptions.** A zero token writes unfenced by design — the
  administrative paths that touch a row never claimed (the delivery test
  endpoint, an operator requeue). The schema sweep recognises a queue by a
  `'pending'`-defaulted `status`; a queue expressing its pending state
  differently would not be detected.
- **Residual risk.** **A replay job whose worker dies is never recovered**:
  `ClaimPendingJobs` selects only `pending`, so a job left `running` is neither
  retried nor terminated. This is a liveness gap, not a correctness one — a stuck
  replay job produces no repeated outbound effect — and it is left open
  deliberately, because giving it lease recovery requires giving it retry
  accounting (ADR-CONCURRENCY-006) as well. Documented by
  `postgres.TestReplayJob_StuckRunningIsNotReclaimed`.

## How to add an entry

Do not add a row until all five hold, or the investigation stays OPEN:

1. the class was exploited against the running service (live evidence);
2. it was remediated to eliminate the class, not the one path;
3. the same exploit was re-run live and now fails;
4. a build-failing test (arch/int/unit/startup) reproduces the regression and
   is shown to bite;
5. the reasoning is written in an ADR and the honest guarantee is not overstated.

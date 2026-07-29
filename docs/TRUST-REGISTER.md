# Trust Register

The running ledger of **vulnerability classes eliminated from the running
service**. An entry is admitted only when the class was proven exploitable
against the live system, remediated, re-attacked live to show the exploit now
fails, and locked shut by a test that fails the build if the class returns. Each
entry cites the ADR that carries the full evidence.

The rule this register enforces: eliminate the *class*, not the instance. A fix
that closes one path but leaves the category open is not an entry here.

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

## How to add an entry

Do not add a row until all five hold, or the investigation stays OPEN:

1. the class was exploited against the running service (live evidence);
2. it was remediated to eliminate the class, not the one path;
3. the same exploit was re-run live and now fails;
4. a build-failing test (arch/int/unit/startup) reproduces the regression and
   is shown to bite;
5. the reasoning is written in an ADR and the honest guarantee is not overstated.

# ADR-SECURITY-004 — Per-tenant, in-memory rate limiting on `/v1`

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-31 |
| **Decider** | Acting CTO |
| **Related** | ADR-SECURITY-002 (invariants are continuously verified — same gate ordering discipline), ADR-SECURITY-003 (one guarded egress), ADR-TENANCY-001 (tenant is the isolation boundary) |

## Context

Every request on `/v1` shares one control-plane instance's CPU, database
connections and downstream capacity. Before this change, nothing metered how
much of that capacity a single tenant could take. A runaway client, a bad
retry loop, or a tenant-supplied integration gone wrong could consume
unbounded request throughput and starve every other tenant on the same
instance — the fair-use half of tenant isolation was missing. ADR-TENANCY-001
established `tenant_id` as the boundary every row belongs to; this ADR extends
that boundary to request *rate*, not just data access.

This is a capacity-fairness gap between tenants sharing an instance, not an
edge denial-of-service problem. The two are deliberately kept separate below.

## Decision

**An in-memory, per-instance, per-tenant token bucket, enforced in the `/v1`
middleware chain after authentication.**

### 1. Token bucket per tenant (`golang.org/x/time/rate`)

`internal/httpapi/ratelimit.go` holds a `rateLimiter`: a `sync.Mutex`-guarded
map from tenant ID to `*rate.Limiter`, created lazily on first use. `allow`
reserves one token via `ReserveN` and cancels the reservation when it would
have to wait, so a rejected request never also consumes bucket capacity — a
burst of 429s must not slow down the tenant's genuine recovery once it backs
off.

### 2. Keyed by the resolved tenant, not the caller or the IP

The limiter reads `domain.TenantIDFrom(ctx)`. This is only meaningful once
`s.authenticate` has run, so `s.rateLimit` is installed **after**
`s.authenticate` in the `/v1` route group — mirroring the ordering discipline
ADR-SECURITY-002 established for the invariant gate, just in the opposite
position: the invariant gate must run *before* authentication (identity is
irrelevant if the isolation boundary is broken); the rate limiter must run
*after* it (there is no tenant to key on until identity is resolved). An
unauthenticated request never reaches it — `s.authenticate` has already
rejected it — so there is no pre-auth bucket to exhaust by omitting a token.

### 3. Idle-tenant eviction, not an unbounded map

A bucket is evicted once it has been idle longer than `rateLimiterIdleTTL`
(10 minutes). Eviction runs as a sweep inside `allow`, throttled to at most
once per TTL window, so the amortized cost of the hot path stays O(1) and the
map's steady-state size is bounded by tenants active within the last TTL
window, not by every tenant that has ever existed.

### 4. Configuration (`internal/config/config.go`)

| Env var | Default | Meaning |
|---|---|---|
| `ONEOPS_RATE_LIMIT_ENABLED` | `true` | `false` is a pass-through, for a clean rollback |
| `ONEOPS_RATE_LIMIT_RPS` | `20` | steady-state requests/second per tenant per instance |
| `ONEOPS_RATE_LIMIT_BURST` | `40` | tokens a tenant can spend instantly |

`Load()` rejects `RateLimitEnabled=true` with `RateLimitRPS<=0` or
`RateLimitBurst<=0`: a limiter configured to admit nothing while claiming to
be enabled is a configuration error, not a runtime condition to discover
later. Disabling the feature is exempt from that check by design — it is the
rollback path and must not itself require a valid budget.

### 5. Response shape and metric

A throttled request gets `429 Too Many Requests`, a `Retry-After` header (the
delay the reservation reports, ceiling-rounded to whole seconds, minimum 1),
and the same RFC 7807 `Problem` body every other httpapi error path uses
(`writeProblem` — no new error shape). `oneops_rate_limited_total` is a single
counter, incremented on every 429. It carries **no tenant label**: a
tenant-keyed label would give the metric unbounded cardinality over the
platform's lifetime, the same reasoning that keeps the map itself bounded.

### 6. What is explicitly out of scope

- **No Redis, no new request-path datastore.** The limiter is process-local
  memory. Adding a shared store to enforce a *global* limit is a real future
  story, deliberately deferred — see Consequences.
- **No per-endpoint limits.** One bucket per tenant, shared across every `/v1`
  route. Differentiated limits per operation are a refinement this ADR does
  not make.
- **No pre-auth or per-IP limiter.** Edge/IP-level denial-of-service belongs at
  the ingress layer (the Helm chart's nginx Ingress), not in the application.
  The application cannot see the real client IP reliably behind arbitrary
  proxies without trusting forwarded headers it does not control, and building
  a second, cruder limiter in front of authentication would duplicate a
  concern the edge is better positioned to enforce. `/healthz`, `/readyz`,
  `/metrics`, `/`, `/openapi.yaml` and `/docs` stay outside `/v1` and outside
  this limiter entirely — rate-limiting `/healthz` would fail Kubernetes
  liveness/readiness probes and take instances out of rotation for no security
  benefit.

## Consequences

**What is now guaranteed.** No single tenant can exceed
`RateLimitRPS`/`RateLimitBurst` requests against one control-plane instance
without being throttled, independent of every other tenant sharing that
instance. A misbehaving integration degrades its own tenant's throughput, not
its neighbours'.

**What is *not* claimed — stated plainly because it is the load-bearing
trade-off of this decision:**

- **The limit is per instance, not per tenant.** With N replicas behind the
  load balancer, a tenant whose requests are spread across all N gets an
  effective global budget of approximately **N × RateLimitRPS** /
  **N × RateLimitBurst**, not the configured single-instance number. This is
  the accepted cost of choosing in-memory state over a shared store: a Redis-
  or database-backed global counter would fix it but adds a new dependency on
  the request hot path, which this decision deliberately defers rather than
  accepts implicitly. Operators sizing limits for a fleet of N replicas should
  divide the intended tenant-wide budget by N.
- **Per-instance state does not survive a restart or a rebalance.** A pod
  restart resets that instance's buckets to full. This is consistent with the
  fairness goal (protect *this instance's* capacity right now) and inconsistent
  with a durable quota (nothing here enforces "N requests per tenant per day").
- **This is not a defence against a distributed or edge-level attacker.** An
  attacker who can open many connections across many instances, or who never
  authenticates, is unaffected by a middleware that runs after authentication
  and is keyed per instance. That threat is out of scope by design (see
  §6) and belongs at the ingress.
- **Detection, not authorization.** A 429 does not distinguish "malicious
  tenant" from "legitimately busy tenant whose limit is set too low" — both
  look identical to this middleware. Tuning `RateLimitRPS`/`RateLimitBurst` per
  deployment is an operational task this ADR does not automate.

## Evidence

- `httpapi.TestRateLimiter_AllowsBurstThenBlocksThenRecovers` — burst admitted,
  the next request refused, refill after the rps window admits again.
- `httpapi.TestRateLimiter_RejectionDoesNotConsumeCapacity` — a rejected
  request does not delay the tenant's own recovery.
- `httpapi.TestRateLimiter_TenantsAreIsolated` — exhausting tenant A's bucket
  does not affect tenant B.
- `httpapi.TestRateLimiter_EvictsIdleTenants` — buckets idle past the TTL are
  swept; the tenant that triggered the sweep survives.
- `httpapi.TestRateLimiter_ConcurrentAccessIsRaceFree` — 50 goroutines hammering
  5 tenants concurrently, race-clean (`go test -race`), map settles at exactly
  one bucket per distinct tenant.
- `httpapi.TestRateLimit_TenantOverLimitGets429_OtherTenantUnaffected` — the
  middleware end to end: tenant A's second request gets `429` with
  `Retry-After` set and an RFC 7807 body decodable as `Problem` with
  `Status: 429`; tenant B's request on the same middleware instance still gets
  `200`.
- `httpapi.TestRateLimit_DisabledIsPassThrough` — `ONEOPS_RATE_LIMIT_ENABLED=false`
  (nil limiter) admits 100/100 requests from one tenant with no throttling.
- `config.TestLoadRejectsRateLimitEnabledWithZeroRPS` /
  `TestLoadRejectsRateLimitEnabledWithZeroBurst` — the config gate rejects an
  enabled limiter with a zero budget.
- `config.TestLoadAllowsRateLimitDisabledWithZeroValues` — disabling is exempt
  from that guard, preserving the rollback path.
- `config.TestLoadDefaultsEnableRateLimiting` — the secure default is enabled
  with a positive budget.
- Full suite green under `-race`: `go test ./... -race -cover`.

## Enforcement

- `httpapi.rateLimit` is wired via `rt.Use(s.rateLimit)` immediately after
  `rt.Use(s.authenticate)` in the `/v1` route group (`server.go`); the infra
  routes (`/healthz`, `/readyz`, `/metrics`, `/`, `/openapi.yaml`, `/docs`) are
  registered outside that group and never pass through it.
- The tests above are regression tests for burst admission, isolation,
  eviction, concurrency safety, the exact 429 response shape, the disabled
  pass-through, and the config validation gate. Any future change that removes
  the post-authenticate ordering, reintroduces a shared bucket across tenants,
  or lets the map grow without eviction should fail one of them.

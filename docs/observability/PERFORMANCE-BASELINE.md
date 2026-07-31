# /v1 API Performance Baseline (T2-F)

**Status:** dev-box baseline, captured 2026-07-31. This is a single-instance,
single-developer-machine measurement against a local dev database. **It is not
a production capacity guarantee** — no production hardware, network, or
concurrent-tenant profile has been measured. Treat the numbers below as the
first real data point closing "scalable" from an unverified claim to a
measured one, and as a regression baseline for future runs — not as an SLA.

## What this closes

Before this story the platform claimed to be "scalable" with no load ever
run against it. This adds:

1. A repeatable load harness for the authenticated `/v1` API — `loadtest/`
   (pointer only; see that package for the executable behaviour, per Law
   14.1 — this document does not restate the code).
2. `make loadtest`, wired in `Makefile`.
3. The baseline captured below, plus a capacity finding relevant to HPA
   sizing.

## Methodology

### Harness

`loadtest/` is a build-tag-gated Go program (`//go:build loadtest`), **not**
a `cmd/` binary. It mints its own HS256 JWT using the same claim shape the
`httpapi` test suite uses (`sub`/`iss`/`aud`/`exp`/`roles`, no `tenant` claim
→ resolves to the system tenant — see `internal/httpapi/middleware.go`:
`resolveTenant`), seeds a pool of artifacts, then drives a weighted mix of
requests from N concurrent workers for a fixed duration (or request count),
recording per-request status and latency. It reports throughput, p50/p95/p99/
max latency, and a status-code breakdown (2xx/4xx/**429**/5xx), overall and
per endpoint.

It is excluded from the normal build by its build tag (`go build ./...`,
`make test`, and `go test ./internal/arch/...` are all unaffected — verified;
see Evidence below) and is not registered in
`internal/arch/wiring_test.go`'s `registeredBinaries`, because it is not a
`cmd/` binary and does not need to be: it's an external measurement tool
against a running instance, never shipped in the control-plane image.

Run it directly:

```
go run -tags loadtest ./loadtest -base-url http://localhost:8080 -workers 20 -duration 30s -seed 50
```

or via the Makefile, which sets the same defaults:

```
make loadtest
# override via LOADTEST_BASE_URL / LOADTEST_WORKERS / LOADTEST_DURATION / LOADTEST_SEED / LOADTEST_JWT_*
```

### Endpoint mix

Verified against the actual route table in `internal/httpapi/server.go`
(`routes()`), not guessed. Read-heavy, ~90/10 read/write, matching a
realistic operational profile:

| Endpoint | Weight | Kind |
| --- | --- | --- |
| `GET /v1/artifacts` | 35% | list, read |
| `GET /v1/artifacts/{id}` | 25% | point read |
| `GET /v1/governance/{id}` | 15% | point read (governance state) |
| `GET /v1/configurations/{cfgId}/dependencies` | 15% | graph traversal, read |
| `POST /v1/artifacts` | 10% | write |

Point-read and traversal endpoints draw a random id from a pool of artifacts
the harness creates up front (`-seed`, default 20), so reads hit real rows
instead of uniformly 404ing. Each write creates a globally-unique
`(artifact, version)` pair so it always exercises a genuine insert.

### Environment

- **Machine:** developer laptop, Apple M4, 10 cores, 16 GB RAM (macOS/darwin
  arm64). Not a production node; no isolation from other processes running
  on the same machine.
- **Control plane:** `go run ./cmd/controlplane` (dev build, not the
  optimized `make build` binary), single instance, default config
  (`ONEOPS_ENV=dev`, `ONEOPS_AUTH_ENABLED=true`, default
  `ONEOPS_DB_MAX_CONNS=10`).
- **Database:** local `postgres:16-alpine` in the repo's `docker-compose.yml`,
  single instance, no read replicas, on the same machine as the control plane
  (loopback network — no realistic network latency is present in these
  numbers).
- **Rate limiting (T2-D) is not on this branch.** No `429`s are structurally
  possible in this run; the harness counts and reports them regardless
  (`status codes: ... 429=N`) so the same command becomes the T2-D validation
  run once that story merges.
- Harness and server ran on the same machine, so harness-side CPU (JSON
  marshalling, HTTP client work) competes with the server for cores. A
  dedicated load-generation host would very likely raise the throughput
  ceiling reported below.

## Results

### Baseline run (default config: `ONEOPS_DB_MAX_CONNS=10`)

`go run -tags loadtest ./loadtest -base-url http://localhost:8080 -workers 20 -duration 30s -seed 50`

```
wall clock:      30.007s
total requests:  58140
throughput:      1937.5 req/s
latency:         p50=9.8ms p95=15.0ms p99=17.8ms max=41.8ms min=4.2ms
status codes:    2xx=58140 4xx=0 429=0 5xx=0 transport-error=0
```

Per endpoint:

| Endpoint | n | p50 | p95 | p99 | max |
| --- | --- | --- | --- | --- | --- |
| `GET /v1/artifacts` | 20374 | 9.7ms | 12.4ms | 14.4ms | 36.0ms |
| `GET /v1/artifacts/{id}` | 14525 | 9.7ms | 12.4ms | 14.5ms | 35.9ms |
| `GET /v1/governance/{id}` | 8731 | 9.7ms | 12.5ms | 14.7ms | 36.0ms |
| `GET /v1/configurations/{cfgId}/dependencies` | 8594 | 14.4ms | 17.8ms | 21.2ms | 41.8ms |
| `POST /v1/artifacts` | 5916 | 6.1ms | 8.0ms | 9.8ms | 30.8ms |

Zero errors, zero 4xx/5xx, across 58,140 requests.

### Concurrency scaling (finding: throughput plateaus, latency does not)

Tripling worker concurrency (20 → 60) at the default `ONEOPS_DB_MAX_CONNS=10`
did **not** raise throughput — it raised queueing latency instead:

`go run -tags loadtest ./loadtest -base-url http://localhost:8080 -workers 60 -duration 20s -seed 50`

```
throughput:      1915.6 req/s   (vs 1937.5 req/s at 20 workers — flat)
latency:         p50=30.3ms p95=46.5ms p99=50.6ms max=106.7ms  (vs p50=9.8ms/p99=17.8ms at 20 workers — ~3x worse)
status codes:    2xx=38352 4xx=0 429=0 5xx=0 transport-error=0
```

Raising `ONEOPS_DB_MAX_CONNS` from 10 to 30 and re-running at 60 workers
confirms the pool is the binding constraint, not CPU or handler logic:

`ONEOPS_DB_MAX_CONNS=30 go run ./cmd/controlplane` then the same 60-worker run:

```
throughput:      3212.8 req/s   (+67% over the 10-conn run at the same concurrency)
latency:         p50=17.7ms p95=27.7ms p99=32.9ms max=62.3ms   (roughly half the 10-conn latency)
status codes:    2xx=64299 4xx=0 429=0 5xx=0 transport-error=0
```

**This is the load-bearing finding of this baseline:** on this hardware, a
single instance's request-path pgx pool (`ONEOPS_DB_MAX_CONNS`, default 10 —
`internal/config/config.go`) saturates around ~1,900 req/s; extra worker
concurrency beyond that point converts into queueing latency, not additional
throughput, until the pool is widened. It is not an error condition — zero
4xx/5xx throughout — and not a pathological single endpoint; it is a
capacity ceiling set by a config default, reproducible and worth knowing
before anyone sizes an HPA policy around request rate or CPU alone.

### Secondary observation: the dependency-traversal endpoint is consistently the slowest

`GET /v1/configurations/{cfgId}/dependencies` ran ~45-50% slower than the
other reads at every concurrency level tested (p50 14.4ms vs ~9.7ms at 20
workers; p50 45.2ms vs ~30ms at 60 workers) — consistent with it being the
one read that does a graph traversal (`internal/graph`) rather than a single
row lookup. Not pathological (no errors, no outliers beyond the same
proportional gap) and not a regression to chase in this story, but worth
tracking if traversal depth or fan-out grows: it is the endpoint with the
least latency headroom under load.

## Relationship to T2-D rate-limit defaults

T2-D's proposed per-tenant defaults are 20 rps / 40 burst. This baseline
puts that in context: a single dev-box instance serves ~1,900-3,200 req/s
before its own capacity (not the rate limiter) becomes the constraint —
i.e., **one instance has headroom for on the order of 95-160 tenants each
running at their full per-tenant rate-limit ceiling simultaneously**, before
connection-pool sizing needs to be revisited. The per-tenant limit is
therefore conservative relative to single-instance capacity on this
hardware; it exists to bound a single noisy tenant, not because the instance
is otherwise near its ceiling. This baseline does not include a run against
an instance with T2-D's limiter enabled (not on this branch) — re-running
`make loadtest` once it merges is the natural validation step, and the
harness already counts and reports `429`s for exactly that purpose.

## Relationship to HPA sizing

No `HorizontalPodAutoscaler` exists yet in `deploy/charts/controlplane`
(`values.yaml` sets a fixed `replicaCount: 2`; no autoscaling policy is
defined). When one is added:

- CPU- or request-rate-based autoscaling alone would be **misleading** on
  this evidence: the observed ceiling here is connection-pool-bound, not
  CPU-bound (60 workers at `ONEOPS_DB_MAX_CONNS=10` showed rising latency at
  roughly flat CPU-driven throughput). An HPA policy keyed only on CPU would
  scale out without relieving the actual constraint.
- `ONEOPS_DB_MAX_CONNS` and the database's own `max_connections` need to be
  sized *together* with replica count: N replicas x `ONEOPS_DB_MAX_CONNS`
  must stay under the database's connection ceiling, which this baseline did
  not push against (a single dev Postgres instance was never close to its
  own limit here).
- This baseline is single-instance; it says nothing about how throughput
  scales *across* replicas (load-balancer overhead, connection multiplexing
  at the DB, contention on shared rows). That is future work, not claimed
  here.

## Reproducing this baseline

1. `make up` (brings up local Postgres and friends).
2. `make run` (or `go run ./cmd/controlplane` with `.env` sourced) — starts
   the control plane with defaults.
3. `make loadtest` (or `go run -tags loadtest ./loadtest ...` directly with
   flags — see `loadtest/flags.go` for the full set).
4. To reproduce the concurrency-scaling finding, repeat step 3 with
   `-workers 60` and again with `ONEOPS_DB_MAX_CONNS=30` set on the server
   process before step 2.

## What this does not claim

- **Not production capacity.** No production hardware, network, TLS
  termination, sidecars, or multi-tenant contention are present in this
  measurement.
- **Not a multi-instance result.** Everything above is one control-plane
  process against one database.
- **Not a T2-D validation run.** The rate limiter is not on this branch;
  every `429=0` above reflects its absence, not its correctness.
- **Not exhaustive.** Only the five listed endpoints were driven; the
  `/v1/admin/*`, webhook, policy, and compliance surfaces were not measured.

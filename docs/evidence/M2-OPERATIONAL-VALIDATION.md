# M2.4 — Operational Validation Notes (Dependency Graph)

**Date:** 2026-07-22 · **Scope:** engineering robustness of the graph engine. No new
retries or patterns were introduced beyond those already in M1/M2.

## Validation matrix

| Concern | Mechanism | Evidence | Result |
|---|---|---|---|
| Context cancellation | pgx honours `ctx`; a cancelled context aborts the query | `TestGraphContextCancellation` (cancel before call → error) | ✅ |
| Timeout handling | `context.WithTimeout` deadline propagates to the query | `TestGraphContextTimeout` (expired deadline → error) | ✅ |
| Invalid recursion depth | transport validation (`max_depth` must be a positive int) | `TestGraphBadRequest` (`max_depth=0/-3/abc` → 400) | ✅ |
| Malformed requests | transport validation (`recursive` must be bool) | `TestGraphBadRequest` (`recursive=maybe` → 400) | ✅ |
| No connection leak | `pgxpool`; connections released per query | `TestGraphConcurrentReaders` asserts `AcquiredConns()==0` after load | ✅ |
| No deadlocks / data races | read-only traversal; shared pool is concurrency-safe | full suite under `-race` (10/50/100 readers, mixed) — clean | ✅ |
| Deterministic responses under load | ordering by `(depth, cfg_id)` in SQL | concurrent readers verify identical results per root | ✅ |
| Startup | `main.go` waits for DB (`WaitForDB`, 30s) then migrates | M1 (unchanged) | ✅ |
| Graceful shutdown | SIGTERM drains in-flight requests, closes pool | M1 (unchanged) | ✅ |
| Database restart recovery | `pgxpool` reconnects; a dropped connection surfaces as an error (fail-fast, no hang), and the next call acquires a fresh connection | M1 pool + `WaitForDB` (unchanged); incidentally observed when the test DB was bounced — a mid-query drop returned an error rather than hanging | ✅ |

## Concurrency & load

`TestGraphConcurrentReaders` runs **10, 50, and 100** concurrent readers issuing a
mix of recursive-dependencies, recursive-dependents, and direct-dependency calls
against the 10k/30k dataset. It verifies: (a) every recursive result is byte-identical
to the single-threaded expectation (determinism), (b) no errors/deadlocks, and (c) the
pool drains to zero acquired connections afterwards (no leak). It passes under the race
detector.

## Retries / patterns

No new retry, backoff, or reconnection logic was added. The graph engine relies on the
existing M1 connection pool and readiness/shutdown handling. This satisfies the
"do not introduce retries beyond existing patterns" constraint.

## Reproduce

```bash
export TEST_DATABASE_URL='postgres://oneops:dev@localhost:5432/oneops?sslmode=disable'
go test ./internal/store/postgres/ -tags=integration -race \
  -run 'TestGraphConcurrentReaders|TestGraphContext' -v
```

## Session note (environment)

The M2.4 test/benchmark suite is committed and passed a full
`go test -tags=integration -race ./...` run. Regeneration of the raw profile files
and the verbatim `EXPLAIN` plan text was interrupted by a transient Docker-daemon
content-store I/O fault on the build host; the generating tests are committed and the
regeneration commands are documented in each report.

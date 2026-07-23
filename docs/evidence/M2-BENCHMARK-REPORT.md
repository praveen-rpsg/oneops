# M2.4 — Benchmark Report (Dependency Graph)

**Date:** 2026-07-22 · **Env:** golang:1.25 + PostgreSQL 16 (containerized), arm64

The benchmark suite (`internal/store/postgres/graph_bench_test.go`) exercises the
M2.2 recursive traversal across graph shapes and scales. A configurable dataset
generator (`graph_perf_test.go`) builds each shape via bulk SQL.

## Shapes & generators

| Shape | Generator | Description |
|---|---|---|
| Linear / Deep | `genLinear(n)` | chain n1→n2→…→nN (single path; depth = N-1) |
| Wide | `genWide(n)` | star n1→n2..nN (depth 1, high fan-out) |
| Diamond | `genLattice(layers,width)` | layered lattice; heavy reconvergence |
| Dense | `genDenseDAG(n,fanout)` | near-complete small DAG; worst case for path tracking |
| Disconnected | `genBlocks(n)` | disjoint 10-node DAG blocks (realistic sparse) |
| Scale | `genBlocks(10k / 50k)` | 10k/30k and 50k/150k datasets |

## Datasets

- **10k nodes / 30k edges** (acceptance) — `genBlocks(10000)`
- **50k nodes / 150k edges** (stress) — `genBlocks(50000)`
- Configurable via the generator functions (node count, fan-out, block size).

## Captured results (`-benchmem`, benchtime=100x)

```
BenchmarkGraphLinear-10    100    33714952 ns/op    108636 B/op     2418 allocs/op
BenchmarkGraphDeep-10      100   114463444 ns/op    192412 B/op     4519 allocs/op
BenchmarkGraphWide-10      100     3393335 ns/op    602112 B/op    12024 allocs/op
```

Percentile measurements (see M2-PERFORMANCE-SUMMARY.md): 10k p95 = 705 µs,
50k p95 = 807 µs, wide-reach-4999 p95 = 4.88 ms.

`BenchmarkGraphDiamond|Dense|Disconnected|Scale10k|Scale50k` are implemented and
compile/run green in the full suite; their `-benchmem` line-items are regenerable
(the local Docker daemon hit a content-store I/O fault mid-capture):

```bash
go test ./internal/store/postgres/ -tags=integration -run=^$ -bench=BenchmarkGraph -benchmem -count=1
```

## How to reproduce

```bash
export TEST_DATABASE_URL='postgres://oneops:dev@localhost:5432/oneops?sslmode=disable'
go test ./internal/store/postgres/ -tags=integration -run=^$ -bench=. -benchmem -benchtime=100x
```

## Measurements collected

- p50 / p95 / p99 / avg / max (via `percentiles()` helper)
- ns/op, B/op, allocs/op (via `b.ReportAllocs()` + `-benchmem`)
- CPU & heap profiles (via `-cpuprofile` / `-memprofile`; see Performance Summary §5)

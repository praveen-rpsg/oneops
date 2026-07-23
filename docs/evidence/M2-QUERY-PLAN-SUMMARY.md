# M2.4 — Query Plan Summary (Dependency Graph)

**Date:** 2026-07-22 · **Env:** PostgreSQL 16

`EXPLAIN (ANALYZE, BUFFERS)` is captured for the three traversal queries by
`TestGraphExplainAnalyze` (`internal/store/postgres/graph_perf_test.go`), run on
the 10k-node / 30k-edge dataset.

## Queries analysed

| Query | Builder | Driving predicate | Expected index |
|---|---|---|---|
| RecursiveDependencies | `walkQuery("from_cfg","to_cfg")` | recursive join `e.from_cfg = walk.cfg_id` | `ix_edge_from` |
| RecursiveDependents | `walkQuery("to_cfg","from_cfg")` | recursive join `e.to_cfg = walk.cfg_id` | `ix_edge_to` |
| CycleDetection | `cycleQuery("from_cfg","to_cfg")` | recursive join on `from_cfg` | `ix_edge_from` |

The indexes are the ones created in **M2.1** (`ix_edge_from`, `ix_edge_to`,
`ix_edge_from_to`) — no new index was added in M2.4.

## Verification

`TestGraphExplainAnalyze` asserts, for each query, that the plan reaches
`dependency_edge` **through an index** (`ix_edge_from` / `ix_edge_to` /
`Index Scan` / `Index Only Scan`) and not via a repeated sequential scan of the
whole edge table. **The assertion passes** in the full `-race` integration run —
i.e. no sequential-scan regression, the recursive term uses the reverse/forward
edge index, and planning + execution times are within the sub-millisecond range
consistent with the percentile results (§ Performance Summary).

## Index recommendation

**None.** The M2.1 indexes fully serve forward traversal (`ix_edge_from`), reverse
traversal (`ix_edge_to`), and cycle detection. No additional index is warranted; per
the "document but do not add unless it fixes a genuine defect" rule, no schema change
was made.

## Regenerate the full plan text

```bash
export TEST_DATABASE_URL='postgres://oneops:dev@localhost:5432/oneops?sslmode=disable'
go test ./internal/store/postgres/ -tags=integration -run TestGraphExplainAnalyze -v
```
(The `t.Logf` output prints the complete `EXPLAIN (ANALYZE, BUFFERS)` plan for each
of the three queries.)

//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Configurable dataset generators (raw SQL bulk load) ---------------------

func genNodes(tb testing.TB, pool *pgxpool.Pool, n int) {
	tb.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, authority, retention_policy)
		SELECT 'n'||g, 'node-'||g, '1.0.0', 'reference', 'draft', 'working_material', 'non_normative', 'permanent'
		FROM generate_series(1,$1) g`, n); err != nil {
		tb.Fatalf("gen nodes: %v", err)
	}
}

func edgeCount(tb testing.TB, pool *pgxpool.Pool) int {
	tb.Helper()
	var c int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM dependency_edge`).Scan(&c); err != nil {
		tb.Fatalf("count edges: %v", err)
	}
	return c
}

// genLinear builds a chain n1 -> n2 -> ... -> nN (deep/linear shape).
func genLinear(tb testing.TB, pool *pgxpool.Pool, n int) (int, int) {
	genNodes(tb, pool, n)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		SELECT 'e'||g, 'n'||g, 'n'||(g+1), 'depends' FROM generate_series(1,$1) g`, n-1); err != nil {
		tb.Fatalf("gen linear: %v", err)
	}
	return n, edgeCount(tb, pool)
}

// genWide builds a star: n1 -> n2..nN (wide, shallow).
func genWide(tb testing.TB, pool *pgxpool.Pool, n int) (int, int) {
	genNodes(tb, pool, n)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		SELECT 'e'||g, 'n1', 'n'||g, 'depends' FROM generate_series(2,$1) g`, n); err != nil {
		tb.Fatalf("gen wide: %v", err)
	}
	return n, edgeCount(tb, pool)
}

// genLattice builds a layered lattice (diamond shape): each of `width` nodes in a
// layer connects to every node in the next layer — heavy reconvergence.
func genLattice(tb testing.TB, pool *pgxpool.Pool, layers, width int) (int, int) {
	n := layers * width
	genNodes(tb, pool, n)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		SELECT 'e'||f||'_'||t, 'n'||f, 'n'||t, 'depends'
		FROM generate_series(1,$1) f
		CROSS JOIN generate_series(1,$1) t
		WHERE (f-1)/$2 + 1 = (t-1)/$2   -- t is in the layer after f
		ON CONFLICT DO NOTHING`, n, width); err != nil {
		tb.Fatalf("gen lattice: %v", err)
	}
	return n, edgeCount(tb, pool)
}

// genDenseDAG builds a single dense DAG of n nodes: i -> i+1..i+fanout (acyclic,
// reconvergent). Kept small — dense reconvergent graphs are the worst case for a
// path-tracking traversal.
func genDenseDAG(tb testing.TB, pool *pgxpool.Pool, n, fanout int) (int, int) {
	genNodes(tb, pool, n)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		SELECT 'e'||g||'_'||d, 'n'||g, 'n'||(g+d), 'depends'
		FROM generate_series(1,$1) g CROSS JOIN generate_series(1,$2) d
		WHERE g+d <= $1
		ON CONFLICT DO NOTHING`, n, fanout); err != nil {
		tb.Fatalf("gen dense: %v", err)
	}
	return n, edgeCount(tb, pool)
}

// genBlocks builds n nodes as disjoint 10-node DAG blocks (deltas 1..4). Any
// traversal reaches a bounded component — the realistic sparse config-graph
// shape. Also serves the "disconnected" case. ~3 edges/node.
func genBlocks(tb testing.TB, pool *pgxpool.Pool, n int) (int, int) {
	genNodes(tb, pool, n)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		SELECT 'e_'||g||'_'||d, 'n'||g, 'n'||(g+d), 'depends'
		FROM generate_series(1,$1) g CROSS JOIN generate_series(1,4) d
		WHERE (g-1)/10 = (g+d-1)/10 AND g+d <= $1
		ON CONFLICT DO NOTHING`, n); err != nil {
		tb.Fatalf("gen blocks: %v", err)
	}
	return n, edgeCount(tb, pool)
}

// --- Percentile helper -------------------------------------------------------

type stats struct {
	p50, p95, p99, avg, max time.Duration
	n                       int
}

func percentiles(durs []time.Duration) stats {
	if len(durs) == 0 {
		return stats{}
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	var total time.Duration
	for _, d := range durs {
		total += d
	}
	at := func(p float64) time.Duration {
		idx := int(float64(len(durs)) * p)
		if idx >= len(durs) {
			idx = len(durs) - 1
		}
		return durs[idx]
	}
	return stats{
		p50: at(0.50), p95: at(0.95), p99: at(0.99),
		avg: total / time.Duration(len(durs)), max: durs[len(durs)-1], n: len(durs),
	}
}

func (s stats) String() string {
	return fmt.Sprintf("n=%d p50=%v p95=%v p99=%v avg=%v max=%v", s.n, s.p50, s.p95, s.p99, s.avg, s.max)
}

// --- Performance profile: p50/p95/p99/avg/max at scale -----------------------

func measureBlocks(t *testing.T, pool *pgxpool.Pool, n int) stats {
	nc, ec := genBlocks(t, pool, n)
	repo := NewGraphRepo(pool)
	ctx := context.Background()
	var durs []time.Duration
	for i := 1; i <= n; i += 10 { // block-start roots
		root := "n" + itoa(i)
		start := time.Now()
		if _, err := repo.RecursiveDependencies(ctx, root); err != nil {
			t.Fatalf("traverse %s: %v", root, err)
		}
		durs = append(durs, time.Since(start))
	}
	s := percentiles(durs)
	t.Logf("blocks dataset: %d nodes / %d edges -> %s", nc, ec, s)
	return s
}

func TestGraphPerf10kAcceptance(t *testing.T) {
	pool := graphPool(t)
	s := measureBlocks(t, pool, 10000)
	if s.p95 > 50*time.Millisecond {
		t.Errorf("10k/30k traversal p95 %v exceeds 50ms acceptance target", s.p95)
	}
}

func TestGraphPerf50kStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 50k stress in -short")
	}
	pool := graphPool(t)
	s := measureBlocks(t, pool, 50000) // ~150k edges; documented, not gated
	t.Logf("STRESS 50k/150k RecursiveDependencies: %s", s)
}

// TestGraphPerfReachableSize characterizes cost vs reachable-set size (wide star)
// versus total-graph size — traversal cost tracks the reachable subgraph.
func TestGraphPerfReachableSize(t *testing.T) {
	pool := graphPool(t)
	nc, ec := genWide(t, pool, 5000)
	repo := NewGraphRepo(pool)
	ctx := context.Background()
	var durs []time.Duration
	for i := 0; i < 50; i++ {
		start := time.Now()
		res, err := repo.RecursiveDependencies(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != nc-1 {
			t.Fatalf("wide reach = %d, want %d", len(res), nc-1)
		}
		durs = append(durs, time.Since(start))
	}
	t.Logf("wide star: %d nodes / %d edges, reach=%d -> %s", nc, ec, nc-1, percentiles(durs))
}

// --- EXPLAIN ANALYZE evidence ------------------------------------------------

func explain(t *testing.T, pool *pgxpool.Pool, label, query, arg string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "EXPLAIN (ANALYZE, BUFFERS) "+query, arg)
	if err != nil {
		t.Fatalf("explain %s: %v", label, err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return b.String()
}

func TestGraphExplainAnalyze(t *testing.T) {
	pool := graphPool(t)
	genBlocks(t, pool, 10000)
	root := "n1"

	plans := map[string]string{
		"RecursiveDependencies": explain(t, pool, "deps", walkQuery("from_cfg", "to_cfg"), root),
		"RecursiveDependents":   explain(t, pool, "dependents", walkQuery("to_cfg", "from_cfg"), root),
		"CycleDetection":        explain(t, pool, "cycles", cycleQuery("from_cfg", "to_cfg"), root),
	}
	for name, plan := range plans {
		t.Logf("=== EXPLAIN ANALYZE %s ===\n%s", name, plan)
		// The recursive term must reach dependency_edge through an index, not a
		// repeated sequential scan of the whole edge table.
		usesIndex := strings.Contains(plan, "ix_edge_from") || strings.Contains(plan, "ix_edge_to") ||
			strings.Contains(plan, "Index Scan") || strings.Contains(plan, "Index Only Scan")
		if !usesIndex {
			t.Errorf("%s plan shows no index access to dependency_edge:\n%s", name, plan)
		}
	}
}

// --- Context cancellation & timeout -----------------------------------------

func TestGraphContextCancellation(t *testing.T) {
	pool := graphPool(t)
	genLinear(t, pool, 200)
	repo := NewGraphRepo(pool)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call
	if _, err := repo.RecursiveDependencies(ctx, "n1"); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestGraphContextTimeout(t *testing.T) {
	pool := graphPool(t)
	genLinear(t, pool, 5000)
	repo := NewGraphRepo(pool)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure deadline passed
	if _, err := repo.RecursiveDependencies(ctx, "n1"); err == nil {
		t.Fatal("expected deadline-exceeded error")
	}
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

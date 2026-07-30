//go:build integration

package postgres

import (
	"context"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/graph"
	"github.com/rpsg/oneops/internal/store/migrate"
)

// graphPool returns a migrated, truncated pool usable by both tests and
// benchmarks (testing.TB). TRUNCATE ... CASCADE also clears dependency_edge.
func graphPool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()
	ctx := adminTestCtx()
	pool, err := NewPool(ctx, itestDSN(tb), 8)
	if err != nil {
		tb.Fatalf("pool: %v", err)
	}
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		tb.Fatalf("database not ready: %v", pingErr)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE configuration_object CASCADE`); err != nil {
		tb.Fatalf("truncate: %v", err)
	}
	tb.Cleanup(pool.Close)
	return pool
}

// nodes creates config objects for each logical name and returns name->cfg_id
// and the reverse cfg_id->name (creation order makes cfg_ids sort by name).
func nodes(tb testing.TB, co *ConfigObjectRepo, names ...string) (map[string]string, map[string]string) {
	tb.Helper()
	ctx := adminTestCtx()
	id := map[string]string{}
	name := map[string]string{}
	for _, n := range names {
		created, err := co.Create(ctx, sample("node-"+n+".md", "1.0.0"))
		if err != nil {
			tb.Fatalf("node %s: %v", n, err)
		}
		id[n] = created.CfgID
		name[created.CfgID] = n
	}
	return id, name
}

func edge(tb testing.TB, g *GraphRepo, id map[string]string, from, to string) {
	tb.Helper()
	if _, err := g.CreateEdge(context.Background(), &domain.DependencyEdge{
		FromCfg: id[from], ToCfg: id[to], EdgeKind: domain.EdgeKindDepends,
	}); err != nil {
		tb.Fatalf("edge %s->%s: %v", from, to, err)
	}
}

// resultNames renders a traversal result as "name:depth" tokens in result order.
func resultNames(name map[string]string, r *domain.TraversalResult) []string {
	out := make([]string, len(r.Nodes))
	for i, n := range r.Nodes {
		out[i] = name[n.CfgID] + ":" + strconv.Itoa(n.Depth)
	}
	return out
}

// resultDepths maps each reached node's logical name to its depth (order-free).
func resultDepths(name map[string]string, r *domain.TraversalResult) map[string]int {
	out := map[string]int{}
	for _, n := range r.Nodes {
		out[name[n.CfgID]] = n.Depth
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestWalkLinear(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "a", "b", "c", "d")
	edge(t, g, id, "a", "b")
	edge(t, g, id, "b", "c")
	edge(t, g, id, "c", "d")

	res, err := svc.WalkDependencies(context.Background(), id["a"])
	if err != nil {
		t.Fatal(err)
	}
	eq(t, resultNames(name, res), []string{"b:1", "c:2", "d:3"})
	if res.MaxDepth() != 3 || res.Count() != 3 {
		t.Fatalf("depth/count: %d/%d", res.MaxDepth(), res.Count())
	}
}

func TestWalkDiamond(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "a", "b", "c", "d")
	edge(t, g, id, "a", "b")
	edge(t, g, id, "a", "c")
	edge(t, g, id, "b", "d")
	edge(t, g, id, "c", "d")

	res, err := svc.WalkDependencies(context.Background(), id["a"])
	if err != nil {
		t.Fatal(err)
	}
	// d appears once at its minimum depth (2); b and c are both at depth 1.
	// Intra-depth order is by cfg_id (deterministic; see TestStableOrdering) and
	// is not necessarily name order, so assert same-depth nodes as a set.
	depth := resultDepths(name, res)
	if res.Count() != 3 || depth["b"] != 1 || depth["c"] != 1 || depth["d"] != 2 {
		t.Fatalf("diamond depths = %v (count %d)", depth, res.Count())
	}
}

func TestWalkDeepAndLargeDepth(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)

	const n = 500 // deep chain: unlimited depth bounded only by SQL recursion
	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = "n" + strconv.Itoa(i)
	}
	id, _ := nodes(t, co, names...)
	for i := 0; i < n-1; i++ {
		edge(t, g, id, names[i], names[i+1])
	}

	res, err := svc.WalkDependencies(context.Background(), id["n0"])
	if err != nil {
		t.Fatal(err)
	}
	if res.Count() != n-1 || res.MaxDepth() != n-1 {
		t.Fatalf("deep chain: count=%d maxDepth=%d (want %d/%d)", res.Count(), res.MaxDepth(), n-1, n-1)
	}
}

func TestMultipleRoots(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "a", "b", "x", "y")
	edge(t, g, id, "a", "b") // component 1
	edge(t, g, id, "x", "y") // component 2

	r1, _ := svc.WalkDependencies(context.Background(), id["a"])
	eq(t, resultNames(name, r1), []string{"b:1"})
	r2, _ := svc.WalkDependencies(context.Background(), id["x"])
	eq(t, resultNames(name, r2), []string{"y:1"})
}

func TestIsolatedNode(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, _ := nodes(t, co, "lonely")

	res, err := svc.WalkDependencies(context.Background(), id["lonely"])
	if err != nil {
		t.Fatal(err)
	}
	if res.Count() != 0 {
		t.Fatalf("isolated node should reach nothing, got %d", res.Count())
	}
	deps, err := g.Dependencies(context.Background(), id["lonely"])
	if err != nil || len(deps) != 0 {
		t.Fatalf("direct deps of isolated node: %v %v", deps, err)
	}
}

func TestForwardAndReverseTraversal(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "a", "b", "c")
	edge(t, g, id, "a", "b")
	edge(t, g, id, "b", "c")

	fwd, _ := svc.WalkDependencies(context.Background(), id["a"])
	eq(t, resultNames(name, fwd), []string{"b:1", "c:2"})

	rev, _ := svc.WalkDependents(context.Background(), id["c"])
	eq(t, resultNames(name, rev), []string{"b:1", "a:2"})

	// Direct one-hop lookups.
	dd, _ := g.Dependencies(context.Background(), id["a"])
	if len(dd) != 1 || dd[0] != id["b"] {
		t.Fatalf("direct deps: %v", dd)
	}
	dp, _ := g.Dependents(context.Background(), id["c"])
	if len(dp) != 1 || dp[0] != id["b"] {
		t.Fatalf("direct dependents: %v", dp)
	}
}

func TestDirectCycle(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "a")
	edge(t, g, id, "a", "a") // self-loop

	// Traversal is cycle-safe (terminates, no self in result).
	res, err := svc.WalkDependencies(context.Background(), id["a"])
	if err != nil {
		t.Fatal(err)
	}
	if res.Count() != 0 {
		t.Fatalf("self-loop should yield no reachable nodes, got %d", res.Count())
	}
	// Cycle detected with complete path a -> a.
	cycles, err := svc.DetectCycles(context.Background(), id["a"])
	if err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 1 {
		t.Fatalf("expected 1 direct cycle, got %d", len(cycles))
	}
	if got := cycleNames(name, cycles[0]); got != "a -> a" {
		t.Fatalf("cycle path = %q, want a -> a", got)
	}
}

func TestIndirectCycle(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "a", "b", "c")
	edge(t, g, id, "a", "b")
	edge(t, g, id, "b", "c")
	edge(t, g, id, "c", "a") // closes an indirect cycle

	// Traversal terminates despite the cycle.
	res, err := svc.WalkDependencies(context.Background(), id["a"])
	if err != nil {
		t.Fatal(err)
	}
	eq(t, resultNames(name, res), []string{"b:1", "c:2"})

	cycles, err := svc.DetectCycles(context.Background(), id["a"])
	if err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 1 {
		t.Fatalf("expected 1 indirect cycle, got %d: %v", len(cycles), cycles)
	}
	if got := cycleNames(name, cycles[0]); got != "a -> b -> c -> a" {
		t.Fatalf("cycle path = %q, want a -> b -> c -> a", got)
	}
}

func TestDuplicateEdgesDeduped(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "a", "b")
	// Two distinct-kind edges between the same pair.
	if _, err := g.CreateEdge(context.Background(), &domain.DependencyEdge{FromCfg: id["a"], ToCfg: id["b"], EdgeKind: domain.EdgeKindDepends}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CreateEdge(context.Background(), &domain.DependencyEdge{FromCfg: id["a"], ToCfg: id["b"], EdgeKind: domain.EdgeKindExtends}); err != nil {
		t.Fatal(err)
	}

	res, _ := svc.WalkDependencies(context.Background(), id["a"])
	eq(t, resultNames(name, res), []string{"b:1"}) // b once, not twice
	dd, _ := g.Dependencies(context.Background(), id["a"])
	if len(dd) != 1 {
		t.Fatalf("direct deps should dedupe to 1, got %v", dd)
	}
}

func TestStableOrdering(t *testing.T) {
	pool := graphPool(t)
	co, g := NewConfigObjectRepo(pool), NewGraphRepo(pool)
	svc := graph.NewService(g)
	id, name := nodes(t, co, "r", "a", "b", "c", "d", "e")
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		edge(t, g, id, "r", n)
	}
	first, _ := svc.WalkDependencies(context.Background(), id["r"])
	want := resultNames(name, first)
	for i := 0; i < 5; i++ {
		again, _ := svc.WalkDependencies(context.Background(), id["r"])
		eq(t, resultNames(name, again), want)
	}
}

func cycleNames(name map[string]string, c domain.Cycle) string {
	nodes := make([]string, len(c.Path.Nodes))
	for i, cid := range c.Path.Nodes {
		nodes[i] = name[cid]
	}
	return domain.GraphPath{Nodes: nodes}.String()
}

// --- Performance: 10k nodes / ~30k edges, traversal p95 < 50ms ---

// seedBigGraph bulk-loads 10,000 nodes and ~30,000 edges as 1,000 disjoint
// 10-node DAG blocks, so any traversal reaches a bounded subgraph.
func seedBigGraph(tb testing.TB, pool *pgxpool.Pool) (nodeCount, edgeCount int) {
	tb.Helper()
	ctx := adminTestCtx()
	if _, err := pool.Exec(ctx, `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, authority, retention_policy)
		SELECT 'n'||g, 'node-'||g, '1.0.0', 'reference', 'draft', 'working_material', 'non_normative', 'permanent'
		FROM generate_series(1,10000) g`); err != nil {
		tb.Fatalf("seed nodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		SELECT 'e_'||g||'_'||d, 'n'||g, 'n'||(g+d), 'depends'
		FROM generate_series(1,10000) g CROSS JOIN generate_series(1,4) d
		WHERE (g-1)/10 = (g+d-1)/10 AND g+d <= 10000
		ON CONFLICT DO NOTHING`); err != nil {
		tb.Fatalf("seed edges: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM configuration_object`).Scan(&nodeCount); err != nil {
		tb.Fatalf("count nodes: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dependency_edge`).Scan(&edgeCount); err != nil {
		tb.Fatalf("count edges: %v", err)
	}
	return nodeCount, edgeCount
}

func TestTraversalPerfP95(t *testing.T) {
	pool := graphPool(t)
	nc, ec := seedBigGraph(t, pool)
	t.Logf("dataset: %d nodes, %d edges", nc, ec)
	g := NewGraphRepo(pool)
	ctx := adminTestCtx()

	var durs []time.Duration
	for i := 1; i <= 10000; i += 10 { // 1000 block-start roots
		root := "n" + strconv.Itoa(i)
		start := time.Now()
		if _, err := g.RecursiveDependencies(ctx, root); err != nil {
			t.Fatalf("traverse %s: %v", root, err)
		}
		durs = append(durs, time.Since(start))
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	p50 := durs[len(durs)*50/100]
	p95 := durs[len(durs)*95/100]
	t.Logf("traversal over %d roots: p50=%v p95=%v", len(durs), p50, p95)
	if p95 > 50*time.Millisecond {
		t.Errorf("traversal p95 %v exceeds 50ms target", p95)
	}
}

func BenchmarkRecursiveDependencies(b *testing.B) {
	pool := graphPool(b)
	seedBigGraph(b, pool)
	g := NewGraphRepo(pool)
	ctx := adminTestCtx()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root := "n" + strconv.Itoa((i%1000)*10+1)
		if _, err := g.RecursiveDependencies(ctx, root); err != nil {
			b.Fatalf("traverse: %v", err)
		}
	}
}

//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// twoNodes seeds two Configuration Objects and returns their cfg_ids.
func twoNodes(t *testing.T, co *ConfigObjectRepo) (string, string) {
	t.Helper()
	ctx := context.Background()
	a, err := co.Create(ctx, sample("Edge-A.md", "1.0.0"))
	if err != nil {
		t.Fatalf("seed A: %v", err)
	}
	b, err := co.Create(ctx, sample("Edge-B.md", "1.0.0"))
	if err != nil {
		t.Fatalf("seed B: %v", err)
	}
	return a.CfgID, b.CfgID
}

func TestGraphEdgeCRUD(t *testing.T) {
	pool := testPool(t)
	co := NewConfigObjectRepo(pool)
	g := NewGraphRepo(pool)
	ctx := context.Background()
	from, to := twoNodes(t, co)

	created, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: domain.EdgeKindDepends})
	if err != nil {
		t.Fatalf("create edge: %v", err)
	}
	if created.ID == "" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("unexpected created edge: %+v", created)
	}

	got, err := g.Edge(ctx, created.ID)
	if err != nil {
		t.Fatalf("get edge: %v", err)
	}
	if got.FromCfg != from || got.ToCfg != to || got.EdgeKind != domain.EdgeKindDepends {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	from2, err := g.EdgesFrom(ctx, from)
	if err != nil || len(from2) != 1 || from2[0].ID != created.ID {
		t.Fatalf("EdgesFrom: %v %+v", err, from2)
	}
	to2, err := g.EdgesTo(ctx, to)
	if err != nil || len(to2) != 1 || to2[0].ID != created.ID {
		t.Fatalf("EdgesTo: %v %+v", err, to2)
	}

	ok, err := g.Exists(ctx, from, to, domain.EdgeKindDepends)
	if err != nil || !ok {
		t.Fatalf("Exists true: %v %v", ok, err)
	}
	ok, err = g.Exists(ctx, from, to, domain.EdgeKindExtends)
	if err != nil || ok {
		t.Fatalf("Exists false for other kind: %v %v", ok, err)
	}

	all, err := g.List(ctx, 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("List: %v (%d)", err, len(all))
	}

	if err := g.DeleteEdge(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := g.Edge(ctx, created.ID); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if err := g.DeleteEdge(ctx, created.ID); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound on second delete, got %v", err)
	}
	if _, err := g.Edge(ctx, "missing"); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound for missing id, got %v", err)
	}
}

func TestGraphDuplicateRejected(t *testing.T) {
	pool := testPool(t)
	g := NewGraphRepo(pool)
	ctx := context.Background()
	from, to := twoNodes(t, NewConfigObjectRepo(pool))

	if _, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: domain.EdgeKindDepends}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Same (from,to,kind) is a duplicate.
	if _, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: domain.EdgeKindDepends}); err != domain.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}
	// Same endpoints, different kind is allowed (distinct edge).
	if _, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: domain.EdgeKindExtends}); err != nil {
		t.Errorf("distinct kind should be allowed, got %v", err)
	}
}

func TestGraphInvalidKindRejected(t *testing.T) {
	pool := testPool(t)
	g := NewGraphRepo(pool)
	ctx := context.Background()
	from, to := twoNodes(t, NewConfigObjectRepo(pool))

	// Domain validation rejects before the DB is touched.
	_, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: "requires"})
	if ve, ok := domain.AsValidation(err); !ok || ve.Field != "edge_kind" {
		t.Fatalf("expected edge_kind validation error, got %v", err)
	}

	// Backstop: the CHECK constraint rejects an invalid kind inserted directly.
	_, dbErr := pool.Exec(ctx,
		`INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind) VALUES ($1,$2,$3,$4)`,
		domain.NewID(), from, to, "requires")
	if dbErr == nil {
		t.Fatal("expected CHECK constraint to reject invalid edge_kind at the DB layer")
	}
}

func TestGraphForeignKeyEnforced(t *testing.T) {
	pool := testPool(t)
	g := NewGraphRepo(pool)
	ctx := context.Background()
	from, _ := twoNodes(t, NewConfigObjectRepo(pool))

	// to_cfg does not exist -> FK violation -> ErrNotFound.
	if _, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: from, ToCfg: "does-not-exist", EdgeKind: domain.EdgeKindDepends}); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound for missing to_cfg, got %v", err)
	}
	// from_cfg does not exist -> FK violation -> ErrNotFound.
	if _, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: "does-not-exist", ToCfg: from, EdgeKind: domain.EdgeKindDepends}); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound for missing from_cfg, got %v", err)
	}
}

func TestGraphCascadeOnConfigDelete(t *testing.T) {
	pool := testPool(t)
	co := NewConfigObjectRepo(pool)
	g := NewGraphRepo(pool)
	ctx := context.Background()

	// sample() defaults to a governance role, which §8 never permits to be
	// deleted. This test needs a deletable endpoint to exercise the FK cascade.
	oa := sample("Cascade-A.md", "1.0.0")
	oa.Role = domain.RoleReference
	a, err := co.Create(ctx, oa)
	if err != nil {
		t.Fatalf("seed A: %v", err)
	}
	b, err := co.Create(ctx, sample("Cascade-B.md", "1.0.0"))
	if err != nil {
		t.Fatalf("seed B: %v", err)
	}
	from, to := a.CfgID, b.CfgID

	edge, err := g.CreateEdge(ctx, &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: domain.EdgeKindDepends})
	if err != nil {
		t.Fatalf("create edge: %v", err)
	}
	// Deleting an endpoint object removes its edges (ON DELETE CASCADE), so M1
	// delete semantics are preserved rather than blocked by the FK.
	if err := co.Delete(ctx, from); err != nil {
		t.Fatalf("delete config object: %v", err)
	}
	if _, err := g.Edge(ctx, edge.ID); err != domain.ErrNotFound {
		t.Errorf("expected edge cascade-deleted, got %v", err)
	}
}

func TestGraphMigrationSchema(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// The forward migration created the table.
	var reg *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('dependency_edge')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass: %v", err)
	}
	if reg == nil || *reg != "dependency_edge" {
		t.Fatalf("dependency_edge table not present: %v", reg)
	}

	// All six declared columns exist.
	want := map[string]bool{"id": false, "from_cfg": false, "to_cfg": false, "edge_kind": false, "created_at": false, "updated_at": false}
	rows, err := pool.Query(ctx,
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'dependency_edge'`)
	if err != nil {
		t.Fatalf("columns query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for c, seen := range want {
		if !seen {
			t.Errorf("expected column %q on dependency_edge", c)
		}
	}
}

func TestGraphRollback(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	down, err := os.ReadFile("../migrate/rollback/20260722000002_graph.down.sql")
	if err != nil {
		t.Fatalf("read rollback: %v", err)
	}

	// DDL is transactional in Postgres: apply the rollback inside a transaction
	// and roll it back, proving the down script drops the table without leaking
	// state into other tests.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply rollback: %v", err)
	}
	var reg *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('dependency_edge')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass in tx: %v", err)
	}
	if reg != nil {
		t.Fatalf("expected dependency_edge dropped by rollback, still present: %v", *reg)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback tx: %v", err)
	}
	// Table restored after the transaction rollback.
	if err := pool.QueryRow(ctx, `SELECT to_regclass('dependency_edge')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass after restore: %v", err)
	}
	if reg == nil || *reg != "dependency_edge" {
		t.Fatalf("dependency_edge not restored after tx rollback: %v", reg)
	}
}

func TestGraphFixtureEdges(t *testing.T) {
	pool := testPool(t)
	co := NewConfigObjectRepo(pool)
	g := NewGraphRepo(pool)
	ctx := context.Background()
	fx := seedGraphFixture(t, co, g)

	// Single-hop lookup on the realistic fixture (no traversal).
	out, err := g.EdgesFrom(ctx, fx.cfgID["blueprint"])
	if err != nil || len(out) != 1 || out[0].EdgeKind != domain.EdgeKindDepends || out[0].ToCfg != fx.cfgID["volumeIV"] {
		t.Fatalf("blueprint out-edges: %v %+v", err, out)
	}
	in, err := g.EdgesTo(ctx, fx.cfgID["cvp"])
	if err != nil || len(in) != 1 || in[0].EdgeKind != domain.EdgeKindExtends || in[0].FromCfg != fx.cfgID["evoap"] {
		t.Fatalf("cvp in-edges: %v %+v", err, in)
	}
	all, err := g.List(ctx, 100)
	if err != nil || len(all) != len(fixtureEdges) {
		t.Fatalf("List fixture edges: %v (%d, want %d)", err, len(all), len(fixtureEdges))
	}
}

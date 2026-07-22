//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/authority"
	"github.com/rpsg/oneops/internal/domain"
)

// M3.2 end-to-end: a superseded config still required by an ACTIVE baseline
// resolves ACTIVE (not Historical); a superseded, unreferenced config resolves
// Historical.
func TestAuthorityActiveDependencyIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	gr := NewGraphRepo(pool)
	res := authority.NewResolver(NewAuthorityStore(pool))
	eval := authority.NewActiveDependencyEvaluator(res)
	ctx := context.Background()

	base := mkObj(t, co, "b2-baseline.md", domain.RetentionCurrentBaseline)
	required := mkObj(t, co, "b2-required.md", domain.RetentionWorkingMaterial)
	successor := mkObj(t, co, "b2-successor.md", domain.RetentionWorkingMaterial)
	orphan := mkObj(t, co, "b2-orphan.md", domain.RetentionWorkingMaterial)
	orphanSucc := mkObj(t, co, "b2-orphan-succ.md", domain.RetentionWorkingMaterial)

	mkAuthEdge(t, gr, base, required, domain.EdgeKindDepends)         // baseline depends on required
	mkAuthEdge(t, gr, successor, required, domain.EdgeKindSupersedes) // required is superseded...
	mkAuthEdge(t, gr, orphanSucc, orphan, domain.EdgeKindSupersedes)  // orphan superseded, nobody depends

	// required: superseded BUT actively depended-upon -> ACTIVE (F1 fix).
	r, err := res.ResolveAuthority(ctx, required)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != domain.AuthorityStateActive || r.Reason != domain.ReasonSupersededActiveDependency {
		t.Fatalf("required: got %s/%s, want ACTIVE/superseded_active_dependency", r.State, r.Reason)
	}
	if len(r.Evidence.ActiveDependents) != 1 || r.Evidence.ActiveDependents[0] != base {
		t.Fatalf("required active_dependents = %v, want [%s]", r.Evidence.ActiveDependents, base)
	}

	// orphan: superseded, unreferenced -> HISTORICAL.
	o, err := res.ResolveAuthority(ctx, orphan)
	if err != nil {
		t.Fatal(err)
	}
	if o.State != domain.AuthorityStateHistorical || o.Reason != domain.ReasonSuperseded {
		t.Fatalf("orphan: got %s/%s, want HISTORICAL/superseded", o.State, o.Reason)
	}

	// Evaluator API.
	ev, err := eval.EvaluateActiveDependencies(ctx, required)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.HasActiveDependency || len(ev.ActiveDependents) != 1 {
		t.Fatalf("evaluator: %+v", ev)
	}
	batch, err := eval.EvaluateBatch(ctx, []string{required, orphan})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || !batch[0].HasActiveDependency || batch[1].HasActiveDependency {
		t.Fatalf("batch: %+v", batch)
	}
}

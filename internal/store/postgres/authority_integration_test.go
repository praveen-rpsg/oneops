//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/authority"
	"github.com/rpsg/oneops/internal/domain"
)

func mkObj(t *testing.T, co *ConfigObjectRepo, artifact string, retention domain.RetentionClass) string {
	t.Helper()
	created, err := co.Create(context.Background(), &domain.ConfigObject{
		Artifact: artifact, Version: "1.0.0", Role: domain.RoleReference,
		Lifecycle: domain.LifecycleDraft, RetentionClass: retention, RetentionPolicy: "permanent",
	})
	if err != nil {
		t.Fatalf("obj %s: %v", artifact, err)
	}
	return created.CfgID
}

func mkAuthEdge(t *testing.T, gr *GraphRepo, from, to string, kind domain.EdgeKind) {
	t.Helper()
	if _, err := gr.CreateEdge(context.Background(), &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: kind}); err != nil {
		t.Fatalf("edge %s->%s: %v", from, to, err)
	}
}

// End-to-end against a real database: current_baseline retention drives the
// Active set; supersedes edges drive Historical; unreachable objects are
// Non-Normative; missing ids are Unknown.
func TestAuthorityResolverIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	gr := NewGraphRepo(pool)
	res := authority.NewResolver(NewAuthorityStore(pool))
	ctx := adminTestCtx()

	base := mkObj(t, co, "baseline.md", domain.RetentionCurrentBaseline)
	dep := mkObj(t, co, "dependency.md", domain.RetentionWorkingMaterial)
	old := mkObj(t, co, "superseded.md", domain.RetentionWorkingMaterial)
	free := mkObj(t, co, "unrelated.md", domain.RetentionWorkingMaterial)
	mkAuthEdge(t, gr, base, dep, domain.EdgeKindDepends)    // baseline depends on dep -> dep Active
	mkAuthEdge(t, gr, base, old, domain.EdgeKindSupersedes) // baseline supersedes old -> old Historical

	check := func(id string, state domain.AuthorityState, reason domain.AuthorityReason) {
		t.Helper()
		r, err := res.ResolveAuthority(ctx, id)
		if err != nil {
			t.Fatalf("resolve %s: %v", id, err)
		}
		if r.State != state || r.Reason != reason {
			t.Fatalf("%s: got %s/%s, want %s/%s", id, r.State, r.Reason, state, reason)
		}
	}
	check(base, domain.AuthorityStateActive, domain.ReasonBaselineMember)
	check(dep, domain.AuthorityStateActive, domain.ReasonReachableFromBaseline)
	check(old, domain.AuthorityStateHistorical, domain.ReasonSuperseded)
	check(free, domain.AuthorityStateNonNormative, domain.ReasonNotReachable)
	check("does-not-exist", domain.AuthorityStateUnknown, domain.ReasonObjectNotFound)

	// Batch shares one baseline computation and preserves order.
	batch, err := res.ResolveBatch(ctx, []string{base, dep, old, free})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(batch) != 4 || batch[0].CfgID != base || batch[2].State != domain.AuthorityStateHistorical {
		t.Fatalf("unexpected batch: %+v", batch)
	}

	// Graph resolution over the baseline's subgraph.
	graphRes, err := res.ResolveGraph(ctx, base)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if len(graphRes) < 2 {
		t.Fatalf("graph scope too small: %+v", graphRes)
	}
}

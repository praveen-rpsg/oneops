//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
)

// End-to-end: the engine over the real store performs §8 transitions atomically.
func TestGovernanceRatifyIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	eng, err := governance.NewEngine(NewGovernanceStore(pool), governance.AllowAllAuthorizer{}, NewAuditAppender(pool, NewAuditStore(pool)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := adminTestCtx()

	cfg := mkObj(t, co, "gov-ratify.md", domain.RetentionWorkingMaterial) // draft, non_normative
	res, err := eng.Execute(ctx, governance.Command{Operation: domain.OpRatification, CfgID: cfg, Actor: "council", OperationID: "op-ratify-" + cfg})
	if err != nil {
		t.Fatalf("ratify: %v", err)
	}
	if res.NewLifecycle != domain.LifecycleRatified || res.NewRetention != domain.RetentionCurrentBaseline || res.NewAuthority != domain.AuthorityActive {
		t.Fatalf("ratify result = %+v", res)
	}
	got, err := co.Get(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != domain.LifecycleRatified || got.RetentionClass != domain.RetentionCurrentBaseline || got.Authority != domain.AuthorityActive {
		t.Fatalf("persisted dimensions = %s/%s/%s", got.Lifecycle, got.RetentionClass, got.Authority)
	}
	if got.RowVersion != res.RowVersion {
		t.Fatalf("row_version mismatch: persisted %d, result %d", got.RowVersion, res.RowVersion)
	}
}

func TestGovernanceOptimisticConcurrencyIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	eng, _ := governance.NewEngine(NewGovernanceStore(pool), governance.AllowAllAuthorizer{}, NewAuditAppender(pool, NewAuditStore(pool)))
	ctx := adminTestCtx()

	cfg := mkObj(t, co, "gov-optim.md", domain.RetentionWorkingMaterial)
	// stale expected version → mismatch, no mutation
	if _, err := eng.Execute(ctx, governance.Command{Operation: domain.OpApproval, CfgID: cfg, Actor: "a", OperationID: "op-approve-" + cfg, ExpectedRowVersion: 999}); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Fatalf("expected version mismatch, got %v", err)
	}
	got, _ := co.Get(ctx, cfg)
	if got.Lifecycle != domain.LifecycleDraft {
		t.Fatalf("object mutated on stale version: %s", got.Lifecycle)
	}
}

func TestGovernanceDeleteIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	gr := NewGraphRepo(pool)
	eng, _ := governance.NewEngine(NewGovernanceStore(pool), governance.AllowAllAuthorizer{}, NewAuditAppender(pool, NewAuditStore(pool)))
	ctx := adminTestCtx()

	// deletable working-material object
	del := mkObj(t, co, "gov-del.md", domain.RetentionWorkingMaterial)
	if _, err := eng.Execute(ctx, governance.Command{Operation: domain.OpDeletion, CfgID: del, Actor: "a", OperationID: "op-del-" + del}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := co.Get(ctx, del); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("object not deleted: %v", err)
	}

	// object with a dependent → deletion refused
	base := mkObj(t, co, "gov-base.md", domain.RetentionWorkingMaterial)
	dependent := mkObj(t, co, "gov-dependent.md", domain.RetentionWorkingMaterial)
	mkAuthEdge(t, gr, dependent, base, domain.EdgeKindDepends) // dependent → base
	if _, err := eng.Execute(ctx, governance.Command{Operation: domain.OpDeletion, CfgID: base, Actor: "a", OperationID: "op-del-" + base}); !errors.Is(err, governance.ErrHasDependents) {
		t.Fatalf("expected ErrHasDependents, got %v", err)
	}
	if _, err := co.Get(ctx, base); err != nil {
		t.Fatalf("base wrongly deleted: %v", err)
	}
}

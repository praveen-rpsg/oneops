//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/authority"
	"github.com/rpsg/oneops/internal/domain"
)

func mkCoverObj(t *testing.T, co *ConfigObjectRepo, artifact string, retention domain.RetentionClass, coverage string) string {
	t.Helper()
	created, err := co.Create(context.Background(), &domain.ConfigObject{
		Artifact: artifact, Version: "1.0.0", Role: domain.RoleReference,
		Lifecycle: domain.LifecycleDraft, RetentionClass: retention,
		RetentionPolicy: "permanent",
	})
	if err != nil {
		t.Fatalf("obj %s: %v", artifact, err)
	}
	// §9.1 inputs are not descriptive metadata and no surface may write them
	// (ADR-GOV-003); seed directly.
	seedConstitutionalInput(t, co, created.CfgID, "coverage", coverage)
	return created.CfgID
}

// M3.5 end-to-end: a superseded config whose declared operational coverage is not
// fully provided by any Active configuration stays Active (operational_gap);
// coverage fully provided elsewhere resolves Historical. Coverage comes only from
// Configuration Object metadata.
func TestAuthorityGapIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	gr := NewGraphRepo(pool)
	res := authority.NewResolver(NewAuthorityStore(pool))
	gapEval := authority.NewGapEvaluator(
		authority.NewResolver(NewAuthorityStore(pool)), NewAuthorityStore(pool))
	ctx := adminTestCtx()

	// An ACTIVE (current_baseline) provider covers CAP1 and CAP3.
	provider := mkCoverObj(t, co, "gap-provider.md", domain.RetentionCurrentBaseline, "CAP1,CAP3")

	// Gap case: Old covers CAP1,CAP2; nothing active provides CAP2 -> stays Active.
	oldGap := mkCoverObj(t, co, "gap-old.md", domain.RetentionWorkingMaterial, "CAP1,CAP2")
	newGap := mkCoverObj(t, co, "gap-new.md", domain.RetentionWorkingMaterial, "CAP1")
	mkAuthEdge(t, gr, newGap, oldGap, domain.EdgeKindSupersedes)

	// No-gap case: Old covers CAP3, provided by the active provider -> Historical.
	oldFree := mkCoverObj(t, co, "gap-old-free.md", domain.RetentionWorkingMaterial, "CAP3")
	newFree := mkCoverObj(t, co, "gap-new-free.md", domain.RetentionWorkingMaterial, "")
	mkAuthEdge(t, gr, newFree, oldFree, domain.EdgeKindSupersedes)

	gap, err := res.ResolveAuthority(ctx, oldGap)
	if err != nil {
		t.Fatal(err)
	}
	if gap.State != domain.AuthorityStateActive || gap.Reason != domain.ReasonOperationalGap {
		t.Fatalf("gap: got %s/%s, want ACTIVE/operational_gap", gap.State, gap.Reason)
	}
	if len(gap.Evidence.UncoveredCapabilities) != 1 || gap.Evidence.UncoveredCapabilities[0] != "CAP2" {
		t.Fatalf("uncovered = %v, want [CAP2]", gap.Evidence.UncoveredCapabilities)
	}

	free, err := res.ResolveAuthority(ctx, oldFree)
	if err != nil {
		t.Fatal(err)
	}
	if free.State != domain.AuthorityStateHistorical || free.Reason != domain.ReasonSuperseded {
		t.Fatalf("no-gap: got %s/%s, want HISTORICAL/superseded", free.State, free.Reason)
	}

	// Evaluator API against the real metadata store.
	gr2, err := gapEval.EvaluateGap(ctx, oldGap)
	if err != nil {
		t.Fatal(err)
	}
	if !gr2.HasGap || len(gr2.UncoveredCapabilities) != 1 || gr2.UncoveredCapabilities[0] != "CAP2" {
		t.Fatalf("EvaluateGap: %+v", gr2)
	}
	rep, err := gapEval.EvaluateReplacement(ctx, oldGap, newGap)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Complete || len(rep.Missing) != 1 || rep.Missing[0] != "CAP2" {
		t.Fatalf("EvaluateReplacement: %+v", rep)
	}
	_ = provider
}

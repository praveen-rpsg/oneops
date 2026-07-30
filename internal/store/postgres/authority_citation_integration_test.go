//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/authority"
	"github.com/rpsg/oneops/internal/domain"
)

func mkCiteObj(t *testing.T, co *ConfigObjectRepo, artifact string, retention domain.RetentionClass, citations string) string {
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
	seedConstitutionalInput(t, co, created.CfgID, "citations", citations)
	return created.CfgID
}

// M3.4 end-to-end: a superseded config with a complete replacement and no active
// dependency stays Active while an ACTIVE artifact still cites it (via metadata),
// and falls to Historical once no active artifact cites it. Citations come only
// from Configuration Object metadata.
func TestAuthorityCitationIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	gr := NewGraphRepo(pool)
	res := authority.NewResolver(NewAuthorityStore(pool))
	citeEval := authority.NewArtifactCitationEvaluator(
		authority.NewResolver(NewAuthorityStore(pool)), NewAuthorityStore(pool))
	ctx := adminTestCtx()

	// Cited case: New supersedes Old; an ACTIVE baseline artifact cites Old.
	oldCited := mkCiteObj(t, co, "cite-old.md", domain.RetentionWorkingMaterial, "")
	newCited := mkCiteObj(t, co, "cite-new.md", domain.RetentionWorkingMaterial, "")
	mkAuthEdge(t, gr, newCited, oldCited, domain.EdgeKindSupersedes)
	citer := mkCiteObj(t, co, "cite-baseline.md", domain.RetentionCurrentBaseline, oldCited)

	// Uncited control: superseded, no active citer -> Historical.
	oldFree := mkCiteObj(t, co, "cite-old-free.md", domain.RetentionWorkingMaterial, "")
	newFree := mkCiteObj(t, co, "cite-new-free.md", domain.RetentionWorkingMaterial, "")
	mkAuthEdge(t, gr, newFree, oldFree, domain.EdgeKindSupersedes)

	cited, err := res.ResolveAuthority(ctx, oldCited)
	if err != nil {
		t.Fatal(err)
	}
	if cited.State != domain.AuthorityStateActive || cited.Reason != domain.ReasonActiveArtifactCitation {
		t.Fatalf("cited: got %s/%s, want ACTIVE/active_artifact_citation", cited.State, cited.Reason)
	}
	if len(cited.Evidence.ActiveArtifactCitations) != 1 || cited.Evidence.ActiveArtifactCitations[0] != citer {
		t.Fatalf("citing artifacts = %v, want [%s]", cited.Evidence.ActiveArtifactCitations, citer)
	}

	free, err := res.ResolveAuthority(ctx, oldFree)
	if err != nil {
		t.Fatal(err)
	}
	if free.State != domain.AuthorityStateHistorical || free.Reason != domain.ReasonSuperseded {
		t.Fatalf("uncited: got %s/%s, want HISTORICAL/superseded", free.State, free.Reason)
	}

	// Evaluator API against the real metadata store.
	ac, err := citeEval.EvaluateCitations(ctx, oldCited)
	if err != nil {
		t.Fatal(err)
	}
	if !ac.HasActiveCitation || len(ac.CitingArtifacts) != 1 || ac.CitingArtifacts[0] != citer {
		t.Fatalf("EvaluateCitations: %+v", ac)
	}
	rep, err := citeEval.EvaluateReplacement(ctx, oldCited, newCited)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Cleared || len(rep.Remaining) != 1 || rep.Remaining[0] != citer {
		t.Fatalf("EvaluateReplacement: %+v", rep)
	}
}

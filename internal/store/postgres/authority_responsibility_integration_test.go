//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/authority"
	"github.com/rpsg/oneops/internal/domain"
)

// seedConstitutionalInput writes a §9.1 input directly.
//
// No API or repository surface may write these keys — they decide computed
// Authority and the Replacement Test verdict, and the descriptive-metadata
// channel is not a constitutional act (ADR-GOV-003). The platform has no
// constitutional operation for declaring them either, which is the open gap
// referred to the governance process; until it exists, direct insertion is the
// only way these inputs come to be, and tests must say so rather than route
// around the guard.
func seedConstitutionalInput(t *testing.T, co *ConfigObjectRepo, cfgID, key, value string) {
	t.Helper()
	if value == "" {
		return
	}
	if _, err := co.pool.Exec(context.Background(),
		`INSERT INTO configuration_metadata (cfg_id, key, value) VALUES ($1,$2,$3)
		 ON CONFLICT (cfg_id, key) DO UPDATE SET value = EXCLUDED.value`,
		cfgID, key, value); err != nil {
		t.Fatalf("seed %s for %s: %v", key, cfgID, err)
	}
}

func mkRespObj(t *testing.T, co *ConfigObjectRepo, artifact string, responsibilities string) string {
	t.Helper()
	created, err := co.Create(context.Background(), &domain.ConfigObject{
		Artifact: artifact, Version: "1.0.0", Role: domain.RoleReference,
		Lifecycle: domain.LifecycleDraft, RetentionClass: domain.RetentionWorkingMaterial,
		RetentionPolicy: "permanent",
	})
	if err != nil {
		t.Fatalf("obj %s: %v", artifact, err)
	}
	seedConstitutionalInput(t, co, created.CfgID, "responsibilities", responsibilities)
	return created.CfgID
}

// M3.3 end-to-end: responsibility ownership drives whether a superseded config
// (with no active dependency) resolves Historical or stays Active.
func TestAuthorityResponsibilityIntegration(t *testing.T) {
	pool := graphPool(t)
	co := NewConfigObjectRepo(pool)
	gr := NewGraphRepo(pool)
	res := authority.NewResolver(NewAuthorityStore(pool))
	respEval := authority.NewResponsibilityEvaluator(NewAuthorityStore(pool))
	ctx := context.Background()

	// Complete transfer: successor owns all of old's responsibilities -> Historical.
	oldFull := mkRespObj(t, co, "resp-old-full.md", "R1,R2")
	newFull := mkRespObj(t, co, "resp-new-full.md", "R1,R2,R3")
	mkAuthEdge(t, gr, newFull, oldFull, domain.EdgeKindSupersedes)

	// Partial transfer: successor misses R2 -> stays Active (replacement_incomplete).
	oldPart := mkRespObj(t, co, "resp-old-part.md", "R1,R2")
	newPart := mkRespObj(t, co, "resp-new-part.md", "R1")
	mkAuthEdge(t, gr, newPart, oldPart, domain.EdgeKindSupersedes)

	full, err := res.ResolveAuthority(ctx, oldFull)
	if err != nil {
		t.Fatal(err)
	}
	if full.State != domain.AuthorityStateHistorical || full.Reason != domain.ReasonSuperseded {
		t.Fatalf("full transfer: got %s/%s, want HISTORICAL/superseded", full.State, full.Reason)
	}

	part, err := res.ResolveAuthority(ctx, oldPart)
	if err != nil {
		t.Fatal(err)
	}
	if part.State != domain.AuthorityStateActive || part.Reason != domain.ReasonReplacementIncomplete {
		t.Fatalf("partial transfer: got %s/%s, want ACTIVE/replacement_incomplete", part.State, part.Reason)
	}
	if len(part.Evidence.MissingResponsibilities) != 1 || part.Evidence.MissingResponsibilities[0] != "R2" {
		t.Fatalf("missing responsibilities = %v, want [R2]", part.Evidence.MissingResponsibilities)
	}

	// Evaluator API against the real metadata store.
	rep, err := respEval.EvaluateReplacement(ctx, oldPart, newPart)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Complete || len(rep.Missing) != 1 || rep.Missing[0] != "R2" {
		t.Fatalf("EvaluateReplacement: %+v", rep)
	}
}

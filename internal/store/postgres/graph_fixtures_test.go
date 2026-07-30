//go:build integration

package postgres

import (
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// graphFixture is a small, realistic dependency graph modeled on the OneOps
// corpus: an engineering spec depends on a constitutional volume, a validation
// program extends its predecessor, and a frozen baseline supersedes a retired
// roadmap. It exercises all three edge kinds without any traversal.
type graphFixture struct {
	cfgID map[string]string // logical name -> cfg_id
	edges []*domain.DependencyEdge
}

// fixtureNodes are the artifacts seeded as Configuration Objects.
var fixtureNodes = []struct {
	name, artifact string
	role           domain.Role
}{
	{"volumeIV", "OneOps-Platform-Architecture-Volume-IV.md", domain.RoleConstitution},
	{"blueprint", "OneOps-Engineering-Blueprint-v1.0.md", domain.RoleEngSpec},
	{"cvp", "OneOps-Constitutional-Validation-Program-v1.0.md", domain.RoleValidation},
	{"evoap", "OneOps-Engineering-Validation-and-Operational-Acceptance-Program.md", domain.RoleValidation},
	{"roadmap", "OneOps-10-Volume-Roadmap.md", domain.RolePlanning},
	{"baseline", "OneOps-Architecture-Baseline-v1.0.md", domain.RoleGovernance},
}

// fixtureEdges are the relationships between the seeded nodes (by logical name).
var fixtureEdges = []struct {
	from, to string
	kind     domain.EdgeKind
}{
	{"blueprint", "volumeIV", domain.EdgeKindDepends},  // Blueprint depends on Volume IV
	{"evoap", "cvp", domain.EdgeKindExtends},           // EVOAP extends the CVP
	{"baseline", "roadmap", domain.EdgeKindSupersedes}, // Baseline supersedes the roadmap
}

// seedGraphFixture creates the fixture's Configuration Objects and edges.
func seedGraphFixture(t *testing.T, co *ConfigObjectRepo, g *GraphRepo) graphFixture {
	t.Helper()
	ctx := adminTestCtx()
	fx := graphFixture{cfgID: map[string]string{}}

	for _, n := range fixtureNodes {
		obj := sample(n.artifact, "1.0.0")
		obj.Role = n.role
		created, err := co.Create(ctx, obj)
		if err != nil {
			t.Fatalf("seed node %s: %v", n.name, err)
		}
		fx.cfgID[n.name] = created.CfgID
	}
	for _, e := range fixtureEdges {
		edge, err := g.CreateEdge(ctx, &domain.DependencyEdge{
			FromCfg: fx.cfgID[e.from], ToCfg: fx.cfgID[e.to], EdgeKind: e.kind,
		})
		if err != nil {
			t.Fatalf("seed edge %s->%s: %v", e.from, e.to, err)
		}
		fx.edges = append(fx.edges, edge)
	}
	return fx
}

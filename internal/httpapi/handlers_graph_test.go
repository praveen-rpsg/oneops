package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

// fakeGraph is an in-memory domain.GraphTraversal for handler unit tests.
type fakeGraph struct {
	directDeps map[string][]string
	directDsts map[string][]string
	recDeps    map[string][]domain.TraversalNode
	recDsts    map[string][]domain.TraversalNode
	cycles     map[string][]domain.GraphPath
}

func newFakeGraph() *fakeGraph {
	return &fakeGraph{
		directDeps: map[string][]string{},
		directDsts: map[string][]string{},
		recDeps:    map[string][]domain.TraversalNode{},
		recDsts:    map[string][]domain.TraversalNode{},
		cycles:     map[string][]domain.GraphPath{},
	}
}

func (f *fakeGraph) Dependencies(_ context.Context, id string) ([]string, error) {
	return f.directDeps[id], nil
}
func (f *fakeGraph) Dependents(_ context.Context, id string) ([]string, error) {
	return f.directDsts[id], nil
}
func (f *fakeGraph) RecursiveDependencies(_ context.Context, id string) ([]domain.TraversalNode, error) {
	return f.recDeps[id], nil
}
func (f *fakeGraph) RecursiveDependents(_ context.Context, id string) ([]domain.TraversalNode, error) {
	return f.recDsts[id], nil
}
func (f *fakeGraph) CyclePaths(_ context.Context, id string, _ domain.Direction) ([]domain.GraphPath, error) {
	return f.cycles[id], nil
}

func seedObj(repo *fakeRepo, id string) {
	_, _ = repo.Create(context.Background(), &domain.ConfigObject{
		CfgID: id, Artifact: "art-" + id + ".md", Version: "1.0.0",
		Role: domain.RoleReference, Lifecycle: domain.LifecycleDraft,
		RetentionClass: domain.RetentionWorkingMaterial, RetentionPolicy: "permanent",
	})
}

func newGraphTestAPI(authEnabled bool, fg domain.GraphTraversal, ids ...string) http.Handler {
	repo := newFakeRepo()
	for _, id := range ids {
		seedObj(repo, id)
	}
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: authEnabled, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo, newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetGraph(fg)
	return s.Router()
}

func decodeTraversal(t *testing.T, rec interface{ Bytes() []byte }) traversalResponse {
	t.Helper()
	var out traversalResponse
	if err := json.Unmarshal(rec.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestGraphDependenciesNonRecursive(t *testing.T) {
	fg := newFakeGraph()
	fg.directDeps["a"] = []string{"b", "c"}
	h := newGraphTestAPI(false, fg, "a")

	rec := do(h, http.MethodGet, "/v1/configurations/a/dependencies", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	res := decodeTraversal(t, rec.Body)
	if res.Root != "a" || res.Direction != "dependencies" || res.Recursive || res.Count != 2 {
		t.Fatalf("unexpected header: %+v", res)
	}
	if res.Nodes[0].CfgID != "b" || res.Nodes[0].Depth != 1 || res.Nodes[1].CfgID != "c" {
		t.Fatalf("unexpected nodes: %+v", res.Nodes)
	}
}

func TestGraphDependenciesRecursive(t *testing.T) {
	fg := newFakeGraph()
	fg.recDeps["a"] = []domain.TraversalNode{{CfgID: "b", Depth: 1}, {CfgID: "c", Depth: 2}}
	h := newGraphTestAPI(false, fg, "a")

	rec := do(h, http.MethodGet, "/v1/configurations/a/dependencies?recursive=true", nil, nil)
	res := decodeTraversal(t, rec.Body)
	if !res.Recursive || res.Count != 2 || res.Nodes[1].Depth != 2 {
		t.Fatalf("recursive result: %+v", res)
	}
}

func TestGraphDependentsReverse(t *testing.T) {
	fg := newFakeGraph()
	fg.recDsts["c"] = []domain.TraversalNode{{CfgID: "b", Depth: 1}, {CfgID: "a", Depth: 2}}
	h := newGraphTestAPI(false, fg, "c")

	rec := do(h, http.MethodGet, "/v1/configurations/c/dependents?recursive=true", nil, nil)
	res := decodeTraversal(t, rec.Body)
	if res.Direction != "dependents" || res.Count != 2 || res.Nodes[0].CfgID != "b" {
		t.Fatalf("dependents result: %+v", res)
	}
}

func TestGraphMaxDepthFilter(t *testing.T) {
	fg := newFakeGraph()
	fg.recDeps["a"] = []domain.TraversalNode{{CfgID: "b", Depth: 1}, {CfgID: "c", Depth: 2}, {CfgID: "d", Depth: 3}}
	h := newGraphTestAPI(false, fg, "a")

	rec := do(h, http.MethodGet, "/v1/configurations/a/dependencies?recursive=true&max_depth=2", nil, nil)
	res := decodeTraversal(t, rec.Body)
	if res.Count != 2 {
		t.Fatalf("max_depth filter: expected 2 nodes, got %d (%+v)", res.Count, res.Nodes)
	}
}

func TestGraphCycles(t *testing.T) {
	fg := newFakeGraph()
	fg.cycles["a"] = []domain.GraphPath{{Nodes: []string{"a", "b", "a"}}}
	h := newGraphTestAPI(false, fg, "a")

	rec := do(h, http.MethodGet, "/v1/configurations/a/cycles", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var res cyclesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Count != 1 || len(res.Cycles) != 1 {
		t.Fatalf("cycles: %+v", res)
	}
	got := res.Cycles[0].Path
	if len(got) != 3 || got[0] != "a" || got[2] != "a" {
		t.Fatalf("cycle path: %v", got)
	}
}

func TestGraphCyclesEmptyList(t *testing.T) {
	h := newGraphTestAPI(false, newFakeGraph(), "a")
	rec := do(h, http.MethodGet, "/v1/configurations/a/cycles", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	// Empty list, not null.
	if got := rec.Body.String(); !jsonHasEmptyCycles(got) {
		t.Fatalf("expected empty cycles array, got %s", got)
	}
}

func jsonHasEmptyCycles(body string) bool {
	var res cyclesResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		return false
	}
	return res.Count == 0 && res.Cycles != nil && len(res.Cycles) == 0
}

func TestGraphNotFound(t *testing.T) {
	h := newGraphTestAPI(false, newFakeGraph()) // nothing seeded
	for _, path := range []string{
		"/v1/configurations/missing/dependencies",
		"/v1/configurations/missing/dependents",
		"/v1/configurations/missing/cycles",
	} {
		rec := do(h, http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("GET %s content-type = %q", path, ct)
		}
	}
}

func TestGraphBadRequest(t *testing.T) {
	h := newGraphTestAPI(false, newFakeGraph(), "a")
	for _, path := range []string{
		"/v1/configurations/a/dependencies?recursive=maybe",
		"/v1/configurations/a/dependencies?max_depth=0",
		"/v1/configurations/a/dependencies?max_depth=-3",
		"/v1/configurations/a/dependencies?max_depth=abc",
	} {
		rec := do(h, http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}

func TestGraphRBAC(t *testing.T) {
	fg := newFakeGraph()
	fg.directDeps["a"] = []string{"b"}
	h := newGraphTestAPI(true, fg, "a")
	path := "/v1/configurations/a/dependencies"

	// 401 — no token.
	if rec := do(h, http.MethodGet, path, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	// 403 — authenticated but without read permission.
	nobody := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-nobody"})}
	if rec := do(h, http.MethodGet, path, nil, nobody); rec.Code != http.StatusForbidden {
		t.Errorf("no-permission = %d, want 403", rec.Code)
	}
	// 200 — reader has the same access as configuration reads.
	reader := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-reader"})}
	if rec := do(h, http.MethodGet, path, nil, reader); rec.Code != 200 {
		t.Errorf("reader = %d, want 200", rec.Code)
	}
}

func TestGraphStableResponse(t *testing.T) {
	fg := newFakeGraph()
	fg.recDeps["a"] = []domain.TraversalNode{{CfgID: "b", Depth: 1}, {CfgID: "c", Depth: 1}, {CfgID: "d", Depth: 2}}
	h := newGraphTestAPI(false, fg, "a")

	var prev string
	for i := 0; i < 5; i++ {
		rec := do(h, http.MethodGet, "/v1/configurations/a/dependencies?recursive=true", nil, nil)
		if i > 0 && rec.Body.String() != prev {
			t.Fatal("response is not deterministic across calls")
		}
		prev = rec.Body.String()
	}
}

func TestGraphOpenAPIDocumented(t *testing.T) {
	h := newGraphTestAPI(false, newFakeGraph())
	rec := do(h, http.MethodGet, "/openapi.yaml", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("openapi = %d", rec.Code)
	}
	spec := rec.Body.String()
	for _, want := range []string{
		"/v1/configurations/{cfgId}/dependencies",
		"/v1/configurations/{cfgId}/dependents",
		"/v1/configurations/{cfgId}/cycles",
		"TraversalResponse",
		"CyclesResponse",
		"getDependencies",
		"getCycles",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("openapi.yaml missing %q", want)
		}
	}
	// Existing contracts remain.
	if !strings.Contains(spec, "operationId: listArtifacts") {
		t.Error("existing artifacts contract disturbed")
	}
}

func TestGraphNoAuthorityLeakage(t *testing.T) {
	fg := newFakeGraph()
	fg.recDeps["a"] = []domain.TraversalNode{{CfgID: "b", Depth: 1}}
	h := newGraphTestAPI(false, fg, "a")
	body := do(h, http.MethodGet, "/v1/configurations/a/dependencies?recursive=true", nil, nil).Body.String()

	for _, forbidden := range []string{"authority", "active", "historical", "baseline", "superseded", "lifecycle", "effective"} {
		if containsField(body, forbidden) {
			t.Errorf("response leaks forbidden field %q: %s", forbidden, body)
		}
	}
}

func containsField(body, field string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return false
	}
	if _, ok := m[field]; ok {
		return true
	}
	// Check inside nodes.
	var res traversalResponse
	if err := json.Unmarshal([]byte(body), &res); err == nil {
		for _, n := range res.Nodes {
			raw, _ := json.Marshal(n)
			var nm map[string]json.RawMessage
			_ = json.Unmarshal(raw, &nm)
			if _, ok := nm[field]; ok {
				return true
			}
		}
	}
	return false
}

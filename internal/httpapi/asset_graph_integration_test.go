//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// assetGraphHarness wires GET /admin/assets/graph against the REAL AssetStore
// — a tenant-scoped appPool (row-level security enforced, exactly as
// production's composition root builds it) plus a privileged pool used ONLY
// for tenant registration, never for anything the graph endpoint itself
// reads. This is the wiring the handler unit tests (fakes) cannot prove:
// that RLS, not a predicate the query happens to carry, is what isolates one
// tenant's graph from another's (ADR-NOC-006).
type assetGraphHarness struct {
	srv     *Server
	router  http.Handler
	dsn     string
	priv    *pgxpool.Pool
	appPool *pgxpool.Pool
	assets  *postgres.AssetStore
	tenants *postgres.TenantStore
}

func realAssetGraphHarness(t *testing.T) *assetGraphHarness {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "options=-c%20search_path%3Dhttpapi_itest"
	ctx := context.Background()

	priv, err := postgres.NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("privileged pool: %v", err)
	}
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = priv.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("db not ready: %v", pingErr)
	}
	if _, err := priv.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS httpapi_itest`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := migrate.Up(ctx, priv); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(priv.Close)

	appPool, err := postgres.NewTenantScopedPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("tenant-scoped pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), priv.Ping)

	h := &assetGraphHarness{
		srv:     s,
		dsn:     dsn,
		priv:    priv,
		appPool: appPool,
		assets:  postgres.NewAssetStore(appPool),
		tenants: postgres.NewTenantStore(priv),
	}
	s.SetTenants(h.tenants)
	s.SetAssets(h.assets)
	h.router = s.Router()
	return h
}

// assetGraphTenant registers a real tenant on the privileged pool.
func (h *assetGraphHarness) assetGraphTenant(t *testing.T, slug string) *domain.Tenant {
	t.Helper()
	slug = uniqueSlug(slug)
	tn, err := h.tenants.Create(domain.WithActor(context.Background(), "test-actor"), &domain.Tenant{
		TenantID: domain.NewID(), Slug: slug, Name: slug, ExternalID: "ext-" + slug,
		Status: domain.TenantActive,
	})
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

func (h *assetGraphHarness) ctx(tn *domain.Tenant) context.Context {
	return domain.WithActor(domain.WithTenant(context.Background(), tn), "test-actor")
}

// createAsset seeds one asset for tn through the real write path (AssetStore
// on the tenant-scoped pool), so RLS's WITH CHECK is exercised exactly as a
// production write would be.
func (h *assetGraphHarness) createAsset(t *testing.T, tn *domain.Tenant, name string) *domain.Asset {
	t.Helper()
	a, err := domain.NewAsset(tn.TenantID, "server", name, nil)
	if err != nil {
		t.Fatalf("new asset %s: %v", name, err)
	}
	created, err := h.assets.Create(h.ctx(tn), a)
	if err != nil {
		t.Fatalf("create asset %s: %v", name, err)
	}
	return created
}

func (h *assetGraphHarness) createRelationship(t *testing.T, tn *domain.Tenant, from, to string, relType domain.RelationshipType) {
	t.Helper()
	rel, err := domain.NewAssetRelationship(tn.TenantID, from, to, relType)
	if err != nil {
		t.Fatalf("new relationship: %v", err)
	}
	if _, err := h.assets.CreateRelationship(h.ctx(tn), rel); err != nil {
		t.Fatalf("create relationship %s->%s: %v", from, to, err)
	}
}

// graphGet issues an authenticated GET /v1/admin/assets/graph as tn's admin,
// against whatever router r serves (so the "bites" test can swap in a
// deliberately loosened one).
func graphGet(t *testing.T, r http.Handler, tn *domain.Tenant) (*httptest.ResponseRecorder, assetGraphResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets/graph", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var got assetGraphResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode graph: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, got
}

func nodeNames(nodes []assetGraphNodeDTO) map[string]assetGraphNodeDTO {
	out := make(map[string]assetGraphNodeDTO, len(nodes))
	for _, n := range nodes {
		out[n.Name] = n
	}
	return out
}

// TestAssetGraphAPI_ReturnsExactNodesAndEdges is the correctness proof: a
// standalone asset appears as a node with no edges naming it, an asset with
// a dependency yields exactly that edge, and a retired asset still appears
// (Graph is deliberately not soft-retire-filtered, ADR-NOC-006 §3).
func TestAssetGraphAPI_ReturnsExactNodesAndEdges(t *testing.T) {
	h := realAssetGraphHarness(t)
	tn := h.assetGraphTenant(t, "asset-graph-correctness")

	standalone := h.createAsset(t, tn, "standalone-host")
	web := h.createAsset(t, tn, "web-server")
	db := h.createAsset(t, tn, "db-server")
	h.createRelationship(t, tn, web.AssetID, db.AssetID, domain.RelationshipDependsOn)

	retired := h.createAsset(t, tn, "retired-host")
	if _, err := h.assets.SetStatus(h.ctx(tn), retired.AssetID, retired.RowVersion, domain.AssetRetired); err != nil {
		t.Fatalf("retire asset: %v", err)
	}

	rec, got := h.graphGetOwn(t, tn)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got.Truncated {
		t.Error("truncated = true, want false (well under either cap)")
	}
	if len(got.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (standalone, web, db, retired): %+v", len(got.Nodes), got.Nodes)
	}
	byName := nodeNames(got.Nodes)
	if _, ok := byName["standalone-host"]; !ok {
		t.Error("standalone asset is missing from the node list")
	}
	if node, ok := byName["retired-host"]; !ok {
		t.Error("retired asset is missing from the node list — Graph must not soft-retire-filter")
	} else if node.Status != "retired" {
		t.Errorf("retired asset status = %q, want retired", node.Status)
	}

	if len(got.Edges) != 1 {
		t.Fatalf("edges = %d, want 1: %+v", len(got.Edges), got.Edges)
	}
	edge := got.Edges[0]
	if edge.FromAssetID != web.AssetID || edge.ToAssetID != db.AssetID || edge.Type != "depends_on" {
		t.Errorf("edge = %+v, want %s->%s depends_on", edge, web.AssetID, db.AssetID)
	}

	// The standalone asset must not appear as either endpoint of any edge.
	for _, e := range got.Edges {
		if e.FromAssetID == standalone.AssetID || e.ToAssetID == standalone.AssetID {
			t.Errorf("standalone asset unexpectedly appears in an edge: %+v", e)
		}
	}
}

// graphGetOwn is graphGet against this harness's own router.
func (h *assetGraphHarness) graphGetOwn(t *testing.T, tn *domain.Tenant) (*httptest.ResponseRecorder, assetGraphResponse) {
	return graphGet(t, h.router, tn)
}

// TestAssetGraphAPI_EmptyTenantIsCleanEmpty proves the empty-tenant bound at
// the real-store layer: a brand-new tenant with no assets/relationships at
// all gets 200 with empty (never null, never omitted) arrays.
func TestAssetGraphAPI_EmptyTenantIsCleanEmpty(t *testing.T) {
	h := realAssetGraphHarness(t)
	tn := h.assetGraphTenant(t, "asset-graph-empty")

	rec, got := h.graphGetOwn(t, tn)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(got.Nodes) != 0 || len(got.Edges) != 0 {
		t.Errorf("nodes/edges = %+v, want both empty", got)
	}
	if got.Truncated {
		t.Error("truncated = true, want false for an empty tenant")
	}
	if !strings.Contains(rec.Body.String(), `"nodes":[]`) || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("body = %s, want nodes/edges rendered as [], not null", rec.Body.String())
	}
}

// TestAssetGraphAPI_TenantIsolation is the security-critical property this
// story exists to prove: two tenants with materially different asset/
// relationship volumes, sharing no rows, each see ONLY their own graph — not
// the other's nodes, not the other's edges, and not the union of both. A
// third, larger bystander tenant proves A's count is not merely coincidental.
func TestAssetGraphAPI_TenantIsolation(t *testing.T) {
	h := realAssetGraphHarness(t)
	tnA := h.assetGraphTenant(t, "asset-graph-iso-a")
	tnB := h.assetGraphTenant(t, "asset-graph-iso-b")
	tnC := h.assetGraphTenant(t, "asset-graph-iso-c")

	a1 := h.createAsset(t, tnA, "a-host-1")
	a2 := h.createAsset(t, tnA, "a-host-2")
	h.createRelationship(t, tnA, a1.AssetID, a2.AssetID, domain.RelationshipDependsOn)

	b1 := h.createAsset(t, tnB, "b-host-1")
	b2 := h.createAsset(t, tnB, "b-host-2")
	b3 := h.createAsset(t, tnB, "b-host-3")
	h.createRelationship(t, tnB, b1.AssetID, b2.AssetID, domain.RelationshipRunsOn)
	h.createRelationship(t, tnB, b2.AssetID, b3.AssetID, domain.RelationshipRunsOn)

	// A larger bystander tenant, so A's own count is not just "everyone's".
	for i := 0; i < 5; i++ {
		h.createAsset(t, tnC, "c-host")
	}

	_, gotA := h.graphGetOwn(t, tnA)
	_, gotB := h.graphGetOwn(t, tnB)

	if len(gotA.Nodes) != 2 || len(gotA.Edges) != 1 {
		t.Fatalf("tenant A graph = %+v, want 2 nodes / 1 edge", gotA)
	}
	if len(gotB.Nodes) != 3 || len(gotB.Edges) != 2 {
		t.Fatalf("tenant B graph = %+v, want 3 nodes / 2 edges", gotB)
	}

	namesA := nodeNames(gotA.Nodes)
	for _, leak := range []string{"b-host-1", "b-host-2", "b-host-3", "c-host"} {
		if _, ok := namesA[leak]; ok {
			t.Errorf("tenant A's graph leaked %q (tenant B's or C's asset)", leak)
		}
	}
	namesB := nodeNames(gotB.Nodes)
	for _, leak := range []string{"a-host-1", "a-host-2", "c-host"} {
		if _, ok := namesB[leak]; ok {
			t.Errorf("tenant B's graph leaked %q (tenant A's or C's asset)", leak)
		}
	}
	for _, e := range gotA.Edges {
		if e.FromAssetID == b1.AssetID || e.ToAssetID == b1.AssetID {
			t.Errorf("tenant A's edges leaked tenant B's asset id: %+v", e)
		}
	}
}

// TestAssetGraphAPI_TenantIsolation_BitesWhenLoosened is the mutation proof
// the review criteria require: re-run the exact isolation fixture and
// assertions against a router deliberately wired with AssetStore built over
// the PRIVILEGED (RLS-bypassing) pool instead of the tenant-scoped one, and
// require the isolation assertion to FAIL there. If it did not fail, the
// "real" test above would be proving nothing — this is the guard against
// that (ADR-NOC-006 §2).
func TestAssetGraphAPI_TenantIsolation_BitesWhenLoosened(t *testing.T) {
	h := realAssetGraphHarness(t)
	tnA := h.assetGraphTenant(t, "asset-graph-bites-a")
	tnB := h.assetGraphTenant(t, "asset-graph-bites-b")

	h.createAsset(t, tnA, "bites-a-host")
	h.createAsset(t, tnB, "bites-b-host")

	// Wire a SECOND server whose AssetStore is built over the privileged pool
	// (postgres.NewPool, table-owner/BYPASSRLS — no app.tenant_id binding at
	// all) — the exact mistake ADR-TENANCY-002 exists to catch.
	loosePool, err := postgres.NewPool(context.Background(), h.dsn, 3)
	if err != nil {
		t.Fatalf("privileged pool for loosened router: %v", err)
	}
	t.Cleanup(loosePool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	loose := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), loosePool.Ping)
	loose.SetTenants(h.tenants)
	loose.SetAssets(postgres.NewAssetStore(loosePool))
	looseRouter := loose.Router()

	_, gotA := graphGet(t, looseRouter, tnA)
	namesA := nodeNames(gotA.Nodes)
	if _, leaked := namesA["bites-b-host"]; !leaked {
		t.Fatal("expected the LOOSENED router (privileged pool, no tenant scoping) to leak tenant B's " +
			"asset into tenant A's graph; it did not, which means this canary no longer proves the real " +
			"harness's isolation is load-bearing")
	}
}

// smallCapAssetStore forwards every AssetRepository method to the real
// AssetStore except Graph, which substitutes a small test cap for whatever
// the caller (the production handler always passes 0,0) asked for — so
// TestAssetGraphAPI_TruncatesAtCap can prove the truncation flag propagates
// all the way through HTTP without seeding thousands of fixture rows.
type smallCapAssetStore struct {
	*postgres.AssetStore
	nodeCap, edgeCap int
}

func (s *smallCapAssetStore) Graph(ctx context.Context, _, _ int) (*domain.AssetGraph, error) {
	return s.AssetStore.Graph(ctx, s.nodeCap, s.edgeCap)
}

// TestAssetGraphAPI_TruncatesAtCap proves the bound: with the cap forced
// down to 2 nodes, a tenant with 3 assets gets exactly 2 back, in
// deterministic asset_id order, plus truncated:true.
func TestAssetGraphAPI_TruncatesAtCap(t *testing.T) {
	h := realAssetGraphHarness(t)
	tn := h.assetGraphTenant(t, "asset-graph-cap")

	created := make([]*domain.Asset, 0, 3)
	for i := 0; i < 3; i++ {
		created = append(created, h.createAsset(t, tn, "cap-host"))
	}

	h.srv.SetAssets(&smallCapAssetStore{AssetStore: h.assets, nodeCap: 2, edgeCap: 5000})
	t.Cleanup(func() { h.srv.SetAssets(h.assets) })

	rec, got := h.graphGetOwn(t, tn)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (capped)", len(got.Nodes))
	}
	if !got.Truncated {
		t.Error("truncated = false, want true (3 assets, cap 2)")
	}

	// Deterministic ordering: the first 2 by asset_id, the same page a
	// repeated call must return.
	wantIDs := make([]string, 0, 2)
	for _, a := range created {
		wantIDs = append(wantIDs, a.AssetID)
	}
	sort.Strings(wantIDs)
	gotIDs := []string{got.Nodes[0].AssetID, got.Nodes[1].AssetID}
	if gotIDs[0] != wantIDs[0] || gotIDs[1] != wantIDs[1] {
		t.Errorf("truncated page = %v, want the first 2 by asset_id: %v", gotIDs, wantIDs[:2])
	}

	_, got2 := h.graphGetOwn(t, tn)
	if got2.Nodes[0].AssetID != got.Nodes[0].AssetID || got2.Nodes[1].AssetID != got.Nodes[1].AssetID {
		t.Error("a repeated call returned a different truncated page — ordering is not stable")
	}
}

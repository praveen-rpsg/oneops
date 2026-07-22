//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

func realGraphAPI(t *testing.T) (http.Handler, *postgres.ConfigObjectRepo, *postgres.GraphRepo) {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	// Isolate this package's integration tables in a dedicated schema so
	// `go test ./...` (which runs packages in parallel against one database)
	// cannot collide with other packages' migrate/truncate.
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "options=-c%20search_path%3Dhttpapi_itest"
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("db not ready: %v", pingErr)
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS httpapi_itest`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE configuration_object CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(pool.Close)

	co := postgres.NewConfigObjectRepo(pool)
	g := postgres.NewGraphRepo(pool)
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: false, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		co, newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), pool.Ping)
	s.SetGraph(g)
	return s.Router(), co, g
}

func mkNode(t *testing.T, co *postgres.ConfigObjectRepo, name string) string {
	t.Helper()
	created, err := co.Create(context.Background(), &domain.ConfigObject{
		Artifact: name, Version: "1.0.0", Role: domain.RoleReference,
		Lifecycle: domain.LifecycleDraft, RetentionClass: domain.RetentionWorkingMaterial,
		RetentionPolicy: "permanent",
	})
	if err != nil {
		t.Fatalf("node %s: %v", name, err)
	}
	return created.CfgID
}

func mkEdge(t *testing.T, g *postgres.GraphRepo, from, to string) {
	t.Helper()
	if _, err := g.CreateEdge(context.Background(), &domain.DependencyEdge{
		FromCfg: from, ToCfg: to, EdgeKind: domain.EdgeKindDepends,
	}); err != nil {
		t.Fatalf("edge %s->%s: %v", from, to, err)
	}
}

func getTraversal(t *testing.T, h http.Handler, path string) traversalResponse {
	t.Helper()
	rec := do(h, http.MethodGet, path, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("GET %s = %d (%s)", path, rec.Code, rec.Body.String())
	}
	var res traversalResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res
}

func TestGraphAPILinear(t *testing.T) {
	h, co, g := realGraphAPI(t)
	a := mkNode(t, co, "lin-a.md")
	b := mkNode(t, co, "lin-b.md")
	c := mkNode(t, co, "lin-c.md")
	mkEdge(t, g, a, b)
	mkEdge(t, g, b, c)

	// Non-recursive: direct dependency only.
	direct := getTraversal(t, h, "/v1/configurations/"+a+"/dependencies")
	if direct.Recursive || direct.Count != 1 || direct.Nodes[0].CfgID != b {
		t.Fatalf("direct deps: %+v", direct)
	}
	// Recursive: full forward closure.
	rec := getTraversal(t, h, "/v1/configurations/"+a+"/dependencies?recursive=true")
	if !rec.Recursive || rec.Count != 2 {
		t.Fatalf("recursive deps: %+v", rec)
	}
	// Reverse recursive from c reaches b then a.
	rev := getTraversal(t, h, "/v1/configurations/"+c+"/dependents?recursive=true")
	if rev.Direction != "dependents" || rev.Count != 2 {
		t.Fatalf("dependents: %+v", rev)
	}
}

func TestGraphAPICycles(t *testing.T) {
	h, co, g := realGraphAPI(t)
	a := mkNode(t, co, "cyc-a.md")
	b := mkNode(t, co, "cyc-b.md")
	c := mkNode(t, co, "cyc-c.md")
	mkEdge(t, g, a, b)
	mkEdge(t, g, b, c)
	mkEdge(t, g, c, a) // closes the cycle

	rec := do(h, http.MethodGet, "/v1/configurations/"+a+"/cycles", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("cycles = %d", rec.Code)
	}
	var res cyclesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Count != 1 || len(res.Cycles[0].Path) != 4 {
		t.Fatalf("expected one 4-node closed cycle, got %+v", res)
	}
	if res.Cycles[0].Path[0] != res.Cycles[0].Path[3] {
		t.Fatalf("cycle path not closed: %v", res.Cycles[0].Path)
	}

	// An acyclic node reports an empty list.
	iso := mkNode(t, co, "cyc-iso.md")
	empty := do(h, http.MethodGet, "/v1/configurations/"+iso+"/cycles", nil, nil)
	var er cyclesResponse
	_ = json.Unmarshal(empty.Body.Bytes(), &er)
	if er.Count != 0 || er.Cycles == nil {
		t.Fatalf("expected empty cycles array, got %+v", er)
	}
}

func TestGraphAPILargeResponse(t *testing.T) {
	h, co, g := realGraphAPI(t)
	const n = 200
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = mkNode(t, co, "big-"+strconv.Itoa(i)+".md")
	}
	for i := 0; i < n-1; i++ {
		mkEdge(t, g, ids[i], ids[i+1])
	}

	res := getTraversal(t, h, "/v1/configurations/"+ids[0]+"/dependencies?recursive=true")
	if res.Count != n-1 {
		t.Fatalf("large response: count=%d, want %d", res.Count, n-1)
	}
	// Deterministic ordering by (depth, cfg_id): depths are strictly increasing here.
	for i := 1; i < len(res.Nodes); i++ {
		if res.Nodes[i].Depth < res.Nodes[i-1].Depth {
			t.Fatalf("non-monotonic depth ordering at %d", i)
		}
	}

	// Depth cap trims the response.
	capped := getTraversal(t, h, "/v1/configurations/"+ids[0]+"/dependencies?recursive=true&max_depth=10")
	if capped.Count != 10 {
		t.Fatalf("max_depth=10: count=%d, want 10", capped.Count)
	}
}

//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// governanceRouter wires the registry and the Governance Engine over one real
// pool, so the destruction routes are exercised end to end.
func governanceRouter(t *testing.T) (http.Handler, *pgxpool.Pool) {
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
	t.Cleanup(pool.Close)

	co := postgres.NewConfigObjectRepo(pool)
	auditStore := postgres.NewAuditStore(pool)
	engine, err := governance.NewEngine(
		postgres.NewGovernanceStore(pool),
		governance.AllowAllAuthorizer{},
		postgres.NewAuditAppender(pool, auditStore),
	)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: false, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		co, newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), pool.Ping)
	s.SetGovernance(engine)
	s.SetGraph(postgres.NewGraphRepo(pool))
	return s.Router(), pool
}

// A ratified, in-force object must not be destroyable through the registry
// route. The Governance Engine refuses it (§8: only working_material may be
// deleted); before ADR-GOV-002 the registry route destroyed it anyway, because
// it was a second door enforcing only the protected-role rule.
//
// Proven live against the running service: DELETE /v1/artifacts/{id} returned
// 204 on a ratified current_baseline object, the row vanished, and the count of
// deletion audit events was 0.
func TestDestruction_RegistryRouteRefusesGovernedObject(t *testing.T) {
	router, pool := governanceRouter(t)

	id := createArtifactViaAPI(t, router, fmt.Sprintf("gov-destroy-%d.md", time.Now().UnixNano()))
	ratifyViaAPI(t, router, id)

	lifecycle, retention := governanceState(t, router, id)
	if lifecycle != "ratified" || retention != "current_baseline" {
		t.Fatalf("precondition: object is %s/%s, want ratified/current_baseline", lifecycle, retention)
	}

	rec := doReq(t, router, http.MethodDelete, "/v1/artifacts/"+id, "del-"+id, "")
	t.Logf("DELETE /v1/artifacts/{id} on a ratified object -> %d", rec.Code)

	if rec.Code == http.StatusNoContent || rec.Code == http.StatusOK {
		t.Errorf("GOVERNANCE BYPASS: the registry route destroyed a ratified, current_baseline "+
			"object (status %d). The engine refuses this (§8: only working_material may be "+
			"deleted); a second door that does not is unaudited destruction of governed content",
			rec.Code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM configuration_object WHERE cfg_id=$1`, id); n != 1 {
		t.Errorf("the governed object no longer exists (rows=%d)", n)
	}
}

// Destruction that IS permitted must still be recorded. An unaudited removal
// makes the audit log an incomplete record of state changes, which is the one
// thing a governance control plane cannot allow.
func TestDestruction_PermittedDeletionIsAudited(t *testing.T) {
	router, pool := governanceRouter(t)

	id := createArtifactViaAPI(t, router, fmt.Sprintf("gov-audited-%d.md", time.Now().UnixNano()))

	rec := doReq(t, router, http.MethodDelete, "/v1/artifacts/"+id, "del-"+id, "")
	t.Logf("DELETE of a working_material draft -> %d", rec.Code)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("a permitted deletion was refused: %d %s", rec.Code, rec.Body.String())
	}

	if n := countRows(t, pool, `SELECT count(*) FROM configuration_object WHERE cfg_id=$1`, id); n != 0 {
		t.Errorf("object still present after a permitted deletion (rows=%d)", n)
	}
	n := countRows(t, pool,
		`SELECT count(*) FROM audit_event WHERE chain_id=$1 AND operation='deletion'`, id)
	t.Logf("deletion audit events: %d", n)
	if n != 1 {
		t.Errorf("UNAUDITED DESTRUCTION: %d deletion audit events for a destroyed governed "+
			"object, want 1 — the audit log would not record that the object ceased to exist, "+
			"nor who did it (ADR-GOV-002)", n)
	}
}

// The dependents guard is a §8 precondition, so it must hold on every route that
// destroys. The registry route skipped it, and ON DELETE CASCADE then silently
// removed the dependency edges an audited Extension had created — leaving the
// audit log asserting a relationship the graph no longer contained.
func TestDestruction_RegistryRouteHonoursTheDependentsGuard(t *testing.T) {
	router, pool := governanceRouter(t)

	stamp := time.Now().UnixNano()
	victim := createArtifactViaAPI(t, router, fmt.Sprintf("gov-victim-%d.md", stamp))
	successor := createArtifactViaAPI(t, router, fmt.Sprintf("gov-succ-%d.md", stamp))

	ext := doReq(t, router, http.MethodPost, "/v1/governance/"+victim+"/extend",
		"ext-"+victim, fmt.Sprintf(`{"successor_id":%q}`, successor))
	if ext.Code != http.StatusOK {
		t.Fatalf("extend: %d %s", ext.Code, ext.Body.String())
	}

	rec := doReq(t, router, http.MethodDelete, "/v1/artifacts/"+victim, "del-"+victim, "")
	t.Logf("DELETE of an object with a dependent -> %d", rec.Code)
	if rec.Code == http.StatusNoContent || rec.Code == http.StatusOK {
		t.Errorf("the registry route destroyed an object that has a dependent (status %d); the "+
			"edge would be silently cascaded away, leaving the audited Extension asserting a "+
			"relationship the graph no longer contains", rec.Code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM dependency_edge WHERE to_cfg=$1`, victim); n != 1 {
		t.Errorf("dependency edge count = %d, want 1 (the audited Extension's effect must survive)", n)
	}
}

// --- helpers -----------------------------------------------------------------

func doReq(t *testing.T, h http.Handler, method, path, idemKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func createArtifactViaAPI(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	body := fmt.Sprintf(`{"artifact":%q,"version":"1.0.0","role":"reference",`+
		`"lifecycle":"draft","retention_class":"working_material"}`, name)
	rec := doReq(t, h, http.MethodPost, "/v1/artifacts", "create-"+name, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
	}
	var o struct {
		CfgID string `json:"cfg_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return o.CfgID
}

func ratifyViaAPI(t *testing.T, h http.Handler, id string) {
	t.Helper()
	rec := doReq(t, h, http.MethodPost, "/v1/governance/"+id+"/ratify", "rat-"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ratify %s: %d %s", id, rec.Code, rec.Body.String())
	}
}

func governanceState(t *testing.T, h http.Handler, id string) (string, string) {
	t.Helper()
	rec := doReq(t, h, http.MethodGet, "/v1/governance/"+id, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get state %s: %d %s", id, rec.Code, rec.Body.String())
	}
	var o struct {
		Lifecycle string `json:"lifecycle"`
		Retention string `json:"retention_class"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	return o.Lifecycle, o.Retention
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

var _ = domain.OpDeletion

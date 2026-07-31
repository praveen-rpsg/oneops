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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/governance"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// ADR-GOV-005 — multi-approver approval quorum, end to end against a real
// service and a real database: the Governance Engine, the approval store's
// RLS-isolated table, and the setting that drives the threshold, all wired
// together exactly as cmd/controlplane/main.go wires them.
//
// AuthEnabled is TRUE here (unlike destruction_governance_integration_test.go's
// dev-mode router): dev mode hardcodes every caller's identity to the literal
// string "system", which makes it impossible to exercise DISTINCT approvers.
// Real bearer tokens with different `sub` claims are the only way to prove the
// quorum's distinctness, not just its count.
// schemaNameRe strips everything but [a-z0-9_] from a sanitized test name, so
// it is always a legal unquoted Postgres identifier.
var schemaNameRe = regexp.MustCompile(`[^a-z0-9_]+`)

func approvalQuorumRouter(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	// Setting is keyed by (tenant, key), not by governance object, so tests
	// that share a schema and a tenant would see each other's
	// governance_required_approvals override — exactly the flakiness a
	// per-object artifact name avoids elsewhere in this file, but settings has
	// no per-object name to vary. Each test therefore gets its own schema,
	// dropped and recreated fresh so a previous run's state cannot leak in
	// either.
	schema := "httpapi_appr_" + schemaNameRe.ReplaceAllString(strings.ToLower(t.Name()), "_")
	dsn := base + sep + "options=-c%20search_path%3D" + schema
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
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
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
	approvalStore := postgres.NewApprovalStore(pool)
	engine.SetApprovalRecorder(approvalStore)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		co, newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), pool.Ping)
	s.SetGovernance(engine)
	s.SetApprovals(approvalStore)
	s.SetSettings(postgres.NewSettingStore(pool))
	return s.Router(), pool
}

// mintApproverToken mints a real bearer token for a given subject, so two
// calls can name two genuinely distinct approvers (unlike doReq's dev-mode
// callers, which are all "system").
func mintApproverToken(t *testing.T, sub string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub, "iss": tIss, "aud": tAud,
		"exp": time.Now().Add(time.Hour).Unix(), "roles": []string{"oneops-admin"},
	})
	signed, err := tok.SignedString([]byte(tSecret))
	if err != nil {
		t.Fatalf("sign token for %s: %v", sub, err)
	}
	return signed
}

// doAuthed is doReq's shape plus a bearer token, since this file's whole point
// is exercising distinct authenticated identities.
func doAuthed(t *testing.T, h http.Handler, method, path, bearer, idemKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func createArtifactAuthed(t *testing.T, h http.Handler, bearer, name string) string {
	t.Helper()
	body := fmt.Sprintf(`{"artifact":%q,"version":"1.0.0","role":"reference",`+
		`"lifecycle":"draft","retention_class":"working_material"}`, name)
	rec := doAuthed(t, h, http.MethodPost, "/v1/artifacts", bearer, "create-"+name, body)
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

func governanceLifecycleAuthed(t *testing.T, h http.Handler, bearer, id string) string {
	t.Helper()
	rec := doAuthed(t, h, http.MethodGet, "/v1/governance/"+id, bearer, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get state %s: %d %s", id, rec.Code, rec.Body.String())
	}
	var o struct {
		Lifecycle string `json:"lifecycle"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	return o.Lifecycle
}

func setRequiredApprovals(t *testing.T, h http.Handler, bearer string, n int) {
	t.Helper()
	rec := doAuthed(t, h, http.MethodPut, "/v1/admin/settings/governance_required_approvals",
		bearer, "", fmt.Sprintf(`{"value":%d,"row_version":0}`, n))
	if rec.Code != http.StatusOK {
		t.Fatalf("set governance_required_approvals=%d: %d %s", n, rec.Code, rec.Body.String())
	}
}

// The end-to-end guarantee this story exists for: with
// governance_required_approvals=2, one approver leaves the object at
// in_review, and a SECOND, DISTINCT approver is what moves it to approved —
// exercised through the full stack (HTTP -> engine -> Postgres), not a mock.
func TestApprovalQuorum_TwoDistinctApproversRequired(t *testing.T) {
	router, pool := approvalQuorumRouter(t)
	admin := mintApproverToken(t, "quorum-admin")
	setRequiredApprovals(t, router, admin, 2)

	id := createArtifactAuthed(t, router, admin, fmt.Sprintf("gov-quorum-%d.md", time.Now().UnixNano()))

	alice := mintApproverToken(t, "alice@oneops")
	firstRec := doAuthed(t, router, http.MethodPost, "/v1/governance/"+id+"/approve", alice, "appr-alice-"+id, "")
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first approve: %d %s", firstRec.Code, firstRec.Body.String())
	}
	var first governanceResponse
	if err := json.Unmarshal(firstRec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first approve: %v", err)
	}
	if first.State == nil || first.State.Lifecycle != "draft" {
		t.Fatalf("after ONE approval, state = %+v, want unchanged at draft — "+
			"a bypass here would show approved below quorum", first.State)
	}
	if first.Approvals == nil || first.Approvals.Count != 1 || first.Approvals.Required != 2 || first.Approvals.Met {
		t.Fatalf("approvals after first = %+v, want count=1 required=2 met=false", first.Approvals)
	}

	if lc := governanceLifecycleAuthed(t, router, admin, id); lc != "draft" {
		t.Fatalf("GET state after one approval = %q, want draft (fail-safe: never below quorum)", lc)
	}

	bob := mintApproverToken(t, "bob@oneops")
	secondRec := doAuthed(t, router, http.MethodPost, "/v1/governance/"+id+"/approve", bob, "appr-bob-"+id, "")
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second approve: %d %s", secondRec.Code, secondRec.Body.String())
	}
	var second governanceResponse
	if err := json.Unmarshal(secondRec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second approve: %v", err)
	}
	if second.State == nil || second.State.Lifecycle != "approved" {
		t.Fatalf("after TWO distinct approvals, state = %+v, want approved", second.State)
	}
	if second.Approvals == nil || !second.Approvals.Met || second.Approvals.Count != 2 {
		t.Fatalf("approvals after second = %+v, want count=2 met=true", second.Approvals)
	}

	// GET .../approvals confirms both distinct approvers are recorded.
	listRec := doAuthed(t, router, http.MethodGet, "/v1/governance/"+id+"/approvals", admin, "", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list approvals: %d %s", listRec.Code, listRec.Body.String())
	}
	var list governanceApprovalsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode approvals list: %v", err)
	}
	if list.Count != 2 || !list.Met || len(list.Approvers) != 2 {
		t.Fatalf("approvals list = %+v, want 2 distinct approvers, met", list)
	}

	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_record WHERE governance_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count approval_record: %v", err)
	}
	if n != 2 {
		t.Fatalf("approval_record rows = %d, want 2", n)
	}
}

// The same approver cannot satisfy a quorum of 2 alone: a second call from
// the identical subject is rejected, and the object never transitions.
func TestApprovalQuorum_SameApproverTwiceNeverMeetsQuorum(t *testing.T) {
	router, _ := approvalQuorumRouter(t)
	admin := mintApproverToken(t, "quorum-admin-2")
	setRequiredApprovals(t, router, admin, 2)

	id := createArtifactAuthed(t, router, admin, fmt.Sprintf("gov-quorum-dup-%d.md", time.Now().UnixNano()))

	alice := mintApproverToken(t, "alice2@oneops")
	first := doAuthed(t, router, http.MethodPost, "/v1/governance/"+id+"/approve", alice, "appr-1-"+id, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first approve: %d %s", first.Code, first.Body.String())
	}

	dup := doAuthed(t, router, http.MethodPost, "/v1/governance/"+id+"/approve", alice, "appr-2-"+id, "")
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate approver: status = %d, want 409, body = %s", dup.Code, dup.Body.String())
	}

	if lc := governanceLifecycleAuthed(t, router, admin, id); lc != "draft" {
		t.Fatalf("lifecycle after a duplicate approver = %q, want unchanged at draft", lc)
	}
}

// Backward compatibility, end to end: with the setting left at its default
// (no override), a single approval reaches approved exactly as it did before
// this story existed.
func TestApprovalQuorum_DefaultSettingReproducesSingleActorApprove(t *testing.T) {
	router, _ := approvalQuorumRouter(t)
	admin := mintApproverToken(t, "quorum-admin-3")

	id := createArtifactAuthed(t, router, admin, fmt.Sprintf("gov-quorum-default-%d.md", time.Now().UnixNano()))

	rec := doAuthed(t, router, http.MethodPost, "/v1/governance/"+id+"/approve", admin, "appr-default-"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	var resp governanceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State == nil || resp.State.Lifecycle != "approved" {
		t.Fatalf("state = %+v, want approved on the first approval (required defaults to 1)", resp.State)
	}
	if resp.Approvals == nil || resp.Approvals.Required != 1 || !resp.Approvals.Met {
		t.Fatalf("approvals = %+v, want required=1 met=true", resp.Approvals)
	}
}

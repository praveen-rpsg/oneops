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
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// organizationRouter wires the real OrganizationStore behind the real router.
//
// The handler tests run against a fake, which proves the transport rules but
// cannot prove the two agree. Everything the store does that the fake only
// imitates — the tenant row written in the same transaction, the cascade
// performed in SQL, the conflict raised by a constraint rather than a loop —
// is only observable here.
func organizationRouter(t *testing.T) (http.Handler, *pgxpool.Pool) {
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

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: false, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), pool.Ping)
	s.SetOrganizations(postgres.NewOrganizationStore(pool))
	return s.Router(), pool
}

// orgReq issues a request with auth disabled, so these tests exercise the store
// rather than re-proving the authorization already covered by the handler tests.
func orgReq(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// uniqueSlug keeps runs independent: the slug is unique by constraint, so a
// fixed one would make the second run of this test conflict with the first.
func uniqueSlug(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// The full transport path against real SQL: create, read back, find by slug,
// page, suspend, reactivate.
func TestOrganizationsAPI_LifecycleAgainstRealStore(t *testing.T) {
	router, pool := organizationRouter(t)
	ctx := context.Background()
	slug := uniqueSlug("itest-org")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organization WHERE slug = $1`, slug)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE slug = $1`, slug)
	})

	rec := orgReq(t, router, http.MethodPost, "/v1/admin/organizations",
		fmt.Sprintf(`{"slug":%q,"name":"Integration Org"}`, slug))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	created := decodeOrg(t, rec)

	// The tenant must exist by the time the response is written. Both rows or
	// neither (ADR-IDENTITY-001 §7.1): an organisation observable without its
	// tenant is an Identity scope with no isolation.
	var tenantSlug, tenantStatus string
	if err := pool.QueryRow(ctx,
		`SELECT slug, status FROM tenant WHERE tenant_id = $1`, created.TenantID,
	).Scan(&tenantSlug, &tenantStatus); err != nil {
		t.Fatalf("the organization's tenant was not created: %v", err)
	}
	if tenantSlug != slug || tenantStatus != "active" {
		t.Errorf("tenant is %s/%s, want %s/active", tenantSlug, tenantStatus, slug)
	}

	rec = orgReq(t, router, http.MethodGet, "/v1/admin/organizations/"+created.OrgID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeOrg(t, rec); got.OrgID != created.OrgID || got.TenantID != created.TenantID {
		t.Errorf("get returned %+v, want the created organization", got)
	}

	rec = orgReq(t, router, http.MethodGet, "/v1/admin/organizations?slug="+slug, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list by slug: got %d, want 200", rec.Code)
	}
	if got := decodeOrgList(t, rec); len(got) != 1 || got[0].OrgID != created.OrgID {
		t.Fatalf("list by slug returned %d rows, want exactly the created organization", len(got))
	}

	rec = orgReq(t, router, http.MethodGet, "/v1/admin/organizations?limit=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200", rec.Code)
	}
	if got := decodeOrgList(t, rec); len(got) != 1 {
		t.Errorf("limit=1 returned %d rows, want 1 — the handler must forward paging", len(got))
	}

	// Suspend, and prove the cascade reached the tenant. This is the assertion
	// the fake can only imitate: the store performs it in the same transaction,
	// and the authentication boundary reads tenant status, not this one.
	rec = orgReq(t, router, http.MethodPatch, "/v1/admin/organizations/"+created.OrgID,
		fmt.Sprintf(`{"row_version":%d,"status":"suspended"}`, created.RowVersion))
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	suspended := decodeOrg(t, rec)
	if suspended.Status != "suspended" || suspended.RowVersion != created.RowVersion+1 {
		t.Errorf("after suspend: status=%s row_version=%d, want suspended/%d",
			suspended.Status, suspended.RowVersion, created.RowVersion+1)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM tenant WHERE tenant_id = $1`, created.TenantID,
	).Scan(&tenantStatus); err != nil {
		t.Fatalf("read tenant: %v", err)
	}
	if tenantStatus != "suspended" {
		t.Errorf("tenant status %q after suspending the organization, want suspended — an "+
			"organization suspended while its tenant still serves is not suspended at all "+
			"(ADR-IDENTITY-001 §8.3)", tenantStatus)
	}

	rec = orgReq(t, router, http.MethodPatch, "/v1/admin/organizations/"+created.OrgID,
		fmt.Sprintf(`{"row_version":%d,"status":"active"}`, suspended.RowVersion))
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM tenant WHERE tenant_id = $1`, created.TenantID,
	).Scan(&tenantStatus); err != nil {
		t.Fatalf("read tenant: %v", err)
	}
	if tenantStatus != "active" {
		t.Errorf("tenant status %q after reactivating, want active", tenantStatus)
	}
}

// A duplicate slug must surface as 409 through the transport, not as a 500 from
// a constraint violation. The conflict is raised by the database here, which is
// the path the fake cannot exercise.
func TestOrganizationsAPI_DuplicateSlugIsConflict(t *testing.T) {
	router, pool := organizationRouter(t)
	ctx := context.Background()
	slug := uniqueSlug("itest-dup")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organization WHERE slug = $1`, slug)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE slug = $1`, slug)
	})

	body := fmt.Sprintf(`{"slug":%q,"name":"First"}`, slug)
	if rec := orgReq(t, router, http.MethodPost, "/v1/admin/organizations", body); rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	rec := orgReq(t, router, http.MethodPost, "/v1/admin/organizations",
		fmt.Sprintf(`{"slug":%q,"name":"Second"}`, slug))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate slug: got %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}

	// The failed attempt must leave nothing behind. Create writes the tenant
	// first, so a non-transactional implementation would strand one here.
	var tenants int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tenant WHERE slug = $1`, slug).Scan(&tenants); err != nil {
		t.Fatalf("count tenants: %v", err)
	}
	if tenants != 1 {
		t.Errorf("%d tenants hold slug %q after one successful and one conflicting create, want 1 "+
			"— the rejected create stranded a tenant row", tenants, slug)
	}
}

// A stale row_version is 409 and must change nothing, including the tenant.
func TestOrganizationsAPI_StaleRowVersionLeavesTenantUntouched(t *testing.T) {
	router, pool := organizationRouter(t)
	ctx := context.Background()
	slug := uniqueSlug("itest-stale")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organization WHERE slug = $1`, slug)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE slug = $1`, slug)
	})

	rec := orgReq(t, router, http.MethodPost, "/v1/admin/organizations",
		fmt.Sprintf(`{"slug":%q,"name":"Stale"}`, slug))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	created := decodeOrg(t, rec)

	rec = orgReq(t, router, http.MethodPatch, "/v1/admin/organizations/"+created.OrgID,
		fmt.Sprintf(`{"row_version":%d,"status":"suspended"}`, created.RowVersion+99))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale row_version: got %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}

	var orgStatus, tenantStatus string
	if err := pool.QueryRow(ctx,
		`SELECT o.status, t.status FROM organization o JOIN tenant t ON t.tenant_id = o.tenant_id
		  WHERE o.org_id = $1`, created.OrgID,
	).Scan(&orgStatus, &tenantStatus); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if orgStatus != "active" || tenantStatus != "active" {
		t.Errorf("after a refused patch: organization=%s tenant=%s, want both active",
			orgStatus, tenantStatus)
	}
}

// An unknown identifier is 404, distinguished from the 409 above. The store
// probes to tell the two apart rather than returning one ambiguous failure.
func TestOrganizationsAPI_UnknownIdentifierIsNotFound(t *testing.T) {
	router, _ := organizationRouter(t)

	if rec := orgReq(t, router, http.MethodGet, "/v1/admin/organizations/"+domain.NewID(), ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown: got %d, want 404", rec.Code)
	}
	rec := orgReq(t, router, http.MethodPatch, "/v1/admin/organizations/"+domain.NewID(),
		`{"row_version":1,"status":"suspended"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("patch unknown: got %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// The response body must be the row that was written, not the request that was
// sent. Serialization is where a normalised value silently reverts.
func TestOrganizationsAPI_ResponseMatchesPersistedRow(t *testing.T) {
	router, pool := organizationRouter(t)
	ctx := context.Background()
	slug := uniqueSlug("itest-serial")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organization WHERE slug = $1`, slug)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE slug = $1`, slug)
	})

	rec := orgReq(t, router, http.MethodPost, "/v1/admin/organizations",
		fmt.Sprintf(`{"slug":%q,"name":"  Serialized Org  "}`, strings.ToUpper(slug)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeOrg(t, rec)

	var row struct {
		orgID, tenantID, slug, name, status string
		rowVersion                          int64
	}
	if err := pool.QueryRow(ctx,
		`SELECT org_id, tenant_id, slug, name, status, row_version FROM organization WHERE org_id = $1`,
		got.OrgID,
	).Scan(&row.orgID, &row.tenantID, &row.slug, &row.name, &row.status, &row.rowVersion); err != nil {
		t.Fatalf("read back: %v", err)
	}

	if got.Slug != row.slug || got.Name != row.name || got.Status != row.status ||
		got.TenantID != row.tenantID || got.RowVersion != row.rowVersion {
		t.Errorf("response %+v does not match the persisted row %+v", got, row)
	}
	if got.Slug != slug {
		t.Errorf("slug %q: an uppercase request must be stored and returned lowercased", got.Slug)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps must be serialized, not zero")
	}

	// The DTO must expose exactly the documented fields — no internal column
	// leaking through a struct change.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	want := []string{"org_id", "tenant_id", "slug", "name", "status", "row_version", "created_at", "updated_at"}
	if len(raw) != len(want) {
		t.Errorf("response has %d fields %v, want exactly %v", len(raw), keysOf(raw), want)
	}
	for _, k := range want {
		if _, ok := raw[k]; !ok {
			t.Errorf("response is missing documented field %q", k)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

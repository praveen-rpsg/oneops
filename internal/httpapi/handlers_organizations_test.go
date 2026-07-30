package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

// ---- fake repository --------------------------------------------------------

// fakeOrganizations mirrors OrganizationStore's observable contract, including
// the parts a handler test could get away with skipping. A fake that is laxer
// than the store pins the wrong contract: the user tests found exactly that when
// fakeUsers.SetStatus skipped Valid() and reported 409 where the store reports
// 422.
type fakeOrganizations struct {
	byID     map[string]*domain.Organization
	failWith error
	// lastLimit/lastAfter record what List was asked for, so a test can prove
	// the handler forwarded the caller's paging rather than substituting its own.
	lastLimit int
	lastAfter string
	// setStatusCalls counts what reached the repository. The handler rejects an
	// undefined status before the round trip; without this counter the
	// repository's own validation returns the same 422 and the handler's check
	// is unobservable — the test would pass with the check deleted.
	setStatusCalls int
	// tenantStatus records the cascade. The store performs it in SQL; here it is
	// observable so a handler test can prove the handler did not bypass
	// SetStatus to update the organisation alone.
	tenantStatus map[string]domain.TenantStatus
}

func newFakeOrganizations(os ...*domain.Organization) *fakeOrganizations {
	f := &fakeOrganizations{
		byID:         map[string]*domain.Organization{},
		tenantStatus: map[string]domain.TenantStatus{},
	}
	for _, o := range os {
		f.byID[o.OrgID] = o
		f.tenantStatus[o.TenantID] = domain.TenantActive
	}
	return f
}

func (f *fakeOrganizations) Create(_ context.Context, o *domain.Organization) (*domain.Organization, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	// The slug is unique across organisations AND tenants; the store discovers
	// that through two constraints, the fake through one check.
	for _, e := range f.byID {
		if e.Slug == o.Slug {
			return nil, domain.ErrConflict
		}
	}
	cp := *o
	cp.RowVersion = 1
	cp.CreatedAt = time.Now().UTC()
	cp.UpdatedAt = cp.CreatedAt
	f.byID[cp.OrgID] = &cp
	f.tenantStatus[cp.TenantID] = domain.TenantActive
	return &cp, nil
}

func (f *fakeOrganizations) Get(_ context.Context, id string) (*domain.Organization, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	if o, ok := f.byID[id]; ok {
		return o, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeOrganizations) GetByTenant(_ context.Context, tenantID string) (*domain.Organization, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	for _, o := range f.byID {
		if o.TenantID == tenantID {
			return o, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeOrganizations) GetBySlug(_ context.Context, slug string) (*domain.Organization, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	for _, o := range f.byID {
		if o.Slug == slug {
			return o, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeOrganizations) List(_ context.Context, limit int, after string) ([]*domain.Organization, error) {
	f.lastLimit, f.lastAfter = limit, after
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := make([]*domain.Organization, 0, len(f.byID))
	for _, o := range f.byID {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OrgID < out[j].OrgID })
	return out, nil
}

func (f *fakeOrganizations) SetStatus(
	_ context.Context, id string, rv int64, st domain.OrganizationStatus,
) (*domain.Organization, error) {
	f.setStatusCalls++
	// Mirrors OrganizationStore.SetStatus: an undefined status is a validation
	// failure, checked before the row is looked up.
	if !st.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: active, suspended")
	}
	o, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if o.RowVersion != rv {
		return nil, domain.ErrVersionMismatch
	}
	cp := *o
	cp.Status = st
	cp.RowVersion++
	f.byID[id] = &cp
	// The cascade (ADR-IDENTITY-001 §8.3), in the same call the store performs
	// it in the same transaction.
	if st == domain.OrganizationSuspended {
		f.tenantStatus[cp.TenantID] = domain.TenantSuspended
	} else {
		f.tenantStatus[cp.TenantID] = domain.TenantActive
	}
	return &cp, nil
}

var _ domain.OrganizationRepository = (*fakeOrganizations)(nil)

// ---- harness ----------------------------------------------------------------

func newOrgAPI(t *testing.T, repo domain.OrganizationRepository) http.Handler {
	t.Helper()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if repo != nil {
		s.SetOrganizations(repo)
	}
	return s.Router()
}

func seedOrg(t *testing.T, slug, name string) *domain.Organization {
	t.Helper()
	o, err := domain.NewOrganization(name, slug)
	if err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	o.RowVersion = 1
	o.CreatedAt = time.Now().UTC()
	o.UpdatedAt = o.CreatedAt
	return o
}

func decodeOrg(t *testing.T, rec *httptest.ResponseRecorder) organizationDTO {
	t.Helper()
	var o organizationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode organization: %v (body %s)", err, rec.Body.String())
	}
	return o
}

func decodeOrgList(t *testing.T, rec *httptest.ResponseRecorder) []organizationDTO {
	t.Helper()
	var page struct {
		Items []organizationDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode organization list: %v (body %s)", err, rec.Body.String())
	}
	return page.Items
}

// ---- authorization ----------------------------------------------------------

// The organisation registry is the most privileged of the three: creating a row
// creates a Tenant, and suspending one cascades to that tenant. A tenant
// administrator reaching these routes could mint isolation boundaries and lock
// any other customer out of the platform.
func TestOrganizations_RequirePlatformAdmin(t *testing.T) {
	h := newOrgAPI(t, newFakeOrganizations())

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/organizations", ""},
		{http.MethodPost, "/v1/admin/organizations", `{"slug":"acme","name":"Acme"}`},
		{http.MethodGet, "/v1/admin/organizations/org_1", ""},
		{http.MethodPatch, "/v1/admin/organizations/org_1", `{"row_version":1,"status":"suspended"}`},
	} {
		rec := userReq(t, h, c.method, c.path, "oneops-admin", c.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a tenant admin: got %d, want 403 — a tenant administrator must "+
				"not administer the global organization registry", c.method, c.path, rec.Code)
		}
	}
}

// ---- not configured ---------------------------------------------------------

// An unwired registry answers 501, not 404: a deployment that has not enabled
// identity says so rather than looking like a routing mistake.
func TestOrganizations_UnconfiguredRegistryIsNotImplemented(t *testing.T) {
	h := newOrgAPI(t, nil)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/organizations", ""},
		{http.MethodPost, "/v1/admin/organizations", `{"slug":"acme","name":"Acme"}`},
		{http.MethodGet, "/v1/admin/organizations/org_1", ""},
		{http.MethodPatch, "/v1/admin/organizations/org_1", `{"row_version":1,"status":"suspended"}`},
	} {
		rec := userReq(t, h, c.method, c.path, padmin, c.body)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s with no registry: got %d, want 501", c.method, c.path, rec.Code)
		}
	}
}

// ---- create -----------------------------------------------------------------

func TestOrganizations_Create(t *testing.T) {
	repo := newFakeOrganizations()
	h := newOrgAPI(t, repo)

	rec := userReq(t, h, http.MethodPost, "/v1/admin/organizations", padmin,
		`{"slug":"Acme-Corp","name":"  Acme Corporation  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeOrg(t, rec)

	// Normalisation is the aggregate's, and the response must show what was
	// actually stored rather than what was sent.
	if got.Slug != "acme-corp" {
		t.Errorf("slug %q: want the aggregate's lowercased form", got.Slug)
	}
	if got.Name != "Acme Corporation" {
		t.Errorf("name %q: want the aggregate's trimmed form", got.Name)
	}
	if got.Status != "active" {
		t.Errorf("status %q: a new organization is active", got.Status)
	}
	if got.RowVersion != 1 {
		t.Errorf("row_version %d: want 1", got.RowVersion)
	}
	if got.OrgID == "" || got.TenantID == "" {
		t.Fatalf("identifiers must be minted server-side, got org_id=%q tenant_id=%q",
			got.OrgID, got.TenantID)
	}
	if got.OrgID == got.TenantID {
		t.Error("org_id and tenant_id must be distinct identifiers")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps must be serialized")
	}
}

// Neither identifier may be chosen by the caller. A caller-supplied tenant_id
// could name the system tenant; a caller-supplied org_id is the cross-tenant
// key-confusion defect the Trust Register records as entry 1.
func TestOrganizations_CreateIgnoresCallerSuppliedIdentifiers(t *testing.T) {
	repo := newFakeOrganizations()
	h := newOrgAPI(t, repo)

	rec := userReq(t, h, http.MethodPost, "/v1/admin/organizations", padmin,
		`{"slug":"acme","name":"Acme","org_id":"org_attacker","tenant_id":"system"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeOrg(t, rec)
	if got.OrgID == "org_attacker" {
		t.Error("org_id was taken from the request body")
	}
	if got.TenantID == "system" {
		t.Error("tenant_id was taken from the request body — a caller could name the system tenant")
	}
}

func TestOrganizations_CreateRejectsInvalidBody(t *testing.T) {
	h := newOrgAPI(t, newFakeOrganizations())

	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"malformed json", `{"slug":`, http.StatusBadRequest},
		{"missing slug", `{"name":"Acme"}`, http.StatusUnprocessableEntity},
		{"missing name", `{"slug":"acme"}`, http.StatusUnprocessableEntity},
		{"slug too short", `{"slug":"a","name":"Acme"}`, http.StatusUnprocessableEntity},
		{"slug starts with dash", `{"slug":"-acme","name":"Acme"}`, http.StatusUnprocessableEntity},
		{"slug has underscore", `{"slug":"acme_corp","name":"Acme"}`, http.StatusUnprocessableEntity},
		{"blank name", `{"slug":"acme","name":"   "}`, http.StatusUnprocessableEntity},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := userReq(t, h, http.MethodPost, "/v1/admin/organizations", padmin, c.body)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func TestOrganizations_CreateDuplicateSlugIsConflict(t *testing.T) {
	repo := newFakeOrganizations(seedOrg(t, "acme", "Acme"))
	h := newOrgAPI(t, repo)

	rec := userReq(t, h, http.MethodPost, "/v1/admin/organizations", padmin,
		`{"slug":"acme","name":"Acme Again"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate slug: got %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

// ---- get --------------------------------------------------------------------

func TestOrganizations_Get(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	h := newOrgAPI(t, newFakeOrganizations(org))

	rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations/"+org.OrgID, padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeOrg(t, rec)
	if got.OrgID != org.OrgID || got.TenantID != org.TenantID || got.Slug != "acme" {
		t.Errorf("get returned %+v, want the seeded organization", got)
	}
}

func TestOrganizations_GetUnknownIsNotFound(t *testing.T) {
	h := newOrgAPI(t, newFakeOrganizations())

	rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations/org_missing", padmin, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get unknown: got %d, want 404", rec.Code)
	}
}

// ---- list -------------------------------------------------------------------

func TestOrganizations_List(t *testing.T) {
	a, b := seedOrg(t, "acme", "Acme"), seedOrg(t, "beta", "Beta")
	h := newOrgAPI(t, newFakeOrganizations(a, b))

	rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200", rec.Code)
	}
	if got := decodeOrgList(t, rec); len(got) != 2 {
		t.Errorf("list returned %d organizations, want 2", len(got))
	}
}

// An empty registry must serialize as [], not null. A null items array is the
// v1.0.0 nil-slice defect (PM-001) in a new place.
func TestOrganizations_ListEmptySerializesAsArray(t *testing.T) {
	h := newOrgAPI(t, newFakeOrganizations())

	rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !jsonHasEmptyItems(body) {
		t.Errorf("empty list serialized as %s, want an empty items array", body)
	}
}

func jsonHasEmptyItems(body string) bool {
	var page struct {
		Items *[]organizationDTO `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		return false
	}
	return page.Items != nil && len(*page.Items) == 0
}

// The handler must forward the caller's paging rather than substituting its
// own; clamping is the repository's decision, made in one place.
func TestOrganizations_ListForwardsPaging(t *testing.T) {
	repo := newFakeOrganizations()
	h := newOrgAPI(t, repo)

	rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations?limit=7&after=org_x", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200", rec.Code)
	}
	if repo.lastLimit != 7 {
		t.Errorf("limit forwarded as %d, want 7", repo.lastLimit)
	}
	if repo.lastAfter != "org_x" {
		t.Errorf("after forwarded as %q, want org_x", repo.lastAfter)
	}
}

func TestOrganizations_ListRejectsInvalidLimit(t *testing.T) {
	h := newOrgAPI(t, newFakeOrganizations())

	for _, raw := range []string{"0", "-1", "abc", "1.5"} {
		rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations?limit="+raw, padmin, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("limit=%s: got %d, want 422", raw, rec.Code)
		}
	}
}

// The slug filter resolves a single organisation and returns a list of zero or
// one. A filter that matches nothing is an empty page: the collection exists
// whether or not anything in it matched.
func TestOrganizations_ListBySlug(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	h := newOrgAPI(t, newFakeOrganizations(org, seedOrg(t, "beta", "Beta")))

	rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations?slug=acme", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list by slug: got %d, want 200", rec.Code)
	}
	got := decodeOrgList(t, rec)
	if len(got) != 1 || got[0].OrgID != org.OrgID {
		t.Fatalf("list by slug returned %+v, want exactly the acme organization", got)
	}
}

func TestOrganizations_ListByUnknownSlugIsEmptyPage(t *testing.T) {
	h := newOrgAPI(t, newFakeOrganizations(seedOrg(t, "acme", "Acme")))

	rec := userReq(t, h, http.MethodGet, "/v1/admin/organizations?slug=nobody", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown slug: got %d, want 200 with an empty page, not 404", rec.Code)
	}
	if got := decodeOrgList(t, rec); len(got) != 0 {
		t.Errorf("unknown slug returned %d organizations, want 0", len(got))
	}
}

// ---- patch ------------------------------------------------------------------

func TestOrganizations_PatchSuspendsAndCascades(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	repo := newFakeOrganizations(org)
	h := newOrgAPI(t, repo)

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/organizations/"+org.OrgID, padmin,
		`{"row_version":1,"status":"suspended"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeOrg(t, rec)
	if got.Status != "suspended" {
		t.Errorf("status %q, want suspended", got.Status)
	}
	if got.RowVersion != 2 {
		t.Errorf("row_version %d, want 2 — a write must consume a version", got.RowVersion)
	}
	// The cascade is the point: the authentication boundary reads tenant status,
	// so an organisation suspended without its tenant is not suspended at all.
	if repo.tenantStatus[org.TenantID] != domain.TenantSuspended {
		t.Errorf("tenant status %q after suspending the organization, want suspended "+
			"(ADR-IDENTITY-001 §8.3)", repo.tenantStatus[org.TenantID])
	}
}

func TestOrganizations_PatchReactivatesAndCascades(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	org.Status = domain.OrganizationSuspended
	repo := newFakeOrganizations(org)
	repo.tenantStatus[org.TenantID] = domain.TenantSuspended
	h := newOrgAPI(t, repo)

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/organizations/"+org.OrgID, padmin,
		`{"row_version":1,"status":"active"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeOrg(t, rec); got.Status != "active" {
		t.Errorf("status %q, want active", got.Status)
	}
	if repo.tenantStatus[org.TenantID] != domain.TenantActive {
		t.Errorf("tenant status %q after reactivating, want active", repo.tenantStatus[org.TenantID])
	}
}

// row_version is required. Without it a console where two operators hold the
// same organisation open loses one of their decisions silently — and suspension
// is not an update anyone should apply to a record they have not read.
func TestOrganizations_PatchRequiresRowVersion(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	repo := newFakeOrganizations(org)
	h := newOrgAPI(t, repo)

	for _, body := range []string{
		`{"status":"suspended"}`,
		`{"row_version":0,"status":"suspended"}`,
		`{"row_version":-1,"status":"suspended"}`,
	} {
		rec := userReq(t, h, http.MethodPatch, "/v1/admin/organizations/"+org.OrgID, padmin, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("patch %s: got %d, want 422", body, rec.Code)
		}
	}
	// Nothing may have changed, and in particular the cascade must not have run.
	if repo.byID[org.OrgID].Status != domain.OrganizationActive {
		t.Error("a refused patch changed the organization")
	}
	if repo.tenantStatus[org.TenantID] != domain.TenantActive {
		t.Error("a refused patch cascaded to the tenant")
	}
}

func TestOrganizations_PatchStaleRowVersionIsConflict(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	repo := newFakeOrganizations(org)
	h := newOrgAPI(t, repo)

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/organizations/"+org.OrgID, padmin,
		`{"row_version":99,"status":"suspended"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale row_version: got %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.tenantStatus[org.TenantID] != domain.TenantActive {
		t.Error("a conflicting patch cascaded to the tenant")
	}
}

// An undefined status is a malformed request, not a refused move: 422, not 409.
func TestOrganizations_PatchRejectsUndefinedStatus(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	repo := newFakeOrganizations(org)
	h := newOrgAPI(t, repo)

	for _, st := range []string{"deleted", "ACTIVE", "", "invited", "archived"} {
		body := `{"row_version":1,"status":"` + st + `"}`
		rec := userReq(t, h, http.MethodPatch, "/v1/admin/organizations/"+org.OrgID, padmin, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status=%q: got %d, want 422 (body %s)", st, rec.Code, rec.Body.String())
		}
	}

	// The status code alone does not prove the handler checked anything: the
	// repository validates too and answers 422 for the same input. What the
	// transport rule actually asserts is that none of these requests reached it.
	if repo.setStatusCalls != 0 {
		t.Errorf("the repository was called %d times for undefined statuses; the handler must "+
			"reject them before the round trip, not rely on the store to repeat the check",
			repo.setStatusCalls)
	}
}

func TestOrganizations_PatchRejectsMalformedBody(t *testing.T) {
	org := seedOrg(t, "acme", "Acme")
	h := newOrgAPI(t, newFakeOrganizations(org))

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/organizations/"+org.OrgID, padmin, `{"row_version":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: got %d, want 400", rec.Code)
	}
}

func TestOrganizations_PatchUnknownIsNotFound(t *testing.T) {
	h := newOrgAPI(t, newFakeOrganizations())

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/organizations/org_missing", padmin,
		`{"row_version":1,"status":"suspended"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch unknown: got %d, want 404", rec.Code)
	}
}

// ---- error mapping ----------------------------------------------------------

// sentinelText is a string no problem+json body should ever contain. The
// failure is recognisable only by its own text, so a response carrying it
// proves the repository's error reached the client.
const sentinelText = "pg: relation \"secret_internal_table\" does not exist"

var errSentinel = errors.New(sentinelText)

// An unrecognized repository failure must not leak its text. The body stays
// opaque and request_id correlates it with the log line.
func TestOrganizations_RepositoryFailureIsOpaque500(t *testing.T) {
	repo := newFakeOrganizations()
	repo.failWith = errSentinel
	h := newOrgAPI(t, repo)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/organizations", ""},
		{http.MethodGet, "/v1/admin/organizations?slug=acme", ""},
		{http.MethodGet, "/v1/admin/organizations/org_1", ""},
		{http.MethodPost, "/v1/admin/organizations", `{"slug":"acme","name":"Acme"}`},
	} {
		rec := userReq(t, h, c.method, c.path, padmin, c.body)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s: got %d, want 500", c.method, c.path, rec.Code)
			continue
		}
		if body := rec.Body.String(); strings.Contains(body, "secret_internal_table") {
			t.Errorf("%s %s leaked the repository error into the response: %s",
				c.method, c.path, body)
		}
	}
}

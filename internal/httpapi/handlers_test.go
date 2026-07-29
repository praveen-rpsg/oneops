package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

const (
	tIss    = "https://oneops.local"
	tAud    = "oneops"
	tSecret = "test-secret"
)

// ---- fakes ------------------------------------------------------------------

type fakeRepo struct {
	mu       sync.Mutex
	items    map[string]*domain.ConfigObject
	seen     map[string]bool
	failList bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{items: map[string]*domain.ConfigObject{}, seen: map[string]bool{}}
}

func clone(o *domain.ConfigObject) *domain.ConfigObject {
	c := *o
	c.Metadata = map[string]string{}
	for k, v := range o.Metadata {
		c.Metadata[k] = v
	}
	return &c
}

func (r *fakeRepo) Create(_ context.Context, obj *domain.ConfigObject) (*domain.ConfigObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := obj.Artifact + "|" + obj.Version
	if r.seen[key] {
		return nil, domain.ErrConflict
	}
	if obj.CfgID == "" {
		obj.CfgID = domain.NewID()
	}
	if obj.Authority == "" {
		obj.Authority = domain.AuthorityNonNormative
	}
	obj.RowVersion = 1
	obj.CreatedAt = time.Now().UTC()
	obj.UpdatedAt = obj.CreatedAt
	r.seen[key] = true
	r.items[obj.CfgID] = clone(obj)
	return clone(obj), nil
}

func (r *fakeRepo) Get(_ context.Context, id string) (*domain.ConfigObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return clone(o), nil
}

func (r *fakeRepo) List(_ context.Context, p domain.ListParams) (*domain.Page, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failList {
		return nil, errors.New("simulated store failure")
	}
	var out []*domain.ConfigObject
	for _, o := range r.items {
		if p.Filter.Role != "" && o.Role != p.Filter.Role {
			continue
		}
		if p.Filter.Lifecycle != "" && o.Lifecycle != p.Filter.Lifecycle {
			continue
		}
		if p.Filter.Authority != "" && o.Authority != p.Filter.Authority {
			continue
		}
		if q := p.Filter.Query; q != "" && !strings.Contains(strings.ToLower(o.Artifact), strings.ToLower(q)) {
			continue
		}
		out = append(out, clone(o))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CfgID < out[j].CfgID })
	if p.Limit > 0 && len(out) > p.Limit {
		out = out[:p.Limit]
	}
	return &domain.Page{Items: out}, nil
}

func (r *fakeRepo) Update(_ context.Context, id string, expected int64, patch *domain.Patch) (*domain.ConfigObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if o.RowVersion != expected {
		return nil, domain.ErrVersionMismatch
	}
	c := clone(o)
	if patch.RatifiedBy != nil {
		c.RatifiedBy = *patch.RatifiedBy
	}
	// ReviewCycle and RetentionPolicy were missing here while the real
	// repository has always written them; no test patched those fields, so the
	// divergence went unnoticed.
	if patch.ReviewCycle != nil {
		c.ReviewCycle = *patch.ReviewCycle
	}
	if patch.RetentionPolicy != nil {
		c.RetentionPolicy = *patch.RetentionPolicy
	}
	if patch.Metadata != nil {
		c.Metadata = patch.Metadata
	}
	c.RowVersion++
	c.UpdatedAt = time.Now().UTC()
	r.items[id] = clone(c)
	return clone(c), nil
}

func (r *fakeRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.items, id)
	return nil
}

func (r *fakeRepo) BulkCreate(ctx context.Context, objs []*domain.ConfigObject) ([]*domain.ConfigObject, error) {
	out := make([]*domain.ConfigObject, 0, len(objs))
	for _, o := range objs {
		created, err := r.Create(ctx, o)
		if err != nil {
			return nil, err
		}
		out = append(out, created)
	}
	return out, nil
}

type fakeIdem struct {
	mu sync.Mutex
	m  map[string]struct {
		status int
		body   []byte
	}
}

func newFakeIdem() *fakeIdem {
	return &fakeIdem{m: map[string]struct {
		status int
		body   []byte
	}{}}
}

func (f *fakeIdem) Lookup(_ context.Context, key string) (int, []byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.m[key]
	return rec.status, rec.body, ok, nil
}

func (f *fakeIdem) Save(_ context.Context, key, _, _ string, status int, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[key]; !ok {
		f.m[key] = struct {
			status int
			body   []byte
		}{status, body}
	}
	return nil
}

// ---- harness ----------------------------------------------------------------

// newTestServer builds the server itself, for tests that need to wire an
// optional subsystem (SetTenants, SetGraph, ...) before routing.
func newTestServer(authEnabled bool) (*Server, *fakeRepo) {
	repo := newFakeRepo()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: authEnabled, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo, newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	return s, repo
}

func newTestAPI(authEnabled bool) (http.Handler, *fakeRepo) {
	s, repo := newTestServer(authEnabled)
	return s.Router(), repo
}

func do(h http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// validCreate is the INT-3 inception state — the only state Registry Create may
// produce (CP-1.2). It previously asserted approved/current_baseline/active,
// which fabricated a ratified baseline artifact without a §8 operation.
func validCreate() createRequest {
	return createRequest{
		Artifact: "OneOps-Test.md", Version: "1.0.0", Role: "governance",
		Lifecycle: "draft", RetentionClass: "working_material",
		Metadata: map[string]string{"owner": "platform"},
	}
}

func decodeCO(t *testing.T, rec *httptest.ResponseRecorder) configObjectResponse {
	t.Helper()
	var out configObjectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func mintToken(t *testing.T, roles []string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u", "iss": tIss, "aud": tAud,
		"exp": time.Now().Add(time.Hour).Unix(), "roles": roles,
	})
	s, err := tok.SignedString([]byte(tSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// mintTenantToken mints an admin token asserting a tenant. An empty external id
// omits the claim entirely, which is the case a pre-tenancy token represents.
func mintTenantToken(t *testing.T, external string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": "u", "iss": tIss, "aud": tAud,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"roles": []string{"oneops-admin"},
	}
	if external != "" {
		claims["tenant"] = external
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(tSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- tests ------------------------------------------------------------------

func TestInfraEndpoints(t *testing.T) {
	h, _ := newTestAPI(false)
	for path, want := range map[string]int{
		"/healthz": 200, "/readyz": 200, "/metrics": 200, "/": 200, "/nope": 404,
	} {
		if rec := do(h, http.MethodGet, path, nil, nil); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
	if rec := do(h, http.MethodGet, "/openapi.yaml", nil, nil); rec.Code != 200 {
		t.Errorf("openapi = %d", rec.Code)
	}
}

func TestCreateGetDelete(t *testing.T) {
	h, _ := newTestAPI(false)

	rec := do(h, http.MethodPost, "/v1/artifacts", validCreate(), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` {
		t.Errorf("ETag = %q", rec.Header().Get("ETag"))
	}
	created := decodeCO(t, rec)
	// Create yields the INT-3 inception state, and Authority is server-computed
	// (§6, RFC-AUTH) — never echoed back from the request.
	if created.CfgID == "" || created.Authority != "non_normative" ||
		created.Lifecycle != "draft" || created.RetentionClass != "working_material" {
		t.Fatalf("unexpected created: %+v", created)
	}

	got := do(h, http.MethodGet, "/v1/artifacts/"+created.CfgID, nil, nil)
	if got.Code != 200 {
		t.Fatalf("get = %d", got.Code)
	}
	if decodeCO(t, got).Artifact != "OneOps-Test.md" {
		t.Error("artifact mismatch")
	}

	// If-None-Match => 304.
	nm := do(h, http.MethodGet, "/v1/artifacts/"+created.CfgID, nil, map[string]string{"If-None-Match": `"1"`})
	if nm.Code != http.StatusNotModified {
		t.Errorf("expected 304, got %d", nm.Code)
	}

	// Destroying a Configuration Object is a §8 constitutional operation executed
	// by the Governance Engine (ADR-GOV-002). This server is built without one,
	// so destruction must be refused — and, critically, the object must survive.
	// The previous behaviour destroyed it here through a repository method that
	// enforced only the protected-role rule and wrote no audit event.
	del := do(h, http.MethodDelete, "/v1/artifacts/"+created.CfgID, nil, nil)
	if del.Code == http.StatusNoContent || del.Code == http.StatusOK {
		t.Fatalf("delete = %d: a governed object was destroyed with no Governance Engine to "+
			"authorise or audit it", del.Code)
	}
	if again := do(h, http.MethodGet, "/v1/artifacts/"+created.CfgID, nil, nil); again.Code != 200 {
		t.Errorf("the object did not survive a refused deletion: GET = %d", again.Code)
	}
}

func TestCreateConflictAndValidation(t *testing.T) {
	h, _ := newTestAPI(false)
	if rec := do(h, http.MethodPost, "/v1/artifacts", validCreate(), nil); rec.Code != 201 {
		t.Fatalf("first create = %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/v1/artifacts", validCreate(), nil); rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rec.Code)
	}
	bad := validCreate()
	bad.Role = "not-a-role"
	if rec := do(h, http.MethodPost, "/v1/artifacts", bad, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/artifacts/missing", nil, nil); rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestListAndFilter(t *testing.T) {
	h, _ := newTestAPI(false)
	a := validCreate()
	b := validCreate()
	b.Artifact = "Other-Doc.md"
	b.Version = "2.0.0"
	b.Role = "evidence"
	do(h, http.MethodPost, "/v1/artifacts", a, nil)
	do(h, http.MethodPost, "/v1/artifacts", b, nil)

	rec := do(h, http.MethodGet, "/v1/artifacts?limit=10", nil, nil)
	var page listResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 2 {
		t.Fatalf("list len = %d", len(page.Items))
	}

	rec = do(h, http.MethodGet, "/v1/artifacts?role=evidence", nil, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].Role != "evidence" {
		t.Fatalf("role filter failed: %+v", page.Items)
	}

	if rec := do(h, http.MethodGet, "/v1/artifacts?limit=-1", nil, nil); rec.Code != 400 {
		t.Errorf("expected 400 for bad limit, got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/artifacts?role=bogus", nil, nil); rec.Code != 400 {
		t.Errorf("expected 400 for bad role, got %d", rec.Code)
	}
}

func TestPatchOptimisticLocking(t *testing.T) {
	h, _ := newTestAPI(false)
	created := decodeCO(t, do(h, http.MethodPost, "/v1/artifacts", validCreate(), nil))

	// Optimistic locking is exercised with a NON-constitutional field: patch may
	// no longer mutate lifecycle (CP-1.3).
	rc := "event-driven"
	patch := patchRequest{ReviewCycle: &rc}

	// Missing If-Match => 428.
	if rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID, patch, nil); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("expected 428, got %d", rec.Code)
	}
	// Wrong If-Match => 412.
	if rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID, patch, map[string]string{"If-Match": `"99"`}); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("expected 412, got %d", rec.Code)
	}
	// Correct If-Match => 200 and row_version bumps.
	rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID, patch, map[string]string{"If-Match": `"1"`})
	if rec.Code != 200 {
		t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
	}
	updated := decodeCO(t, rec)
	if updated.RowVersion != 2 || updated.ReviewCycle != "event-driven" {
		t.Fatalf("unexpected update: %+v", updated)
	}
	// The dimensions are untouched by a patch.
	if updated.Lifecycle != "draft" || updated.RetentionClass != "working_material" ||
		updated.Authority != "non_normative" {
		t.Fatalf("patch altered a constitutional dimension: %+v", updated)
	}
	// Validate() now runs on the patch path: clearing retention_policy is a
	// field-level violation and must be refused rather than persisted.
	empty := ""
	if rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID,
		patchRequest{RetentionPolicy: &empty}, map[string]string{"If-Match": `"2"`}); rec.Code != 422 {
		t.Errorf("clearing retention_policy: expected 422, got %d", rec.Code)
	}
}

func TestBulkAndIdempotency(t *testing.T) {
	h, _ := newTestAPI(false)

	a := validCreate()
	b := validCreate()
	b.Artifact = "Bulk-2.md"
	bulk := bulkCreateRequest{Items: []createRequest{a, b}}
	if rec := do(h, http.MethodPost, "/v1/artifacts/bulk", bulk, nil); rec.Code != 201 {
		t.Fatalf("bulk = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(h, http.MethodPost, "/v1/artifacts/bulk", bulkCreateRequest{}, nil); rec.Code != 422 {
		t.Errorf("expected 422 for empty bulk, got %d", rec.Code)
	}

	// Idempotency: same key returns the first response and does not double-create.
	c := validCreate()
	c.Artifact = "Idem.md"
	hdr := map[string]string{"Idempotency-Key": "abc-123"}
	first := do(h, http.MethodPost, "/v1/artifacts", c, hdr)
	if first.Code != 201 {
		t.Fatalf("first idem create = %d", first.Code)
	}
	second := do(h, http.MethodPost, "/v1/artifacts", c, hdr)
	if second.Code != 201 || second.Body.String() != first.Body.String() {
		t.Errorf("idempotent replay mismatch: %d vs %d", first.Code, second.Code)
	}
}

func TestAuthAndRBAC(t *testing.T) {
	h, _ := newTestAPI(true)

	// No token => 401.
	if rec := do(h, http.MethodGet, "/v1/artifacts", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	// Reader cannot write => 403.
	readerHdr := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-reader"})}
	if rec := do(h, http.MethodPost, "/v1/artifacts", validCreate(), readerHdr); rec.Code != http.StatusForbidden {
		t.Errorf("reader write = %d, want 403", rec.Code)
	}
	// Reader can read => 200.
	if rec := do(h, http.MethodGet, "/v1/artifacts", nil, readerHdr); rec.Code != 200 {
		t.Errorf("reader read = %d, want 200", rec.Code)
	}
	// Admin can write => 201.
	adminHdr := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-admin"})}
	if rec := do(h, http.MethodPost, "/v1/artifacts", validCreate(), adminHdr); rec.Code != 201 {
		t.Errorf("admin write = %d, want 201", rec.Code)
	}
	// Garbage token => 401.
	if rec := do(h, http.MethodGet, "/v1/artifacts", nil, map[string]string{"Authorization": "Bearer garbage"}); rec.Code != 401 {
		t.Errorf("garbage token = %d, want 401", rec.Code)
	}
}

// CP-1.2 — Registry Create yields the INT-3 inception state and nothing else.
// Before this, a single POST could fabricate a ratified baseline artifact with
// Authority asserted by the client, no §8 operation, and no audit event.
func TestCreateRejectsNonInceptionState(t *testing.T) {
	h, _ := newTestAPI(false)

	cases := []struct {
		name      string
		lifecycle string
		retention string
	}{
		{"ratified lifecycle", "ratified", "working_material"},
		{"approved lifecycle", "approved", "working_material"},
		{"withdrawn lifecycle", "withdrawn", "working_material"},
		{"current_baseline retention", "draft", "current_baseline"},
		{"historical_record retention", "draft", "historical_record"},
		{"audit_record retention", "draft", "audit_record"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validCreate()
			req.Lifecycle, req.RetentionClass = tc.lifecycle, tc.retention
			rec := do(h, http.MethodPost, "/v1/artifacts", req, nil)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (INT-3)", rec.Code)
			}
		})
	}

	// The two inception lifecycles remain accepted.
	for _, lc := range []string{"draft", "in_progress"} {
		req := validCreate()
		req.Lifecycle = lc
		req.Artifact = "Inception-" + lc + ".md"
		if rec := do(h, http.MethodPost, "/v1/artifacts", req, nil); rec.Code != http.StatusCreated {
			t.Errorf("lifecycle %s: status = %d, want 201", lc, rec.Code)
		}
	}
}

// Authority is computed (§6). A client-supplied value must be ignored entirely
// rather than honoured — the field no longer exists on the request contract.
func TestCreateIgnoresClientSuppliedAuthority(t *testing.T) {
	h, _ := newTestAPI(false)
	body := map[string]any{
		"artifact": "Asserted.md", "version": "1.0.0", "role": "reference",
		"lifecycle": "draft", "retention_class": "working_material",
		"authority": "active", // must not be honoured
	}
	rec := do(h, http.MethodPost, "/v1/artifacts", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if got := decodeCO(t, rec); got.Authority != "non_normative" {
		t.Fatalf("authority = %q, want non_normative — a client asserted it", got.Authority)
	}
}

// Bulk create enforces the same constraint per item.
func TestBulkCreateRejectsNonInceptionState(t *testing.T) {
	h, _ := newTestAPI(false)
	ok := validCreate()
	bad := validCreate()
	bad.Artifact, bad.Lifecycle, bad.RetentionClass = "Bad.md", "ratified", "current_baseline"

	rec := do(h, http.MethodPost, "/v1/artifacts/bulk",
		map[string]any{"items": []createRequest{ok, bad}}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a non-inception item", rec.Code)
	}
}

// CP-1.3 — PATCH may no longer carry constitutional state. Before this, a single
// PATCH moved a draft artifact to ratified/current_baseline/active with no §8
// operation and no audit event (PM/CP-0.2 measured 0 audit rows).
func TestPatchRejectsConstitutionalDimensions(t *testing.T) {
	h, _ := newTestAPI(false)
	created := decodeCO(t, do(h, http.MethodPost, "/v1/artifacts", validCreate(), nil))
	hdr := map[string]string{"If-Match": `"1"`}

	// The fields no longer exist on the contract, so they are sent as raw JSON.
	for _, body := range []map[string]any{
		{"lifecycle": "ratified"},
		{"retention_class": "current_baseline"},
		{"authority": "active"},
		{"lifecycle": "ratified", "retention_class": "current_baseline", "authority": "active"},
	} {
		rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID, body, hdr)
		if rec.Code != http.StatusOK {
			t.Fatalf("body %v: status = %d, want 200 (unknown fields ignored)", body, rec.Code)
		}
		got := decodeCO(t, rec)
		if got.Lifecycle != "draft" || got.RetentionClass != "working_material" ||
			got.Authority != "non_normative" {
			t.Fatalf("body %v mutated a constitutional dimension: %+v", body, got)
		}
		hdr = map[string]string{"If-Match": `"` + itoa(got.RowVersion) + `"`}
	}
}

// The three §9.1 input keys are constitutional inputs carried in metadata
// (CP-0.1); patching them would change computed Authority and the Replacement
// Test verdict without a §8 operation.
func TestPatchRejectsConstitutionalMetadataKeys(t *testing.T) {
	h, _ := newTestAPI(false)
	created := decodeCO(t, do(h, http.MethodPost, "/v1/artifacts", validCreate(), nil))

	for _, key := range []string{"responsibilities", "citations", "coverage"} {
		rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID,
			patchRequest{Metadata: map[string]string{key: "x"}}, map[string]string{"If-Match": `"1"`})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("metadata key %q: status = %d, want 422", key, rec.Code)
		}
	}

	// Descriptive metadata remains writable.
	rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID,
		patchRequest{Metadata: map[string]string{"owner": "platform"}}, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("descriptive metadata: status = %d, want 200", rec.Code)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

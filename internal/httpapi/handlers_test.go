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
	if patch.Lifecycle != nil {
		c.Lifecycle = *patch.Lifecycle
	}
	if patch.RetentionClass != nil {
		c.RetentionClass = *patch.RetentionClass
	}
	if patch.Authority != nil {
		c.Authority = *patch.Authority
	}
	if patch.RatifiedBy != nil {
		c.RatifiedBy = *patch.RatifiedBy
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

func newTestAPI(authEnabled bool) (http.Handler, *fakeRepo) {
	repo := newFakeRepo()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: authEnabled, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo, newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
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

func validCreate() createRequest {
	return createRequest{
		Artifact: "OneOps-Test.md", Version: "1.0.0", Role: "governance",
		Lifecycle: "approved", RetentionClass: "current_baseline", Authority: "active",
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
	if created.CfgID == "" || created.Authority != "active" {
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

	del := do(h, http.MethodDelete, "/v1/artifacts/"+created.CfgID, nil, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", del.Code)
	}
	if again := do(h, http.MethodGet, "/v1/artifacts/"+created.CfgID, nil, nil); again.Code != 404 {
		t.Errorf("expected 404 after delete, got %d", again.Code)
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

	lc := "complete"
	patch := patchRequest{Lifecycle: &lc}

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
	if updated.RowVersion != 2 || updated.Lifecycle != "complete" {
		t.Fatalf("unexpected update: %+v", updated)
	}
	// Bad enum in patch => 422.
	blc := "bogus"
	if rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID, patchRequest{Lifecycle: &blc}, map[string]string{"If-Match": `"2"`}); rec.Code != 422 {
		t.Errorf("expected 422, got %d", rec.Code)
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

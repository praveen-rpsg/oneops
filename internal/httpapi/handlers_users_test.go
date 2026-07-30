package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

// ---- fake repository --------------------------------------------------------

type fakeUsers struct {
	byID     map[string]*domain.User
	failWith error
	// lastLimit records what List was asked for, so a test can prove the handler
	// forwarded the caller's paging rather than substituting its own.
	lastLimit int
	lastAfter string
	// setStatusCalls counts what reached the repository. The handler rejects an
	// undefined status before the round trip; without this counter the
	// repository's own validation returns the same 422 and the handler's check
	// is unobservable — the test passed with the check deleted.
	setStatusCalls int
}

func newFakeUsers(us ...*domain.User) *fakeUsers {
	f := &fakeUsers{byID: map[string]*domain.User{}}
	for _, u := range us {
		f.byID[u.UserID] = u
	}
	return f
}

func (f *fakeUsers) Create(_ context.Context, u *domain.User) (*domain.User, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	for _, e := range f.byID {
		if domain.NormalizeEmail(e.Email) == domain.NormalizeEmail(u.Email) {
			return nil, domain.ErrConflict
		}
	}
	cp := *u
	cp.RowVersion = 1
	cp.CreatedAt = time.Now().UTC()
	cp.UpdatedAt = cp.CreatedAt
	f.byID[cp.UserID] = &cp
	return &cp, nil
}

func (f *fakeUsers) Get(_ context.Context, id string) (*domain.User, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeUsers) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	want := domain.NormalizeEmail(email)
	for _, u := range f.byID {
		if domain.NormalizeEmail(u.Email) == want {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeUsers) List(_ context.Context, limit int, after string) ([]*domain.User, error) {
	f.lastLimit, f.lastAfter = limit, after
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := make([]*domain.User, 0, len(f.byID))
	for _, u := range f.byID {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeUsers) UpdateProfile(_ context.Context, id string, rv int64, name string) (*domain.User, error) {
	u, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if u.RowVersion != rv {
		return nil, domain.ErrVersionMismatch
	}
	cp := *u
	cp.DisplayName = name
	cp.RowVersion++
	f.byID[id] = &cp
	return &cp, nil
}

func (f *fakeUsers) SetStatus(_ context.Context, id string, rv int64, st domain.UserStatus) (*domain.User, error) {
	f.setStatusCalls++
	// Mirrors UserStore.SetStatus: an undefined status is a validation failure,
	// not a refused transition. A fake that skips this reports 409 where the
	// real store reports 422, and the handler test then pins the wrong contract.
	if !st.Valid() {
		return nil, domain.NewValidationError("status",
			"must be one of: invited, active, suspended, deactivated")
	}
	u, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if u.RowVersion != rv {
		return nil, domain.ErrVersionMismatch
	}
	if !u.Status.CanTransitionTo(st) {
		return nil, domain.NewTransitionError(u.Status, st)
	}
	cp := *u
	cp.Status = st
	cp.RowVersion++
	f.byID[id] = &cp
	return &cp, nil
}

var _ domain.UserRepository = (*fakeUsers)(nil)

// ---- harness ----------------------------------------------------------------

func newUserAPI(t *testing.T, repo domain.UserRepository) http.Handler {
	t.Helper()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if repo != nil {
		s.SetUsers(repo)
	}
	return s.Router()
}

// mintRoleToken issues a token for the named role. The user registry is a
// platform operation, so most of these tests need oneops-platform-admin —
// and one needs a tenant admin, to prove that is not enough.
func mintRoleToken(t *testing.T, role string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": "u", "iss": tIss, "aud": tAud,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"roles": []string{role},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(tSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func userReq(t *testing.T, h http.Handler, method, path, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+mintRoleToken(t, role))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const padmin = "oneops-platform-admin"

func decodeUser(t *testing.T, rec *httptest.ResponseRecorder) userDTO {
	t.Helper()
	var u userDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode user: %v (body %s)", err, rec.Body.String())
	}
	return u
}

// ---- authorization ----------------------------------------------------------

// The user registry administers a global table; it is a platform operation, not
// an act inside any one tenant's boundary. A tenant administrator holding
// PermAdmin must not reach it — the wildcard-permission defect ADR-AUTHZ-001
// records is exactly this shape.
func TestUsers_RequirePlatformAdmin(t *testing.T) {
	h := newUserAPI(t, newFakeUsers())

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/users", ""},
		{http.MethodPost, "/v1/admin/users", `{"email":"a@b.com"}`},
		{http.MethodGet, "/v1/admin/users/usr_1", ""},
		{http.MethodPatch, "/v1/admin/users/usr_1", `{"row_version":1,"status":"active"}`},
	} {
		rec := userReq(t, h, c.method, c.path, "oneops-admin", c.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a tenant admin: got %d, want 403 — a tenant administrator "+
				"must not administer the global user registry", c.method, c.path, rec.Code)
		}
	}
}

// ---- not wired --------------------------------------------------------------

func TestUsers_NotConfiguredReports501(t *testing.T) {
	h := newUserAPI(t, nil)
	rec := userReq(t, h, http.MethodGet, "/v1/admin/users", padmin, "")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("got %d, want 501 — an unconfigured registry must say so rather than "+
			"look like a routing mistake", rec.Code)
	}
}

// ---- create -----------------------------------------------------------------

func TestUsers_Create(t *testing.T) {
	h := newUserAPI(t, newFakeUsers())

	rec := userReq(t, h, http.MethodPost, "/v1/admin/users", padmin,
		`{"email":"  New.Person@Example.COM  ","display_name":"  New Person  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	u := decodeUser(t, rec)
	if u.Email != "new.person@example.com" {
		t.Errorf("email = %q, want it normalised by the aggregate", u.Email)
	}
	if u.DisplayName != "New Person" {
		t.Errorf("display name = %q, want it trimmed", u.DisplayName)
	}
	if u.Status != string(domain.UserInvited) {
		t.Errorf("status = %q, want invited", u.Status)
	}
	if u.UserID == "" {
		t.Error("no user_id was returned")
	}
	if u.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", u.RowVersion)
	}
}

// A client must not be able to choose the identifier. The request DTO has no
// such field, so a supplied one is ignored by the decoder — asserted rather
// than assumed, because the property is what keeps a global key space safe
// (Trust Register entry 1).
func TestUsers_CreateIgnoresClientSuppliedIdentity(t *testing.T) {
	h := newUserAPI(t, newFakeUsers())
	rec := userReq(t, h, http.MethodPost, "/v1/admin/users", padmin,
		`{"user_id":"usr_chosen_by_client","id":"usr_also_chosen","email":"x@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201", rec.Code)
	}
	if u := decodeUser(t, rec); u.UserID == "usr_chosen_by_client" || u.UserID == "usr_also_chosen" {
		t.Errorf("the client chose the user id (%q); identifiers must be minted server-side", u.UserID)
	}
}

func TestUsers_CreateValidation(t *testing.T) {
	h := newUserAPI(t, newFakeUsers())

	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"missing email", `{"display_name":"No Address"}`, http.StatusUnprocessableEntity},
		{"invalid email", `{"email":"not-an-address"}`, http.StatusUnprocessableEntity},
		{"blank email", `{"email":"   "}`, http.StatusUnprocessableEntity},
		{"display name too long", `{"email":"a@b.com","display_name":"` +
			strings.Repeat("x", domain.MaxDisplayNameLength+1) + `"}`, http.StatusUnprocessableEntity},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := userReq(t, h, http.MethodPost, "/v1/admin/users", padmin, c.body)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
				t.Errorf("content type %q, want an RFC 7807 problem document", ct)
			}
		})
	}
}

func TestUsers_CreateDuplicateIsConflict(t *testing.T) {
	existing, err := domain.NewUser("taken@example.com", "Taken")
	if err != nil {
		t.Fatal(err)
	}
	h := newUserAPI(t, newFakeUsers(existing))

	rec := userReq(t, h, http.MethodPost, "/v1/admin/users", padmin, `{"email":"TAKEN@Example.com"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d, want 409 — a differently cased duplicate is the same person", rec.Code)
	}
}

// ---- get --------------------------------------------------------------------

func TestUsers_Get(t *testing.T) {
	u, err := domain.NewUser("get@example.com", "Get Me")
	if err != nil {
		t.Fatal(err)
	}
	u.RowVersion = 1
	h := newUserAPI(t, newFakeUsers(u))

	rec := userReq(t, h, http.MethodGet, "/v1/admin/users/"+u.UserID, padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if got := decodeUser(t, rec); got.UserID != u.UserID {
		t.Errorf("returned %q, want %q", got.UserID, u.UserID)
	}

	if rec := userReq(t, h, http.MethodGet, "/v1/admin/users/usr_missing", padmin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: got %d, want 404", rec.Code)
	}
}

// ---- list and lookup --------------------------------------------------------

func TestUsers_ListForwardsPaging(t *testing.T) {
	f := newFakeUsers()
	h := newUserAPI(t, f)

	rec := userReq(t, h, http.MethodGet, "/v1/admin/users?limit=7&after=usr_cursor", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if f.lastLimit != 7 || f.lastAfter != "usr_cursor" {
		t.Errorf("handler forwarded limit=%d after=%q, want 7 / usr_cursor — a handler that "+
			"substitutes its own paging makes the cursor meaningless", f.lastLimit, f.lastAfter)
	}
}

func TestUsers_ListRejectsBadLimit(t *testing.T) {
	h := newUserAPI(t, newFakeUsers())
	for _, q := range []string{"limit=0", "limit=-3", "limit=abc"} {
		rec := userReq(t, h, http.MethodGet, "/v1/admin/users?"+q, padmin, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422", q, rec.Code)
		}
	}
}

func TestUsers_LookupByEmail(t *testing.T) {
	u, err := domain.NewUser("findme@example.com", "Find Me")
	if err != nil {
		t.Fatal(err)
	}
	h := newUserAPI(t, newFakeUsers(u))

	rec := userReq(t, h, http.MethodGet, "/v1/admin/users?email=FindMe@Example.COM", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var out struct {
		Items []userDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].UserID != u.UserID {
		t.Fatalf("got %d items, want the one matching user", len(out.Items))
	}

	// A filter that matches nothing is an empty page. The collection exists; a
	// 404 would say the endpoint does not.
	rec = userReq(t, h, http.MethodGet, "/v1/admin/users?email=nobody@example.com", padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unmatched filter: got %d, want 200", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 0 {
		t.Errorf("unmatched filter returned %d items, want an empty page", len(out.Items))
	}
}

// ---- patch ------------------------------------------------------------------

func TestUsers_PatchDisplayName(t *testing.T) {
	u, err := domain.NewUser("patch@example.com", "Before")
	if err != nil {
		t.Fatal(err)
	}
	u.RowVersion = 1
	h := newUserAPI(t, newFakeUsers(u))

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/users/"+u.UserID, padmin,
		`{"row_version":1,"display_name":"  After  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeUser(t, rec)
	if got.DisplayName != "After" {
		t.Errorf("display name = %q, want it trimmed to After", got.DisplayName)
	}
	if got.RowVersion != 2 {
		t.Errorf("row_version = %d, want 2", got.RowVersion)
	}
}

func TestUsers_PatchStatus(t *testing.T) {
	u, err := domain.NewUser("status@example.com", "Status")
	if err != nil {
		t.Fatal(err)
	}
	u.RowVersion = 1
	h := newUserAPI(t, newFakeUsers(u))

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/users/"+u.UserID, padmin,
		`{"row_version":1,"status":"active"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeUser(t, rec); got.Status != string(domain.UserActive) {
		t.Errorf("status = %q, want active", got.Status)
	}
}

// A refused lifecycle move is a 409, not a 500. Without an explicit case in
// mapError it falls to the default branch and is logged as an unhandled server
// fault — a normal client outcome reported as a platform failure.
func TestUsers_PatchRefusedTransitionIsConflict(t *testing.T) {
	u, err := domain.NewUser("terminal@example.com", "Terminal")
	if err != nil {
		t.Fatal(err)
	}
	u.Status = domain.UserDeactivated
	u.RowVersion = 1
	h := newUserAPI(t, newFakeUsers(u))

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/users/"+u.UserID, padmin,
		`{"row_version":1,"status":"active"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 — deactivation is terminal, and the refusal is a "+
			"conflict with the record's state, not a server error (body %s)",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "deactivated") {
		t.Errorf("body %s should name the refused move", rec.Body.String())
	}
}

func TestUsers_PatchValidation(t *testing.T) {
	u, err := domain.NewUser("validate@example.com", "Validate")
	if err != nil {
		t.Fatal(err)
	}
	u.RowVersion = 1
	h := newUserAPI(t, newFakeUsers(u))
	path := "/v1/admin/users/" + u.UserID

	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"no row_version", `{"display_name":"x"}`, http.StatusUnprocessableEntity},
		{"zero row_version", `{"row_version":0,"display_name":"x"}`, http.StatusUnprocessableEntity},
		{"neither field", `{"row_version":1}`, http.StatusUnprocessableEntity},
		{"both fields", `{"row_version":1,"display_name":"x","status":"active"}`, http.StatusUnprocessableEntity},
		{"unknown status", `{"row_version":1,"status":"banned"}`, http.StatusUnprocessableEntity},
		{"stale row_version", `{"row_version":99,"status":"active"}`, http.StatusConflict},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := userReq(t, h, http.MethodPatch, path, padmin, c.body)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}

	if rec := userReq(t, h, http.MethodPatch, "/v1/admin/users/usr_missing", padmin,
		`{"row_version":1,"status":"active"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user: got %d, want 404", rec.Code)
	}
}

// The 422 above does not prove the handler checked the status: the repository
// validates too and answers 422 for the same input, so the assertion held with
// the handler's check deleted. What the transport rule asserts is that an
// undefined status never reaches the repository at all.
func TestUsers_PatchUndefinedStatusNeverReachesTheRepository(t *testing.T) {
	u, err := domain.NewUser("prefilter@example.com", "Prefilter")
	if err != nil {
		t.Fatal(err)
	}
	u.RowVersion = 1
	repo := newFakeUsers(u)
	h := newUserAPI(t, repo)
	path := "/v1/admin/users/" + u.UserID

	for _, st := range []string{"banned", "ACTIVE", "", "archived", "unknown"} {
		rec := userReq(t, h, http.MethodPatch, path, padmin,
			`{"row_version":1,"status":"`+st+`"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status=%q: got %d, want 422 (body %s)", st, rec.Code, rec.Body.String())
		}
	}
	if repo.setStatusCalls != 0 {
		t.Errorf("the repository was called %d times for undefined statuses; the handler must "+
			"reject them before the round trip, not rely on the store to repeat the check",
			repo.setStatusCalls)
	}
}

// An empty display_name is a clear, not an omission. The pointer field is what
// makes the two distinguishable, and this asserts the distinction survives.
func TestUsers_PatchEmptyDisplayNameIsAClear(t *testing.T) {
	u, err := domain.NewUser("clear@example.com", "Has A Name")
	if err != nil {
		t.Fatal(err)
	}
	u.RowVersion = 1
	h := newUserAPI(t, newFakeUsers(u))

	rec := userReq(t, h, http.MethodPatch, "/v1/admin/users/"+u.UserID, padmin,
		`{"row_version":1,"display_name":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeUser(t, rec); got.DisplayName != "" {
		t.Errorf("display name = %q, want it cleared — an explicit empty string is a "+
			"request to clear, and treating it as absent silently ignores the caller",
			got.DisplayName)
	}
}

// ---- serialization ----------------------------------------------------------

// The wire shape is the contract. Field names are asserted literally because a
// rename is invisible to a round-trip test that decodes into the same struct.
func TestUsers_ResponseShape(t *testing.T) {
	u, err := domain.NewUser("shape@example.com", "Shape")
	if err != nil {
		t.Fatal(err)
	}
	u.RowVersion = 1
	h := newUserAPI(t, newFakeUsers(u))

	rec := userReq(t, h, http.MethodGet, "/v1/admin/users/"+u.UserID, padmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{
		"user_id", "email", "display_name", "status", "row_version", "created_at", "updated_at",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("response is missing %q; the OpenAPI schema requires it", field)
		}
	}
	if len(raw) != 7 {
		t.Errorf("response has %d fields, want exactly 7 — an undeclared field is not in "+
			"the published contract: %v", len(raw), raw)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type %q, want application/json", ct)
	}
}

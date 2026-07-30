package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The recorder keeps the body as raw bytes as well as a decoded map: the
// difference between "field absent" and "field present and empty" is only
// visible in the literal JSON, and that distinction is what the pointer fields
// on PatchUserInput exist to express.
//
// recorder captures what the client actually put on the wire, which is the only
// thing these tests can meaningfully assert about request generation.
type recorder struct {
	method string
	path   string
	// escapedPath is the wire form. r.URL.Path is already decoded, so asserting
	// on it cannot tell an escaped separator from a real one — the exact
	// distinction path escaping exists to make.
	escapedPath string
	rawQuery    string
	query       url.Values
	body        map[string]any
	bodyRaw     string
	auth        string
}

// identityServer returns a client and a recorder, answering every identity
// route with the fixture the handler supplies.
func identityServer(t *testing.T, respond http.HandlerFunc) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path = r.Method, r.URL.Path
		rec.escapedPath = r.URL.EscapedPath()
		rec.rawQuery = r.URL.RawQuery
		rec.query = r.URL.Query()
		rec.auth = r.Header.Get("Authorization")
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			rec.bodyRaw = string(raw)
			rec.body = nil
			_ = json.Unmarshal(raw, &rec.body)
		}
		w.Header().Set("Content-Type", "application/json")
		respond(w, r)
	}))
	return c, rec
}

func ptr[T any](v T) *T { return &v }

// ---- serialization ----------------------------------------------------------

// Every documented field must survive the round trip. A missing or misspelled
// tag is silent: the field simply stays zero and the caller sees a blank value
// rather than an error.
func TestUser_Deserialization(t *testing.T) {
	const payload = `{
		"user_id":"usr_1","email":"a@b.com","display_name":"A B","status":"active",
		"row_version":7,"created_at":"2026-08-01T12:00:00Z","updated_at":"2026-08-02T09:30:00Z"}`
	var u User
	if err := json.Unmarshal([]byte(payload), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if u.UserID != "usr_1" || u.Email != "a@b.com" || u.DisplayName != "A B" || u.Status != "active" {
		t.Errorf("decoded %+v", u)
	}
	if u.RowVersion != 7 {
		t.Errorf("row_version %d, want 7", u.RowVersion)
	}
	if !u.CreatedAt.Equal(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("created_at %v", u.CreatedAt)
	}
	if !u.UpdatedAt.Equal(time.Date(2026, 8, 2, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("updated_at %v", u.UpdatedAt)
	}
}

func TestOrganization_Deserialization(t *testing.T) {
	const payload = `{
		"org_id":"org_1","tenant_id":"tn_1","slug":"acme","name":"Acme","status":"suspended",
		"row_version":3,"created_at":"2026-08-01T12:00:00Z","updated_at":"2026-08-01T12:00:00Z"}`
	var o Organization
	if err := json.Unmarshal([]byte(payload), &o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if o.OrgID != "org_1" || o.TenantID != "tn_1" || o.Slug != "acme" ||
		o.Name != "Acme" || o.Status != "suspended" || o.RowVersion != 3 {
		t.Errorf("decoded %+v", o)
	}
	if o.CreatedAt.IsZero() {
		t.Error("created_at did not decode")
	}
}

// ---- request generation -----------------------------------------------------

func TestUsers_CreateSendsTheDocumentedBody(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(User{UserID: "usr_1", Email: "a@b.com", Status: "invited", RowVersion: 1})
	})

	got, err := c.Users().Create(context.Background(), CreateUserInput{Email: "a@b.com", DisplayName: "A B"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/admin/users" {
		t.Errorf("sent %s %s, want POST /v1/admin/users", rec.method, rec.path)
	}
	if rec.body["email"] != "a@b.com" || rec.body["display_name"] != "A B" {
		t.Errorf("body %v", rec.body)
	}
	// The identifier is minted server-side; sending one would be a request the
	// server must reject.
	if _, ok := rec.body["user_id"]; ok {
		t.Error("the request carried a user_id; the identifier is minted server-side")
	}
	if got.UserID != "usr_1" || got.Status != "invited" {
		t.Errorf("decoded %+v", got)
	}
	if rec.auth != "Bearer tok" {
		t.Errorf("Authorization %q; the identity surface is platform-admin only", rec.auth)
	}
}

func TestOrganizations_CreateSendsTheDocumentedBody(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Organization{OrgID: "org_1", TenantID: "tn_1", Slug: "acme"})
	})

	got, err := c.Organizations().Create(context.Background(),
		CreateOrganizationInput{Slug: "acme", Name: "Acme"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/admin/organizations" {
		t.Errorf("sent %s %s", rec.method, rec.path)
	}
	if rec.body["slug"] != "acme" || rec.body["name"] != "Acme" {
		t.Errorf("body %v", rec.body)
	}
	for _, minted := range []string{"org_id", "tenant_id"} {
		if _, ok := rec.body[minted]; ok {
			t.Errorf("the request carried %s; both identifiers are minted server-side", minted)
		}
	}
	if got.TenantID != "tn_1" {
		t.Errorf("tenant_id %q did not decode", got.TenantID)
	}
}

// An identifier goes in a path segment and must be escaped. Without it a slash
// in an id silently addresses a different route, and the caller is told "not
// found" for a resource that exists.
func TestIdentity_PathIdentifiersAreEscaped(t *testing.T) {
	for _, c := range []struct{ name, id, wantPath string }{
		{"plain", "usr_1", "/v1/admin/users/usr_1"},
		{"slash", "usr/1", "/v1/admin/users/usr%2F1"},
		{"space", "usr 1", "/v1/admin/users/usr%201"},
		{"percent", "usr%2F", "/v1/admin/users/usr%252F"},
		{"traversal", "../tenants", "/v1/admin/users/..%2Ftenants"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(User{UserID: "usr_1"})
			})
			if _, err := cl.Users().Get(context.Background(), c.id); err != nil {
				t.Fatalf("Get: %v", err)
			}
			if rec.escapedPath != c.wantPath {
				t.Errorf("id %q reached the server as %q, want %q — an unescaped separator "+
					"addresses a different route and the caller is told the resource does "+
					"not exist", c.id, rec.escapedPath, c.wantPath)
			}
		})
	}
}

// ---- pagination and filtering ----------------------------------------------

// Unset filters must not appear at all. An empty `email=` is a filter for the
// empty address, which is a different request from "no filter".
func TestUsers_ListOmitsUnsetParameters(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []User{}})
	})

	if _, err := c.Users().List(context.Background(), ListUsersInput{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.rawQuery != "" {
		t.Errorf("the zero input produced query %q, want none", rec.rawQuery)
	}
}

func TestUsers_ListSendsFiltersAndPaging(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []User{{UserID: "usr_1"}}})
	})

	items, err := c.Users().List(context.Background(),
		ListUsersInput{Email: "a@b.com", Limit: 25, After: "usr_0"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := rec.query.Get("email"); got != "a@b.com" {
		t.Errorf("email arrived as %q", got)
	}
	if got := rec.query.Get("limit"); got != "25" {
		t.Errorf("limit arrived as %q", got)
	}
	if got := rec.query.Get("after"); got != "usr_0" {
		t.Errorf("after arrived as %q", got)
	}
}

// The case this surface exists for. `+` is a space in a decoded query string,
// so a hand-assembled query looks up a different address — and `a+tag@b.com` is
// an ordinary address, not an edge case.
func TestUsers_ListEncodesAddressesThatNeedEscaping(t *testing.T) {
	for _, email := range []string{
		"a+tag@b.com",
		"a b@example.com",
		"a&b=c@example.com",
		"a%40b@example.com",
		"ünïcode@example.com",
	} {
		t.Run(email, func(t *testing.T) {
			c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []User{}})
			})
			if _, err := c.Users().List(context.Background(), ListUsersInput{Email: email}); err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := rec.query.Get("email"); got != email {
				t.Errorf("address %q arrived as %q (raw query %q) — the filter would resolve "+
					"the wrong account", email, got, rec.rawQuery)
			}
		})
	}
}

// A non-positive limit means "unset", so the server applies its own default.
// Sending limit=0 or limit=-1 is a validation failure the caller did not ask for.
func TestUsers_ListTreatsNonPositiveLimitAsUnset(t *testing.T) {
	for _, limit := range []int{0, -1, -100} {
		c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []User{}})
		})
		if _, err := c.Users().List(context.Background(), ListUsersInput{Limit: limit}); err != nil {
			t.Fatalf("List: %v", err)
		}
		if rec.query.Has("limit") {
			t.Errorf("limit=%d was sent as %q; a non-positive page size must be omitted",
				limit, rec.query.Get("limit"))
		}
	}
}

func TestOrganizations_ListSendsSlugAndPaging(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []Organization{{OrgID: "org_1"}}})
	})

	items, err := c.Organizations().List(context.Background(),
		ListOrganizationsInput{Slug: "acme", Limit: 10, After: "org_0"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].OrgID != "org_1" {
		t.Fatalf("items %+v", items)
	}
	if rec.query.Get("slug") != "acme" || rec.query.Get("limit") != "10" || rec.query.Get("after") != "org_0" {
		t.Errorf("query %q", rec.rawQuery)
	}
	if rec.path != "/v1/admin/organizations" {
		t.Errorf("path %q", rec.path)
	}
}

// A filter that matches nothing is an empty page, not an error. The server says
// so deliberately, and the SDK must not turn it into a failure.
func TestIdentity_EmptyPageIsNotAnError(t *testing.T) {
	c, _ := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []User{}})
	})
	users, err := c.Users().List(context.Background(), ListUsersInput{Email: "nobody@example.com"})
	if err != nil {
		t.Fatalf("empty page returned an error: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("got %d users, want 0", len(users))
	}
}

// ---- patch semantics --------------------------------------------------------

// The pointer fields exist to distinguish absent from empty. An unset field must
// not reach the wire at all, or every rename would also be a status change.
func TestUsers_PatchOmitsUnsetFields(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(User{UserID: "usr_1", RowVersion: 2})
	})

	if _, err := c.Users().Patch(context.Background(), "usr_1",
		PatchUserInput{RowVersion: 1, Status: ptr("active")}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if rec.method != http.MethodPatch || rec.path != "/v1/admin/users/usr_1" {
		t.Errorf("sent %s %s", rec.method, rec.path)
	}
	if _, ok := rec.body["display_name"]; ok {
		t.Error("display_name was sent although it was not set; the server would read the " +
			"request as changing both fields and refuse it")
	}
	if rec.body["status"] != "active" {
		t.Errorf("status %v", rec.body["status"])
	}
	if rec.body["row_version"] != float64(1) {
		t.Errorf("row_version %v, want 1", rec.body["row_version"])
	}
}

// An empty display name is a clear, not an omission — the distinction the
// pointer exists for. It must appear on the wire as an empty string.
func TestUsers_PatchSendsAnEmptyDisplayNameAsAClear(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(User{UserID: "usr_1"})
	})

	if _, err := c.Users().Patch(context.Background(), "usr_1",
		PatchUserInput{RowVersion: 4, DisplayName: ptr("")}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !strings.Contains(rec.bodyRaw, `"display_name":""`) {
		t.Errorf("body %s does not carry an explicit empty display_name; clearing a name and "+
			"not touching it would be indistinguishable", rec.bodyRaw)
	}
	if _, ok := rec.body["status"]; ok {
		t.Error("status was sent although it was not set")
	}
}

// row_version must always reach the server, including when it is zero. Omitting
// it turns "I did not supply a guard" into "I supplied nothing", and the server
// cannot tell the caller which mistake they made.
func TestUsers_PatchAlwaysSendsRowVersion(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "validation failed"})
	})

	_, _ = c.Users().Patch(context.Background(), "usr_1", PatchUserInput{DisplayName: ptr("X")})
	if !strings.Contains(rec.bodyRaw, `"row_version":0`) {
		t.Errorf("body %s omitted row_version; the guard must be visible to the server even "+
			"when it is zero", rec.bodyRaw)
	}
}

func TestOrganizations_PatchSendsStatusAndRowVersion(t *testing.T) {
	c, rec := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Organization{OrgID: "org_1", Status: "suspended", RowVersion: 2})
	})

	got, err := c.Organizations().Patch(context.Background(), "org_1",
		PatchOrganizationInput{RowVersion: 1, Status: "suspended"})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if rec.method != http.MethodPatch || rec.path != "/v1/admin/organizations/org_1" {
		t.Errorf("sent %s %s", rec.method, rec.path)
	}
	if rec.body["status"] != "suspended" || rec.body["row_version"] != float64(1) {
		t.Errorf("body %v", rec.body)
	}
	if got.Status != "suspended" || got.RowVersion != 2 {
		t.Errorf("decoded %+v", got)
	}
}

// ---- error handling ---------------------------------------------------------

// The classification helpers must work on identity responses, because the
// distinctions are what a caller acts on: 409 means re-read and retry, 422
// means the request itself was wrong, 403 means this credential never will.
func TestIdentity_ErrorsAreClassified(t *testing.T) {
	for _, c := range []struct {
		status int
		title  string
		is     func(error) bool
		name   string
	}{
		{http.StatusNotFound, "not found", IsNotFound, "404 -> IsNotFound"},
		{http.StatusConflict, "conflict", IsConflict, "409 -> IsConflict"},
		{http.StatusUnprocessableEntity, "validation failed", IsValidation, "422 -> IsValidation"},
		{http.StatusForbidden, "forbidden", IsForbidden, "403 -> IsForbidden"},
		{http.StatusUnauthorized, "unauthorized", IsUnauthorized, "401 -> IsUnauthorized"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl, _ := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"title": c.title, "detail": "d", "instance": "req-1",
				})
			})
			_, err := cl.Users().Get(context.Background(), "usr_1")
			if err == nil {
				t.Fatalf("status %d returned no error", c.status)
			}
			if !c.is(err) {
				t.Errorf("status %d was not classified: %v", c.status, err)
			}
			var api *APIError
			if !errors.As(err, &api) {
				t.Fatalf("error is not an *APIError: %v", err)
			}
			if api.Status != c.status || api.Title != c.title {
				t.Errorf("decoded %+v", api)
			}
		})
	}
}

// A failed call must return no value alongside its error. A caller that checks
// the error second would otherwise read a zero-valued user as real.
func TestIdentity_FailedCallsReturnNoValue(t *testing.T) {
	c, _ := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "forbidden"})
	})
	ctx := context.Background()

	if u, err := c.Users().Get(ctx, "usr_1"); err == nil || u != nil {
		t.Errorf("Get returned (%v, %v)", u, err)
	}
	if u, err := c.Users().Create(ctx, CreateUserInput{Email: "a@b.com"}); err == nil || u != nil {
		t.Errorf("Create returned (%v, %v)", u, err)
	}
	if u, err := c.Users().Patch(ctx, "usr_1", PatchUserInput{RowVersion: 1}); err == nil || u != nil {
		t.Errorf("Patch returned (%v, %v)", u, err)
	}
	if l, err := c.Users().List(ctx, ListUsersInput{}); err == nil || l != nil {
		t.Errorf("List returned (%v, %v)", l, err)
	}
	if o, err := c.Organizations().Get(ctx, "org_1"); err == nil || o != nil {
		t.Errorf("Get returned (%v, %v)", o, err)
	}
	if o, err := c.Organizations().Create(ctx, CreateOrganizationInput{Slug: "a", Name: "A"}); err == nil || o != nil {
		t.Errorf("Create returned (%v, %v)", o, err)
	}
	if o, err := c.Organizations().Patch(ctx, "org_1", PatchOrganizationInput{RowVersion: 1, Status: "active"}); err == nil || o != nil {
		t.Errorf("Patch returned (%v, %v)", o, err)
	}
	if l, err := c.Organizations().List(ctx, ListOrganizationsInput{}); err == nil || l != nil {
		t.Errorf("List returned (%v, %v)", l, err)
	}
}

// A 501 is what the platform returns when the registry is not wired. It must
// surface as an error rather than an empty result, so an operator sees the
// deployment gap instead of concluding there are no users.
func TestIdentity_UnconfiguredRegistryIsAnError(t *testing.T) {
	c, _ := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"title": "not implemented", "detail": "user registry is not configured",
		})
	})
	users, err := c.Users().List(context.Background(), ListUsersInput{})
	if err == nil {
		t.Fatal("a 501 returned no error; an unwired registry would look like an empty platform")
	}
	if users != nil {
		t.Errorf("got %v users alongside the error", users)
	}
}

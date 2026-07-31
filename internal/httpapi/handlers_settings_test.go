package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeSettings struct {
	getAlls, upserts int
	lastSetting      *domain.Setting
	overrides        []*domain.Setting
	upsertResult     *domain.Setting
	upsertErr        error
}

func (f *fakeSettings) GetAll(context.Context) ([]*domain.Setting, error) {
	f.getAlls++
	return f.overrides, nil
}

func (f *fakeSettings) Upsert(_ context.Context, s *domain.Setting) (*domain.Setting, error) {
	f.upserts++
	f.lastSetting = s
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	if f.upsertResult != nil {
		return f.upsertResult, nil
	}
	out := *s
	out.RowVersion = s.RowVersion + 1
	return &out, nil
}

func (f *fakeSettings) touched() int { return f.getAlls + f.upserts }

// Setting is tenant-owned, so the authorization tier is tenant
// administration — not platform administration, exactly as team's is.
//
// Every refusal asserts the STORE WAS NEVER REACHED, not merely the status
// code: authorization that runs after the write is not authorization.
func TestSettings_Authorization(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/settings", ""},
		{http.MethodPut, "/v1/admin/settings/default_page_size", `{"value":100,"row_version":0}`},
	}
	cases := []struct {
		name  string
		token func(t *testing.T) string
		want  int
	}{
		{"no token", func(*testing.T) string { return "" }, http.StatusUnauthorized},
		{"read-only principal", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-reader"})
		}, http.StatusForbidden},
	}

	for _, tc := range cases {
		for _, rt := range routes {
			t.Run(tc.name+" "+rt.method+" "+rt.path, func(t *testing.T) {
				srv, _ := newTestServer(true)
				srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
				spy := &fakeSettings{}
				srv.SetSettings(spy)

				body := strings.NewReader(rt.body)
				req := httptest.NewRequest(rt.method, rt.path, body)
				if tok := tc.token(t); tok != "" {
					req.Header.Set("Authorization", "Bearer "+tok)
				}
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)

				if rec.Code != tc.want {
					t.Errorf("%s %s = %d, want %d", rt.method, rt.path, rec.Code, tc.want)
				}
				if spy.touched() != 0 {
					t.Errorf("%s %s: the store was reached despite refusal — authorization must "+
						"refuse before any setting is read or written", rt.method, rt.path)
				}
			})
		}
	}
}

// The tenant administrator succeeds, so the refusals above are about
// authorization and not about a route that never works.
func TestSettings_TenantAdminIsAdmitted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeSettings{}
	srv.SetSettings(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/settings", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("tenant administrator = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.getAlls != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.getAlls)
	}
}

// GET returns every defined setting, resolved to its default when the tenant
// has no override — the "effective settings" behaviour the story asks for.
func TestSettings_ListReturnsEffectiveDefaultsWhenNoOverrides(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	srv.SetSettings(&fakeSettings{})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/settings", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Key        string `json:"key"`
			Value      any    `json:"value"`
			Default    any    `json:"default"`
			Overridden bool   `json:"overridden"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != len(domain.SettingDefinitions) {
		t.Fatalf("returned %d settings, want %d (one per definition)", len(body.Items), len(domain.SettingDefinitions))
	}
	for _, item := range body.Items {
		if item.Overridden {
			t.Errorf("%s: reported overridden with no stored override", item.Key)
		}
	}
}

// GET overlays a stored override onto the effective view, with an int
// setting rendering as a JSON number, not a string.
func TestSettings_ListOverlaysStoredOverride(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	srv.SetSettings(&fakeSettings{
		overrides: []*domain.Setting{{TenantID: "t-acme", Key: "default_page_size", Value: "250", RowVersion: 2}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/settings", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Key        string `json:"key"`
			Value      any    `json:"value"`
			Overridden bool   `json:"overridden"`
			RowVersion int64  `json:"row_version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var found bool
	for _, item := range body.Items {
		if item.Key != "default_page_size" {
			continue
		}
		found = true
		if !item.Overridden {
			t.Error("expected overridden = true")
		}
		// Decoded through interface{}, a JSON number arrives as float64: this
		// also proves the value rendered as a number, not the quoted string it
		// is stored as internally.
		if v, ok := item.Value.(float64); !ok || v != 250 {
			t.Errorf("value = %#v (%T), want the number 250", item.Value, item.Value)
		}
		if item.RowVersion != 2 {
			t.Errorf("row_version = %d, want 2", item.RowVersion)
		}
	}
	if !found {
		t.Fatal("default_page_size not present in the response")
	}
}

// PUT to an unknown key is rejected before the store is ever reached.
func TestSettings_PutUnknownKeyIsRejected(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeSettings{}
	srv.SetSettings(spy)

	req := httptest.NewRequest(http.MethodPut, "/v1/admin/settings/not_a_real_setting",
		strings.NewReader(`{"value":"anything","row_version":0}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.touched() != 0 {
		t.Error("the store was reached for an unknown key")
	}
}

// A value that fails its definition's validation is rejected the same way —
// out of range, wrong type, or not an allowed enum value.
func TestSettings_PutInvalidValueIsRejected(t *testing.T) {
	for _, c := range []struct {
		name, key, body string
	}{
		{"out of range int", "default_page_size", `{"value":9999,"row_version":0}`},
		{"wrong JSON type for int", "default_page_size", `{"value":"fifty","row_version":0}`},
		{"unknown enum value", "notification_default_channel", `{"value":"carrier_pigeon","row_version":0}`},
		{"wrong JSON type for bool", "webhooks_enabled_by_default", `{"value":"true","row_version":0}`},
		{"empty string setting", "notification_from_name", `{"value":"","row_version":0}`},
		{"negative row_version", "default_page_size", `{"value":100,"row_version":-1}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeSettings{}
			srv.SetSettings(spy)

			req := httptest.NewRequest(http.MethodPut, "/v1/admin/settings/"+c.key, strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if spy.touched() != 0 {
				t.Error("the store was reached for an invalid value")
			}
		})
	}
}

// A valid PUT reaches the store with the tenant resolved from the bound
// connection, never from anything the caller could supply.
func TestSettings_PutValidValueReachesStoreWithResolvedTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeSettings{}
	srv.SetSettings(spy)

	req := httptest.NewRequest(http.MethodPut, "/v1/admin/settings/default_page_size",
		strings.NewReader(`{"value":100,"row_version":0}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.upserts != 1 {
		t.Fatalf("store reached %d time(s), want 1", spy.upserts)
	}
	if spy.lastSetting.TenantID != "t-acme" {
		t.Errorf("setting was written to tenant %q, want the caller's resolved tenant %q",
			spy.lastSetting.TenantID, "t-acme")
	}
	if spy.lastSetting.Value != "100" {
		t.Errorf("stored value = %q, want %q", spy.lastSetting.Value, "100")
	}
}

// An optimistic-lock conflict from the store surfaces as 409, the same shape
// patchTeam uses for its own version mismatch.
func TestSettings_PutVersionConflictReturns409(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	srv.SetSettings(&fakeSettings{upsertErr: domain.ErrVersionMismatch})

	req := httptest.NewRequest(http.MethodPut, "/v1/admin/settings/default_page_size",
		strings.NewReader(`{"value":100,"row_version":5}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestSettings_PutMalformedBodyIsRejected(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeSettings{}
	srv.SetSettings(spy)

	req := httptest.NewRequest(http.MethodPut, "/v1/admin/settings/default_page_size", strings.NewReader(`{`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if spy.touched() != 0 {
		t.Error("the store was reached on malformed input")
	}
}

// 501 rather than 404 when the repository is unwired, so an operator sees a
// deliberate configuration gap rather than what looks like a routing mistake.
func TestSettings_NotConfiguredReports501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/settings", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

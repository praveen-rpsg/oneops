package domain

import (
	"context"
	"testing"
)

func validTenant() *Tenant {
	return &Tenant{
		TenantID: "01J0000000000000000000000", Slug: "acme-corp",
		Name: "Acme Corp", Status: TenantActive,
	}
}

func TestTenantValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Tenant)
		field   string
		wantErr bool
	}{
		{name: "valid", mutate: func(*Tenant) {}},
		{name: "empty id", mutate: func(x *Tenant) { x.TenantID = "" }, field: "tenant_id", wantErr: true},
		{name: "blank id", mutate: func(x *Tenant) { x.TenantID = "   " }, field: "tenant_id", wantErr: true},
		{name: "empty name", mutate: func(x *Tenant) { x.Name = "" }, field: "name", wantErr: true},
		{name: "bad status", mutate: func(x *Tenant) { x.Status = "deleted" }, field: "status", wantErr: true},
		{name: "slug uppercase", mutate: func(x *Tenant) { x.Slug = "Acme" }, field: "slug", wantErr: true},
		{name: "slug too short", mutate: func(x *Tenant) { x.Slug = "a" }, field: "slug", wantErr: true},
		{name: "slug leading dash", mutate: func(x *Tenant) { x.Slug = "-acme" }, field: "slug", wantErr: true},
		{name: "slug with space", mutate: func(x *Tenant) { x.Slug = "acme corp" }, field: "slug", wantErr: true},
		{name: "slug with underscore", mutate: func(x *Tenant) { x.Slug = "acme_corp" }, field: "slug", wantErr: true},
		{name: "slug digits ok", mutate: func(x *Tenant) { x.Slug = "acme2" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tn := validTenant()
			tc.mutate(tn)
			err := tn.Validate()
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if ve.Field != tc.field {
				t.Errorf("field = %q, want %q", ve.Field, tc.field)
			}
		})
	}
}

// The slug bound is 63 characters so it stays usable as a DNS label and as a
// metric label value. The regex in the database enforces the same bound, so a
// drift between the two would surface as a 500 rather than a 422.
func TestTenantSlugLengthBoundary(t *testing.T) {
	tn := validTenant()

	tn.Slug = "ab"
	if err := tn.Validate(); err != nil {
		t.Errorf("2-character slug should be valid: %v", err)
	}

	long := "a"
	for len(long) < 63 {
		long += "b"
	}
	tn.Slug = long
	if err := tn.Validate(); err != nil {
		t.Errorf("63-character slug should be valid: %v", err)
	}

	tn.Slug = long + "c" // 64
	if err := tn.Validate(); err == nil {
		t.Error("64-character slug should be rejected")
	}
}

func TestTenantActive(t *testing.T) {
	tn := validTenant()
	if !tn.Active() {
		t.Error("active tenant should report Active")
	}
	tn.Status = TenantSuspended
	if tn.Active() {
		t.Error("suspended tenant must not report Active")
	}
}

func TestTenantStatusValid(t *testing.T) {
	for _, s := range []TenantStatus{TenantActive, TenantSuspended} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []TenantStatus{"", "deleted", "ACTIVE"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestTenantContextRoundTrip(t *testing.T) {
	tn := validTenant()
	ctx := WithTenant(context.Background(), tn)

	got, ok := TenantFrom(ctx)
	if !ok || got.TenantID != tn.TenantID {
		t.Fatalf("TenantFrom = %+v, %v", got, ok)
	}
	if id := TenantIDFrom(ctx); id != tn.TenantID {
		t.Errorf("TenantIDFrom = %q, want %q", id, tn.TenantID)
	}
}

// A context that never passed through the authentication boundary must still
// yield a usable tenant id. Returning an empty string would violate the
// foreign key and surface to the caller as a 500 rather than as data written
// to the system tenant.
func TestTenantIDFromDefaultsToSystem(t *testing.T) {
	if id := TenantIDFrom(context.Background()); id != SystemTenantID {
		t.Errorf("TenantIDFrom(empty) = %q, want %q", id, SystemTenantID)
	}

	// A tenant present but with an empty id is treated the same way.
	ctx := WithTenant(context.Background(), &Tenant{})
	if id := TenantIDFrom(ctx); id != SystemTenantID {
		t.Errorf("TenantIDFrom(empty tenant) = %q, want %q", id, SystemTenantID)
	}
}

// AllowsIssuer encodes the safe-by-default binding: an empty set means the
// default IdP only, an explicit set is exact membership, and every degenerate
// input fails closed (ADR-IDENTITY-003).
func TestTenantAllowsIssuer(t *testing.T) {
	const def = "https://oneops.local"
	cases := []struct {
		name    string
		allowed []string
		issuer  string
		want    bool
	}{
		{"empty set allows the default issuer", nil, def, true},
		{"empty set refuses a non-default issuer", nil, "https://idp-b.example", false},
		{"empty issuer is refused even with empty set", nil, "", false},
		{"empty default with empty set refuses all", nil, "", false},
		{"explicit set allows a listed issuer", []string{"https://idp-b.example"}, "https://idp-b.example", true},
		{"explicit set refuses an unlisted issuer", []string{"https://idp-b.example"}, "https://idp-c.example", false},
		{"explicit set does NOT implicitly include the default", []string{"https://idp-b.example"}, def, false},
		{"explicit set refuses an empty issuer", []string{"https://idp-b.example"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tn := &Tenant{AllowedIssuers: c.allowed}
			d := def
			if c.name == "empty default with empty set refuses all" {
				d = ""
			}
			if got := tn.AllowsIssuer(c.issuer, d); got != c.want {
				t.Errorf("AllowsIssuer(%q, %q) with %v = %v, want %v",
					c.issuer, d, c.allowed, got, c.want)
			}
		})
	}
}

func TestValidateAllowedIssuersRejectsBlank(t *testing.T) {
	if err := ValidateAllowedIssuers([]string{"https://idp-b.example", "  "}); err == nil {
		t.Error("a blank issuer entry must be rejected")
	}
	if err := ValidateAllowedIssuers([]string{"https://idp-b.example"}); err != nil {
		t.Errorf("a clean set must validate: %v", err)
	}
}

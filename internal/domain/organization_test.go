package domain

import (
	"strings"
	"testing"
)

func TestOrganizationStatus_Valid(t *testing.T) {
	for _, s := range []OrganizationStatus{OrganizationActive, OrganizationSuspended} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []OrganizationStatus{"", "deleted", "Active", "dissolved"} {
		if s.Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

func TestOrganization_Validate(t *testing.T) {
	valid := func() *Organization {
		return &Organization{
			OrgID: "org_1", TenantID: "tn_1",
			Slug: "acme-corp", Name: "Acme Corp", Status: OrganizationActive,
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("a valid organisation was rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Organization)
		field  string
	}{
		{"empty org id", func(o *Organization) { o.OrgID = "" }, "org_id"},
		{"whitespace org id", func(o *Organization) { o.OrgID = "  " }, "org_id"},
		{"empty tenant id", func(o *Organization) { o.TenantID = "" }, "tenant_id"},
		{"uppercase slug", func(o *Organization) { o.Slug = "Acme" }, "slug"},
		{"leading dash slug", func(o *Organization) { o.Slug = "-acme" }, "slug"},
		{"single char slug", func(o *Organization) { o.Slug = "a" }, "slug"},
		{"slug too long", func(o *Organization) { o.Slug = strings.Repeat("a", 64) }, "slug"},
		{"underscore slug", func(o *Organization) { o.Slug = "acme_corp" }, "slug"},
		{"empty name", func(o *Organization) { o.Name = "" }, "name"},
		{"whitespace name", func(o *Organization) { o.Name = "   " }, "name"},
		{"empty status", func(o *Organization) { o.Status = "" }, "status"},
		{"unknown status", func(o *Organization) { o.Status = "dissolved" }, "status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := valid()
			c.mutate(o)
			err := o.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			v, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected a ValidationError, got %T", err)
			}
			if v.Field != c.field {
				t.Errorf("reported field %q, want %q", v.Field, c.field)
			}
		})
	}
}

// The organisation slug rule must be the SAME expression the tenant rule uses.
// The backfill copies tenant slugs verbatim, so a laxer rule here would admit an
// organisation slug its own tenant could not hold.
func TestOrganization_SlugRuleMatchesTenant(t *testing.T) {
	for _, slug := range []string{"acme", "acme-corp", "a1", strings.Repeat("a", 63)} {
		o := &Organization{OrgID: "o", TenantID: "t", Slug: slug, Name: "N", Status: OrganizationActive}
		tn := &Tenant{TenantID: "t", Slug: slug, Name: "N", Status: TenantActive}
		if (o.Validate() == nil) != (tn.Validate() == nil) {
			t.Errorf("slug %q: organisation and tenant disagree on validity", slug)
		}
	}
}

func TestNewOrganization(t *testing.T) {
	o, err := NewOrganization("  Acme Corp  ", "  Acme-Corp  ")
	if err != nil {
		t.Fatalf("NewOrganization: %v", err)
	}
	if o.Slug != "acme-corp" {
		t.Errorf("slug = %q, want it lowercased and trimmed", o.Slug)
	}
	if o.Name != "Acme Corp" {
		t.Errorf("name = %q, want it trimmed", o.Name)
	}
	if o.Status != OrganizationActive {
		t.Errorf("status = %q, want active", o.Status)
	}
	if o.OrgID == "" || o.TenantID == "" {
		t.Fatal("both identifiers must be minted; an organisation without a tenant is an " +
			"Identity scope with no isolation")
	}
	if o.OrgID == o.TenantID {
		t.Error("org id and tenant id are the same value; they are different entities and " +
			"sharing an identifier would make the 1:1 impossible to observe")
	}

	other, err := NewOrganization("Other", "other")
	if err != nil {
		t.Fatal(err)
	}
	if other.OrgID == o.OrgID || other.TenantID == o.TenantID {
		t.Error("two organisations were minted with a shared identifier")
	}
}

func TestNewOrganization_RejectsInvalid(t *testing.T) {
	if _, err := NewOrganization("Acme", "Not A Slug"); err == nil {
		t.Error("a slug with spaces was accepted")
	}
	if _, err := NewOrganization("", "acme"); err == nil {
		t.Error("an empty name was accepted")
	}
}

func TestOrganization_Active(t *testing.T) {
	for status, want := range map[OrganizationStatus]bool{
		OrganizationActive:    true,
		OrganizationSuspended: false,
	} {
		o := &Organization{Status: status}
		if got := o.Active(); got != want {
			t.Errorf("Active() with %q = %v, want %v", status, got, want)
		}
	}
}

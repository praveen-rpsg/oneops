package domain

import "testing"

func TestNewMembership_BuildsAnActiveGrant(t *testing.T) {
	m, err := NewMembership(" org_1 ", " tn-1 ", " usr_1 ")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}
	if m.OrgID != "org_1" || m.TenantID != "tn-1" || m.UserID != "usr_1" {
		t.Errorf("identifiers not trimmed: %+v", m)
	}
	if m.Status != MembershipActive {
		t.Errorf("status %q, want active", m.Status)
	}
	if !m.Active() {
		t.Error("a new membership must be active")
	}
	if m.MembershipID == "" {
		t.Error("membership_id must be minted")
	}
}

// tenant_id is the RLS key. A membership without one is a grant that no policy
// confines, so it must not be constructible.
func TestNewMembership_RequiresEveryIdentifier(t *testing.T) {
	for _, c := range []struct{ name, org, tenant, user string }{
		{"no org", "", "tn-1", "usr_1"},
		{"no tenant", "org_1", "", "usr_1"},
		{"no user", "org_1", "tn-1", ""},
		{"blank tenant", "org_1", "   ", "usr_1"},
		{"blank org", "   ", "tn-1", "usr_1"},
		{"blank user", "org_1", "tn-1", "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewMembership(c.org, c.tenant, c.user); err == nil {
				t.Error("an incomplete membership was constructed")
			}
		})
	}
}

func TestMembershipStatus_Valid(t *testing.T) {
	for _, s := range []MembershipStatus{MembershipActive, MembershipRevoked} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
	}
	for _, s := range []MembershipStatus{"", "ACTIVE", "deleted", "suspended", "pending"} {
		if MembershipStatus(s).Valid() {
			t.Errorf("%q must not be valid", s)
		}
	}
}

func TestMembership_ValidateRejectsUndefinedStatus(t *testing.T) {
	m, err := NewMembership("org_1", "tn-1", "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	m.Status = "archived"
	if err := m.Validate(); err == nil {
		t.Error("an undefined status was accepted")
	}
	if m.Active() {
		t.Error("a membership with an undefined status must not report active")
	}
}

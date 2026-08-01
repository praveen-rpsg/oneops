package domain

import (
	"strings"
	"testing"
)

func TestNewTeam_BuildsAnActiveTeam(t *testing.T) {
	tm, err := NewTeam(" org_1 ", " tn-1 ", " Platform Team ")
	if err != nil {
		t.Fatalf("NewTeam: %v", err)
	}
	if tm.OrgID != "org_1" || tm.TenantID != "tn-1" || tm.Name != "Platform Team" {
		t.Errorf("identifiers/name not trimmed: %+v", tm)
	}
	if tm.Status != TeamActive {
		t.Errorf("status %q, want active", tm.Status)
	}
	if !tm.Active() {
		t.Error("a new team must be active")
	}
	if tm.TeamID == "" {
		t.Error("team_id must be minted")
	}
}

// tenant_id is the RLS key. A team without one is a row that no policy
// confines, so it must not be constructible.
func TestNewTeam_RequiresEveryIdentifier(t *testing.T) {
	for _, c := range []struct{ name, org, tenant, teamName string }{
		{"no org", "", "tn-1", "Team"},
		{"no tenant", "org_1", "", "Team"},
		{"no name", "org_1", "tn-1", ""},
		{"blank tenant", "org_1", "   ", "Team"},
		{"blank org", "   ", "tn-1", "Team"},
		{"blank name", "org_1", "tn-1", "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewTeam(c.org, c.tenant, c.teamName); err == nil {
				t.Error("an incomplete team was constructed")
			}
		})
	}
}

func TestNewTeam_RejectsAnOverlongName(t *testing.T) {
	if _, err := NewTeam("org_1", "tn-1", strings.Repeat("x", MaxTeamNameLength+1)); err == nil {
		t.Error("a name over the length bound was accepted")
	}
}

func TestTeamStatus_Valid(t *testing.T) {
	for _, s := range []TeamStatus{TeamActive, TeamArchived} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
	}
	for _, s := range []TeamStatus{"", "ACTIVE", "deleted", "suspended", "revoked"} {
		if TeamStatus(s).Valid() {
			t.Errorf("%q must not be valid", s)
		}
	}
}

func TestTeam_ValidateRejectsUndefinedStatus(t *testing.T) {
	tm, err := NewTeam("org_1", "tn-1", "Team")
	if err != nil {
		t.Fatal(err)
	}
	tm.Status = "deleted"
	if err := tm.Validate(); err == nil {
		t.Error("an undefined status was accepted")
	}
	if tm.Active() {
		t.Error("a team with an undefined status must not report active")
	}
}

func TestNewTeamMembership_BuildsAMembership(t *testing.T) {
	m, err := NewTeamMembership(" team_1 ", " tn-1 ", " usr_1 ")
	if err != nil {
		t.Fatalf("NewTeamMembership: %v", err)
	}
	if m.TeamID != "team_1" || m.TenantID != "tn-1" || m.UserID != "usr_1" {
		t.Errorf("identifiers not trimmed: %+v", m)
	}
	if m.TeamMembershipID == "" {
		t.Error("team_membership_id must be minted")
	}
}

func TestNewTeamMembership_RequiresEveryIdentifier(t *testing.T) {
	for _, c := range []struct{ name, team, tenant, user string }{
		{"no team", "", "tn-1", "usr_1"},
		{"no tenant", "team_1", "", "usr_1"},
		{"no user", "team_1", "tn-1", ""},
		{"blank team", "   ", "tn-1", "usr_1"},
		{"blank tenant", "team_1", "   ", "usr_1"},
		{"blank user", "team_1", "tn-1", "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewTeamMembership(c.team, c.tenant, c.user); err == nil {
				t.Error("an incomplete team membership was constructed")
			}
		})
	}
}

package domain

import (
	"strings"
	"testing"
)

func TestNewEscalationPolicy_BuildsAnActivePolicy(t *testing.T) {
	p, err := NewEscalationPolicy(" tn-1 ", " Default Policy ")
	if err != nil {
		t.Fatalf("NewEscalationPolicy: %v", err)
	}
	if p.TenantID != "tn-1" || p.Name != "Default Policy" {
		t.Errorf("identifiers/name not trimmed: %+v", p)
	}
	if p.Status != EscalationPolicyActive || !p.Active() {
		t.Errorf("a new policy must be active: %+v", p)
	}
	if p.PolicyID == "" {
		t.Error("policy_id must be minted")
	}
}

func TestNewEscalationPolicy_RequiresEveryField(t *testing.T) {
	for _, c := range []struct {
		name   string
		tenant string
		policy string
	}{
		{"no tenant", "", "Policy"},
		{"blank tenant", "   ", "Policy"},
		{"no name", "tn-1", ""},
		{"blank name", "tn-1", "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewEscalationPolicy(c.tenant, c.policy); err == nil {
				t.Error("an invalid policy was constructed")
			}
		})
	}
}

func TestNewEscalationPolicy_RejectsAnOverlongName(t *testing.T) {
	if _, err := NewEscalationPolicy("tn-1", strings.Repeat("x", MaxEscalationPolicyNameLength+1)); err == nil {
		t.Error("a name over the length bound was accepted")
	}
}

func TestEscalationPolicyStatus_Valid(t *testing.T) {
	for _, c := range []struct {
		status EscalationPolicyStatus
		want   bool
	}{
		{EscalationPolicyActive, true},
		{EscalationPolicyArchived, true},
		{EscalationPolicyStatus("deleted"), false},
		{EscalationPolicyStatus(""), false},
	} {
		if got := c.status.Valid(); got != c.want {
			t.Errorf("%q.Valid() = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestEscalationPolicy_ValidateRejectsBadStatus(t *testing.T) {
	p := &EscalationPolicy{PolicyID: "pol1", TenantID: "tn-1", Name: "Policy", Status: "bogus"}
	if err := p.Validate(); err == nil {
		t.Error("a bogus status was accepted")
	}
}

func TestNewEscalationTier_BuildsATier(t *testing.T) {
	tr, err := NewEscalationTier(" pol-1 ", " tn-1 ", " sch-1 ", 300, 0)
	if err != nil {
		t.Fatalf("NewEscalationTier: %v", err)
	}
	if tr.PolicyID != "pol-1" || tr.TenantID != "tn-1" || tr.OnCallScheduleID != "sch-1" {
		t.Errorf("identifiers not trimmed: %+v", tr)
	}
	if tr.WaitSeconds != 300 || tr.Position != 0 {
		t.Errorf("wait_seconds/position not preserved: %+v", tr)
	}
	if tr.TierID == "" {
		t.Error("tier_id must be minted")
	}
}

func TestNewEscalationTier_RequiresEveryField(t *testing.T) {
	for _, c := range []struct {
		name        string
		policy      string
		tenant      string
		schedule    string
		waitSeconds int
		position    int
	}{
		{"no policy", "", "tn-1", "sch-1", 60, 0},
		{"blank policy", "   ", "tn-1", "sch-1", 60, 0},
		{"no tenant", "pol-1", "", "sch-1", 60, 0},
		{"blank tenant", "pol-1", "   ", "sch-1", 60, 0},
		{"no schedule", "pol-1", "tn-1", "", 60, 0},
		{"blank schedule", "pol-1", "tn-1", "   ", 60, 0},
		{"zero wait_seconds", "pol-1", "tn-1", "sch-1", 0, 0},
		{"negative wait_seconds", "pol-1", "tn-1", "sch-1", -1, 0},
		{"negative position", "pol-1", "tn-1", "sch-1", 60, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewEscalationTier(c.policy, c.tenant, c.schedule, c.waitSeconds, c.position); err == nil {
				t.Error("an invalid tier was constructed")
			}
		})
	}
}

// MinEscalationWaitSeconds is the exact floor: 1 is accepted, 0 is not — the
// mutation this pins is a fencepost off-by-one on the comparison operator.
func TestMinEscalationWaitSeconds_IsTheExactFloor(t *testing.T) {
	if _, err := NewEscalationTier("pol-1", "tn-1", "sch-1", MinEscalationWaitSeconds, 0); err != nil {
		t.Errorf("wait_seconds = %d (the floor) was rejected: %v", MinEscalationWaitSeconds, err)
	}
	if _, err := NewEscalationTier("pol-1", "tn-1", "sch-1", MinEscalationWaitSeconds-1, 0); err == nil {
		t.Errorf("wait_seconds = %d (below the floor) was accepted", MinEscalationWaitSeconds-1)
	}
}

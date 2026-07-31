package domain

import "testing"

func TestEffectiveRequiredApprovals_DefaultsToOne(t *testing.T) {
	if n := EffectiveRequiredApprovals(nil); n != 1 {
		t.Fatalf("EffectiveRequiredApprovals(nil) = %d, want 1 (backward-compatible default)", n)
	}
	if n := EffectiveRequiredApprovals([]*Setting{{Key: "default_page_size", Value: "100"}}); n != 1 {
		t.Fatalf("unrelated overrides changed the default: got %d, want 1", n)
	}
}

func TestEffectiveRequiredApprovals_UsesOverride(t *testing.T) {
	overrides := []*Setting{{Key: GovernanceRequiredApprovalsKey, Value: "3"}}
	if n := EffectiveRequiredApprovals(overrides); n != 3 {
		t.Fatalf("EffectiveRequiredApprovals = %d, want 3", n)
	}
}

func TestNewApprovalRecord(t *testing.T) {
	a, err := NewApprovalRecord("tenant-1", "cfg-1", "alice")
	if err != nil {
		t.Fatalf("NewApprovalRecord: %v", err)
	}
	if a.ApprovalID == "" || a.TenantID != "tenant-1" || a.GovernanceID != "cfg-1" || a.ApproverUserID != "alice" {
		t.Fatalf("record = %+v", a)
	}
}

func TestNewApprovalRecord_RequiresFields(t *testing.T) {
	for _, c := range []struct{ tenant, gov, approver string }{
		{"", "cfg-1", "alice"},
		{"tenant-1", "", "alice"},
		{"tenant-1", "cfg-1", ""},
	} {
		if _, err := NewApprovalRecord(c.tenant, c.gov, c.approver); err == nil {
			t.Errorf("NewApprovalRecord(%q,%q,%q) accepted a missing field", c.tenant, c.gov, c.approver)
		}
	}
}

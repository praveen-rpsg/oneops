package domain

import "testing"

func TestAuditChainID_MapsObjectToItsChain(t *testing.T) {
	for _, cfgID := range []string{"ONEOPS-CFG-0001", "ONEOPS-CFG-0014", "x"} {
		if got := AuditChainID(cfgID); got != cfgID {
			t.Errorf("AuditChainID(%q) = %q, want %q", cfgID, got, cfgID)
		}
	}
}

func TestAuditChainID_Stable(t *testing.T) {
	const cfgID = "ONEOPS-CFG-0007"
	first := AuditChainID(cfgID)
	for i := 0; i < 10; i++ {
		if got := AuditChainID(cfgID); got != first {
			t.Fatalf("AuditChainID not stable: %q vs %q", got, first)
		}
	}
}

func TestAuditChainID_DistinctObjectsDistinctChains(t *testing.T) {
	if AuditChainID("ONEOPS-CFG-0001") == AuditChainID("ONEOPS-CFG-0002") {
		t.Fatal("distinct objects mapped to the same chain")
	}
}

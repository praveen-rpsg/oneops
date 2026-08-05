package domain

import "testing"

func TestNewEscalationState_BuildsAnActiveStateAtTierZero(t *testing.T) {
	st, err := NewEscalationState(" tn-1 ", " inc-1 ", " pol-1 ")
	if err != nil {
		t.Fatalf("NewEscalationState: %v", err)
	}
	if st.TenantID != "tn-1" || st.IncidentID != "inc-1" || st.PolicyID != "pol-1" {
		t.Errorf("identifiers not trimmed: %+v", st)
	}
	if st.StateID == "" {
		t.Error("state_id must be minted")
	}
	if st.CurrentTierIndex != 0 {
		t.Errorf("CurrentTierIndex = %d, want 0", st.CurrentTierIndex)
	}
	if st.Status != EscalationStateActive {
		t.Errorf("Status = %q, want active", st.Status)
	}
	if st.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0", st.Attempts)
	}
	if st.RowVersion != 1 {
		t.Errorf("RowVersion = %d, want 1", st.RowVersion)
	}
	if st.NextAttemptAt.IsZero() {
		t.Error("a freshly seeded state must be due immediately, not zero")
	}
	if !st.ClaimedAt.IsZero() {
		t.Error("a freshly seeded state must be unclaimed")
	}
}

func TestNewEscalationState_RequiresEveryIdentifier(t *testing.T) {
	for _, c := range []struct {
		name             string
		tenant, inc, pol string
	}{
		{"no tenant", "", "inc-1", "pol-1"},
		{"blank tenant", "   ", "inc-1", "pol-1"},
		{"no incident", "tn-1", "", "pol-1"},
		{"blank incident", "tn-1", "   ", "pol-1"},
		{"no policy", "tn-1", "inc-1", ""},
		{"blank policy", "tn-1", "inc-1", "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewEscalationState(c.tenant, c.inc, c.pol); err == nil {
				t.Error("an invalid escalation state was constructed")
			}
		})
	}
}

func TestEscalationStateStatus_Valid(t *testing.T) {
	for _, c := range []struct {
		status EscalationStateStatus
		want   bool
	}{
		{EscalationStateActive, true},
		{EscalationStateAcked, true},
		{EscalationStateResolved, true},
		{EscalationStateExhausted, true},
		{EscalationStateStatus("pending"), false},
		{EscalationStateStatus(""), false},
	} {
		if got := c.status.Valid(); got != c.want {
			t.Errorf("%q.Valid() = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestEscalationStateStatus_Terminal(t *testing.T) {
	for _, c := range []struct {
		status EscalationStateStatus
		want   bool
	}{
		{EscalationStateActive, false},
		{EscalationStateAcked, true},
		{EscalationStateResolved, true},
		{EscalationStateExhausted, true},
	} {
		if got := c.status.Terminal(); got != c.want {
			t.Errorf("%q.Terminal() = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestEscalationState_ValidateRejectsNegativeTierIndex(t *testing.T) {
	st, err := NewEscalationState("tn-1", "inc-1", "pol-1")
	if err != nil {
		t.Fatalf("NewEscalationState: %v", err)
	}
	st.CurrentTierIndex = -1
	if err := st.Validate(); err == nil {
		t.Error("a negative current_tier_index was accepted")
	}
}

func TestEscalationState_ValidateRejectsNegativeAttempts(t *testing.T) {
	st, err := NewEscalationState("tn-1", "inc-1", "pol-1")
	if err != nil {
		t.Fatalf("NewEscalationState: %v", err)
	}
	st.Attempts = -1
	if err := st.Validate(); err == nil {
		t.Error("negative attempts were accepted")
	}
}

func TestEscalationState_ValidateRejectsUndefinedStatus(t *testing.T) {
	st, err := NewEscalationState("tn-1", "inc-1", "pol-1")
	if err != nil {
		t.Fatalf("NewEscalationState: %v", err)
	}
	st.Status = EscalationStateStatus("bogus")
	if err := st.Validate(); err == nil {
		t.Error("an undefined status was accepted")
	}
}

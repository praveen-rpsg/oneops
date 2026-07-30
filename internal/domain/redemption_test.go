package domain

import "testing"

// Which account states may accept an invitation is a security rule, not a
// convenience. The two refusals matter more than the two permissions.
func TestUserStatus_MayAcceptInvitation(t *testing.T) {
	for _, c := range []struct {
		status UserStatus
		want   bool
		why    string
	}{
		{UserInvited, true, "an invited user accepting is the ordinary path"},
		{UserActive, true, "an active user may be invited to a second organisation"},
		{UserSuspended, false, "suspension is an administrative decision and must not be " +
			"overturnable by anyone able to send one invitation"},
		{UserDeactivated, false, "deactivation is terminal; the row is retained for audit " +
			"attribution, not so the account can be resumed"},
	} {
		if got := c.status.MayAcceptInvitation(); got != c.want {
			t.Errorf("%q.MayAcceptInvitation() = %v, want %v — %s", c.status, got, c.want, c.why)
		}
	}
}

// An undefined status must not fall through to permitted. A default-allow here
// would mean any state added later silently gains the right to redeem.
func TestUserStatus_MayAcceptInvitationRefusesUndefinedStates(t *testing.T) {
	for _, s := range []UserStatus{"", "ACTIVE", "archived", "pending", "unknown"} {
		if UserStatus(s).MayAcceptInvitation() {
			t.Errorf("undefined status %q was permitted to redeem", s)
		}
	}
}

// The permitted set must agree with the lifecycle: every status allowed to
// accept must be able to reach `active`, or the redemption would refuse a
// transition it had just authorised.
func TestUserStatus_AcceptanceAgreesWithTheLifecycle(t *testing.T) {
	for _, s := range []UserStatus{UserInvited, UserActive, UserSuspended, UserDeactivated} {
		if !s.MayAcceptInvitation() {
			continue
		}
		if s != UserActive && !s.CanTransitionTo(UserActive) {
			t.Errorf("%q may accept an invitation but cannot transition to active; redemption "+
				"would authorise a move the lifecycle then refuses", s)
		}
	}
}

package domain

import "context"

// Redemption is the outcome of accepting an invitation: the invitation that was
// consumed, the account that now exists, and the membership that grants access.
//
// All three are returned because all three were written in one transaction. A
// caller that received only the membership could not tell whether it had just
// created an account or attached to an existing one, which is the difference
// between "welcome" and "you have been added to another organisation".
type Redemption struct {
	Invitation *Invitation
	User       *User
	Membership *Membership
	// UserCreated distinguishes a new account from an existing one.
	UserCreated bool
	// MembershipCreated is false when the user was already a member and the
	// redemption reattached or confirmed an existing row.
	MembershipCreated bool
}

// MayAcceptInvitation reports whether a user in this state may redeem one.
//
// Only `invited` and `active` may. The other two are refusals with teeth:
//
// A suspended user must not regain access by following an invitation link.
// Suspension is an administrative decision, and an invitation — which any
// administrator of any organisation can issue — must not be able to overturn
// it. Allowing it would make suspension bypassable by anyone who could get one
// email sent.
//
// A deactivated user must not be revived at all. Deactivation is terminal by
// the lifecycle (ADR-IDENTITY-001 §8.3): the row is retained so past audit
// events keep an attributable author, not so the account can be resumed.
func (s UserStatus) MayAcceptInvitation() bool {
	return s == UserInvited || s == UserActive
}

// RedemptionRepository turns a token into membership.
//
// It is a repository of its own rather than a method on InvitationRepository
// because the operation spans three tables and its correctness is the
// transaction, not any one of them.
type RedemptionRepository interface {
	// Redeem consumes the invitation named by token and returns the resulting
	// account and membership. It is atomic: either the invitation is consumed
	// AND the user and membership exist, or nothing happened and the token is
	// still usable.
	//
	// Every failure returns ErrTokenNotRedeemable. A token that never existed,
	// one already redeemed, one revoked, one expired, and one belonging to a
	// suspended account are indistinguishable — otherwise redemption reports
	// whether a guessed token was real, and which organisations exist.
	//
	// displayName is used only when the account is created. It is ignored for
	// an existing user: an invitation is an invitation, not a licence for
	// whoever issued it to rename someone else's account.
	Redeem(ctx context.Context, token, displayName string) (*Redemption, error)
}

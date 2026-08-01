package domain

import (
	"context"
	"testing"
)

// ---- AdminOperation.Valid ---------------------------------------------------
//
// ck_admin_audit_operation is the same list, enforced in the database; this is
// the pre-check that turns a would-be constraint violation into a readable
// refusal. It must accept exactly the administrative operations and
// nothing else — an operation from the disjoint ConfigurationOperation
// vocabulary must not pass, since the two stores must never record the same
// fact twice.

func TestAdminOperationValid(t *testing.T) {
	valid := []AdminOperation{
		AdminUserCreated, AdminUserProfileUpdated, AdminUserStatusChanged,
		AdminOrgCreated, AdminOrgStatusChanged,
		AdminTenantCreated, AdminTenantStatusChanged, AdminTenantIssuersChanged,
		AdminInvitationCreated, AdminInvitationRedeemed, AdminInvitationRevoked,
		AdminMembershipGranted, AdminMembershipRevoked,
	}
	for _, op := range valid {
		if !op.Valid() {
			t.Errorf("administrative operation %q should be valid", op)
		}
	}
	for _, op := range []AdminOperation{"", "bogus", "user_created", "configuration.created"} {
		if op.Valid() {
			t.Errorf("operation %q should not be a valid administrative operation", op)
		}
	}
}

// ---- AdminSubject.ChainID ---------------------------------------------------
//
// ADR-AUDIT-007 §6.8: one chain per organisation, plus the platform chain for
// acts with no organisation. This routing decides which row's FOR UPDATE lock
// an append takes, so it must be exact and must not collide with an
// organisation literally named "platform".

func TestAdminSubjectChainID_RoutesToOwnOrganisationChain(t *testing.T) {
	s := AdminSubject{OrgID: "acme"}
	if got, want := s.ChainID(), "chain:org:acme"; got != want {
		t.Errorf("ChainID() = %q, want %q", got, want)
	}
}

func TestAdminSubjectChainID_NoOrganisationRoutesToPlatformChain(t *testing.T) {
	s := AdminSubject{TenantID: "t1", UserID: "u1"}
	if got := s.ChainID(); got != PlatformAuditChain {
		t.Errorf("ChainID() with no org = %q, want the platform chain %q", got, PlatformAuditChain)
	}
}

func TestAdminSubjectChainID_OrgLiterallyNamedPlatformDoesNotCollide(t *testing.T) {
	// The prefix exists precisely so this cannot happen: an organisation whose
	// id is "platform" must route to its own chain, not the platform chain.
	s := AdminSubject{OrgID: "platform"}
	if got := s.ChainID(); got == PlatformAuditChain {
		t.Errorf("an organisation named %q must not collide with the platform chain, got %q", "platform", got)
	}
}

// ---- WithActor / ActorFrom --------------------------------------------------
//
// ADR-AUDIT-007 §6.9: an unauditable administrative act must fail, not
// silently attribute to nobody. ActorFrom's boolean is the only signal a
// caller has to refuse — there is deliberately no fallback identity, unlike
// TenantIDFrom.

func TestActorFrom_ReturnsWhatWithActorSet(t *testing.T) {
	ctx := WithActor(context.Background(), "user-123")
	actor, ok := ActorFrom(ctx)
	if !ok || actor != "user-123" {
		t.Errorf("ActorFrom() = (%q, %v), want (%q, true)", actor, ok, "user-123")
	}
}

func TestActorFrom_FalseWhenNeverSet(t *testing.T) {
	actor, ok := ActorFrom(context.Background())
	if ok || actor != "" {
		t.Errorf("ActorFrom on a context with no actor = (%q, %v), want (\"\", false) — "+
			"there must be no fallback identity", actor, ok)
	}
}

func TestActorFrom_FalseWhenSetToEmptyString(t *testing.T) {
	// An explicitly empty actor must be treated the same as no actor at all:
	// callers must refuse the act, not attribute it to "".
	ctx := WithActor(context.Background(), "")
	actor, ok := ActorFrom(ctx)
	if ok || actor != "" {
		t.Errorf("ActorFrom with an empty actor set = (%q, %v), want (\"\", false)", actor, ok)
	}
}

// ---- AdminChainIDs -----------------------------------------------------------
//
// The ordering is the point: a single total order over the chain ids an
// administrative act touches prevents two transactions from acquiring the
// same two chain-head locks in opposite orders and deadlocking.

func TestAdminChainIDs_DeduplicatesAndSortsAscending(t *testing.T) {
	acts := []AdminAct{
		{Subject: AdminSubject{OrgID: "zeta"}},
		{Subject: AdminSubject{OrgID: "acme"}},
		{Subject: AdminSubject{OrgID: "acme"}}, // duplicate chain
		{Subject: AdminSubject{}},              // platform chain
	}
	got := AdminChainIDs(acts)
	want := []string{"chain:org:acme", "chain:org:zeta", PlatformAuditChain}
	// PlatformAuditChain is "chain:platform", which sorts between
	// "chain:org:acme" and "chain:org:zeta" lexicographically — assert the
	// actual sorted order rather than assuming it, since getting this wrong
	// silently reopens the deadlock the ordering exists to prevent.
	if len(got) != 3 {
		t.Fatalf("AdminChainIDs = %v, want 3 distinct chains", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("AdminChainIDs = %v, not strictly ascending at index %d", got, i)
		}
	}
	seen := make(map[string]bool)
	for _, id := range got {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("AdminChainIDs = %v, missing expected chain %q", got, id)
		}
	}
}

func TestAdminChainIDs_EmptyActsReturnsEmpty(t *testing.T) {
	got := AdminChainIDs(nil)
	if len(got) != 0 {
		t.Errorf("AdminChainIDs(nil) = %v, want empty", got)
	}
}

func TestAdminChainIDs_OrderIsDeterministicRegardlessOfInputOrder(t *testing.T) {
	// Two transactions that touch the same set of chains in different input
	// order must still acquire locks in the same total order, or the
	// deadlock this function exists to prevent reopens.
	a := []AdminAct{
		{Subject: AdminSubject{OrgID: "acme"}},
		{Subject: AdminSubject{OrgID: "zeta"}},
	}
	b := []AdminAct{
		{Subject: AdminSubject{OrgID: "zeta"}},
		{Subject: AdminSubject{OrgID: "acme"}},
	}
	gotA, gotB := AdminChainIDs(a), AdminChainIDs(b)
	if len(gotA) != len(gotB) {
		t.Fatalf("AdminChainIDs(a)=%v AdminChainIDs(b)=%v differ in length", gotA, gotB)
	}
	for i := range gotA {
		if gotA[i] != gotB[i] {
			t.Fatalf("AdminChainIDs order depends on input order: %v vs %v", gotA, gotB)
		}
	}
}

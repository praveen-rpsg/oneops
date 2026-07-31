package domain

import (
	"context"
	"errors"
	"testing"
)

// fakeOwned is the minimal Owned implementation the ownership tests drive
// directly, so these tests exercise domain's own logic rather than any
// package that happens to implement Owned (events.Webhook, policy.Policy...).
type fakeOwned string

func (f fakeOwned) OwnerTenantID() string { return string(f) }

// ---- SameOwner ---------------------------------------------------------
//
// ADR-TENANCY-002/003: empty is never equal to empty. A value of unknown
// ownership must fail closed — it matches nothing, never everything.

func TestSameOwner_TrueOnlyForMatchingKnownTenant(t *testing.T) {
	if !SameOwner(fakeOwned("tenant-a"), fakeOwned("tenant-a")) {
		t.Error("SameOwner must be true for two values naming the same known tenant")
	}
}

func TestSameOwner_FalseAcrossDifferentTenants(t *testing.T) {
	if SameOwner(fakeOwned("tenant-a"), fakeOwned("tenant-b")) {
		t.Error("SameOwner must be false across differing tenants — this is the cross-tenant boundary")
	}
}

func TestSameOwner_FalseClosedOnUnknownOwner(t *testing.T) {
	cases := []struct {
		name string
		a, b Owned
	}{
		{"both empty", fakeOwned(""), fakeOwned("")},
		{"a empty, b known", fakeOwned(""), fakeOwned("tenant-a")},
		{"a known, b empty", fakeOwned("tenant-a"), fakeOwned("")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if SameOwner(c.a, c.b) {
				t.Errorf("SameOwner(%q) must fail closed: unknown ownership must never match, "+
					"not even another unknown", c.name)
			}
		})
	}
}

// ---- FanOut -------------------------------------------------------------
//
// ADR-TENANCY-002: a privileged worker's cross-tenant read must never widen
// into a cross-tenant act. FanOut is the one place that boundary is drawn;
// the predicate only narrows what is delivered, never who it is delivered to.

func TestFanOut_EventWithUnknownOwnerDeliversToNobody(t *testing.T) {
	ev := fakeOwned("")
	subs := []fakeOwned{"tenant-a", "tenant-b"}
	got := FanOut(ev, subs, func(fakeOwned) bool { return true })
	if got != nil {
		t.Errorf("FanOut on an unknown-owner event must return nil, got %v", got)
	}
}

func TestFanOut_EmptySubscriberListReturnsNothing(t *testing.T) {
	ev := fakeOwned("tenant-a")
	got := FanOut[fakeOwned](ev, nil, func(fakeOwned) bool { return true })
	if len(got) != 0 {
		t.Errorf("FanOut over no subscribers must return nothing, got %v", got)
	}
}

func TestFanOut_ExcludesOtherTenantsEvenWhenPredicateMatchesEverything(t *testing.T) {
	ev := fakeOwned("victim")
	subs := []fakeOwned{"attacker", "victim", "attacker", "another-tenant"}
	// A predicate that says "yes" to everything must not be able to widen the
	// tenant boundary. This is the exact defect ADR-TENANCY-002 fixed: a
	// no-filter subscription selected every tenant's events.
	got := FanOut(ev, subs, func(fakeOwned) bool { return true })
	if len(got) != 1 || got[0] != "victim" {
		t.Fatalf("FanOut must select only the event's own tenant regardless of the predicate, got %v", got)
	}
}

func TestFanOut_PredicateNarrowsWithinTheOwningTenant(t *testing.T) {
	ev := fakeOwned("tenant-a")
	subs := []fakeOwned{"tenant-a", "tenant-a"}
	calls := 0
	got := FanOut(ev, subs, func(fakeOwned) bool {
		calls++
		return calls == 1 // only the first same-tenant subscriber matches
	})
	if len(got) != 1 {
		t.Fatalf("predicate must be able to narrow within the owning tenant, got %v", got)
	}
}

func TestFanOut_DuplicateMatchingSubscribersAreAllReturned(t *testing.T) {
	ev := fakeOwned("tenant-a")
	subs := []fakeOwned{"tenant-a", "tenant-a", "tenant-a"}
	got := FanOut(ev, subs, func(fakeOwned) bool { return true })
	if len(got) != 3 {
		t.Fatalf("FanOut must not deduplicate — every matching subscriber row must be returned, got %v", got)
	}
}

func TestFanOut_MultiTenantInputReturnsOnlyTheMatchingSubset(t *testing.T) {
	ev := fakeOwned("tenant-b")
	subs := []fakeOwned{"tenant-a", "tenant-b", "tenant-c", "tenant-b", "tenant-a"}
	got := FanOut(ev, subs, func(fakeOwned) bool { return true })
	if len(got) != 2 || got[0] != "tenant-b" || got[1] != "tenant-b" {
		t.Fatalf("FanOut over mixed-tenant subscribers = %v, want exactly the tenant-b rows in order", got)
	}
}

// ---- AuthorizeExecution ---------------------------------------------------
//
// ADR-TENANCY-003: the check a privileged worker performs immediately before
// an outbound side effect. Work whose owner is unknown or differs from the
// target's must be refused.

func TestAuthorizeExecution_AllowsMatchingOwners(t *testing.T) {
	if err := AuthorizeExecution(fakeOwned("tenant-a"), fakeOwned("tenant-a")); err != nil {
		t.Errorf("same-tenant target and work must be authorized, got %v", err)
	}
}

func TestAuthorizeExecution_DeniesCrossTenantWork(t *testing.T) {
	err := AuthorizeExecution(fakeOwned("victim"), fakeOwned("attacker"))
	if err == nil {
		t.Fatal("cross-tenant target/work must be refused, got nil error")
	}
	if !errors.Is(err, ErrOwnershipMismatch) {
		t.Errorf("cross-tenant refusal must be (or wrap) ErrOwnershipMismatch, got %v", err)
	}
}

func TestAuthorizeExecution_FailsClosedOnUnknownOwner(t *testing.T) {
	cases := []struct {
		name         string
		target, work Owned
	}{
		{"target unknown", fakeOwned(""), fakeOwned("tenant-a")},
		{"work unknown", fakeOwned("tenant-a"), fakeOwned("")},
		{"both unknown", fakeOwned(""), fakeOwned("")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := AuthorizeExecution(c.target, c.work); !errors.Is(err, ErrOwnershipMismatch) {
				t.Errorf("%s must be refused (fail closed), got %v", c.name, err)
			}
		})
	}
}

// ---- ResolveAndAuthorize --------------------------------------------------
//
// ADR-TENANCY-003/004: the one execution-security check every privileged
// consumer performs. It must never trust the queue row; it re-derives the
// owner from the resolver and refuses on mismatch or resolver error,
// including the ADR-TENANCY-004 ambiguity signal.

type stubResolver struct {
	owner string
	err   error
	// calledWith records the arguments the resolver was invoked with, so a
	// test can prove ResolveAndAuthorize consulted the authoritative source
	// and not some other value.
	calledWith struct {
		chainID string
		seq     int64
	}
}

func (s *stubResolver) ResolveEventOwner(_ context.Context, chainID string, seq int64) (string, error) {
	s.calledWith.chainID = chainID
	s.calledWith.seq = seq
	return s.owner, s.err
}

func TestResolveAndAuthorize_AllowsWhenResolvedOwnerMatchesTarget(t *testing.T) {
	r := &stubResolver{owner: "tenant-a"}
	err := ResolveAndAuthorize(context.Background(), r, fakeOwned("tenant-a"), "chain-1", 7)
	if err != nil {
		t.Errorf("matching resolved owner must be authorized, got %v", err)
	}
	if r.calledWith.chainID != "chain-1" || r.calledWith.seq != 7 {
		t.Errorf("resolver must be called with the event's own coordinates, got %+v", r.calledWith)
	}
}

func TestResolveAndAuthorize_DeniesWhenResolvedOwnerDiffersFromTarget(t *testing.T) {
	// This is the exact forged-row scenario ADR-TENANCY-003 exists to defeat:
	// the queue/target claims one tenant, the authoritative source says another.
	r := &stubResolver{owner: "victim"}
	err := ResolveAndAuthorize(context.Background(), r, fakeOwned("attacker"), "chain-1", 7)
	if !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("resolved owner diverging from target must be refused with ErrOwnershipMismatch, got %v", err)
	}
}

func TestResolveAndAuthorize_NeverTrustsTheTargetsOwnLabelOverTheResolver(t *testing.T) {
	// Even a target that happens to claim the resolved tenant's name in some
	// other field must be judged only on OwnerTenantID() vs. the resolver's
	// answer — there is nothing else to trust. This pins that the comparison
	// is strictly resolver-owner vs. target-owner.
	r := &stubResolver{owner: "tenant-a"}
	target := fakeOwned("tenant-b")
	if err := ResolveAndAuthorize(context.Background(), r, target, "chain-1", 1); err == nil {
		t.Error("target's own claimed tenant must not override the resolver's authoritative answer")
	}
}

func TestResolveAndAuthorize_PropagatesEventNotFoundAndFailsClosed(t *testing.T) {
	r := &stubResolver{err: ErrEventNotFound}
	err := ResolveAndAuthorize(context.Background(), r, fakeOwned("tenant-a"), "chain-1", 1)
	if !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("ErrEventNotFound must propagate unchanged so the caller dead-letters, got %v", err)
	}
}

func TestResolveAndAuthorize_PropagatesOwnershipAmbiguityAndFailsClosed(t *testing.T) {
	// ADR-TENANCY-004: a resolver detecting object/audit-log divergence
	// returns ErrOwnershipAmbiguous. ResolveAndAuthorize must not swallow or
	// reinterpret it — ambiguous authority is refused, never guessed.
	r := &stubResolver{err: ErrOwnershipAmbiguous}
	err := ResolveAndAuthorize(context.Background(), r, fakeOwned("tenant-a"), "chain-1", 1)
	if !errors.Is(err, ErrOwnershipAmbiguous) {
		t.Fatalf("ErrOwnershipAmbiguous must propagate unchanged, got %v", err)
	}
}

func TestResolveAndAuthorize_PropagatesArbitraryResolverErrors(t *testing.T) {
	// An unavailable resolver must refuse rather than deliver (ADR-TENANCY-003).
	sentinel := errors.New("resolver unavailable")
	r := &stubResolver{err: sentinel}
	err := ResolveAndAuthorize(context.Background(), r, fakeOwned("tenant-a"), "chain-1", 1)
	if !errors.Is(err, sentinel) {
		t.Fatalf("resolver errors must propagate unchanged rather than being masked, got %v", err)
	}
}

// ---- OwnerTenantID (the ownerString adapter) ------------------------------

func TestOwnerString_ReturnsItsOwnValueVerbatim(t *testing.T) {
	// ownerString adapts a re-derived owner id to Owned so
	// AuthorizeExecution compares the authoritative tenant, never a queue
	// label. It must not transform, trim, or normalise the value.
	for _, v := range []string{"", "tenant-a", "system"} {
		if got := ownerString(v).OwnerTenantID(); got != v {
			t.Errorf("ownerString(%q).OwnerTenantID() = %q, want %q", v, got, v)
		}
	}
}

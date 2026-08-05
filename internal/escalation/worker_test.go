package escalation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func newTestWorker(
	store *fakeStore, incidents *fakeIncidents, policies *fakePolicies,
	oncall *fakeOnCall, membership *fakeMembership, users *fakeUsers,
	notifier *fakeNotifier, metrics Metrics,
) *Worker {
	return NewWorker(store, incidents, policies, oncall, membership, users, notifier, metrics, quiet(), WorkerConfig{})
}

// ---- seed -> page ------------------------------------------------------

func TestWorker_PagesTier0ForOpenUnackedIncident(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
	incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"))
	policies := newFakePolicies()
	policies.setTiers("pol-1", newTier("pol-1", 0, "sched-0", 300))
	oncall := newFakeOnCall()
	oncall.set("sched-0", "usr-1")
	membership := newFakeMembership()
	membership.setActive("usr-1", true)
	users := newFakeUsers()
	users.set("usr-1", "usr1@example.test")
	notifier := newFakeNotifier()
	m := &countMetrics{}

	w := newTestWorker(store, incidents, policies, oncall, membership, users, notifier, m)
	w.RunOnce(context.Background())

	if notifier.count() != 1 {
		t.Fatalf("notifications enqueued = %d, want 1", notifier.count())
	}
	n := notifier.last()
	if n.Channel != domain.NotificationEmail || n.Recipient != "usr1@example.test" || n.TenantID != "tn-1" {
		t.Errorf("notification = %+v, want email to usr1@example.test for tn-1", n)
	}
	got := store.get("st1")
	if got.CurrentTierIndex != 1 {
		t.Errorf("CurrentTierIndex = %d, want 1", got.CurrentTierIndex)
	}
	if !got.NextAttemptAt.After(now) {
		t.Error("NextAttemptAt was not advanced")
	}
	if got.Status != domain.EscalationStateActive {
		t.Errorf("Status = %q, want active", got.Status)
	}
	if m.paged != 1 || m.escalated != 1 {
		t.Errorf("metrics paged=%d escalated=%d, want 1/1", m.paged, m.escalated)
	}
}

// ---- escalate: still unacked after wait_seconds -------------------------

func TestWorker_EscalatesToNextTierWhenStillUnacked(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 1, now))
	incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"))
	policies := newFakePolicies()
	policies.setTiers("pol-1",
		newTier("pol-1", 0, "sched-0", 60),
		newTier("pol-1", 1, "sched-1", 120))
	oncall := newFakeOnCall()
	oncall.set("sched-1", "usr-2")
	membership := newFakeMembership()
	membership.setActive("usr-2", true)
	users := newFakeUsers()
	users.set("usr-2", "usr2@example.test")
	notifier := newFakeNotifier()

	w := newTestWorker(store, incidents, policies, oncall, membership, users, notifier, nil)
	w.RunOnce(context.Background())

	if notifier.count() != 1 || notifier.last().Recipient != "usr2@example.test" {
		t.Fatalf("expected tier-1's on-call user to be paged, got %+v", notifier.out)
	}
	got := store.get("st1")
	if got.CurrentTierIndex != 2 {
		t.Errorf("CurrentTierIndex = %d, want 2", got.CurrentTierIndex)
	}
}

// ---- exhausted: no further pages after the last tier ---------------------

func TestWorker_ExhaustedAfterLastTier(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 1, now))
	incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"))
	policies := newFakePolicies()
	policies.setTiers("pol-1", newTier("pol-1", 0, "sched-0", 60)) // only ONE tier; index 1 is past it
	notifier := newFakeNotifier()
	m := &countMetrics{}

	w := newTestWorker(store, incidents, policies, newFakeOnCall(), newFakeMembership(), newFakeUsers(), notifier, m)
	w.RunOnce(context.Background())

	if notifier.count() != 0 {
		t.Errorf("an exhausted state paged %d times, want 0", notifier.count())
	}
	got := store.get("st1")
	if got.Status != domain.EscalationStateExhausted {
		t.Errorf("Status = %q, want exhausted", got.Status)
	}
	if m.exhausted != 1 {
		t.Errorf("exhausted metric = %d, want 1", m.exhausted)
	}

	// A second cycle must not page again — exhausted is terminal.
	w.RunOnce(context.Background())
	if notifier.count() != 0 {
		t.Error("an exhausted state was reprocessed and paged")
	}
}

func TestWorker_EmptyTierLadder_ExhaustsImmediately(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
	incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"))
	policies := newFakePolicies() // pol-1 has no tiers at all
	notifier := newFakeNotifier()

	w := newTestWorker(store, incidents, policies, newFakeOnCall(), newFakeMembership(), newFakeUsers(), notifier, nil)
	w.RunOnce(context.Background())

	if notifier.count() != 0 {
		t.Error("a policy with zero tiers must not page")
	}
	if got := store.get("st1"); got.Status != domain.EscalationStateExhausted {
		t.Errorf("Status = %q, want exhausted", got.Status)
	}
}

// ---- ack/resolve cancels ---------------------------------------------------

func TestWorker_AckedIncident_StopsEscalationAndNeverPages(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
	inc := newOpenIncident("inc-1", "tn-1")
	inc.Status = domain.IncidentAcknowledged
	incidents := newFakeIncidents(inc)
	policies := newFakePolicies()
	policies.setTiers("pol-1", newTier("pol-1", 0, "sched-0", 60))
	oncall := newFakeOnCall()
	oncall.set("sched-0", "usr-1") // an on-call user EXISTS — the ack must still win
	membership := newFakeMembership()
	membership.setActive("usr-1", true)
	notifier := newFakeNotifier()
	m := &countMetrics{}

	w := newTestWorker(store, incidents, policies, oncall, membership, newFakeUsers(), notifier, m)
	w.RunOnce(context.Background())

	if notifier.count() != 0 {
		t.Errorf("an acknowledged incident was paged %d times, want 0", notifier.count())
	}
	if got := store.get("st1"); got.Status != domain.EscalationStateAcked {
		t.Errorf("Status = %q, want acked", got.Status)
	}
	if m.acked != 1 {
		t.Errorf("acked metric = %d, want 1", m.acked)
	}
}

func TestWorker_ResolvedIncident_StopsEscalation(t *testing.T) {
	for _, status := range []domain.IncidentStatus{
		domain.IncidentInvestigating, domain.IncidentResolved, domain.IncidentClosed, domain.IncidentReopened,
	} {
		t.Run(string(status), func(t *testing.T) {
			now := time.Now().UTC()
			store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
			inc := newOpenIncident("inc-1", "tn-1")
			inc.Status = status
			incidents := newFakeIncidents(inc)
			policies := newFakePolicies()
			policies.setTiers("pol-1", newTier("pol-1", 0, "sched-0", 60))
			notifier := newFakeNotifier()
			m := &countMetrics{}

			w := newTestWorker(store, incidents, policies, newFakeOnCall(), newFakeMembership(), newFakeUsers(), notifier, m)
			w.RunOnce(context.Background())

			if notifier.count() != 0 {
				t.Errorf("status %q was paged, want 0 pages", status)
			}
			if got := store.get("st1"); got.Status != domain.EscalationStateResolved {
				t.Errorf("Status = %q, want resolved", got.Status)
			}
			if m.resolved != 1 {
				t.Errorf("resolved metric = %d, want 1", m.resolved)
			}
		})
	}
}

// ---- page-time active-membership re-check (mutation-verified) ------------

func TestWorker_NoOnCallUser_AdvancesWithoutPaging(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
	incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"))
	policies := newFakePolicies()
	policies.setTiers("pol-1", newTier("pol-1", 0, "sched-empty", 60))
	notifier := newFakeNotifier()
	m := &countMetrics{}

	// sched-empty has an EMPTY roster: fakeOnCall returns a nil UserID.
	w := newTestWorker(store, incidents, policies, newFakeOnCall(), newFakeMembership(), newFakeUsers(), notifier, m)
	w.RunOnce(context.Background())

	if notifier.count() != 0 {
		t.Errorf("an empty roster was paged %d times, want 0", notifier.count())
	}
	got := store.get("st1")
	if got.CurrentTierIndex != 1 {
		t.Errorf("CurrentTierIndex = %d, want 1 — a revoked/absent on-call user must still advance the ladder", got.CurrentTierIndex)
	}
	if got.Status != domain.EscalationStateActive {
		t.Errorf("Status = %q, want still active (there is a further tier to try)", got.Status)
	}
	if m.skippedRevoked != 1 {
		t.Errorf("skippedRevoked metric = %d, want 1", m.skippedRevoked)
	}
	if m.paged != 0 {
		t.Errorf("paged metric = %d, want 0", m.paged)
	}
}

// TestWorker_RevokedMember_NotPaged_MutationVerified is the mutation-proof
// the review criteria demands: the SAME scenario with the on-call user
// present but NOT an active member must not page (the guard), and flipping
// membership to active — changing nothing else — must page (proving the
// check actually gates the send rather than being dead code).
func TestWorker_RevokedMember_NotPaged_MutationVerified(t *testing.T) {
	build := func(active bool) (*fakeStore, *fakeNotifier, *countMetrics) {
		now := time.Now().UTC()
		store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
		incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"))
		policies := newFakePolicies()
		policies.setTiers("pol-1", newTier("pol-1", 0, "sched-0", 60))
		oncall := newFakeOnCall()
		oncall.set("sched-0", "usr-revoked")
		membership := newFakeMembership()
		membership.setActive("usr-revoked", active)
		users := newFakeUsers()
		users.set("usr-revoked", "revoked@example.test")
		notifier := newFakeNotifier()
		m := &countMetrics{}
		w := newTestWorker(store, incidents, policies, oncall, membership, users, notifier, m)
		w.RunOnce(context.Background())
		return store, notifier, m
	}

	// The guard: a resolved-but-inactive on-call user is never paged.
	store, notifier, m := build(false)
	if notifier.count() != 0 {
		t.Fatalf("a revoked on-call user was paged %d times, want 0", notifier.count())
	}
	if got := store.get("st1"); got.CurrentTierIndex != 1 {
		t.Errorf("CurrentTierIndex = %d, want 1 — advance past the tier a revoked user cannot satisfy", got.CurrentTierIndex)
	}
	if m.skippedRevoked != 1 || m.paged != 0 {
		t.Errorf("metrics skippedRevoked=%d paged=%d, want 1/0", m.skippedRevoked, m.paged)
	}

	// The bite: change nothing except membership status — an active member IS
	// paged. If the check were dead code (e.g. always false, or never
	// consulted), this second run would also come back with zero pages.
	_, notifier2, m2 := build(true)
	if notifier2.count() != 1 {
		t.Fatalf("an active member was not paged: %d notifications, want 1 — the membership check "+
			"does not actually gate the send (it either always skips or is not consulted)", notifier2.count())
	}
	if m2.paged != 1 || m2.skippedRevoked != 0 {
		t.Errorf("metrics paged=%d skippedRevoked=%d, want 1/0", m2.paged, m2.skippedRevoked)
	}
}

// ---- no double-page under concurrent workers ------------------------------

// TestWorker_ConcurrentRunOnce_NeverPagesTheSameTierTwice forces the double
// -claim scenario the review criteria requires: two Workers sharing the same
// Store race to claim and process the same due row. The fenced claim
// (claimed_at) plus the due/unclaimed predicate ClaimDue applies — the fake's
// stand-in for FOR UPDATE SKIP LOCKED — must let exactly one of them win.
func TestWorker_ConcurrentRunOnce_NeverPagesTheSameTierTwice(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
	incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"))
	policies := newFakePolicies()
	policies.setTiers("pol-1", newTier("pol-1", 0, "sched-0", 60))
	oncall := newFakeOnCall()
	oncall.set("sched-0", "usr-1")
	membership := newFakeMembership()
	membership.setActive("usr-1", true)
	users := newFakeUsers()
	users.set("usr-1", "usr1@example.test")
	notifier := newFakeNotifier()

	w1 := newTestWorker(store, incidents, policies, oncall, membership, users, notifier, nil)
	w2 := newTestWorker(store, incidents, policies, oncall, membership, users, notifier, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	for _, w := range []*Worker{w1, w2} {
		w := w
		go func() {
			defer wg.Done()
			w.RunOnce(context.Background())
		}()
	}
	wg.Wait()

	if notifier.count() != 1 {
		t.Fatalf("tier 0 was paged %d times by two concurrent workers, want exactly 1", notifier.count())
	}
	if got := store.get("st1"); got.CurrentTierIndex != 1 {
		t.Errorf("CurrentTierIndex = %d, want 1 (advanced exactly once)", got.CurrentTierIndex)
	}
}

// TestWorker_StaleClaimFence_OutcomeNotOverwritten proves the OTHER half of
// ADR-CONCURRENCY-005: a Worker whose claim was reclaimed (e.g. its lease
// expired mid-processing) must not overwrite whatever the new owner already
// decided — Store.Advance/MarkTerminal must be presented the exact claim
// token ClaimDue handed out, and a stale one is rejected rather than applied.
func TestWorker_StaleClaimFence_OutcomeNotOverwritten(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))

	// "Worker A" claims the row.
	claimedA, err := store.ClaimDue(context.Background(), now, time.Minute, 10)
	if err != nil || len(claimedA) != 1 {
		t.Fatalf("claim: %v, %d rows", err, len(claimedA))
	}
	staleToken := claimedA[0].ClaimedAt

	// The lease elapses and "Worker B" reclaims the same row with a fresh
	// token, then finishes it (marks it exhausted) before Worker A's own
	// outcome write ever lands.
	store.m["st1"].ClaimedAt = staleToken.Add(-2 * time.Minute)
	claimedB, err := store.ClaimDue(context.Background(), now, time.Minute, 10)
	if err != nil || len(claimedB) != 1 {
		t.Fatalf("reclaim: %v, %d rows", err, len(claimedB))
	}
	if err := store.MarkTerminal(context.Background(), "st1", claimedB[0].ClaimedAt, domain.EscalationStateExhausted); err != nil {
		t.Fatalf("worker B's terminal write: %v", err)
	}

	// Worker A's own (stale) outcome write must be fenced off, not applied.
	if err := store.Advance(context.Background(), "st1", staleToken, 1, now.Add(time.Minute)); err == nil {
		t.Fatal("worker A's stale-token Advance succeeded — it must be fenced (ADR-CONCURRENCY-005)")
	}

	got := store.get("st1")
	if got.Status != domain.EscalationStateExhausted {
		t.Errorf("worker A's late write overwrote worker B's outcome: status = %q, want exhausted", got.Status)
	}
	if got.CurrentTierIndex != 0 {
		t.Errorf("worker A's late Advance moved current_tier_index to %d despite being fenced", got.CurrentTierIndex)
	}
}

// ---- error paths ------------------------------------------------------

func TestWorker_ClaimError_LogsAndCountsWithoutPanicking(t *testing.T) {
	store := newFakeStore()
	store.claimErr = errBoom
	m := &countMetrics{}

	w := newTestWorker(store, newFakeIncidents(), newFakePolicies(), newFakeOnCall(), newFakeMembership(), newFakeUsers(), newFakeNotifier(), m)
	w.RunOnce(context.Background()) // must not panic

	if m.errors != 1 {
		t.Errorf("errors metric = %d, want 1", m.errors)
	}
}

func TestWorker_IncidentReadError_ReleasesClaimAndCountsError(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now))
	incidents := newFakeIncidents()
	incidents.err = errBoom // the incident read itself fails
	m := &countMetrics{}

	w := newTestWorker(store, incidents, newFakePolicies(), newFakeOnCall(), newFakeMembership(), newFakeUsers(), newFakeNotifier(), m)
	w.RunOnce(context.Background())

	if m.errors == 0 {
		t.Error("an incident read failure must be counted as an error")
	}
	got := store.get("st1")
	if !got.ClaimedAt.IsZero() {
		t.Error("a row left unprocessed by a read failure must have its claim released, not held for a full lease")
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (the charged attempt must be given back on release)", got.Attempts)
	}
}

func TestWorker_MidBatchCancellation_ReleasesUnprocessedClaims(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(
		newActiveState("st1", "tn-1", "inc-1", "pol-1", 0, now),
		newActiveState("st2", "tn-1", "inc-2", "pol-1", 0, now),
	)
	incidents := newFakeIncidents(newOpenIncident("inc-1", "tn-1"), newOpenIncident("inc-2", "tn-1"))
	policies := newFakePolicies()
	policies.setTiers("pol-1", newTier("pol-1", 0, "sched-0", 60))

	w := newTestWorker(store, incidents, policies, newFakeOnCall(), newFakeMembership(), newFakeUsers(), newFakeNotifier(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: RunOnce claims, then must release the whole batch untouched
	w.RunOnce(ctx)

	for _, id := range []string{"st1", "st2"} {
		got := store.get(id)
		if !got.ClaimedAt.IsZero() {
			t.Errorf("%s: claim was not released after mid-batch cancellation", id)
		}
		if got.Attempts != 0 {
			t.Errorf("%s: attempts = %d, want 0 after release", id, got.Attempts)
		}
		if got.CurrentTierIndex != 0 {
			t.Errorf("%s: current_tier_index = %d, want 0 (never processed)", id, got.CurrentTierIndex)
		}
	}
}

// ---- tenant re-derivation --------------------------------------------------

// TestWorker_BindsEachTenantScopedReadToTheStateRowsOwnTenant proves
// ADR-TENANCY-003 is actually exercised: the incident/policy/on-call reads
// are bound to st.TenantID via domain.WithTenant on every call, not a fixed
// or ambient tenant.
func TestWorker_BindsEachTenantScopedReadToTheStateRowsOwnTenant(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeStore(
		newActiveState("st-a", "tn-a", "inc-a", "pol-a", 0, now),
		newActiveState("st-b", "tn-b", "inc-b", "pol-b", 0, now),
	)
	incidents := newFakeIncidents(newOpenIncident("inc-a", "tn-a"), newOpenIncident("inc-b", "tn-b"))
	policies := newFakePolicies()
	policies.setTiers("pol-a", newTier("pol-a", 0, "sched-a", 60))
	policies.setTiers("pol-b", newTier("pol-b", 0, "sched-b", 60))
	oncall := newFakeOnCall()
	oncall.set("sched-a", "usr-a")
	oncall.set("sched-b", "usr-b")
	membership := newFakeMembership()
	membership.setActive("usr-a", true)
	membership.setActive("usr-b", true)
	users := newFakeUsers()
	users.set("usr-a", "a@example.test")
	users.set("usr-b", "b@example.test")
	notifier := newFakeNotifier()

	w := newTestWorker(store, incidents, policies, oncall, membership, users, notifier, nil)
	w.RunOnce(context.Background())

	if notifier.count() != 2 {
		t.Fatalf("notifications = %d, want 2 (one per tenant, never crossed)", notifier.count())
	}
	recipients := map[string]string{}
	for _, n := range notifier.out {
		recipients[n.TenantID] = n.Recipient
	}
	if recipients["tn-a"] != "a@example.test" || recipients["tn-b"] != "b@example.test" {
		t.Errorf("tenant->recipient = %+v, want tn-a->a@example.test, tn-b->b@example.test (no cross-tenant mixing)", recipients)
	}
}

package escalation

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore is an in-memory Store. Its ClaimDue/Advance/MarkTerminal mirror
// the real store's contract closely enough to exercise the Worker: the claim
// charges the attempt, a claimed row is excluded until its lease elapses, and
// every outcome write is fenced on the claim token — the exact shape
// notification's own fakeStore uses for its worker.
type fakeStore struct {
	mu sync.Mutex
	m  map[string]*domain.EscalationState

	seedFn func(now time.Time) (seeded, skippedNoPolicy int, err error)

	claimErr   error
	releaseErr error
	staleIDs   map[string]bool
}

func newFakeStore(seed ...*domain.EscalationState) *fakeStore {
	f := &fakeStore{m: map[string]*domain.EscalationState{}, staleIDs: map[string]bool{}}
	for _, s := range seed {
		cp := *s
		f.m[s.StateID] = &cp
	}
	return f
}

func (f *fakeStore) Seed(_ context.Context, now time.Time) (int, int, error) {
	if f.seedFn != nil {
		return f.seedFn(now)
	}
	return 0, 0, nil
}

func (f *fakeStore) ClaimDue(_ context.Context, now time.Time, lease time.Duration, limit int) ([]*domain.EscalationState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	var out []*domain.EscalationState
	for _, st := range f.m {
		due := !st.NextAttemptAt.After(now)
		unclaimed := st.ClaimedAt.IsZero() || st.ClaimedAt.Before(now.Add(-lease))
		if st.Status == domain.EscalationStateActive && due && unclaimed {
			st.Attempts++
			st.ClaimedAt = now
			cp := *st
			out = append(out, &cp)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeStore) Advance(_ context.Context, stateID string, claimToken time.Time, nextTierIndex int, nextAttemptAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staleIDs[stateID] {
		return ErrStaleClaim
	}
	st := f.m[stateID]
	if st == nil || st.ClaimedAt != claimToken {
		return ErrStaleClaim
	}
	st.CurrentTierIndex = nextTierIndex
	st.NextAttemptAt = nextAttemptAt
	st.ClaimedAt = time.Time{}
	return nil
}

func (f *fakeStore) MarkTerminal(_ context.Context, stateID string, claimToken time.Time, status domain.EscalationStateStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staleIDs[stateID] {
		return ErrStaleClaim
	}
	st := f.m[stateID]
	if st == nil || st.ClaimedAt != claimToken {
		return ErrStaleClaim
	}
	st.Status = status
	st.ClaimedAt = time.Time{}
	return nil
}

func (f *fakeStore) ReleaseClaim(_ context.Context, stateID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return f.releaseErr
	}
	st := f.m[stateID]
	if st == nil {
		return nil
	}
	if st.Attempts > 0 {
		st.Attempts--
	}
	st.ClaimedAt = time.Time{}
	return nil
}

func (f *fakeStore) get(id string) *domain.EscalationState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st := f.m[id]; st != nil {
		cp := *st
		return &cp
	}
	return nil
}

func newActiveState(id, tenantID, incidentID, policyID string, tierIndex int, due time.Time) *domain.EscalationState {
	return &domain.EscalationState{
		StateID: id, TenantID: tenantID, IncidentID: incidentID, PolicyID: policyID,
		CurrentTierIndex: tierIndex, NextAttemptAt: due, Status: domain.EscalationStateActive,
		RowVersion: 1, CreatedAt: due, UpdatedAt: due,
	}
}

// fakeIncidents is a fake IncidentReader.
type fakeIncidents struct {
	mu  sync.Mutex
	m   map[string]*domain.Incident
	err error
}

func newFakeIncidents(incs ...*domain.Incident) *fakeIncidents {
	f := &fakeIncidents{m: map[string]*domain.Incident{}}
	for _, inc := range incs {
		f.m[inc.IncidentID] = inc
	}
	return f
}

func (f *fakeIncidents) Get(_ context.Context, incidentID string) (*domain.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	inc, ok := f.m[incidentID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return inc, nil
}

func newOpenIncident(id, tenantID string) *domain.Incident {
	return &domain.Incident{
		IncidentID: id, TenantID: tenantID, Title: "t", Severity: domain.IncidentSeverityHigh,
		Status: domain.IncidentOpen, Source: domain.IncidentSourceAlert,
	}
}

// fakePolicies is a fake PolicyReader: policyID -> ordered tiers.
type fakePolicies struct {
	mu sync.Mutex
	m  map[string][]*domain.EscalationTier
}

func newFakePolicies() *fakePolicies { return &fakePolicies{m: map[string][]*domain.EscalationTier{}} }

func (f *fakePolicies) setTiers(policyID string, tiers ...*domain.EscalationTier) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[policyID] = tiers
}

func (f *fakePolicies) ListTiers(_ context.Context, policyID string, limit int, _ string) ([]*domain.EscalationTier, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.m[policyID]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newTier(policyID string, position int, scheduleID string, waitSeconds int) *domain.EscalationTier {
	return &domain.EscalationTier{
		TierID: "tier_" + policyID + "_" + string(rune('0'+position)), PolicyID: policyID, Position: position,
		OnCallScheduleID: scheduleID, WaitSeconds: waitSeconds, RowVersion: 1,
	}
}

// fakeOnCall is a fake OnCallResolver: scheduleID -> the userID on call (or
// none).
type fakeOnCall struct {
	mu sync.Mutex
	m  map[string]*string
}

func newFakeOnCall() *fakeOnCall { return &fakeOnCall{m: map[string]*string{}} }

func (f *fakeOnCall) set(scheduleID, userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := userID
	f.m[scheduleID] = &u
}

func (f *fakeOnCall) OnCall(_ context.Context, scheduleID string, at time.Time) (*domain.OnCallResolution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := &domain.OnCallResolution{ScheduleID: scheduleID, At: at}
	if u, ok := f.m[scheduleID]; ok && u != nil {
		res.UserID = u
	}
	return res, nil
}

// fakeMembership is a fake MembershipChecker: userID -> active?
type fakeMembership struct {
	mu sync.Mutex
	m  map[string]bool
}

func newFakeMembership() *fakeMembership { return &fakeMembership{m: map[string]bool{}} }

func (f *fakeMembership) setActive(userID string, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[userID] = active
}

func (f *fakeMembership) ActiveMember(_ context.Context, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[userID], nil
}

// fakeUsers is a fake UserReader: userID -> email.
type fakeUsers struct {
	mu sync.Mutex
	m  map[string]*domain.User
}

func newFakeUsers() *fakeUsers { return &fakeUsers{m: map[string]*domain.User{}} }

func (f *fakeUsers) set(userID, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[userID] = &domain.User{UserID: userID, Email: email}
}

func (f *fakeUsers) Get(_ context.Context, userID string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.m[userID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return u, nil
}

// fakeNotifier records every enqueued Notification.
type fakeNotifier struct {
	mu  sync.Mutex
	out []*domain.Notification
	err error
}

func newFakeNotifier() *fakeNotifier { return &fakeNotifier{} }

func (f *fakeNotifier) Enqueue(_ context.Context, n *domain.Notification) (*domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.out = append(f.out, n)
	return n, nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.out)
}

func (f *fakeNotifier) last() *domain.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.out) == 0 {
		return nil
	}
	return f.out[len(f.out)-1]
}

// countMetrics records every call, for assertions on which counters fired.
type countMetrics struct {
	mu                                                                                            sync.Mutex
	seeded, skippedNoPolicy, paged, escalated, skippedRevoked, acked, resolved, exhausted, errors int
}

func (m *countMetrics) IncSeeded(n int)          { m.mu.Lock(); m.seeded += n; m.mu.Unlock() }
func (m *countMetrics) IncSkippedNoPolicy(n int) { m.mu.Lock(); m.skippedNoPolicy += n; m.mu.Unlock() }
func (m *countMetrics) IncPaged()                { m.mu.Lock(); m.paged++; m.mu.Unlock() }
func (m *countMetrics) IncEscalated()            { m.mu.Lock(); m.escalated++; m.mu.Unlock() }
func (m *countMetrics) IncSkippedRevoked()       { m.mu.Lock(); m.skippedRevoked++; m.mu.Unlock() }
func (m *countMetrics) IncAcked()                { m.mu.Lock(); m.acked++; m.mu.Unlock() }
func (m *countMetrics) IncResolved()             { m.mu.Lock(); m.resolved++; m.mu.Unlock() }
func (m *countMetrics) IncExhausted()            { m.mu.Lock(); m.exhausted++; m.mu.Unlock() }
func (m *countMetrics) IncErrors()               { m.mu.Lock(); m.errors++; m.mu.Unlock() }

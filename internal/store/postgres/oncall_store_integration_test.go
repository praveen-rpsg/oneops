//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// onCallTestCtx carries only the tenant binding: unlike Incident/
// MaintenanceWindow, on_call_schedule/on_call_participant carry no actor
// column (domain.OnCallSchedule's doc comment — no created_by), so no
// domain.WithActor is needed.
func onCallTestCtx(tn *domain.Tenant) context.Context {
	return domain.WithTenant(context.Background(), tn)
}

func onCallTenant(t *testing.T, tenants *TenantStore, slug string) *domain.Tenant {
	t.Helper()
	tn, err := tenants.Create(adminTestCtx(), newTenant(slug, "ext-"+slug))
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

// onCallMemberFixture creates two organisations (=> two tenants) A and B, one
// user with an ACTIVE membership in tenant A only, and one user whose
// membership in tenant A has been revoked — mirroring incidentRefFixture's
// shape (asset_store_integration_test.go / incident_store_integration_test.go)
// exactly, applied to on_call_participant's own cross-tenant/non-active
// reference defense.
func onCallMemberFixture(t *testing.T) (orgA, orgB *domain.Organization, activeUser, revokedUser, foreignUser *domain.User) {
	t.Helper()
	priv := testPool(t)
	ctx := adminTestCtx()

	newOrg := func(prefix string) *domain.Organization {
		o, err := domain.NewOrganization(prefix+" Probe",
			strings.ToLower(prefix+"-"+ulid.Make().String()[:10]))
		if err != nil {
			t.Fatalf("new org %s: %v", prefix, err)
		}
		created, err := NewOrganizationStore(priv).Create(ctx, o)
		if err != nil {
			t.Fatalf("create org %s: %v", prefix, err)
		}
		return created
	}
	orgA = newOrg("oncall-a")
	orgB = newOrg("oncall-b")

	newActiveMember := func(org *domain.Organization) *domain.User {
		u := newTestUser(t)
		if _, err := NewUserStore(priv).Create(ctx, u); err != nil {
			t.Fatalf("create user: %v", err)
		}
		m, err := domain.NewMembership(org.OrgID, org.TenantID, u.UserID)
		if err != nil {
			t.Fatalf("new membership: %v", err)
		}
		if _, err := NewMembershipStore(priv).Grant(ctx, m); err != nil {
			t.Fatalf("grant membership: %v", err)
		}
		return u
	}

	activeUser = newActiveMember(orgA)

	revokedUser = newTestUser(t)
	if _, err := NewUserStore(priv).Create(ctx, revokedUser); err != nil {
		t.Fatalf("create revoked-to-be user: %v", err)
	}
	mr, err := domain.NewMembership(orgA.OrgID, orgA.TenantID, revokedUser.UserID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	granted, err := NewMembershipStore(priv).Grant(ctx, mr)
	if err != nil {
		t.Fatalf("grant membership: %v", err)
	}
	if _, err := NewMembershipStore(priv).Revoke(ctx, granted.MembershipID); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}

	foreignUser = newActiveMember(orgB)

	return orgA, orgB, activeUser, revokedUser, foreignUser
}

func TestOnCallScheduleStore_CreateGetListUpdateDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := onCallTenant(t, NewTenantStore(priv), "oncall-crud")

	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctx := onCallTestCtx(tn)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, err := domain.NewOnCallSchedule(tn.TenantID, "Platform On-Call", 3600, start)
	if err != nil {
		t.Fatalf("new schedule: %v", err)
	}
	created, err := store.Create(ctx, sch)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 || created.Status != domain.OnCallScheduleActive {
		t.Errorf("unexpected created schedule: %+v", created)
	}

	got, err := store.Get(ctx, created.ScheduleID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Platform On-Call" || got.HandoffIntervalSeconds != 3600 {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := store.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range list {
		if item.ScheduleID == created.ScheduleID {
			found = true
		}
	}
	if !found {
		t.Errorf("list did not include the created schedule: %+v", list)
	}

	newName := "Renamed Rotation"
	updated, err := store.Update(ctx, created.ScheduleID, created.RowVersion, domain.OnCallSchedulePatch{Name: &newName})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != newName || updated.RowVersion != created.RowVersion+1 {
		t.Errorf("unexpected update result: %+v", updated)
	}

	// Reusing the now-stale row_version must conflict, not silently apply.
	stale := "Stale Name"
	if _, err := store.Update(ctx, created.ScheduleID, created.RowVersion, domain.OnCallSchedulePatch{Name: &stale}); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale update err = %v, want ErrVersionMismatch", err)
	}

	if err := store.Delete(ctx, created.ScheduleID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, created.ScheduleID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, created.ScheduleID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete of an already-deleted schedule err = %v, want ErrNotFound", err)
	}
}

// THIS MUST BITE: tenant B cannot see, patch, delete or add a participant to
// tenant A's schedule — row-level security, not a WHERE clause the store
// remembered to add — even naming tenant B's own active member (whose
// user_id is a value tenant A has never heard of, ruling out a coincidental
// pass).
func TestOnCallScheduleStore_RLSIsolatesByTenant(t *testing.T) {
	orgA, orgB, _, _, foreignUser := onCallMemberFixture(t)
	a := &domain.Tenant{TenantID: orgA.TenantID}
	b := &domain.Tenant{TenantID: orgB.TenantID}

	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(a)
	ctxB := onCallTestCtx(b)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	schA, _ := domain.NewOnCallSchedule(a.TenantID, "Tenant A Rotation", 60, start)
	createdA, err := store.Create(ctxA, schA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	if _, err := store.Get(ctxB, createdA.ScheduleID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B read tenant A's schedule: err = %v, want ErrNotFound", err)
	}
	listB, err := store.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("tenant B saw tenant A's schedules: %+v", listB)
	}
	newName := "Hijacked"
	if _, err := store.Update(ctxB, createdA.ScheduleID, createdA.RowVersion, domain.OnCallSchedulePatch{Name: &newName}); !errors.Is(err, domain.ErrNotFound) {
		// Update reads the current row via Get first (mirrors
		// AlertRuleStore.Update), so a row RLS has already made invisible is
		// ErrNotFound before the UPDATE statement itself ever runs.
		t.Errorf("tenant B patched tenant A's schedule: err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctxB, createdA.ScheduleID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B deleted tenant A's schedule: err = %v, want ErrNotFound", err)
	}

	// Tenant B cannot add a participant to tenant A's schedule either, even
	// naming its own active member (foreignUser, who tenant A has never
	// heard of) — the schedule itself is invisible under RLS before the
	// membership check is ever reached.
	if _, err := store.AddParticipant(ctxB, createdA.ScheduleID, foreignUser.UserID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B added a participant to tenant A's schedule: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees and can use its own schedule, undisturbed.
	stillThere, err := store.Get(ctxA, createdA.ScheduleID)
	if err != nil || stillThere.ScheduleID != createdA.ScheduleID {
		t.Fatalf("tenant A lost its own schedule: %v, %+v", err, stillThere)
	}
}

// AddParticipant re-verifies ACTIVE membership before writing — mirroring
// IncidentStore.verifyAssigneeExists — and rejects both a cross-tenant user
// and a same-tenant but non-active (revoked) one, in both cases with
// ErrNotFound rather than a 500.
//
// Mutation-verified by hand (implementer's evidence report): removing the
// AddParticipant->verifyActiveMember call makes both sub-tests below pass a
// row that should have been refused.
func TestOnCallScheduleStore_AddParticipantRejectsNonActiveMember(t *testing.T) {
	orgA, _, _, revokedUser, foreignUser := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Membership Probe", 60, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	t.Run("cross-tenant user", func(t *testing.T) {
		if _, err := store.AddParticipant(ctxA, created.ScheduleID, foreignUser.UserID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("adding tenant B's member to tenant A's schedule = %v, want ErrNotFound", err)
		}
	})
	t.Run("revoked membership", func(t *testing.T) {
		if _, err := store.AddParticipant(ctxA, created.ScheduleID, revokedUser.UserID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("adding a revoked member = %v, want ErrNotFound", err)
		}
	})

	// Neither attempt left a row behind.
	participants, err := store.ListParticipants(ctxA, created.ScheduleID, 0, "")
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	if len(participants) != 0 {
		t.Fatalf("a rejected add left a row: %+v", participants)
	}
}

// AddParticipant appends at the next position; positions start at 0 and are
// assigned in insertion order.
func TestOnCallScheduleStore_AddParticipantAppendsAtNextPosition(t *testing.T) {
	orgA, _, activeUser, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Position Probe", 60, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	first, err := store.AddParticipant(ctxA, created.ScheduleID, activeUser.UserID)
	if err != nil {
		t.Fatalf("add first participant: %v", err)
	}
	if first.Position != 0 {
		t.Errorf("first participant position = %d, want 0", first.Position)
	}

	// A second, distinct active member of the same tenant.
	second := onCallExtraActiveMember(t, orgA)
	addedSecond, err := store.AddParticipant(ctxA, created.ScheduleID, second.UserID)
	if err != nil {
		t.Fatalf("add second participant: %v", err)
	}
	if addedSecond.Position != 1 {
		t.Errorf("second participant position = %d, want 1", addedSecond.Position)
	}

	third := onCallExtraActiveMember(t, orgA)
	addedThird, err := store.AddParticipant(ctxA, created.ScheduleID, third.UserID)
	if err != nil {
		t.Fatalf("add third participant: %v", err)
	}
	if addedThird.Position != 2 {
		t.Errorf("third participant position = %d, want 2", addedThird.Position)
	}
}

// Adding the same user twice is a conflict, not a silent no-op: unlike
// TeamMembership, a participant carries an ordering position that a
// duplicate add would leave ambiguous.
func TestOnCallScheduleStore_AddParticipantRejectsDuplicateUser(t *testing.T) {
	orgA, _, activeUser, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Duplicate Probe", 60, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if _, err := store.AddParticipant(ctxA, created.ScheduleID, activeUser.UserID); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := store.AddParticipant(ctxA, created.ScheduleID, activeUser.UserID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate add err = %v, want ErrConflict", err)
	}
}

// RemoveParticipant closes the resulting gap: positions stay contiguous
// 0..N-1, and who's-on-call recomputes immediately once the removed user is
// gone from the rotation.
func TestOnCallScheduleStore_RemoveParticipantCompactsPositionsAndRotationUpdates(t *testing.T) {
	orgA, _, activeUser, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Removal Probe", 3600, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	p0, err := store.AddParticipant(ctxA, created.ScheduleID, activeUser.UserID)
	if err != nil {
		t.Fatalf("add p0: %v", err)
	}
	u1 := onCallExtraActiveMember(t, orgA)
	p1, err := store.AddParticipant(ctxA, created.ScheduleID, u1.UserID)
	if err != nil {
		t.Fatalf("add p1: %v", err)
	}
	u2 := onCallExtraActiveMember(t, orgA)
	p2, err := store.AddParticipant(ctxA, created.ScheduleID, u2.UserID)
	if err != nil {
		t.Fatalf("add p2: %v", err)
	}

	// Remove the middle participant (position 1).
	if err := store.RemoveParticipant(ctxA, created.ScheduleID, p1.ParticipantID); err != nil {
		t.Fatalf("remove p1: %v", err)
	}

	remaining, err := store.ListParticipants(ctxA, created.ScheduleID, 0, "")
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("%d participants remain, want 2", len(remaining))
	}
	if remaining[0].ParticipantID != p0.ParticipantID || remaining[0].Position != 0 {
		t.Errorf("remaining[0] = %+v, want p0 at position 0", remaining[0])
	}
	if remaining[1].ParticipantID != p2.ParticipantID || remaining[1].Position != 1 {
		t.Errorf("remaining[1] = %+v, want p2 shifted to position 1", remaining[1])
	}

	// A second, still-live participant removed leaves a single-participant,
	// contiguous roster.
	if err := store.RemoveParticipant(ctxA, created.ScheduleID, p0.ParticipantID); err != nil {
		t.Fatalf("remove p0: %v", err)
	}
	final, err := store.ListParticipants(ctxA, created.ScheduleID, 0, "")
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	if len(final) != 1 || final[0].ParticipantID != p2.ParticipantID || final[0].Position != 0 {
		t.Fatalf("final roster = %+v, want [p2 at position 0]", final)
	}

	// Removing an unknown participant is a not-found, not a silent success.
	if err := store.RemoveParticipant(ctxA, created.ScheduleID, "no-such-participant"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("remove unknown participant err = %v, want ErrNotFound", err)
	}

	// Who's on call now reflects the single surviving participant, and the
	// removed user never appears again regardless of `at`.
	res, err := store.OnCall(ctxA, created.ScheduleID, start.Add(10000*time.Hour))
	if err != nil {
		t.Fatalf("on-call: %v", err)
	}
	if res.UserID == nil || *res.UserID != u2.UserID {
		t.Errorf("on-call = %+v, want %s", res, u2.UserID)
	}
}

// ReorderParticipants atomically replaces the rotation order, positions stay
// contiguous, and who's-on-call reflects the new order immediately.
func TestOnCallScheduleStore_ReorderParticipantsIsAtomicAndRotationReflectsImmediately(t *testing.T) {
	orgA, _, u0, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Reorder Probe", 60, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	p0, err := store.AddParticipant(ctxA, created.ScheduleID, u0.UserID)
	if err != nil {
		t.Fatalf("add p0: %v", err)
	}
	u1 := onCallExtraActiveMember(t, orgA)
	p1, err := store.AddParticipant(ctxA, created.ScheduleID, u1.UserID)
	if err != nil {
		t.Fatalf("add p1: %v", err)
	}
	u2 := onCallExtraActiveMember(t, orgA)
	p2, err := store.AddParticipant(ctxA, created.ScheduleID, u2.UserID)
	if err != nil {
		t.Fatalf("add p2: %v", err)
	}

	// Reverse the order: p2, p0, p1 — exercising a genuine permutation
	// (position 0 and 2 swap, not merely an append), which is exactly the
	// shape that would collide against a non-deferred unique constraint.
	reordered, err := store.ReorderParticipants(ctxA, created.ScheduleID, []string{p2.ParticipantID, p0.ParticipantID, p1.ParticipantID})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if len(reordered) != 3 {
		t.Fatalf("reorder returned %d participants, want 3", len(reordered))
	}
	wantOrder := []string{p2.ParticipantID, p0.ParticipantID, p1.ParticipantID}
	for i, want := range wantOrder {
		if reordered[i].ParticipantID != want || reordered[i].Position != i {
			t.Errorf("reordered[%d] = %+v, want participant %s at position %d", i, reordered[i], want, i)
		}
	}

	// Who's on call now reflects the NEW order immediately: at exactly
	// rotation_start_at, the first seat (now p2's user) holds it.
	res, err := store.OnCall(ctxA, created.ScheduleID, start)
	if err != nil {
		t.Fatalf("on-call: %v", err)
	}
	if res.UserID == nil || *res.UserID != u2.UserID {
		t.Errorf("on-call at rotation start after reorder = %+v, want %s (the new first participant)", res, u2.UserID)
	}
}

// A reorder naming anything other than exactly the schedule's current
// participant set — missing one, adding a foreign one, or repeating one — is
// refused before anything is written.
func TestOnCallScheduleStore_ReorderParticipantsRejectsMismatchedSet(t *testing.T) {
	orgA, _, u0, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Mismatch Probe", 60, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	p0, err := store.AddParticipant(ctxA, created.ScheduleID, u0.UserID)
	if err != nil {
		t.Fatalf("add p0: %v", err)
	}
	u1 := onCallExtraActiveMember(t, orgA)
	p1, err := store.AddParticipant(ctxA, created.ScheduleID, u1.UserID)
	if err != nil {
		t.Fatalf("add p1: %v", err)
	}

	cases := [][]string{
		{p0.ParticipantID}, // missing one
		{p0.ParticipantID, p1.ParticipantID, "no-such-id"}, // extra, unknown id
		{p0.ParticipantID, p0.ParticipantID},               // repeated, still short of p1
	}
	for _, ids := range cases {
		if _, err := store.ReorderParticipants(ctxA, created.ScheduleID, ids); err == nil {
			t.Errorf("reorder with %v was accepted, want a validation error", ids)
		} else if _, ok := domain.AsValidation(err); !ok {
			t.Errorf("reorder with %v err = %v, want a *domain.ValidationError", ids, err)
		}
	}

	// Positions are untouched by every rejected attempt.
	still, err := store.ListParticipants(ctxA, created.ScheduleID, 0, "")
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	if len(still) != 2 || still[0].ParticipantID != p0.ParticipantID || still[1].ParticipantID != p1.ParticipantID {
		t.Fatalf("roster changed after a rejected reorder: %+v", still)
	}
}

// OnCall is a deterministic, pure-of-the-store read: it cross-checks the
// store's answer against domain.OnCallRotationIndex applied directly to the
// same inputs, across several handoffs.
func TestOnCallScheduleStore_OnCallMatchesTheDomainRotationFunction(t *testing.T) {
	orgA, _, u0, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const interval = 1800
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Rotation Probe", interval, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	users := []*domain.User{u0, onCallExtraActiveMember(t, orgA), onCallExtraActiveMember(t, orgA)}
	userIDs := make([]string, len(users))
	for i, u := range users {
		if _, err := store.AddParticipant(ctxA, created.ScheduleID, u.UserID); err != nil {
			t.Fatalf("add participant %d: %v", i, err)
		}
		userIDs[i] = u.UserID
	}

	for k := 0; k < 7; k++ {
		at := start.Add(time.Duration(k) * interval * time.Second)
		wantIdx, ok := domain.OnCallRotationIndex(len(userIDs), start, interval, at)
		if !ok {
			t.Fatalf("handoff %d: domain function returned ok=false unexpectedly", k)
		}
		res, err := store.OnCall(ctxA, created.ScheduleID, at)
		if err != nil {
			t.Fatalf("handoff %d: on-call: %v", k, err)
		}
		if res.UserID == nil || *res.UserID != userIDs[wantIdx] {
			t.Errorf("handoff %d (at=%v): on-call = %+v, want participant %d (%s)",
				k, at, res, wantIdx, userIDs[wantIdx])
		}
	}
}

// N=0: an empty roster is a valid, non-error state — not a 500, not
// ErrNotFound (the schedule itself exists).
func TestOnCallScheduleStore_OnCallWithNoParticipantsIsEmptyNotAnError(t *testing.T) {
	orgA, _, _, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Empty Probe", 60, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	res, err := store.OnCall(ctxA, created.ScheduleID, start)
	if err != nil {
		t.Fatalf("on-call on an empty roster returned an error, want a clean empty result: %v", err)
	}
	if res.UserID != nil || res.DisplayName != nil {
		t.Errorf("on-call on an empty roster = %+v, want nil user_id/display_name", res)
	}
}

// A nonexistent schedule is ErrNotFound, not a panic or an empty result.
func TestOnCallScheduleStore_OnCallOnUnknownScheduleIsNotFound(t *testing.T) {
	orgA, _, _, _, _ := onCallMemberFixture(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	if _, err := store.OnCall(ctxA, "no-such-schedule", time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("on-call for an unknown schedule err = %v, want ErrNotFound", err)
	}
}

// Deleting a schedule removes its participants with it (ON DELETE CASCADE) —
// proven both by direct row count and by a subsequent Get returning
// ErrNotFound for the schedule itself.
func TestOnCallScheduleStore_DeleteCascadesToParticipants(t *testing.T) {
	orgA, _, u0, _, _ := onCallMemberFixture(t)
	priv := testPool(t)
	scoped := tenantScopedPool(t)
	store := NewOnCallScheduleStore(scoped)
	ctxA := onCallTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, _ := domain.NewOnCallSchedule(orgA.TenantID, "Cascade Probe", 60, start)
	created, err := store.Create(ctxA, sch)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if _, err := store.AddParticipant(ctxA, created.ScheduleID, u0.UserID); err != nil {
		t.Fatalf("add participant: %v", err)
	}

	if err := store.Delete(ctxA, created.ScheduleID); err != nil {
		t.Fatalf("delete schedule: %v", err)
	}

	var n int
	if err := priv.QueryRow(context.Background(),
		`SELECT count(*) FROM on_call_participant WHERE schedule_id = $1`, created.ScheduleID).Scan(&n); err != nil {
		t.Fatalf("count participants: %v", err)
	}
	if n != 0 {
		t.Errorf("%d participant row(s) survived their schedule's deletion, want 0 (ON DELETE CASCADE)", n)
	}

	if _, err := store.OnCall(ctxA, created.ScheduleID, start); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("on-call after cascade delete err = %v, want ErrNotFound", err)
	}
}

// onCallExtraActiveMember grants an additional, distinct active member of
// org's tenant — used where a test needs more than one participant beyond
// onCallMemberFixture's own activeUser.
func onCallExtraActiveMember(t *testing.T, org *domain.Organization) *domain.User {
	t.Helper()
	priv := testPool(t)
	ctx := adminTestCtx()
	u := newTestUser(t)
	if _, err := NewUserStore(priv).Create(ctx, u); err != nil {
		t.Fatalf("create extra user: %v", err)
	}
	m, err := domain.NewMembership(org.OrgID, org.TenantID, u.UserID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	if _, err := NewMembershipStore(priv).Grant(ctx, m); err != nil {
		t.Fatalf("grant membership: %v", err)
	}
	return u
}

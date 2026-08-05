package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNewOnCallSchedule_BuildsAnActiveSchedule(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := NewOnCallSchedule(" tn-1 ", " Platform On-Call ", 3600, start)
	if err != nil {
		t.Fatalf("NewOnCallSchedule: %v", err)
	}
	if s.TenantID != "tn-1" || s.Name != "Platform On-Call" {
		t.Errorf("identifiers/name not trimmed: %+v", s)
	}
	if s.Status != OnCallScheduleActive || !s.Active() {
		t.Errorf("a new schedule must be active: %+v", s)
	}
	if s.ScheduleID == "" {
		t.Error("schedule_id must be minted")
	}
	if !s.RotationStartAt.Equal(start) {
		t.Errorf("rotation_start_at = %v, want %v", s.RotationStartAt, start)
	}
}

func TestNewOnCallSchedule_RequiresEveryField(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name     string
		tenant   string
		schedule string
		interval int
		start    time.Time
	}{
		{"no tenant", "", "Sched", 60, start},
		{"blank tenant", "   ", "Sched", 60, start},
		{"no name", "tn-1", "", 60, start},
		{"blank name", "tn-1", "   ", 60, start},
		{"zero interval", "tn-1", "Sched", 0, start},
		{"negative interval", "tn-1", "Sched", -1, start},
		{"zero rotation start", "tn-1", "Sched", 60, time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewOnCallSchedule(c.tenant, c.schedule, c.interval, c.start); err == nil {
				t.Error("an invalid schedule was constructed")
			}
		})
	}
}

func TestNewOnCallSchedule_RejectsAnOverlongName(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NewOnCallSchedule("tn-1", strings.Repeat("x", MaxOnCallScheduleNameLength+1), 60, start); err == nil {
		t.Error("a name over the length bound was accepted")
	}
}

func TestOnCallScheduleStatus_Valid(t *testing.T) {
	for _, s := range []OnCallScheduleStatus{OnCallScheduleActive, OnCallScheduleArchived} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
	}
	for _, s := range []OnCallScheduleStatus{"", "ACTIVE", "deleted", "paused"} {
		if OnCallScheduleStatus(s).Valid() {
			t.Errorf("%q must not be valid", s)
		}
	}
}

func TestNewOnCallParticipant_BuildsAParticipant(t *testing.T) {
	p, err := NewOnCallParticipant(" sched-1 ", " tn-1 ", " usr-1 ", 0)
	if err != nil {
		t.Fatalf("NewOnCallParticipant: %v", err)
	}
	if p.ScheduleID != "sched-1" || p.TenantID != "tn-1" || p.UserID != "usr-1" {
		t.Errorf("identifiers not trimmed: %+v", p)
	}
	if p.Position != 0 {
		t.Errorf("position = %d, want 0", p.Position)
	}
	if p.ParticipantID == "" {
		t.Error("participant_id must be minted")
	}
}

func TestNewOnCallParticipant_RejectsNegativePosition(t *testing.T) {
	if _, err := NewOnCallParticipant("sched-1", "tn-1", "usr-1", -1); err == nil {
		t.Error("a negative position was accepted")
	}
}

func TestNewOnCallParticipant_RequiresEveryIdentifier(t *testing.T) {
	for _, c := range []struct{ name, schedule, tenant, user string }{
		{"no schedule", "", "tn-1", "usr-1"},
		{"no tenant", "sched-1", "", "usr-1"},
		{"no user", "sched-1", "tn-1", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewOnCallParticipant(c.schedule, c.tenant, c.user, 0); err == nil {
				t.Error("an incomplete participant was constructed")
			}
		})
	}
}

// ---------------------------------------------------------------- rotation

// The rotation math is pure and DB-free: every case here is a unit test of
// OnCallRotationIndex alone, per docs/PLATFORM-BUILD-PLAN.md E5.2a's review
// criteria.

func TestOnCallRotationIndex_ZeroParticipantsMeansNobodyOnCall(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	idx, ok := OnCallRotationIndex(0, start, 3600, start)
	if ok {
		t.Fatalf("ok = true with 0 participants, want false")
	}
	if idx != 0 {
		t.Errorf("index = %d, want 0", idx)
	}
}

func TestOnCallRotationIndex_SingleParticipantAlwaysHoldsTheSeat(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{
		start.Add(-time.Hour),
		start,
		start.Add(time.Second),
		start.Add(1000 * time.Hour),
		start.Add(87600 * time.Hour), // ~10 years
	} {
		idx, ok := OnCallRotationIndex(1, start, 3600, at)
		if !ok || idx != 0 {
			t.Errorf("at %v: index=%d ok=%v, want 0/true", at, idx, ok)
		}
	}
}

func TestOnCallRotationIndex_BeforeRotationStartHoldsTheFirstParticipant(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	idx, ok := OnCallRotationIndex(3, start, 3600, start.Add(-time.Nanosecond))
	if !ok {
		t.Fatal("ok = false before rotation start, want true")
	}
	if idx != 0 {
		t.Errorf("index = %d, want 0 (rotation has not begun)", idx)
	}
}

// Three participants, several handoffs: the rotation must visit 0,1,2,0,1,2...
// in order, one full cycle and into a second.
func TestOnCallRotationIndex_ThreeParticipantsAcrossSeveralHandoffs(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const interval = 3600 // seconds
	want := []int{0, 1, 2, 0, 1, 2, 0}
	for k, w := range want {
		at := start.Add(time.Duration(k) * time.Duration(interval) * time.Second)
		idx, ok := OnCallRotationIndex(3, start, interval, at)
		if !ok {
			t.Fatalf("handoff %d: ok = false, want true", k)
		}
		if idx != w {
			t.Errorf("handoff %d (at=%v): index = %d, want %d", k, at, idx, w)
		}
	}
}

// Half-open: AT the boundary rotation_start + k*interval, the index has
// already advanced to the next participant — the interval that just ended is
// [rotation_start+(k-1)*interval, rotation_start+k*interval).
func TestOnCallRotationIndex_BoundaryIsHalfOpen(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const interval = 60

	// One nanosecond before the first boundary: still participant 0.
	idx, ok := OnCallRotationIndex(2, start, interval, start.Add(interval*time.Second-time.Nanosecond))
	if !ok || idx != 0 {
		t.Errorf("just before boundary: index=%d ok=%v, want 0/true", idx, ok)
	}
	// Exactly at the boundary: participant 1 (the index has incremented).
	idx, ok = OnCallRotationIndex(2, start, interval, start.Add(interval*time.Second))
	if !ok || idx != 1 {
		t.Errorf("at boundary: index=%d ok=%v, want 1/true", idx, ok)
	}
	// One nanosecond after: still participant 1.
	idx, ok = OnCallRotationIndex(2, start, interval, start.Add(interval*time.Second+time.Nanosecond))
	if !ok || idx != 1 {
		t.Errorf("just after boundary: index=%d ok=%v, want 1/true", idx, ok)
	}
}

// A large multiple far in the future must still resolve to a valid,
// non-negative index — proof the modulo is positive even though Go's %
// itself can return a negative result for a negative dividend (steps here
// is always >= 0, but the function computes the positive form
// unconditionally, and this is the case that would expose a broken formula:
// a naive `steps % n` compiles identically for non-negative steps, so this
// test alone would not catch a missing "+n) % n" — the mutation proof in the
// implementer's evidence report defeats the modulo directly to show it bites).
func TestOnCallRotationIndex_FarFutureStaysInBounds(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const interval = 300 // 5 minutes
	const n = 7
	// ~50 years of 5-minute handoffs.
	at := start.Add(50 * 365 * 24 * time.Hour)
	idx, ok := OnCallRotationIndex(n, start, interval, at)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if idx < 0 || idx >= n {
		t.Errorf("index = %d, out of bounds [0,%d)", idx, n)
	}

	// Cross-check against big.Int-free arithmetic done a different way
	// (int64 division), so this is not merely "the same formula twice".
	elapsedSeconds := int64(at.Sub(start).Seconds())
	steps := elapsedSeconds / interval
	want := int(steps % n)
	if want < 0 {
		want += n
	}
	if idx != want {
		t.Errorf("index = %d, want %d (cross-checked)", idx, want)
	}
}

// positiveModulo is exercised directly, with negative x, because
// OnCallRotationIndex's own "rotation has not begun" branch keeps every
// value it passes to positiveModulo non-negative — so a mutation of the
// formula back to a bare `x % n` would otherwise pass every
// OnCallRotationIndex test in this file unchanged. This is the test that
// makes the positive-modulo guard's own doc comment true.
func TestPositiveModulo(t *testing.T) {
	for _, c := range []struct{ x, n, want int64 }{
		{0, 3, 0}, {1, 3, 1}, {2, 3, 2}, {3, 3, 0}, {4, 3, 1},
		{-1, 3, 2}, {-2, 3, 1}, {-3, 3, 0}, {-4, 3, 2}, {-9, 3, 0},
		{5, 1, 0}, {-5, 1, 0},
		{-1, 7, 6},
	} {
		if got := positiveModulo(c.x, c.n); got != c.want {
			t.Errorf("positiveModulo(%d, %d) = %d, want %d", c.x, c.n, got, c.want)
		}
	}
}

func TestOnCallRotationIndex_NonPositiveIntervalDoesNotPanic(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Defensive branch only: Validate refuses this at construction time. This
	// proves the pure function itself never panics (divide-by-zero) even if
	// called directly with a bad interval.
	idx, ok := OnCallRotationIndex(3, start, 0, start.Add(time.Hour))
	if !ok || idx != 0 {
		t.Errorf("index=%d ok=%v, want 0/true for a non-positive interval", idx, ok)
	}
}

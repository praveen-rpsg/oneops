package escalation

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

func TestSeeder_RunOnce_RecordsSeededAndSkipped(t *testing.T) {
	store := newFakeStore()
	var gotNow time.Time
	store.seedFn = func(now time.Time) (int, int, error) {
		gotNow = now
		return 3, 2, nil
	}
	m := &countMetrics{}

	s := NewSeeder(store, m, quiet(), SeederConfig{})
	s.RunOnce(context.Background())

	if gotNow.IsZero() {
		t.Error("Seed was not called with a clock value")
	}
	if m.seeded != 3 {
		t.Errorf("seeded metric = %d, want 3", m.seeded)
	}
	if m.skippedNoPolicy != 2 {
		t.Errorf("skippedNoPolicy metric = %d, want 2", m.skippedNoPolicy)
	}
	if m.errors != 0 {
		t.Errorf("errors metric = %d, want 0", m.errors)
	}
}

func TestSeeder_RunOnce_NoActivePolicy_SeedsNothingNotAnError(t *testing.T) {
	store := newFakeStore()
	store.seedFn = func(time.Time) (int, int, error) { return 0, 1, nil }
	m := &countMetrics{}

	s := NewSeeder(store, m, quiet(), SeederConfig{})
	s.RunOnce(context.Background())

	if m.seeded != 0 {
		t.Errorf("seeded metric = %d, want 0", m.seeded)
	}
	if m.skippedNoPolicy != 1 {
		t.Errorf("skippedNoPolicy metric = %d, want 1", m.skippedNoPolicy)
	}
	if m.errors != 0 {
		t.Error("a tenant with no active policy must not be treated as an error")
	}
}

func TestSeeder_RunOnce_SeedErrorIsCountedAndSurvivable(t *testing.T) {
	store := newFakeStore()
	store.seedFn = func(time.Time) (int, int, error) { return 0, 0, errBoom }
	m := &countMetrics{}

	s := NewSeeder(store, m, quiet(), SeederConfig{})
	s.RunOnce(context.Background()) // must not panic

	if m.errors != 1 {
		t.Errorf("errors metric = %d, want 1", m.errors)
	}
}

func TestSeeder_ConfigDefaults(t *testing.T) {
	var c SeederConfig
	c.withDefaults()
	if c.Interval <= 0 {
		t.Error("Interval must default to a positive duration")
	}
}

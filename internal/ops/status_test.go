package ops

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func TestStatus_BeforeAndAfterRun(t *testing.T) {
	v := newFakeVerifier()
	v.results["a"] = domain.VerifyResult{OK: true, HeadSeq: 2, Checked: 2}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"a"}}, nil, Config{Interval: time.Minute})

	// Before any run.
	st := s.Status()
	if st.HasRun || st.Running || st.Stalled {
		t.Fatalf("pre-run status = %+v", st)
	}
	if st.IntervalSeconds != 60 {
		t.Errorf("interval = %v, want 60", st.IntervalSeconds)
	}

	// After a healthy sweep.
	s.RunOnce(context.Background())
	st = s.Status()
	if !st.HasRun || !st.LastHealthy || st.Stalled {
		t.Fatalf("post-run status = %+v", st)
	}
	if st.ChainsTotal != 1 || st.ChainsOK != 1 || st.Failures != 0 {
		t.Errorf("summary wrong: %+v", st)
	}
	if st.SinceLastRunSeconds < 0 {
		t.Errorf("since-last-run = %v", st.SinceLastRunSeconds)
	}
}

func TestStatus_ReflectsIntegrityBreak(t *testing.T) {
	brk := int64(1)
	v := newFakeVerifier()
	v.results["bad"] = domain.VerifyResult{OK: false, FirstBreakSeq: &brk, BreakReason: "x"}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"bad"}}, nil, Config{Interval: time.Minute})

	s.RunOnce(context.Background())
	st := s.Status()
	if st.LastHealthy || st.Failures != 1 {
		t.Fatalf("status should reflect the break: %+v", st)
	}
}

func TestStatus_StalledWhenLastRunOld(t *testing.T) {
	v := newFakeVerifier()
	v.results["a"] = domain.VerifyResult{OK: true}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"a"}}, nil, Config{Interval: 10 * time.Millisecond})

	s.RunOnce(context.Background())
	// The interval is tiny; after a short wait the last run is older than 2x interval.
	time.Sleep(30 * time.Millisecond)
	if st := s.Status(); !st.Stalled {
		t.Fatalf("expected stalled status, got %+v", st)
	}
}

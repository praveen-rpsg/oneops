package ops

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeVerifier returns a configured result/error per chain and counts calls.
type fakeVerifier struct {
	mu      sync.Mutex
	results map[string]domain.VerifyResult
	errs    map[string]error
	calls   map[string]int
	block   func(ctx context.Context, chainID string) error // optional: simulate slow/timeout
}

func newFakeVerifier() *fakeVerifier {
	return &fakeVerifier{
		results: map[string]domain.VerifyResult{},
		errs:    map[string]error{},
		calls:   map[string]int{},
	}
}

func (f *fakeVerifier) VerifyChain(ctx context.Context, chainID string) (domain.VerifyResult, error) {
	f.mu.Lock()
	f.calls[chainID]++
	block := f.block
	err := f.errs[chainID]
	res := f.results[chainID]
	f.mu.Unlock()

	if block != nil {
		if berr := block(ctx, chainID); berr != nil {
			return domain.VerifyResult{}, berr
		}
	}
	if err != nil {
		return domain.VerifyResult{}, err
	}
	res.ChainID = chainID
	return res, nil
}

func (f *fakeVerifier) VerifyRange(context.Context, string, int64, int64) (domain.VerifyResult, error) {
	return domain.VerifyResult{}, nil
}

func (f *fakeVerifier) callCount(chainID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[chainID]
}

type fakeLister struct {
	ids []string
	err error
}

func (l fakeLister) ListChainIDs(context.Context) ([]string, error) { return l.ids, l.err }

// countingRecorder records aggregate Recorder signals.
type countingRecorder struct {
	verified atomic.Int64
	failures atomic.Int64
	errors   atomic.Int64
	runs     atomic.Int64
}

func (r *countingRecorder) ChainVerified(_ string, res domain.VerifyResult, _ time.Duration) {
	r.verified.Add(1)
	if !res.OK {
		r.failures.Add(1)
	}
}
func (r *countingRecorder) ChainError(string, time.Duration) { r.errors.Add(1) }
func (r *countingRecorder) RunCompleted(IntegrityReport)     { r.runs.Add(1) }

func newTestScheduler(t *testing.T, v *fakeVerifier, l ChainLister, rec Recorder, cfg Config) *Scheduler {
	t.Helper()
	s, err := New(v, l, rec, quietLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNew_Validation(t *testing.T) {
	if _, err := New(nil, fakeLister{}, nil, nil, Config{}); !errors.Is(err, errNilVerifier) {
		t.Errorf("nil verifier: got %v", err)
	}
	if _, err := New(newFakeVerifier(), nil, nil, nil, Config{}); !errors.Is(err, errNilLister) {
		t.Errorf("nil lister: got %v", err)
	}
	// Defaults applied; nil recorder/logger tolerated.
	s, err := New(newFakeVerifier(), fakeLister{}, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.cfg.Interval != 5*time.Minute || s.cfg.RunTimeout != 30*time.Second {
		t.Fatalf("defaults not applied: %+v", s.cfg)
	}
}

func TestRunOnce_AllHealthy(t *testing.T) {
	v := newFakeVerifier()
	v.results["a"] = domain.VerifyResult{OK: true, HeadSeq: 3, Checked: 3}
	v.results["b"] = domain.VerifyResult{OK: true, HeadSeq: 1, Checked: 1}
	rec := &countingRecorder{}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"a", "b"}}, rec, Config{})

	report := s.RunOnce(context.Background())
	if !report.Healthy() || report.ChainsTotal != 2 || report.ChainsOK != 2 {
		t.Fatalf("report = %+v", report)
	}
	if rec.verified.Load() != 2 || rec.failures.Load() != 0 || rec.runs.Load() != 1 {
		t.Fatalf("recorder: verified=%d failures=%d runs=%d", rec.verified.Load(), rec.failures.Load(), rec.runs.Load())
	}
}

func TestRunOnce_IntegrityBreakNotRetried(t *testing.T) {
	brk := int64(2)
	v := newFakeVerifier()
	v.results["bad"] = domain.VerifyResult{OK: false, FirstBreakSeq: &brk, BreakReason: "hash_mismatch", HeadSeq: 5, Checked: 5}
	rec := &countingRecorder{}
	// RetryAttempts high — must NOT be used for an integrity break.
	s := newTestScheduler(t, v, fakeLister{ids: []string{"bad"}}, rec, Config{RetryAttempts: 5})

	report := s.RunOnce(context.Background())
	if report.Healthy() {
		t.Fatal("expected an unhealthy report")
	}
	if len(report.Failures) != 1 || report.Failures[0].ChainID != "bad" || report.Failures[0].BreakReason != "hash_mismatch" {
		t.Fatalf("failures = %+v", report.Failures)
	}
	if got := v.callCount("bad"); got != 1 {
		t.Fatalf("integrity break was retried %d times, want exactly 1 call", got)
	}
	if rec.failures.Load() != 1 {
		t.Fatalf("failure metric = %d, want 1", rec.failures.Load())
	}
}

func TestRunOnce_TransportErrorRetried(t *testing.T) {
	v := newFakeVerifier()
	v.errs["flaky"] = errors.New("connection reset")
	rec := &countingRecorder{}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"flaky"}}, rec, Config{RetryAttempts: 2, RetryBackoff: 0})

	report := s.RunOnce(context.Background())
	if len(report.Errors) != 1 || report.Errors[0].ChainID != "flaky" {
		t.Fatalf("errors = %+v", report.Errors)
	}
	// 1 initial try + 2 retries = 3 calls.
	if got := v.callCount("flaky"); got != 3 {
		t.Fatalf("verify called %d times, want 3 (1 try + 2 retries)", got)
	}
	if rec.errors.Load() != 1 {
		t.Fatalf("error metric = %d, want 1", rec.errors.Load())
	}
}

func TestRunOnce_TransportErrorThenSuccess(t *testing.T) {
	v := newFakeVerifier()
	var attempts atomic.Int64
	v.block = func(context.Context, string) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient")
		}
		return nil
	}
	v.results["c"] = domain.VerifyResult{OK: true, HeadSeq: 1, Checked: 1}
	rec := &countingRecorder{}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"c"}}, rec, Config{RetryAttempts: 3, RetryBackoff: 0})

	report := s.RunOnce(context.Background())
	if !report.Healthy() {
		t.Fatalf("expected recovery to healthy, got %+v", report)
	}
	if v.callCount("c") != 2 {
		t.Fatalf("verify called %d times, want 2 (1 fail + 1 success)", v.callCount("c"))
	}
}

func TestRunOnce_Timeout(t *testing.T) {
	v := newFakeVerifier()
	v.block = func(ctx context.Context, _ string) error {
		<-ctx.Done() // block until the per-run timeout fires
		return ctx.Err()
	}
	rec := &countingRecorder{}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"slow"}}, rec, Config{RunTimeout: 20 * time.Millisecond})

	report := s.RunOnce(context.Background())
	if len(report.Errors) != 1 {
		t.Fatalf("expected a timeout error, got %+v", report)
	}
	if rec.errors.Load() != 1 {
		t.Fatalf("error metric = %d, want 1", rec.errors.Load())
	}
}

func TestRunOnce_ListerError(t *testing.T) {
	rec := &countingRecorder{}
	s := newTestScheduler(t, newFakeVerifier(), fakeLister{err: errors.New("db down")}, rec, Config{})

	report := s.RunOnce(context.Background())
	if report.Healthy() || len(report.Errors) != 1 || report.Errors[0].ChainID != "" {
		t.Fatalf("report = %+v", report)
	}
	if rec.runs.Load() != 1 {
		t.Fatalf("run completed metric = %d, want 1", rec.runs.Load())
	}
}

func TestRun_GracefulShutdown(t *testing.T) {
	v := newFakeVerifier()
	v.results["a"] = domain.VerifyResult{OK: true}
	rec := &countingRecorder{}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"a"}}, rec, Config{Interval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// The immediate sweep must run; then cancelling stops Run promptly.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
	if rec.runs.Load() < 1 {
		t.Fatal("expected at least the immediate sweep to run")
	}
}

func TestRun_PeriodicSweeps(t *testing.T) {
	v := newFakeVerifier()
	v.results["a"] = domain.VerifyResult{OK: true}
	rec := &countingRecorder{}
	s := newTestScheduler(t, v, fakeLister{ids: []string{"a"}}, rec, Config{Interval: 15 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for rec.runs.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("expected >=3 periodic sweeps, got %d", rec.runs.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

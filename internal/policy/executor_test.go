package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturingLogger is quiet() but keeps the text so a test can assert a
// particular log line was (or was not) emitted, instead of only that the
// code path did not panic.
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// ---- RunOnce: claim-pass failure ---------------------------------------------

// A claim-pass failure must halt the pass without running any action.
func TestExecutor_RunOnce_ClaimErrorHaltsWithoutRunning(t *testing.T) {
	execs := newFakeExecs()
	execs.claimErr = errors.New("db unavailable")
	reg := NewRegistry()
	called := false
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })})
	e := NewExecutor(execs, newFakePolicies(), newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})

	e.RunOnce(context.Background()) // must not panic

	if called {
		t.Fatal("a claim-pass error must not be followed by any action run")
	}
}

// ---- RunOnce: cancellation mid-batch gives back unattempted claims -----------

// A worker demoted or shut down mid-batch must give back every claim it has
// not yet run — those rows were charged an attempt by ClaimDue
// (ADR-CONCURRENCY-006) but never executed.
func TestExecutor_RunOnce_CtxCancelMidBatchReleasesUnattemptedClaims(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{
		execPending("x1", "p1"), execPending("x2", "p1"), execPending("x3", "p1"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var runs int
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		cancel() // simulate demotion landing right after the first action runs
		return resp(200), nil
	})})
	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})
	e.RunOnce(ctx)

	mu.Lock()
	got := runs
	mu.Unlock()
	if got != 1 {
		t.Fatalf("action runs = %d, want exactly 1 (the rest must be released, not run, after cancellation)", got)
	}

	succeeded, released := 0, 0
	for _, id := range []string{"x1", "x2", "x3"} {
		x, _, _ := execsGet(execs, id)
		switch x.Status {
		case ExecSucceeded:
			succeeded++
		case ExecPending:
			released++
			if x.RetryCount != 0 {
				t.Errorf("%s: RetryCount = %d, want 0 — the claim's charged attempt must be given back on release", id, x.RetryCount)
			}
		default:
			t.Errorf("%s: status = %q, want succeeded or released-to-pending", id, x.Status)
		}
	}
	if succeeded != 1 || released != 2 {
		t.Fatalf("succeeded=%d released=%d, want 1 and 2 (unattempted claims must be released, not left running)", succeeded, released)
	}
}

func execsGet(f *fakeExecs, id string) (Execution, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.m[id]
	return x, ok, nil
}

// A context already cancelled before the pass begins must release the entire
// claimed batch: nothing runs, and every claim is given back.
func TestExecutor_RunOnce_PreCancelledCtxReleasesWholeBatch(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{execPending("x1", "p1"), execPending("x2", "p1")})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })})
	NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{}).RunOnce(ctx)

	if called {
		t.Fatal("a pre-cancelled context must not be followed by any action run")
	}
	for _, id := range []string{"x1", "x2"} {
		x, _, _ := execsGet(execs, id)
		if x.Status != ExecPending || x.RetryCount != 0 {
			t.Errorf("%s: status=%q retry=%d, want released to pending with attempt given back", id, x.Status, x.RetryCount)
		}
	}
}

// A failed release of an unused claim must not crash the worker.
func TestExecutor_RunOnce_ReleaseClaimErrorIsLoggedNotFatal(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{execPending("x1", "p1")})
	execs.releaseErr = errors.New("release write failed")

	log, buf := capturingLogger()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("must not run the action once cancelled")
		return resp(200), nil
	})})
	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, log, ExecutorConfig{})

	e.RunOnce(ctx) // must not panic despite ReleaseClaim failing

	if !strings.Contains(buf.String(), "release unused claim") {
		t.Errorf("release failure was not logged: %s", buf.String())
	}
}

// ---- attempt: policy not found -------------------------------------------------

// The policy is fetched by id from the authoritative table, never trusted
// from the queue. A row referencing a policy that no longer exists (deleted
// after being enqueued) must dead-letter, not retry forever against a policy
// that will never come back.
func TestExecutor_Attempt_PolicyNotFoundDeadLetters(t *testing.T) {
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{execPending("x1", "p-gone")})
	reg := NewRegistry()
	called := false
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })})

	NewExecutor(execs, newFakePolicies(), newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{}).RunOnce(context.Background())

	if called {
		t.Fatal("no action may run for a policy that no longer exists")
	}
	if execs.status("x1") != ExecDeadLetter {
		t.Fatalf("status = %q, want dead_letter", execs.status("x1"))
	}
}

// ---- Attempt: the synchronous admin-test entry point ---------------------------

// Attempt is the synchronous wrapper the admin "test policy" endpoint drives.
// It must behave exactly like the asynchronous path for the same execution.
func TestExecutor_Attempt_RunsSynchronously(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	var called bool
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })})
	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})

	// Modeled on the admin "test" execution: an unclaimed row (RetryCount 0,
	// ClaimedAt zero) driven directly, exactly as cmd/controlplane/main.go's
	// SetPolicies callback does.
	got := e.Attempt(context.Background(), Execution{
		ID: "poltest_p1", PolicyID: "p1",
		Event: Event{TenantID: testTenant, Operation: "test", CfgID: "p1"},
	})

	if !called {
		t.Fatal("Attempt did not run the action")
	}
	if got != ExecSucceeded {
		t.Fatalf("Attempt returned %q, want succeeded", got)
	}
}

// ---- attempt: MarkResult fencing on success ------------------------------------

// A successful action whose outcome write is fenced by a reclaim must not be
// recorded over the reclaimer's state and must not double-count success
// (ADR-CONCURRENCY-005): the action already ran (at-least-once), but this
// worker no longer owns the row.
func TestExecutor_Attempt_SuccessFencedByReclaimDoesNotDoubleCount(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	execs.staleIDs = map[string]bool{"x1": true}
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil })})
	m := &countPolicyMetrics{}
	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, m, quiet(), ExecutorConfig{})

	got := e.Attempt(context.Background(), execPending("x1", "p1"))

	if got != ExecSucceeded {
		t.Fatalf("status = %q, want succeeded (the run happened)", got)
	}
	// The executor has no dedicated "succeeded" counter distinct from the
	// execution/failure counters it does expose; the invariant under test is
	// that a fenced write logs and returns without treating the reclaim as a
	// failure of this worker's own attempt.
	if m.failure != 0 {
		t.Errorf("failure metric = %d, want 0 — a fenced success is not this worker's failure", m.failure)
	}
}

// A successful action whose outcome write fails for a reason OTHER than
// fencing must not crash — the action ran but was not recorded, so the row
// remains claimed for reclaim and re-run (ADR-CONCURRENCY-006).
func TestExecutor_Attempt_SuccessMarkResultErrorIsLoggedNotFatal(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	execs.markErr = map[string]error{"x1": errors.New("write timeout")}
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil })})
	log, buf := capturingLogger()
	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, log, ExecutorConfig{})

	got := e.Attempt(context.Background(), execPending("x1", "p1")) // must not panic

	if got != ExecSucceeded {
		t.Fatalf("status = %q, want succeeded (the action ran even though recording it failed)", got)
	}
	if !strings.Contains(buf.String(), "outcome not recorded") {
		t.Errorf("unrecorded outcome was not logged: %s", buf.String())
	}
}

// A failed action whose retry/dead-letter write is fenced by a reclaim must
// not crash and must not reschedule against the reclaimer's row.
func TestExecutor_Attempt_FailureFencedByReclaimIsNotFatal(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	execs.staleIDs = map[string]bool{"x1": true}
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })})
	log, buf := capturingLogger()
	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, log, ExecutorConfig{BaseBackoff: time.Millisecond})

	x := execPending("x1", "p1")
	x.RetryCount = 1 // the minimum ClaimDue ever produces (ADR-CONCURRENCY-006) —
	// this test is about fencing, not the RetryCount==0 case. See
	// TestExecutor_Backoff_ZeroRetryDoesNotPanic and
	// TestExecutor_Attempt_FreshRecordAgainstFailingActionDoesNotPanic below for
	// that dedicated regression.
	got := e.Attempt(context.Background(), x)

	if got != ExecFailed {
		t.Fatalf("status = %q, want failed", got)
	}
	if !strings.Contains(buf.String(), "failed result fenced") {
		t.Errorf("fenced retry-write was not logged: %s", buf.String())
	}
}

type countPolicyMetrics struct {
	execution, failure, retry, active int
	durations                         int
}

func (m *countPolicyMetrics) IncExecution()                 { m.execution++ }
func (m *countPolicyMetrics) IncFailure()                   { m.failure++ }
func (m *countPolicyMetrics) IncRetry()                     { m.retry++ }
func (m *countPolicyMetrics) ObserveDuration(time.Duration) { m.durations++ }
func (m *countPolicyMetrics) SetActivePolicies(n int)       { m.active = n }

// ---- backoff --------------------------------------------------------------------

// The backoff schedule is capped: it must never exceed MaxBackoff even when
// doubling would otherwise overflow or run past it.
func TestExecutor_Backoff_CapsAtMaxBackoff(t *testing.T) {
	e := NewExecutor(newFakeExecs(), newFakePolicies(), newFakePolicyOwners(), NewRegistry(), nil, quiet(),
		ExecutorConfig{BaseBackoff: time.Second, MaxBackoff: time.Minute})

	if got := e.backoff(1); got != time.Second {
		t.Errorf("backoff(1) = %v, want %v (first retry is the base delay)", got, time.Second)
	}
	if got := e.backoff(10); got != time.Minute {
		t.Errorf("backoff(10) = %v, want capped at MaxBackoff %v", got, time.Minute)
	}
	if got := e.backoff(100); got != time.Minute {
		t.Errorf("backoff(100) = %v, want capped at MaxBackoff %v (overflow must not escape the cap)", got, time.Minute)
	}
}

// backoff(0) must be total: it must return a positive delay (BaseBackoff) and
// must not panic. RetryCount 0 is the value on every unclaimed execution —
// exactly what Executor.Attempt receives from the admin "test this policy"
// endpoint (cmd/controlplane/main.go), which builds a fresh Execution{} rather
// than going through ClaimDue.
//
// BEFORE THE FIX: `BaseBackoff << (retry - 1)` computed a shift of -1 for
// retry==0, which is a negative shift count and panics
// ("runtime error: negative shift amount") — this test panicked the test
// binary on the pre-fix backoff(). The clamp in backoff() (shift := retry-1;
// if shift<0 { shift=0 }) is what makes this pass.
func TestExecutor_Backoff_ZeroRetryDoesNotPanic(t *testing.T) {
	e := NewExecutor(newFakeExecs(), newFakePolicies(), newFakePolicyOwners(), NewRegistry(), nil, quiet(),
		ExecutorConfig{BaseBackoff: time.Second, MaxBackoff: time.Minute})

	got := e.backoff(0) // must not panic
	if got != time.Second {
		t.Errorf("backoff(0) = %v, want %v (same as backoff(1): the first delay, never negative or zero)", got, time.Second)
	}
}

// ---- Attempt: the synchronous admin "test policy" entry point, unclaimed ----

// The admin "test this policy" endpoint drives Attempt directly with a fresh,
// never-claimed Execution (RetryCount at its zero value) against whatever
// action the operator is testing. If that action fails and the policy has
// MaxRetries>0 (the normal case), the executor must report a retryable
// failure — not panic. This is the end-to-end shape of the bug: reproduces
// cmd/controlplane/main.go's SetPolicies callback exactly.
func TestExecutor_Attempt_FreshRecordAgainstFailingActionDoesNotPanic(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })})
	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})

	fresh := Execution{ // exactly what the admin "test" callback builds: no claim, RetryCount 0
		ID: "poltest_p1", PolicyID: "p1",
		Event: Event{TenantID: testTenant, Operation: "test", CfgID: "p1", EventID: "test"},
	}

	got := e.Attempt(context.Background(), fresh) // must not panic

	if got != ExecFailed {
		t.Fatalf("status = %q, want failed (a retryable failure, not a crash)", got)
	}
}

// ---- NewExecutor defaults --------------------------------------------------------

// A nil logger must not be carried forward as a nil.
func TestNewExecutor_NilLoggerDefaults(_ *testing.T) {
	e := NewExecutor(newFakeExecs(), newFakePolicies(), newFakePolicyOwners(), NewRegistry(), nil, nil, ExecutorConfig{})
	e.RunOnce(context.Background()) // must not panic on a nil-defaulted logger
}

// ---- Run: clean shutdown ----------------------------------------------------------

// Run must perform an initial pass and then return ctx.Err() promptly once the
// context is cancelled.
func TestExecutor_Run_ReturnsCtxErrOnCancel(t *testing.T) {
	e := NewExecutor(newFakeExecs(), newFakePolicies(), newFakePolicyOwners(), NewRegistry(), nil, quiet(), ExecutorConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

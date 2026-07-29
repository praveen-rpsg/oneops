package policy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// testTenant owns every fixture. The fan-out is ownership-first, so a fixture
// without an owner is selected for nothing — which is the intended default.
const testTenant = "t-test"

// fakePolicyOwners is a stand-in domain.EventOwnerResolver. It returns the
// authoritative owner per chain, defaulting to testTenant so existing fixtures
// (whose policy and event both name testTenant) resolve consistently. A test
// that needs the authoritative owner to disagree sets it explicitly.
type fakePolicyOwners struct {
	byChain map[string]string
	err     error
}

func newFakePolicyOwners() *fakePolicyOwners { return &fakePolicyOwners{byChain: map[string]string{}} }

func (f *fakePolicyOwners) ResolveEventOwner(_ context.Context, chainID string, _ int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if o, ok := f.byChain[chainID]; ok {
		return o, nil
	}
	return testTenant, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- fakes ------------------------------------------------------------------

type fakeSource struct {
	chains map[string][]domain.AuditEvent
}

func (f *fakeSource) ListChainIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(f.chains))
	for id := range f.chains {
		ids = append(ids, id)
	}
	return ids, nil
}
func (f *fakeSource) ListEvents(_ context.Context, chainID string, cursor int64, _ bool, limit int, _ string) ([]domain.AuditEvent, error) {
	var out []domain.AuditEvent
	for _, e := range f.chains[chainID] {
		if e.Seq > cursor {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

type fakeCursor struct {
	mu sync.Mutex
	m  map[string]int64
}

func newFakeCursor() *fakeCursor { return &fakeCursor{m: map[string]int64{}} }
func (c *fakeCursor) GetPolicyCursor(_ context.Context, id string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id], nil
}
func (c *fakeCursor) SetPolicyCursor(_ context.Context, id string, seq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[id] = seq
	return nil
}

type fakePolicies struct {
	mu sync.Mutex
	m  map[string]Policy
}

func newFakePolicies(ps ...Policy) *fakePolicies {
	f := &fakePolicies{m: map[string]Policy{}}
	for _, p := range ps {
		f.m[p.ID] = p
	}
	return f
}
func (f *fakePolicies) Create(_ context.Context, p Policy) error { f.set(p); return nil }
func (f *fakePolicies) Update(_ context.Context, p Policy) error { f.set(p); return nil }
func (f *fakePolicies) set(p Policy) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[p.ID] = p
}
func (f *fakePolicies) Get(_ context.Context, id string) (Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.m[id]
	if !ok {
		return Policy{}, domain.ErrNotFound
	}
	return p, nil
}
func (f *fakePolicies) List(context.Context) ([]Policy, error)        { return f.snap(false), nil }
func (f *fakePolicies) ListEnabled(context.Context) ([]Policy, error) { return f.snap(true), nil }
func (f *fakePolicies) snap(enabledOnly bool) []Policy {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Policy
	for _, p := range f.m {
		if !enabledOnly || p.Enabled {
			out = append(out, p)
		}
	}
	return out
}
func (f *fakePolicies) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
	return nil
}

type fakeExecs struct {
	mu sync.Mutex
	m  map[string]Execution
}

func newFakeExecs() *fakeExecs { return &fakeExecs{m: map[string]Execution{}} }
func (f *fakeExecs) Enqueue(_ context.Context, xs []Execution) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range xs {
		f.m[x.ID] = x
	}
	return nil
}

// ClaimDue models the real store's contract: the claim charges the attempt
// (retry_count is "attempts started") so a worker that never reports back still
// consumes budget (ADR-CONCURRENCY-006).
func (f *fakeExecs) ClaimDue(_ context.Context, now time.Time, _ time.Duration, limit int) ([]Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Execution
	for _, x := range f.m {
		if (x.Status == ExecPending || x.Status == ExecFailed) && !x.NextAttemptAt.After(now) {
			x.RetryCount++
			x.Status = ExecRunning
			f.m[x.ID] = x
			out = append(out, x)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (f *fakeExecs) ReleaseClaim(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.m[id]
	if x.RetryCount > 0 {
		x.RetryCount--
	}
	x.Status = ExecPending
	f.m[id] = x
	return nil
}
func (f *fakeExecs) MarkResult(_ context.Context, id string, _ time.Time, status ExecutionStatus, retry int, errMsg string, started, ended, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.m[id]
	x.Status, x.RetryCount, x.Error, x.StartedAt, x.EndedAt, x.NextAttemptAt = status, retry, errMsg, started, ended, next
	f.m[id] = x
	return nil
}
func (f *fakeExecs) ListByPolicy(_ context.Context, policyID string, _ int) ([]Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Execution
	for _, x := range f.m {
		if x.PolicyID == policyID {
			out = append(out, x)
		}
	}
	return out, nil
}
func (f *fakeExecs) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.m)
}
func (f *fakeExecs) status(id string) ExecutionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[id].Status
}
func (f *fakeExecs) any() Execution {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.m {
		return x
	}
	return Execution{}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (d doerFunc) Do(r *http.Request) (*http.Response, error) { return d(r) }

func resp(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(bodyReader(""))}
}

type sr struct {
	s string
	i int
}

func bodyReader(s string) io.Reader { return &sr{s: s} }
func (r *sr) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

func auditEvt(chain string, seq int64, op, actor string, payload string) domain.AuditEvent {
	return domain.AuditEvent{TenantID: testTenant,
		ChainID: chain, Seq: seq, EventID: "evt_" + strconv.FormatInt(seq, 10),
		OperationID: "op-" + strconv.FormatInt(seq, 10), Operation: domain.ConfigurationOperation(op),
		Actor: actor, PayloadCanonical: []byte(payload), OccurredAt: time.Now().UTC(),
	}
}

// ---- condition matching -----------------------------------------------------

func TestCondition_Matches(t *testing.T) {
	ev := Event{TenantID: testTenant, Operation: "ratification", CfgID: "c1", Actor: "user:alice", Metadata: map[string]string{"new_lifecycle": "ratified"}}
	cases := []struct {
		c    Condition
		want bool
	}{
		{Condition{}, true},
		{Condition{Operations: []string{"ratification"}}, true},
		{Condition{Operations: []string{"deprecation"}}, false},
		{Condition{Resources: []string{"c1"}}, true},
		{Condition{Resources: []string{"c2"}}, false},
		{Condition{Actors: []string{"user:alice"}}, true},
		{Condition{Actors: []string{"user:bob"}}, false},
		{Condition{Metadata: map[string]string{"new_lifecycle": "ratified"}}, true},
		{Condition{Metadata: map[string]string{"new_lifecycle": "draft"}}, false},
		{Condition{Operations: []string{"ratification"}, Actors: []string{"user:alice"}}, true},
	}
	for i, tc := range cases {
		if got := tc.c.Matches(ev); got != tc.want {
			t.Errorf("case %d: got %v want %v", i, got, tc.want)
		}
	}
}

// ---- consumer: only committed events ----------------------------------------

func TestConsumer_OnlyCommittedEventsEnqueue(t *testing.T) {
	src := &fakeSource{chains: map[string][]domain.AuditEvent{
		"c1": {
			auditEvt("c1", 1, "ratification", "user:alice", `{"new_lifecycle":"ratified"}`),
			auditEvt("c1", 2, "deprecation", "user:bob", `{}`),
		},
	}}
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Condition: Condition{Operations: []string{"ratification"}}, Action: ActionSpec{Type: "notification"}})
	execs := newFakeExecs()
	c := NewConsumer(src, newFakeCursor(), pols, execs, nil, quiet(), ConsumerConfig{})

	c.RunOnce(context.Background())
	if execs.count() != 1 { // only the ratification matched
		t.Fatalf("executions = %d, want 1", execs.count())
	}
	// Idempotent tail — no duplicates on a second pass.
	c.RunOnce(context.Background())
	if execs.count() != 1 {
		t.Fatalf("executions after 2nd pass = %d, want 1", execs.count())
	}
}

func TestConsumer_NoEventsNoExecutions(t *testing.T) {
	// A rolled-back operation leaves no audit event, so nothing is evaluated.
	src := &fakeSource{chains: map[string][]domain.AuditEvent{"c1": nil}}
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 1, Action: ActionSpec{Type: "notification"}})
	execs := newFakeExecs()
	NewConsumer(src, newFakeCursor(), pols, execs, nil, quiet(), ConsumerConfig{}).RunOnce(context.Background())
	if execs.count() != 0 {
		t.Fatalf("executions = %d, want 0", execs.count())
	}
}

// TestConsumer_ReplayedEventTriggers proves a replayed committed event flows
// through the same Evaluate path and triggers matching policies.
func TestConsumer_ReplayedEventTriggers(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Condition: Condition{Operations: []string{"ratification"}}, Action: ActionSpec{Type: "http"}})
	c := NewConsumer(&fakeSource{}, newFakeCursor(), pols, newFakeExecs(), nil, quiet(), ConsumerConfig{})

	replayed := eventFrom(auditEvt("c1", 1, "ratification", "user:alice", `{}`))
	all, _ := pols.ListEnabled(context.Background())
	out := c.Evaluate(replayed, all, time.Now())
	if len(out) != 1 || out[0].PolicyID != "p1" {
		t.Fatalf("replayed event did not trigger policy: %+v", out)
	}
}

// ---- executor: retries, dead-letter, isolation ------------------------------

func execPending(id, policyID string) Execution {
	return Execution{ID: id, PolicyID: policyID, Status: ExecPending, NextAttemptAt: time.Unix(0, 0), Event: Event{TenantID: testTenant, Operation: "ratification"}}
}

func TestExecutor_SuccessRunsAction(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{execPending("x1", "p1")})
	var called bool
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })})

	NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{}).RunOnce(context.Background())
	if !called || execs.status("x1") != ExecSucceeded {
		t.Fatalf("action not run or status wrong: called=%v status=%q", called, execs.status("x1"))
	}
}

func TestExecutor_RetryThenDeadLetter(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 2, Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)}})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{execPending("x1", "p1")})
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })})
	ex := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{BaseBackoff: time.Millisecond})

	ex.RunOnce(context.Background())
	if execs.status("x1") != ExecFailed {
		t.Fatalf("after attempt 1: %q, want failed", execs.status("x1"))
	}
	_ = execs.MarkResult(context.Background(), "x1", time.Time{}, ExecFailed, 1, "", time.Unix(0, 0), time.Unix(0, 0), time.Unix(0, 0))
	ex.RunOnce(context.Background())
	if execs.status("x1") != ExecDeadLetter {
		t.Fatalf("after attempt 2: %q, want dead_letter", execs.status("x1"))
	}
}

// TestExecutor_FailureIsolated proves a panicking/erroring action is captured on
// the execution and never propagates (no panic escapes RunOnce).
func TestExecutor_FailureIsolated(t *testing.T) {
	pols := newFakePolicies(Policy{ID: "p1", TenantID: testTenant, Enabled: true, MaxRetries: 1, Action: ActionSpec{Type: "boom"}})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{execPending("x1", "p1")})
	reg := NewRegistry()
	reg.Register("boom", actionFunc(func(context.Context, Event, json.RawMessage) error { panic("kaboom") }))

	// Must not panic; execution dead-letters with the captured error.
	NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{}).RunOnce(context.Background())
	got := execs.any()
	if got.Status != ExecDeadLetter || got.Error == "" {
		t.Fatalf("panic not isolated onto execution: %+v", got)
	}
}

type actionFunc func(context.Context, Event, json.RawMessage) error

func (f actionFunc) Run(ctx context.Context, ev Event, c json.RawMessage) error { return f(ctx, ev, c) }

// ---- actions ----------------------------------------------------------------

func TestActions_Builtins(t *testing.T) {
	ctx := context.Background()
	ev := Event{TenantID: testTenant, Operation: "ratification", EventID: "evt_1"}

	// HTTP action posts the event.
	var gotBody []byte
	http := HTTPAction{Doer: doerFunc(func(r *http.Request) (*http.Response, error) {
		gotBody, _ = io.ReadAll(r.Body)
		return resp(204), nil
	})}
	if err := http.Run(ctx, ev, json.RawMessage(`{"url":"http://x"}`)); err != nil {
		t.Fatalf("http action: %v", err)
	}
	if len(gotBody) == 0 {
		t.Fatal("http action sent no body")
	}

	// Email/command with no backend are interface-only (not configured).
	if err := (EmailAction{}).Run(ctx, ev, nil); !errors.Is(err, ErrActionNotConfigured) {
		t.Errorf("email: %v", err)
	}
	if err := (CommandAction{}).Run(ctx, ev, nil); !errors.Is(err, ErrActionNotConfigured) {
		t.Errorf("command: %v", err)
	}
	// Notification default sink logs and succeeds.
	if err := (NotificationAction{Log: quiet()}).Run(ctx, ev, nil); err != nil {
		t.Errorf("notification: %v", err)
	}
	// Unknown action type.
	if err := NewRegistry().Run(ctx, "nope", ev, nil); !errors.Is(err, ErrUnknownAction) {
		t.Errorf("unknown: %v", err)
	}
}

// The exploit, as a regression. A queued execution pairs the attacker's policy
// with the victim's event; the event snapshot in the row is queue metadata, and
// a policy action POSTs it outbound. Against the running service this
// exfiltrated a victim's governance event to the attacker's endpoint. The
// executor must re-derive the event's owner from the audit log and refuse when
// it does not match the policy — regardless of what the queued row claims.
func TestExecutor_RefusesForgedCrossTenantExecution(t *testing.T) {
	// Policy owned by the attacker.
	pols := newFakePolicies(Policy{
		ID: "p-attacker", TenantID: "t-attacker", Enabled: true, MaxRetries: 3,
		Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://attacker"}`)},
	})
	execs := newFakeExecs()

	// The forged execution: attacker policy, but the triggering event belongs to
	// the victim. The queued row even labels the event t-attacker (self-consistent).
	_ = execs.Enqueue(context.Background(), []Execution{{
		ID: "x-forged", PolicyID: "p-attacker", Status: ExecPending, NextAttemptAt: time.Unix(0, 0),
		Event: Event{TenantID: "t-attacker", CfgID: "c-victim", Seq: 1, Operation: "ratification"},
	}})

	// The authoritative log says the event at c-victim/1 belongs to the victim.
	owners := newFakePolicyOwners()
	owners.byChain["c-victim"] = "t-victim"

	var called bool
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return resp(200), nil
	})})

	NewExecutor(execs, pols, owners, reg, nil, quiet(), ExecutorConfig{}).RunOnce(context.Background())

	if called {
		t.Fatal("the executor ran a policy action against another tenant's event")
	}
	if execs.status("x-forged") != ExecDeadLetter {
		t.Fatalf("status = %q, want dead_letter", execs.status("x-forged"))
	}
}

// An event absent from the authoritative log is refused, not retried.
func TestExecutor_RefusesUnknownEvent(t *testing.T) {
	pols := newFakePolicies(Policy{
		ID: "p", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)},
	})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{{
		ID: "x-phantom", PolicyID: "p", Status: ExecPending, NextAttemptAt: time.Unix(0, 0),
		Event: Event{TenantID: testTenant, CfgID: "c-nope", Seq: 99, Operation: "ratification"},
	}})
	owners := newFakePolicyOwners()
	owners.err = domain.ErrEventNotFound

	var called bool
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return resp(200), nil
	})})

	NewExecutor(execs, pols, owners, reg, nil, quiet(), ExecutorConfig{}).RunOnce(context.Background())

	if called {
		t.Fatal("the executor ran a policy for an event absent from the authoritative log")
	}
	if execs.status("x-phantom") != ExecDeadLetter {
		t.Fatalf("status = %q, want dead_letter", execs.status("x-phantom"))
	}
}

// A resolver failure fails closed: no action runs when ownership is unresolvable.
func TestExecutor_ResolverFailureFailsClosed(t *testing.T) {
	pols := newFakePolicies(Policy{
		ID: "p", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Action: ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"http://x"}`)},
	})
	execs := newFakeExecs()
	_ = execs.Enqueue(context.Background(), []Execution{execPending("x1", "p")})
	owners := newFakePolicyOwners()
	owners.err = errContext

	var called bool
	reg := NewRegistry()
	reg.Register("http", HTTPAction{Doer: doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return resp(200), nil
	})})

	NewExecutor(execs, pols, owners, reg, nil, quiet(), ExecutorConfig{}).RunOnce(context.Background())

	if called {
		t.Fatal("the executor ran an action while ownership was unresolvable")
	}
}

var errContext = errorsNew("database unavailable")

func errorsNew(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

package events

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

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

type fakeCursors struct {
	mu sync.Mutex
	m  map[string]int64
}

func newFakeCursors() *fakeCursors { return &fakeCursors{m: map[string]int64{}} }
func (c *fakeCursors) GetCursor(_ context.Context, id string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id], nil
}
func (c *fakeCursors) SetCursor(_ context.Context, id string, seq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[id] = seq
	return nil
}

type fakeWebhooks struct {
	mu sync.Mutex
	m  map[string]Webhook
}

func newFakeWebhooks(ws ...Webhook) *fakeWebhooks {
	f := &fakeWebhooks{m: map[string]Webhook{}}
	for _, w := range ws {
		f.m[w.ID] = w
	}
	return f
}
func (f *fakeWebhooks) Create(_ context.Context, w Webhook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[w.ID] = w
	return nil
}
func (f *fakeWebhooks) Get(_ context.Context, id string) (Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.m[id]
	if !ok {
		return Webhook{}, domain.ErrNotFound
	}
	return w, nil
}
func (f *fakeWebhooks) List(context.Context) ([]Webhook, error) { return f.snapshot(false), nil }
func (f *fakeWebhooks) ListEnabled(context.Context) ([]Webhook, error) {
	return f.snapshot(true), nil
}
func (f *fakeWebhooks) snapshot(enabledOnly bool) []Webhook {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Webhook
	for _, w := range f.m {
		if !enabledOnly || w.Enabled {
			out = append(out, w)
		}
	}
	return out
}
func (f *fakeWebhooks) Update(_ context.Context, w Webhook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[w.ID] = w
	return nil
}
func (f *fakeWebhooks) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
	return nil
}

type fakeDeliveries struct {
	mu sync.Mutex
	m  map[string]Delivery
	n  int
}

func newFakeDeliveries() *fakeDeliveries { return &fakeDeliveries{m: map[string]Delivery{}} }
func (f *fakeDeliveries) Enqueue(_ context.Context, ds []Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range ds {
		f.m[d.ID] = d
		f.n++
	}
	return nil
}
func (f *fakeDeliveries) ClaimDue(_ context.Context, now time.Time, limit int) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Delivery
	for _, d := range f.m {
		if (d.Status == StatusPending || d.Status == StatusFailed) && !d.NextAttemptAt.After(now) {
			out = append(out, d)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (f *fakeDeliveries) MarkResult(_ context.Context, id string, status DeliveryStatus, retry, code int, last, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.m[id]
	d.Status, d.RetryCount, d.LastStatusCode, d.LastAttempt, d.NextAttemptAt = status, retry, code, last, next
	f.m[id] = d
	return nil
}
func (f *fakeDeliveries) ListByWebhook(_ context.Context, webhookID string, limit int) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Delivery
	for _, d := range f.m {
		if d.WebhookID == webhookID {
			out = append(out, d)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (f *fakeDeliveries) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.m)
}
func (f *fakeDeliveries) status(id string) DeliveryStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[id].Status
}
func (f *fakeDeliveries) all() []Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Delivery, 0, len(f.m))
	for _, d := range f.m {
		out = append(out, d)
	}
	return out
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}
}

// testTenant owns every fixture in the single-tenant relay tests. Matching now
// requires the subscription and the event to agree on an owner, so a fixture
// without one matches nothing — which is the fail-closed behaviour these tests
// would otherwise silently depend on.
const testTenant = "t-test"

func auditEvt(chain string, seq int64, op string) domain.AuditEvent {
	return domain.AuditEvent{
		TenantID: testTenant,
		ChainID:  chain, Seq: seq, EventID: "evt_" + strconv.FormatInt(seq, 10),
		OperationID: "op-" + strconv.FormatInt(seq, 10), Operation: domain.ConfigurationOperation(op),
		Actor: "user:alice", OccurredAt: time.Now().UTC(),
	}
}

// ---- signing ----------------------------------------------------------------

func TestSignAndVerify(t *testing.T) {
	payload := []byte(`{"a":1}`)
	sig := Sign("s3cr3t", 1000, "dlv_1", payload)
	if !Verify("s3cr3t", 1000, "dlv_1", payload, sig) {
		t.Fatal("valid signature rejected")
	}
	// Wrong secret / timestamp / delivery / payload all fail.
	if Verify("other", 1000, "dlv_1", payload, sig) {
		t.Error("wrong secret accepted")
	}
	if Verify("s3cr3t", 1001, "dlv_1", payload, sig) {
		t.Error("wrong timestamp accepted")
	}
	if Verify("s3cr3t", 1000, "dlv_2", payload, sig) {
		t.Error("wrong delivery id accepted")
	}
	if Verify("s3cr3t", 1000, "dlv_1", []byte(`{"a":2}`), sig) {
		t.Error("tampered payload accepted")
	}
}

// ---- relay: publish only after commit ---------------------------------------

func TestRelay_EnqueuesForCommittedEvents(t *testing.T) {
	src := &fakeSource{chains: map[string][]domain.AuditEvent{
		"c1": {auditEvt("c1", 1, "ratification"), auditEvt("c1", 2, "deprecation")},
	}}
	cur := newFakeCursors()
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	r := NewRelay(src, cur, wh, del, nil, quiet(), RelayConfig{})

	r.RunOnce(context.Background())
	if del.count() != 2 {
		t.Fatalf("deliveries = %d, want 2", del.count())
	}
	// Cursor advanced; a second pass enqueues nothing (idempotent tail).
	r.RunOnce(context.Background())
	if del.count() != 2 {
		t.Fatalf("deliveries after 2nd pass = %d, want 2 (no duplicates)", del.count())
	}
	if seq, _ := cur.GetCursor(context.Background(), "c1"); seq != 2 {
		t.Fatalf("cursor = %d, want 2", seq)
	}
}

func TestRelay_NoEventsMeansNoDeliveries(t *testing.T) {
	// A rolled-back operation leaves no audit event, so the source yields nothing.
	src := &fakeSource{chains: map[string][]domain.AuditEvent{"c1": nil}}
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, Enabled: true, MaxRetries: 1})
	del := newFakeDeliveries()
	r := NewRelay(src, newFakeCursors(), wh, del, nil, quiet(), RelayConfig{})

	r.RunOnce(context.Background())
	if del.count() != 0 {
		t.Fatalf("deliveries = %d, want 0 (no committed events)", del.count())
	}
}

func TestRelay_Subscriptions(t *testing.T) {
	src := &fakeSource{chains: map[string][]domain.AuditEvent{
		"c1": {auditEvt("c1", 1, "ratification")},
		"c2": {auditEvt("c2", 1, "deprecation")},
	}}
	wh := newFakeWebhooks(
		Webhook{ID: "all", TenantID: testTenant, Enabled: true, MaxRetries: 1},                                              // all
		Webhook{ID: "onlyRatify", TenantID: testTenant, Enabled: true, Operations: []string{"ratification"}, MaxRetries: 1}, // op filter
		Webhook{ID: "onlyC2", TenantID: testTenant, Enabled: true, Resources: []string{"c2"}, MaxRetries: 1},                // resource filter
		Webhook{ID: "disabled", TenantID: testTenant, Enabled: false, MaxRetries: 1},                                        // disabled
	)
	del := newFakeDeliveries()
	NewRelay(src, newFakeCursors(), wh, del, nil, quiet(), RelayConfig{}).RunOnce(context.Background())

	counts := map[string]int{}
	for _, d := range del.all() {
		counts[d.WebhookID]++
	}
	if counts["all"] != 2 || counts["onlyRatify"] != 1 || counts["onlyC2"] != 1 || counts["disabled"] != 0 {
		t.Fatalf("subscription fan-out = %v", counts)
	}
}

// ---- dispatcher: delivery, retry, dead-letter, rotation ---------------------

func newDelivery(id, webhookID string, at time.Time) Delivery {
	return Delivery{ID: id, WebhookID: webhookID, Event: Event{TenantID: testTenant, Operation: "ratification", ChainID: "c1", CfgID: "c1", Seq: 1}, Status: StatusPending, NextAttemptAt: at, CreatedAt: at}
}

func TestDispatcher_DeliversSignedPayload(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s3cr3t", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{newDelivery("d1", "w1", time.Now())})

	var gotSig, gotTS, gotDelivery string
	var gotBody []byte
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotSig = r.Header.Get(HeaderSignature)
		gotTS = r.Header.Get(HeaderTimestamp)
		gotDelivery = r.Header.Get(HeaderDelivery)
		gotBody, _ = io.ReadAll(r.Body)
		return resp(200), nil
	})
	d := NewDispatcher(del, wh, doer, nil, quiet(), DispatcherConfig{})
	d.RunOnce(context.Background())

	if del.status("d1") != StatusDelivered {
		t.Fatalf("status = %q, want delivered", del.status("d1"))
	}
	ts, _ := strconv.ParseInt(gotTS, 10, 64)
	if gotDelivery != "d1" || !Verify("s3cr3t", ts, "d1", gotBody, gotSig) {
		t.Fatalf("signature invalid: sig=%q ts=%q", gotSig, gotTS)
	}
}

func TestDispatcher_RetryThenDeadLetter(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 2})
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{newDelivery("d1", "w1", time.Unix(0, 0))})
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })
	d := NewDispatcher(del, wh, doer, nil, quiet(), DispatcherConfig{BaseBackoff: time.Millisecond})

	// Attempt 1: fail -> retry (retryCount 1 < 2).
	d.RunOnce(context.Background())
	if s := del.status("d1"); s != StatusFailed {
		t.Fatalf("after attempt 1: status = %q, want failed", s)
	}
	// Make it due again and attempt 2: retryCount 2 >= 2 -> dead-letter.
	_ = del.MarkResult(context.Background(), "d1", StatusFailed, 1, 500, time.Unix(0, 0), time.Unix(0, 0))
	d.RunOnce(context.Background())
	if s := del.status("d1"); s != StatusDeadLetter {
		t.Fatalf("after attempt 2: status = %q, want dead_letter", s)
	}
}

func TestDispatcher_SecretRotationAppliesToPending(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "old", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{newDelivery("d1", "w1", time.Now())})

	// Rotate the secret before delivery; the dispatcher reads it live.
	_ = wh.Update(context.Background(), Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "new", Enabled: true, MaxRetries: 3})

	var gotSig, gotTS string
	var gotBody []byte
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotSig = r.Header.Get(HeaderSignature)
		gotTS = r.Header.Get(HeaderTimestamp)
		gotBody, _ = io.ReadAll(r.Body)
		return resp(200), nil
	})
	NewDispatcher(del, wh, doer, nil, quiet(), DispatcherConfig{}).RunOnce(context.Background())

	ts, _ := strconv.ParseInt(gotTS, 10, 64)
	if !Verify("new", ts, "d1", gotBody, gotSig) || Verify("old", ts, "d1", gotBody, gotSig) {
		t.Fatal("rotation not applied: signature must verify with the new secret only")
	}
}

func TestDispatcher_MissingWebhookDeadLetters(t *testing.T) {
	wh := newFakeWebhooks() // empty
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{newDelivery("d1", "gone", time.Now())})
	NewDispatcher(del, wh, doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil }), nil, quiet(), DispatcherConfig{}).RunOnce(context.Background())
	if del.status("d1") != StatusDeadLetter {
		t.Fatalf("status = %q, want dead_letter", del.status("d1"))
	}
}

// ---- metrics ----------------------------------------------------------------

type countMetrics struct {
	delivered, failure, retry, active int
	latencies                         int
}

func (m *countMetrics) IncDelivered()                        { m.delivered++ }
func (m *countMetrics) IncFailure()                          { m.failure++ }
func (m *countMetrics) IncRetry()                            { m.retry++ }
func (m *countMetrics) ObserveDeliveryLatency(time.Duration) { m.latencies++ }
func (m *countMetrics) SetActiveWebhooks(n int)              { m.active = n }

func TestDispatcher_MetricsRecorded(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 2})
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{newDelivery("d1", "w1", time.Now())})
	m := &countMetrics{}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })
	NewDispatcher(del, wh, doer, m, quiet(), DispatcherConfig{BaseBackoff: time.Millisecond}).RunOnce(context.Background())
	if m.failure != 1 || m.retry != 1 || m.latencies != 1 {
		t.Fatalf("metrics = %+v", m)
	}
}

// A worker that reads across tenants must re-establish tenant identity before
// acting on what it read.
//
// The relay lists every chain and every enabled subscription on a privileged
// connection — row-level security is bypassed there by design — and then
// cross-products them. Matching on operation and resource alone meant a
// subscription with no filters, which is the documented way to subscribe to
// everything, received every other tenant's governance events signed with its
// own HMAC secret.
//
// Verified against the running service before the fix: an attacker's endpoint
// received the victim's chain_id, cfg_id, operation, actor and event_id.
func TestFanOut_RequiresSameTenant(t *testing.T) {
	victimEvent := Event{
		TenantID: "t-victim", ChainID: "c-victim", CfgID: "c-victim",
		Operation: "ratification", Seq: 1,
	}
	subs := []Webhook{
		// The unfiltered subscription — the shape that made this total.
		{ID: "atk", TenantID: "t-attacker", Enabled: true, MaxRetries: 1},
		{ID: "own", TenantID: "t-victim", Enabled: true, MaxRetries: 1},
	}

	got := domain.FanOut(victimEvent, subs, func(w Webhook) bool { return w.Matches(victimEvent) })

	for _, w := range got {
		if w.TenantID != victimEvent.TenantID {
			t.Fatalf("fan-out selected %q, owned by %q, for an event owned by %q",
				w.ID, w.TenantID, victimEvent.TenantID)
		}
	}
	if len(got) != 1 || got[0].ID != "own" {
		t.Fatalf("fan-out = %v, want exactly the owning subscription", got)
	}
}

// Matches decides what, never who. Left alone it selects another tenant's
// event, which is precisely why ownership is enforced above it rather than
// inside it — a predicate cannot widen the boundary it is not consulted about.
func TestMatches_DecidesWhatNotWho(t *testing.T) {
	foreign := Event{TenantID: "t-victim", ChainID: "c", CfgID: "c", Operation: "ratification"}
	attacker := Webhook{ID: "atk", TenantID: "t-attacker", Enabled: true}

	if !attacker.Matches(foreign) {
		t.Error("Matches should still answer the 'what' question on its own")
	}
	if domain.SameOwner(attacker, foreign) {
		t.Error("SameOwner must reject subscriptions owned by another tenant")
	}
}

// A missing owner on either side must select nothing. Treating an absent tenant
// as a wildcard would restore the vulnerability for any row written before the
// column existed, or by any future path that forgets to populate it.
func TestFanOut_MissingTenantFailsClosed(t *testing.T) {
	owned := Event{TenantID: "t-a", ChainID: "c", CfgID: "c", Operation: "ratification"}
	unowned := Event{ChainID: "c", CfgID: "c", Operation: "ratification"}
	yes := func(Webhook) bool { return true }

	if got := domain.FanOut(owned, []Webhook{{ID: "w", Enabled: true}}, yes); len(got) != 0 {
		t.Error("a subscription with no owner must be selected for nothing")
	}
	if got := domain.FanOut(unowned, []Webhook{{ID: "w", TenantID: "t-a", Enabled: true}}, yes); len(got) != 0 {
		t.Error("an event with no owner must select nothing")
	}
	if got := domain.FanOut(unowned, []Webhook{{ID: "w", Enabled: true}}, yes); len(got) != 0 {
		t.Error("two absent owners must not be treated as equal")
	}
}

// The relay must not deliver across tenants even when every other filter agrees.
func TestRelay_DoesNotCrossTenantBoundary(t *testing.T) {
	victim := auditEvt("c-victim", 1, "ratification")
	victim.TenantID = "t-victim"

	src := &fakeSource{chains: map[string][]domain.AuditEvent{"c-victim": {victim}}}
	wh := newFakeWebhooks(
		Webhook{ID: "attacker", TenantID: "t-attacker", Enabled: true, MaxRetries: 1},
		Webhook{ID: "owner", TenantID: "t-victim", Enabled: true, MaxRetries: 1},
	)
	deliv := newFakeDeliveries()
	r := NewRelay(src, newFakeCursors(), wh, deliv, NopMetrics{},
		slog.New(slog.NewTextHandler(io.Discard, nil)), RelayConfig{})

	r.RunOnce(context.Background())

	for _, d := range deliv.all() {
		if d.WebhookID == "attacker" {
			t.Errorf("the relay enqueued the victim's event for another tenant's endpoint "+
				"(delivery %s, chain %s)", d.ID, d.Event.ChainID)
		}
	}
	if len(deliv.all()) == 0 {
		t.Error("the owning tenant's subscription received nothing")
	}
}

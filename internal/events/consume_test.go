package events

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ---- extra fakes ------------------------------------------------------------

func (f *fakeDeliveries) GetDelivery(_ context.Context, id string) (Delivery, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.m[id]
	return d, ok, nil
}
func (f *fakeDeliveries) Requeue(_ context.Context, ids []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, id := range ids {
		if d, ok := f.m[id]; ok {
			d.Status, d.RetryCount = StatusPending, 0
			f.m[id] = d
			n++
		}
	}
	return n, nil
}
func (f *fakeDeliveries) RequeueDeadLetters(_ context.Context, webhookID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for id, d := range f.m {
		if d.Status == StatusDeadLetter && (webhookID == "" || d.WebhookID == webhookID) {
			d.Status, d.RetryCount = StatusPending, 0
			f.m[id] = d
			n++
		}
	}
	return n, nil
}
func (f *fakeDeliveries) ListDeadLetters(_ context.Context, webhookID string, _ int) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Delivery
	for _, d := range f.m {
		if d.Status == StatusDeadLetter && (webhookID == "" || d.WebhookID == webhookID) {
			out = append(out, d)
		}
	}
	return out, nil
}
func (f *fakeDeliveries) DeleteOlderThan(_ context.Context, cutoff time.Time, statuses []DeliveryStatus) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inSet := func(s DeliveryStatus) bool {
		for _, x := range statuses {
			if x == s {
				return true
			}
		}
		return false
	}
	n := 0
	for id, d := range f.m {
		if d.CreatedAt.Before(cutoff) && inSet(d.Status) {
			delete(f.m, id)
			n++
		}
	}
	return n, nil
}
func (f *fakeDeliveries) CountByStatus(_ context.Context, status DeliveryStatus) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.m {
		if d.Status == status {
			n++
		}
	}
	return n, nil
}

type fakeJobs struct {
	m map[string]ReplayJob
}

func newFakeJobs() *fakeJobs { return &fakeJobs{m: map[string]ReplayJob{}} }
func (f *fakeJobs) CreateJob(_ context.Context, j ReplayJob) error {
	f.m[j.ID] = j
	return nil
}
func (f *fakeJobs) GetJob(_ context.Context, id string) (ReplayJob, bool, error) {
	j, ok := f.m[id]
	return j, ok, nil
}
func (f *fakeJobs) ListJobs(context.Context, int) ([]ReplayJob, error) {
	var out []ReplayJob
	for _, j := range f.m {
		out = append(out, j)
	}
	return out, nil
}
func (f *fakeJobs) ClaimPendingJobs(context.Context, int) ([]ReplayJob, error) {
	var out []ReplayJob
	for _, j := range f.m {
		if j.Status == JobPending {
			out = append(out, j)
		}
	}
	return out, nil
}
func (f *fakeJobs) UpdateJob(_ context.Context, j ReplayJob) error {
	f.m[j.ID] = j
	return nil
}

// ---- replay tests -----------------------------------------------------------

func TestReplay_WindowReadsCommittedOnlyAndPreserves(t *testing.T) {
	src := &fakeSource{chains: map[string][]domain.AuditEvent{
		"c1": {auditEvt("c1", 1, "ratification"), auditEvt("c1", 2, "deprecation")},
	}}
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, Secret: "s3cr3t", Enabled: true, Operations: []string{"ratification"}, MaxRetries: 3})
	del := newFakeDeliveries()
	jobs := newFakeJobs()
	_ = jobs.CreateJob(context.Background(), ReplayJob{ID: "j1", WebhookID: "w1", Status: JobPending})

	NewReplayWorker(jobs, src, wh, del, del, nil, quiet(), ReplayConfig{}).RunOnce(context.Background())

	// Only the subscribed (ratification) committed event was replayed.
	all := del.all()
	if len(all) != 1 {
		t.Fatalf("deliveries = %d, want 1 (only subscribed committed event)", len(all))
	}
	d := all[0]
	// Payload preserved: event content equals the committed audit event verbatim.
	if d.Event.EventID != "evt_1" || d.Event.Operation != "ratification" || d.Event.ChainID != "c1" || d.Event.Seq != 1 {
		t.Fatalf("event not preserved: %+v", d.Event)
	}
	// Signature preserved: verifies over the canonical payload with the secret.
	payload, headers, err := DeliveryView(d, "s3cr3t", time.Now())
	if err != nil {
		t.Fatalf("DeliveryView: %v", err)
	}
	ts, _ := strconv.ParseInt(headers[HeaderTimestamp], 10, 64)
	if !Verify("s3cr3t", ts, d.ID, payload, headers[HeaderSignature]) {
		t.Fatal("replayed delivery signature does not verify")
	}
	// Job completed and counted.
	j, _, _ := jobs.GetJob(context.Background(), "j1")
	if j.Status != JobCompleted || j.EventsReplayed != 1 {
		t.Fatalf("job = %+v", j)
	}
}

func TestReplay_ByDeliveryIDsRequeues(t *testing.T) {
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{{ID: "d1", WebhookID: "w1", Status: StatusDeadLetter, Event: Event{Operation: "ratification"}}})
	jobs := newFakeJobs()
	_ = jobs.CreateJob(context.Background(), ReplayJob{ID: "j1", WebhookID: "w1", DeliveryIDs: []string{"d1"}, Status: JobPending})

	NewReplayWorker(jobs, &fakeSource{}, newFakeWebhooks(), del, del, nil, quiet(), ReplayConfig{}).RunOnce(context.Background())

	if del.status("d1") != StatusPending {
		t.Fatalf("delivery not requeued: %q", del.status("d1"))
	}
	j, _, _ := jobs.GetJob(context.Background(), "j1")
	if j.Status != JobCompleted || j.EventsReplayed != 1 {
		t.Fatalf("job = %+v", j)
	}
}

// TestRetry_ReusesExistingDispatcher proves dead-letter recovery re-drives the
// unchanged dispatcher: requeue flips status to pending, then the PRS-018
// dispatcher delivers it.
func TestRetry_ReusesExistingDispatcher(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{{ID: "d1", WebhookID: "w1", Status: StatusDeadLetter, NextAttemptAt: time.Unix(0, 0), Event: Event{Operation: "ratification"}}})

	n, _ := del.RequeueDeadLetters(context.Background(), "w1")
	if n != 1 || del.status("d1") != StatusPending {
		t.Fatalf("requeue failed: n=%d status=%q", n, del.status("d1"))
	}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil })
	NewDispatcher(del, wh, doer, nil, quiet(), DispatcherConfig{}).RunOnce(context.Background())
	if del.status("d1") != StatusDelivered {
		t.Fatalf("existing dispatcher did not deliver requeued item: %q", del.status("d1"))
	}
}

// ---- retention --------------------------------------------------------------

func TestRetention_DeletesTerminalOnly(t *testing.T) {
	del := newFakeDeliveries()
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()
	_ = del.Enqueue(context.Background(), []Delivery{
		{ID: "old-delivered", Status: StatusDelivered, CreatedAt: old},
		{ID: "old-deadletter", Status: StatusDeadLetter, CreatedAt: old},
		{ID: "old-pending", Status: StatusPending, CreatedAt: old},
		{ID: "old-failed", Status: StatusFailed, CreatedAt: old},
		{ID: "fresh-delivered", Status: StatusDelivered, CreatedAt: fresh},
	})
	m := &countMetrics{}
	w := NewRetentionWorker(del, m, quiet(), RetentionConfig{MaxAge: 24 * time.Hour})
	w.RunOnce(context.Background())

	// Old terminal deliveries pruned; pending/failed and fresh retained.
	for _, id := range []string{"old-delivered", "old-deadletter"} {
		if _, ok, _ := del.GetDelivery(context.Background(), id); ok {
			t.Errorf("%s should have been pruned", id)
		}
	}
	for _, id := range []string{"old-pending", "old-failed", "fresh-delivered"} {
		if _, ok, _ := del.GetDelivery(context.Background(), id); !ok {
			t.Errorf("%s must be retained", id)
		}
	}
	if m.active != 0 { // countMetrics has no dead-letter field; SetDeadLetters unused there
		_ = m
	}
}

func (m *countMetrics) IncReplayJob()                       {}
func (m *countMetrics) AddReplayEvents(int)                 {}
func (m *countMetrics) ObserveReplayDuration(time.Duration) {}
func (m *countMetrics) SetDeadLetters(int)                  {}

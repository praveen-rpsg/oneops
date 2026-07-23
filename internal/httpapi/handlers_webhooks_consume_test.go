package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/observability"
)

type fakeConsume struct {
	deliveries map[string]events.Delivery
	jobs       map[string]events.ReplayJob
	requeued   []string
	dlRetried  int
}

func newFakeConsume() *fakeConsume {
	return &fakeConsume{deliveries: map[string]events.Delivery{}, jobs: map[string]events.ReplayJob{}}
}

func (f *fakeConsume) GetDelivery(_ context.Context, id string) (events.Delivery, bool, error) {
	d, ok := f.deliveries[id]
	return d, ok, nil
}
func (f *fakeConsume) Requeue(_ context.Context, ids []string) (int, error) {
	f.requeued = append(f.requeued, ids...)
	return len(ids), nil
}
func (f *fakeConsume) RequeueDeadLetters(_ context.Context, _ string) (int, error) {
	f.dlRetried++
	return 2, nil
}
func (f *fakeConsume) ListDeadLetters(_ context.Context, _ string, _ int) ([]events.Delivery, error) {
	return []events.Delivery{{ID: "dl1", Status: events.StatusDeadLetter, Event: events.Event{Operation: "ratification"}}}, nil
}
func (f *fakeConsume) DeleteOlderThan(context.Context, time.Time, []events.DeliveryStatus) (int, error) {
	return 0, nil
}
func (f *fakeConsume) CountByStatus(context.Context, events.DeliveryStatus) (int, error) {
	return 0, nil
}

func (f *fakeConsume) CreateJob(_ context.Context, j events.ReplayJob) error {
	f.jobs[j.ID] = j
	return nil
}
func (f *fakeConsume) GetJob(_ context.Context, id string) (events.ReplayJob, bool, error) {
	j, ok := f.jobs[id]
	return j, ok, nil
}
func (f *fakeConsume) ListJobs(context.Context, int) ([]events.ReplayJob, error) {
	var out []events.ReplayJob
	for _, j := range f.jobs {
		out = append(out, j)
	}
	return out, nil
}
func (f *fakeConsume) ClaimPendingJobs(context.Context, int) ([]events.ReplayJob, error) {
	return nil, nil
}
func (f *fakeConsume) UpdateJob(_ context.Context, j events.ReplayJob) error {
	f.jobs[j.ID] = j
	return nil
}

func newConsumeAPI(t *testing.T) (http.Handler, *fakeWebhookReg, *fakeConsume) {
	t.Helper()
	reg := newFakeWebhookReg()
	con := newFakeConsume()
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: false}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetWebhooks(reg, func(context.Context, events.Webhook) (events.DeliveryStatus, error) {
		return events.StatusDelivered, nil
	})
	s.SetWebhookConsume(con, con)
	return s.Router(), reg, con
}

func TestReplayEndpointCreatesJob(t *testing.T) {
	h, reg, con := newConsumeAPI(t)
	_ = reg.Create(context.Background(), events.Webhook{ID: "wh_1", URL: "https://x", Enabled: true})

	rec := do(h, http.MethodPost, "/v1/admin/webhooks/wh_1/replay", map[string]any{"delivery_ids": []string{"d1", "d2"}}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var job replayJobDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &job)
	if job.ID == "" || job.Status != "pending" || len(job.DeliveryIDs) != 2 {
		t.Fatalf("job = %+v", job)
	}
	if len(con.jobs) != 1 {
		t.Fatalf("job not persisted: %d", len(con.jobs))
	}
}

func TestDeliveryInspection(t *testing.T) {
	h, reg, con := newConsumeAPI(t)
	_ = reg.Create(context.Background(), events.Webhook{ID: "wh_1", URL: "https://x", Secret: "s3cr3t", Enabled: true})
	con.deliveries["d1"] = events.Delivery{
		ID: "d1", WebhookID: "wh_1", Status: events.StatusDelivered, RetryCount: 2, LastStatusCode: 200,
		Event: events.Event{Operation: "ratification", ChainID: "c1", CfgID: "c1", Seq: 1, EventID: "evt_1"},
	}
	rec := do(h, http.MethodGet, "/v1/admin/webhooks/wh_1/deliveries/d1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var detail deliveryDetailDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.Attempts != 2 || detail.LastStatusCode != 200 || len(detail.Payload) == 0 {
		t.Fatalf("detail = %+v", detail)
	}
	// Headers include the signature; the payload verifies against the secret.
	sig := detail.Headers[events.HeaderSignature]
	ts := detail.Headers[events.HeaderTimestamp]
	if sig == "" || ts == "" {
		t.Fatalf("missing signature headers: %v", detail.Headers)
	}
	// Wrong webhook -> 404.
	if r2 := do(h, http.MethodGet, "/v1/admin/webhooks/other/deliveries/d1", nil, nil); r2.Code != http.StatusNotFound {
		t.Fatalf("cross-webhook: status = %d, want 404", r2.Code)
	}
}

func TestRetryDeliveryAndDeadLetters(t *testing.T) {
	h, reg, con := newConsumeAPI(t)
	_ = reg.Create(context.Background(), events.Webhook{ID: "wh_1", URL: "https://x", Enabled: true})
	con.deliveries["d1"] = events.Delivery{ID: "d1", WebhookID: "wh_1", Status: events.StatusDeadLetter}

	if rec := do(h, http.MethodPost, "/v1/admin/webhooks/wh_1/deliveries/d1/retry", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d", rec.Code)
	}
	if len(con.requeued) != 1 || con.requeued[0] != "d1" {
		t.Fatalf("requeued = %v", con.requeued)
	}

	dl := do(h, http.MethodGet, "/v1/admin/webhooks/deadletters", nil, nil)
	if dl.Code != http.StatusOK {
		t.Fatalf("deadletters status = %d", dl.Code)
	}
	var list struct {
		Items []deliveryDTO `json:"items"`
	}
	_ = json.Unmarshal(dl.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Status != "dead_letter" {
		t.Fatalf("deadletters = %+v", list.Items)
	}

	if rec := do(h, http.MethodPost, "/v1/admin/webhooks/deadletters/retry", map[string]any{}, nil); rec.Code != http.StatusOK {
		t.Fatalf("dl retry status = %d", rec.Code)
	}
	if con.dlRetried != 1 {
		t.Fatalf("dead-letter retry not invoked: %d", con.dlRetried)
	}
}

func TestReplayJobsListingAndGet(t *testing.T) {
	h, _, con := newConsumeAPI(t)
	con.jobs["rpl_1"] = events.ReplayJob{ID: "rpl_1", WebhookID: "wh_1", Status: events.JobCompleted, EventsReplayed: 5}

	l := do(h, http.MethodGet, "/v1/admin/webhooks/replay/jobs", nil, nil)
	if l.Code != http.StatusOK {
		t.Fatalf("list status = %d", l.Code)
	}
	var list struct {
		Items []replayJobDTO `json:"items"`
	}
	_ = json.Unmarshal(l.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].EventsReplayed != 5 {
		t.Fatalf("jobs = %+v", list.Items)
	}

	g := do(h, http.MethodGet, "/v1/admin/webhooks/replay/jobs/rpl_1", nil, nil)
	if g.Code != http.StatusOK {
		t.Fatalf("get status = %d", g.Code)
	}
	if miss := do(h, http.MethodGet, "/v1/admin/webhooks/replay/jobs/nope", nil, nil); miss.Code != http.StatusNotFound {
		t.Fatalf("missing job: status = %d, want 404", miss.Code)
	}
}

func TestConsumeUnwiredReturns500(t *testing.T) {
	reg := newFakeWebhookReg()
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: false}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetWebhooks(reg, nil) // registry set, consume not
	if rec := do(s.Router(), http.MethodGet, "/v1/admin/webhooks/replay/jobs", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

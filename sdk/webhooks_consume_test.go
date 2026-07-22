package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWebhooksConsumeClient(t *testing.T) {
	var lastPath, lastMethod string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/admin/webhooks/wh_1/replay":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(ReplayJob{ID: "rpl_1", Status: "pending", DeliveryIDs: []string{"d1"}})
		case "/v1/admin/webhooks/wh_1/deliveries/d1":
			_ = json.NewEncoder(w).Encode(DeliveryDetail{ID: "d1", Attempts: 2, Status: "delivered", Headers: map[string]string{"X-OneOps-Signature": "sig"}})
		case "/v1/admin/webhooks/wh_1/deliveries/d1/retry":
			_ = json.NewEncoder(w).Encode(map[string]any{"requeued": 1})
		case "/v1/admin/webhooks/deadletters":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []WebhookDelivery{{ID: "dl1", Status: "dead_letter"}}})
		case "/v1/admin/webhooks/deadletters/retry":
			_ = json.NewEncoder(w).Encode(map[string]any{"requeued": 3})
		case "/v1/admin/webhooks/replay/jobs":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []ReplayJob{{ID: "rpl_1", EventsReplayed: 5}}})
		case "/v1/admin/webhooks/replay/jobs/rpl_1":
			_ = json.NewEncoder(w).Encode(ReplayJob{ID: "rpl_1", Status: "completed"})
		}
	}))
	wh := c.Webhooks()
	ctx := context.Background()

	job, err := wh.Replay(ctx, "wh_1", ReplayInput{DeliveryIDs: []string{"d1"}})
	if err != nil || job.ID != "rpl_1" || lastMethod != http.MethodPost {
		t.Fatalf("Replay: %+v %v", job, err)
	}
	d, err := wh.Delivery(ctx, "wh_1", "d1")
	if err != nil || d.Attempts != 2 || d.Headers["X-OneOps-Signature"] != "sig" {
		t.Fatalf("Delivery: %+v %v", d, err)
	}
	if n, err := wh.RetryDelivery(ctx, "wh_1", "d1"); err != nil || n != 1 {
		t.Fatalf("RetryDelivery: %d %v", n, err)
	}
	dls, err := wh.DeadLetters(ctx, "", 10)
	if err != nil || len(dls) != 1 || dls[0].Status != "dead_letter" {
		t.Fatalf("DeadLetters: %+v %v", dls, err)
	}
	if n, err := wh.RetryDeadLetters(ctx, ""); err != nil || n != 3 {
		t.Fatalf("RetryDeadLetters: %d %v", n, err)
	}
	jobs, err := wh.ReplayJobs(ctx, 0)
	if err != nil || len(jobs) != 1 || jobs[0].EventsReplayed != 5 {
		t.Fatalf("ReplayJobs: %+v %v", jobs, err)
	}
	got, err := wh.ReplayJob(ctx, "rpl_1")
	if err != nil || got.Status != "completed" || lastPath != "/v1/admin/webhooks/replay/jobs/rpl_1" {
		t.Fatalf("ReplayJob: %+v %v", got, err)
	}
}

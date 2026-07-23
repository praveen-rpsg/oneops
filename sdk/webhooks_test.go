package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWebhooksClient(t *testing.T) {
	var lastMethod, lastPath string
	var lastBody map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastMethod, lastPath = r.Method, r.URL.Path
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&lastBody)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/webhooks":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Webhook{ID: "wh_1", URL: "https://x", Secret: "s3cr3t", Enabled: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/webhooks":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []Webhook{{ID: "wh_1", URL: "https://x"}}})
		case r.Method == http.MethodPatch:
			_ = json.NewEncoder(w).Encode(Webhook{ID: "wh_1", Secret: "rotated"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/admin/webhooks/wh_1/test":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "delivered"})
		case r.URL.Path == "/v1/admin/webhooks/wh_1/deliveries":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []WebhookDelivery{{ID: "d1", Status: "delivered"}}})
		}
	}))
	wh := c.Webhooks()
	ctx := context.Background()

	created, err := wh.CreateWebhook(ctx, CreateWebhookInput{URL: "https://x", Operations: []string{"ratification"}})
	if err != nil || created.Secret != "s3cr3t" {
		t.Fatalf("CreateWebhook: %+v %v", created, err)
	}
	if lastMethod != http.MethodPost || lastBody["url"] != "https://x" {
		t.Fatalf("create request wrong: %s %v", lastMethod, lastBody)
	}

	list, err := wh.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "wh_1" {
		t.Fatalf("List: %+v %v", list, err)
	}

	upd, err := wh.UpdateWebhook(ctx, "wh_1", UpdateWebhookInput{RotateSecret: true})
	if err != nil || upd.Secret != "rotated" || lastPath != "/v1/admin/webhooks/wh_1" {
		t.Fatalf("UpdateWebhook: %+v %v", upd, err)
	}

	if err := wh.DeleteWebhook(ctx, "wh_1"); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}

	status, err := wh.TestWebhook(ctx, "wh_1")
	if err != nil || status != "delivered" {
		t.Fatalf("TestWebhook: %q %v", status, err)
	}

	ds, err := wh.Deliveries(ctx, "wh_1", 10)
	if err != nil || len(ds) != 1 || ds[0].Status != "delivered" {
		t.Fatalf("Deliveries: %+v %v", ds, err)
	}
}

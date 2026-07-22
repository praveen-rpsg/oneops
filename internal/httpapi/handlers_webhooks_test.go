package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/observability"
)

type fakeWebhookReg struct {
	mu         sync.Mutex
	m          map[string]events.Webhook
	deliveries map[string][]events.Delivery
}

func newFakeWebhookReg() *fakeWebhookReg {
	return &fakeWebhookReg{m: map[string]events.Webhook{}, deliveries: map[string][]events.Delivery{}}
}
func (f *fakeWebhookReg) Create(_ context.Context, w events.Webhook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[w.ID] = w
	return nil
}
func (f *fakeWebhookReg) Get(_ context.Context, id string) (events.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.m[id]
	if !ok {
		return events.Webhook{}, domain.ErrNotFound
	}
	return w, nil
}
func (f *fakeWebhookReg) List(context.Context) ([]events.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []events.Webhook
	for _, w := range f.m {
		out = append(out, w)
	}
	return out, nil
}
func (f *fakeWebhookReg) Update(_ context.Context, w events.Webhook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[w.ID]; !ok {
		return domain.ErrNotFound
	}
	f.m[w.ID] = w
	return nil
}
func (f *fakeWebhookReg) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.m, id)
	return nil
}
func (f *fakeWebhookReg) ListByWebhook(_ context.Context, id string, _ int) ([]events.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deliveries[id], nil
}

func newWebhookAPI(t *testing.T, wire bool) (http.Handler, *fakeWebhookReg) {
	t.Helper()
	reg := newFakeWebhookReg()
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: false}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if wire {
		s.SetWebhooks(reg, func(context.Context, events.Webhook) (events.DeliveryStatus, error) {
			return events.StatusDelivered, nil
		})
	}
	return s.Router(), reg
}

func TestWebhookCreateRevealsSecretThenRedacts(t *testing.T) {
	h, _ := newWebhookAPI(t, true)

	rec := do(h, http.MethodPost, "/v1/admin/webhooks", map[string]any{"url": "https://sub/hook", "operations": []string{"ratification"}}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created webhookDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" || created.Secret == "" || created.URL != "https://sub/hook" {
		t.Fatalf("created = %+v", created)
	}

	// List redacts the secret.
	l := do(h, http.MethodGet, "/v1/admin/webhooks", nil, nil)
	var list struct {
		Items []webhookDTO `json:"items"`
	}
	_ = json.Unmarshal(l.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Secret != "" {
		t.Fatalf("list leaked secret or wrong count: %+v", list.Items)
	}
}

func TestWebhookMissingURL(t *testing.T) {
	h, _ := newWebhookAPI(t, true)
	if rec := do(h, http.MethodPost, "/v1/admin/webhooks", map[string]any{}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestWebhookRotateSecret(t *testing.T) {
	h, reg := newWebhookAPI(t, true)
	_ = reg.Create(context.Background(), events.Webhook{ID: "wh_1", URL: "https://x", Secret: "old", Enabled: true, MaxRetries: 3})

	rec := do(h, http.MethodPatch, "/v1/admin/webhooks/wh_1", map[string]any{"rotate_secret": true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var updated webhookDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Secret == "" || updated.Secret == "old" {
		t.Fatalf("secret not rotated: %+v", updated)
	}
	got, _ := reg.Get(context.Background(), "wh_1")
	if got.Secret == "old" {
		t.Fatal("stored secret not rotated")
	}
}

func TestWebhookEnableDisableAndDelete(t *testing.T) {
	h, reg := newWebhookAPI(t, true)
	_ = reg.Create(context.Background(), events.Webhook{ID: "wh_1", URL: "https://x", Enabled: true, MaxRetries: 3})

	do(h, http.MethodPatch, "/v1/admin/webhooks/wh_1", map[string]any{"enabled": false}, nil)
	if got, _ := reg.Get(context.Background(), "wh_1"); got.Enabled {
		t.Fatal("webhook not disabled")
	}
	if rec := do(h, http.MethodDelete, "/v1/admin/webhooks/wh_1", nil, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}
	if rec := do(h, http.MethodDelete, "/v1/admin/webhooks/wh_1", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("re-delete status = %d, want 404", rec.Code)
	}
}

func TestWebhookTestAndDeliveries(t *testing.T) {
	h, reg := newWebhookAPI(t, true)
	_ = reg.Create(context.Background(), events.Webhook{ID: "wh_1", URL: "https://x", Enabled: true, MaxRetries: 3})
	reg.deliveries["wh_1"] = []events.Delivery{{ID: "d1", WebhookID: "wh_1", Status: events.StatusDelivered, Event: events.Event{Operation: "ratification", CfgID: "c1", Seq: 1}}}

	tr := do(h, http.MethodPost, "/v1/admin/webhooks/wh_1/test", nil, nil)
	if tr.Code != http.StatusOK {
		t.Fatalf("test status = %d", tr.Code)
	}
	var testResp map[string]any
	_ = json.Unmarshal(tr.Body.Bytes(), &testResp)
	if testResp["status"] != "delivered" {
		t.Fatalf("test result = %v", testResp)
	}

	dr := do(h, http.MethodGet, "/v1/admin/webhooks/wh_1/deliveries", nil, nil)
	var deliveries struct {
		Items []deliveryDTO `json:"items"`
	}
	_ = json.Unmarshal(dr.Body.Bytes(), &deliveries)
	if len(deliveries.Items) != 1 || deliveries.Items[0].Status != "delivered" {
		t.Fatalf("deliveries = %+v", deliveries.Items)
	}
}

func TestWebhookRBAC(t *testing.T) {
	reg := newFakeWebhookReg()
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetWebhooks(reg, func(context.Context, events.Webhook) (events.DeliveryStatus, error) {
		return events.StatusDelivered, nil
	})
	h := s.Router()

	editor := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-editor"})}
	if rec := do(h, http.MethodGet, "/v1/admin/webhooks", nil, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("editor: status = %d, want 403", rec.Code)
	}
	admin := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-admin"})}
	if rec := do(h, http.MethodGet, "/v1/admin/webhooks", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200", rec.Code)
	}
}

func TestWebhookUnwiredReturns500(t *testing.T) {
	h, _ := newWebhookAPI(t, false)
	if rec := do(h, http.MethodGet, "/v1/admin/webhooks", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

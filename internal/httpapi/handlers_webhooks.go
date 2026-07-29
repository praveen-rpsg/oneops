package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/safehttp"
)

// webhookRegistry is the read/write surface the admin webhook API depends on.
// *postgres.WebhookStore satisfies it. Delivery is triggered via webhookTester,
// which reuses the events Dispatcher (no delivery logic in the transport layer).
type webhookRegistry interface {
	Create(ctx context.Context, w events.Webhook) error
	Get(ctx context.Context, id string) (events.Webhook, error)
	List(ctx context.Context) ([]events.Webhook, error)
	Update(ctx context.Context, w events.Webhook) error
	Delete(ctx context.Context, id string) error
	ListByWebhook(ctx context.Context, id string, limit int) ([]events.Delivery, error)
}

// SetWebhooks wires the webhook administration API. tester delivers one synthetic
// event to a webhook (reusing the dispatcher) and returns the resulting status.
func (s *Server) SetWebhooks(reg webhookRegistry, tester func(ctx context.Context, w events.Webhook) (events.DeliveryStatus, error)) {
	s.webhooks = reg
	s.webhookTester = tester
}

type webhookDTO struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	Enabled    bool      `json:"enabled"`
	Operations []string  `json:"operations"`
	Resources  []string  `json:"resources"`
	MaxRetries int       `json:"max_retries"`
	Secret     string    `json:"secret,omitempty"` // returned ONLY on create/rotate
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toWebhookDTO(w events.Webhook, revealSecret bool) webhookDTO {
	d := webhookDTO{
		ID: w.ID, URL: w.URL, Enabled: w.Enabled, Operations: w.Operations,
		Resources: w.Resources, MaxRetries: w.MaxRetries, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
	if d.Operations == nil {
		d.Operations = []string{}
	}
	if d.Resources == nil {
		d.Resources = []string{}
	}
	if revealSecret {
		d.Secret = w.Secret
	}
	return d
}

type createWebhookRequest struct {
	URL        string   `json:"url"`
	Enabled    *bool    `json:"enabled,omitempty"`
	Operations []string `json:"operations,omitempty"`
	Resources  []string `json:"resources,omitempty"`
	MaxRetries int      `json:"max_retries,omitempty"`
}

type patchWebhookRequest struct {
	URL          *string  `json:"url,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
	Operations   []string `json:"operations,omitempty"`
	Resources    []string `json:"resources,omitempty"`
	MaxRetries   *int     `json:"max_retries,omitempty"`
	RotateSecret bool     `json:"rotate_secret,omitempty"`
}

type deliveryDTO struct {
	ID             string    `json:"id"`
	WebhookID      string    `json:"webhook_id"`
	Operation      string    `json:"operation"`
	CfgID          string    `json:"cfg_id"`
	Seq            int64     `json:"seq"`
	Status         string    `json:"status"`
	RetryCount     int       `json:"retry_count"`
	LastStatusCode int       `json:"last_status_code"`
	LastAttempt    time.Time `json:"last_attempt,omitempty"`
	NextAttemptAt  time.Time `json:"next_attempt_at,omitempty"`
	// DeliveredTo is where the most recent attempt actually went, recorded at
	// attempt time. It is deliberately not the subscription's current URL: that
	// is mutable, and reading it here made the history of where governed data
	// was sent retroactively rewritable (ADR-GOV-004). Absent until attempted.
	DeliveredTo string `json:"delivered_to,omitempty"`
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksReady(w, r) {
		return
	}
	items, err := s.webhooks.List(r.Context())
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]webhookDTO, 0, len(items))
	for _, wh := range items {
		out = append(out, toWebhookDTO(wh, false)) // secrets redacted
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksReady(w, r) {
		return
	}
	var req createWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
		return
	}
	if req.URL == "" {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "url is required")
		return
	}
	// Reject non-http(s) schemes at creation for fast feedback; the authoritative
	// SSRF egress guard (non-public IPs) is enforced at dial time in safehttp
	// (ADR-SECURITY-001), because a hostname's resolution can change afterwards.
	if err := safehttp.ValidateWebhookURL(req.URL); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	maxRetries := req.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}
	wh := events.Webhook{
		ID: "wh_" + randHex(8), URL: req.URL, Secret: randHex(32), Enabled: enabled,
		Operations: req.Operations, Resources: req.Resources, MaxRetries: maxRetries,
	}
	if err := s.webhooks.Create(r.Context(), wh); err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.webhooks.Get(r.Context(), wh.ID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	s.log.Info("webhook created", "webhook_id", wh.ID, "request_id", RequestIDFrom(r.Context()))
	writeJSON(w, http.StatusCreated, toWebhookDTO(created, true)) // reveal secret once
}

func (s *Server) patchWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	cur, err := s.webhooks.Get(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	var req patchWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
		return
	}
	if req.URL != nil {
		cur.URL = *req.URL
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if req.Operations != nil {
		cur.Operations = req.Operations
	}
	if req.Resources != nil {
		cur.Resources = req.Resources
	}
	if req.MaxRetries != nil && *req.MaxRetries > 0 {
		cur.MaxRetries = *req.MaxRetries
	}
	rotated := false
	if req.RotateSecret {
		cur.Secret = randHex(32)
		rotated = true
	}
	if err := s.webhooks.Update(r.Context(), cur); err != nil {
		s.mapError(w, r, err)
		return
	}
	s.log.Info("webhook updated", "webhook_id", id, "secret_rotated", rotated, "request_id", RequestIDFrom(r.Context()))
	writeJSON(w, http.StatusOK, toWebhookDTO(cur, rotated)) // reveal secret only if rotated
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksReady(w, r) {
		return
	}
	if err := s.webhooks.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksReady(w, r) {
		return
	}
	if s.webhookTester == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "delivery unavailable")
		return
	}
	wh, err := s.webhooks.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	status, terr := s.webhookTester(r.Context(), wh)
	resp := map[string]any{"status": string(status)}
	if terr != nil {
		resp["error"] = terr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.webhooks.Get(r.Context(), id); err != nil {
		s.mapError(w, r, err)
		return
	}
	ds, err := s.webhooks.ListByWebhook(r.Context(), id, s.pageLimit(r))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]deliveryDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, deliveryDTO{
			ID: d.ID, WebhookID: d.WebhookID, Operation: d.Event.Operation, CfgID: d.Event.CfgID,
			Seq: d.Event.Seq, Status: string(d.Status), RetryCount: d.RetryCount,
			LastStatusCode: d.LastStatusCode, LastAttempt: d.LastAttempt, NextAttemptAt: d.NextAttemptAt,
			DeliveredTo: d.DeliveredTo,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) webhooksReady(w http.ResponseWriter, r *http.Request) bool {
	if s.webhooks == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "webhook registry unavailable")
		return false
	}
	return true
}

var errRand = errors.New("httpapi: random source failed")

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(errRand)
	}
	return hex.EncodeToString(b)
}

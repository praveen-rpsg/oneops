package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ReplayInput selects what to replay: a time window, specific delivery ids, or
// (both empty) the webhook's full committed history.
type ReplayInput struct {
	From        *time.Time `json:"from,omitempty"`
	To          *time.Time `json:"to,omitempty"`
	DeliveryIDs []string   `json:"delivery_ids,omitempty"`
}

// ReplayJob is an async replay job.
type ReplayJob struct {
	ID             string    `json:"id"`
	WebhookID      string    `json:"webhook_id"`
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
	DeliveryIDs    []string  `json:"delivery_ids"`
	Status         string    `json:"status"`
	EventsReplayed int       `json:"events_replayed"`
	Error          string    `json:"error"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DeliveryDetail is a delivery's full inspection view.
type DeliveryDetail struct {
	ID             string            `json:"id"`
	WebhookID      string            `json:"webhook_id"`
	Operation      string            `json:"operation"`
	CfgID          string            `json:"cfg_id"`
	Seq            int64             `json:"seq"`
	Status         string            `json:"status"`
	Attempts       int               `json:"attempts"`
	LastStatusCode int               `json:"last_status_code"`
	LastAttempt    time.Time         `json:"last_attempt"`
	NextAttemptAt  time.Time         `json:"next_attempt_at"`
	Headers        map[string]string `json:"headers"`
	Payload        json.RawMessage   `json:"payload"`
}

// Replay requests a replay of committed events to a webhook (async job).
func (wc *WebhooksClient) Replay(ctx context.Context, id string, in ReplayInput) (*ReplayJob, error) {
	var out ReplayJob
	if err := wc.c.do(ctx, http.MethodPost, "/v1/admin/webhooks/"+url.PathEscape(id)+"/replay", nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delivery returns a single delivery's full inspection view.
func (wc *WebhooksClient) Delivery(ctx context.Context, id, deliveryID string) (*DeliveryDetail, error) {
	var out DeliveryDetail
	path := "/v1/admin/webhooks/" + url.PathEscape(id) + "/deliveries/" + url.PathEscape(deliveryID)
	if err := wc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetryDelivery re-queues a single delivery; returns the number requeued.
func (wc *WebhooksClient) RetryDelivery(ctx context.Context, id, deliveryID string) (int, error) {
	var out struct {
		Requeued int `json:"requeued"`
	}
	path := "/v1/admin/webhooks/" + url.PathEscape(id) + "/deliveries/" + url.PathEscape(deliveryID) + "/retry"
	if err := wc.c.do(ctx, http.MethodPost, path, nil, nil, &out); err != nil {
		return 0, err
	}
	return out.Requeued, nil
}

// DeadLetters lists dead-letter deliveries (optionally for one webhook).
func (wc *WebhooksClient) DeadLetters(ctx context.Context, webhookID string, limit int) ([]WebhookDelivery, error) {
	qs := url.Values{}
	if webhookID != "" {
		qs.Set("webhook_id", webhookID)
	}
	if limit > 0 {
		qs.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/admin/webhooks/deadletters"
	if len(qs) > 0 {
		path += "?" + qs.Encode()
	}
	var out struct {
		Items []WebhookDelivery `json:"items"`
	}
	if err := wc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// RetryDeadLetters re-queues dead-letter deliveries (empty webhookID = all).
func (wc *WebhooksClient) RetryDeadLetters(ctx context.Context, webhookID string) (int, error) {
	var out struct {
		Requeued int `json:"requeued"`
	}
	body := map[string]string{}
	if webhookID != "" {
		body["webhook_id"] = webhookID
	}
	if err := wc.c.do(ctx, http.MethodPost, "/v1/admin/webhooks/deadletters/retry", nil, body, &out); err != nil {
		return 0, err
	}
	return out.Requeued, nil
}

// ReplayJobs lists recent replay jobs.
func (wc *WebhooksClient) ReplayJobs(ctx context.Context, limit int) ([]ReplayJob, error) {
	path := "/v1/admin/webhooks/replay/jobs"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out struct {
		Items []ReplayJob `json:"items"`
	}
	if err := wc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ReplayJob returns a single replay job.
func (wc *WebhooksClient) ReplayJob(ctx context.Context, jobID string) (*ReplayJob, error) {
	var out ReplayJob
	if err := wc.c.do(ctx, http.MethodGet, "/v1/admin/webhooks/replay/jobs/"+url.PathEscape(jobID), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

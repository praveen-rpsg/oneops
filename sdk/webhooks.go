package sdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Webhook is a registered event subscriber. Secret is populated only in the
// response to CreateWebhook and to UpdateWebhook with RotateSecret=true.
type Webhook struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	Enabled    bool      `json:"enabled"`
	Operations []string  `json:"operations"`
	Resources  []string  `json:"resources"`
	MaxRetries int       `json:"max_retries"`
	Secret     string    `json:"secret,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WebhookDelivery is a delivery record for a webhook.
type WebhookDelivery struct {
	ID             string    `json:"id"`
	WebhookID      string    `json:"webhook_id"`
	Operation      string    `json:"operation"`
	CfgID          string    `json:"cfg_id"`
	Seq            int64     `json:"seq"`
	Status         string    `json:"status"`
	RetryCount     int       `json:"retry_count"`
	LastStatusCode int       `json:"last_status_code"`
	LastAttempt    time.Time `json:"last_attempt"`
	NextAttemptAt  time.Time `json:"next_attempt_at"`
}

// CreateWebhookInput is the body for CreateWebhook.
type CreateWebhookInput struct {
	URL        string   `json:"url"`
	Enabled    *bool    `json:"enabled,omitempty"`
	Operations []string `json:"operations,omitempty"`
	Resources  []string `json:"resources,omitempty"`
	MaxRetries int      `json:"max_retries,omitempty"`
}

// UpdateWebhookInput is the body for UpdateWebhook; nil fields are unchanged.
type UpdateWebhookInput struct {
	URL          *string  `json:"url,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
	Operations   []string `json:"operations,omitempty"`
	Resources    []string `json:"resources,omitempty"`
	MaxRetries   *int     `json:"max_retries,omitempty"`
	RotateSecret bool     `json:"rotate_secret,omitempty"`
}

// WebhooksClient administers event-delivery webhooks (admin permission required).
type WebhooksClient struct {
	c *Client
}

// Webhooks returns the webhook administration client.
func (c *Client) Webhooks() *WebhooksClient { return &WebhooksClient{c: c} }

// List returns all registered webhooks (secrets redacted).
func (wc *WebhooksClient) List(ctx context.Context) ([]Webhook, error) {
	var out struct {
		Items []Webhook `json:"items"`
	}
	if err := wc.c.do(ctx, http.MethodGet, "/v1/admin/webhooks", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreateWebhook registers a webhook; the response includes the generated secret.
func (wc *WebhooksClient) CreateWebhook(ctx context.Context, in CreateWebhookInput) (*Webhook, error) {
	var out Webhook
	if err := wc.c.do(ctx, http.MethodPost, "/v1/admin/webhooks", nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateWebhook patches a webhook; if RotateSecret is set the new secret is returned.
func (wc *WebhooksClient) UpdateWebhook(ctx context.Context, id string, in UpdateWebhookInput) (*Webhook, error) {
	var out Webhook
	if err := wc.c.do(ctx, http.MethodPatch, "/v1/admin/webhooks/"+url.PathEscape(id), nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWebhook removes a webhook (delivery history is retained).
func (wc *WebhooksClient) DeleteWebhook(ctx context.Context, id string) error {
	return wc.c.do(ctx, http.MethodDelete, "/v1/admin/webhooks/"+url.PathEscape(id), nil, nil, nil)
}

// TestWebhook triggers one synthetic delivery and returns its status.
func (wc *WebhooksClient) TestWebhook(ctx context.Context, id string) (string, error) {
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := wc.c.do(ctx, http.MethodPost, "/v1/admin/webhooks/"+url.PathEscape(id)+"/test", nil, nil, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

// Deliveries returns a webhook's recent deliveries.
func (wc *WebhooksClient) Deliveries(ctx context.Context, id string, limit int) ([]WebhookDelivery, error) {
	path := "/v1/admin/webhooks/" + url.PathEscape(id) + "/deliveries"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out struct {
		Items []WebhookDelivery `json:"items"`
	}
	if err := wc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

package events

import (
	"context"
	"net/http"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// EventSource is the read-only, committed audit log the relay tails. It is
// satisfied by *postgres.AuditStore (ListChainIDs + ListEvents) — the relay adds
// no new read path and never touches the write/verify path.
type EventSource interface {
	ListChainIDs(ctx context.Context) ([]string, error)
	ListEvents(ctx context.Context, chainID string, cursor int64, desc bool, limit int, operation string) ([]domain.AuditEvent, error)
}

// CursorStore persists the relay's per-chain progress cursor (last relayed seq).
// It lives in its own table — it never touches the audit schema.
type CursorStore interface {
	GetCursor(ctx context.Context, chainID string) (int64, error)
	SetCursor(ctx context.Context, chainID string, seq int64) error
}

// WebhookStore persists the webhook registry (CRUD + secret rotation).
type WebhookStore interface {
	Create(ctx context.Context, w Webhook) error
	Get(ctx context.Context, id string) (Webhook, error)
	List(ctx context.Context) ([]Webhook, error)
	ListEnabled(ctx context.Context) ([]Webhook, error)
	Update(ctx context.Context, w Webhook) error
	Delete(ctx context.Context, id string) error
}

// DeliveryStore persists delivery records and their status transitions.
type DeliveryStore interface {
	Enqueue(ctx context.Context, ds []Delivery) error
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Delivery, error)
	MarkResult(ctx context.Context, id string, claimToken time.Time, status DeliveryStatus, retryCount, statusCode int, lastAttempt, nextAttempt time.Time) error
	// ReleaseClaim returns an unused claim and the attempt it consumed, for a
	// worker stopped between claiming and attempting (ADR-CONCURRENCY-006).
	ReleaseClaim(ctx context.Context, id string, claimToken time.Time) error
	ListByWebhook(ctx context.Context, webhookID string, limit int) ([]Delivery, error)
}

// HTTPDoer is the minimal HTTP client the dispatcher needs (satisfied by
// *http.Client). Declaring it keeps the dispatcher testable without a network.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// EventOwnerResolver and ErrEventNotFound are the shared execution-security
// framework, defined in domain so the dispatcher and the policy executor use
// one contract and one error value (ADR-TENANCY-003). Aliased here so this
// package's call sites and fakes read naturally.
type EventOwnerResolver = domain.EventOwnerResolver

// ErrEventNotFound is domain.ErrEventNotFound.
var ErrEventNotFound = domain.ErrEventNotFound

package events

import (
	"context"
	"errors"
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
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]Delivery, error)
	MarkResult(ctx context.Context, id string, status DeliveryStatus, retryCount, statusCode int, lastAttempt, nextAttempt time.Time) error
	ListByWebhook(ctx context.Context, webhookID string, limit int) ([]Delivery, error)
}

// HTTPDoer is the minimal HTTP client the dispatcher needs (satisfied by
// *http.Client). Declaring it keeps the dispatcher testable without a network.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// EventOwnerResolver returns the authoritative tenant that owns a committed
// event, read from the audit log rather than from any queue row.
//
// This is the source of truth for execution-time ownership. A delivery row
// carries an owner label, but that label is queue metadata: it can be forged
// self-consistently by anyone with database write access, and the dispatcher
// dead-lettering a mismatch between two fields of the same forged row proves
// nothing. audit_event is append-only and its tenant_id is written inside the
// single governance transaction (ADR-AUDIT-005), so it is the one place an
// event's owner cannot be rewritten after the fact.
//
// ErrEventNotFound means no committed event matches — the delivery references
// an event that does not exist, which is itself grounds to refuse.
type EventOwnerResolver interface {
	ResolveEventOwner(ctx context.Context, chainID string, seq int64) (string, error)
}

// ErrEventNotFound is returned by EventOwnerResolver when no committed event
// matches the chain and sequence. Execution treats it as a refusal, not a
// transient error: an event that is not in the authoritative log will never
// appear there, because the log is append-only and sequence numbers are dense.
var ErrEventNotFound = errors.New("no committed event for chain and sequence")

// Package notification is the Notification Service: it enqueues persisted,
// tracked notifications and delivers them at least once under leadership,
// mirroring internal/events' webhook-delivery shape (ADR-CONCURRENCY-001/002/005/007).
//
// It replaces the fire-and-forget internal/policy.Notifier call with a real
// record: every notification a policy action produces (and any future
// producer) is persisted with a delivery status, an attempt count and a last
// error, rather than being logged and forgotten.
package notification

import (
	"context"
	"errors"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// Store is the persistence port both the enqueuing Service and the delivery
// Worker depend on. *postgres.NotificationStore satisfies it.
type Store interface {
	// Enqueue inserts a pending notification, due immediately.
	Enqueue(ctx context.Context, n *domain.Notification) (*domain.Notification, error)
	// Get returns a notification by id, or domain.ErrNotFound.
	Get(ctx context.Context, id string) (*domain.Notification, error)
	// List returns a tenant-scoped page of notifications, optionally filtered by
	// status ("" means all), newest first.
	List(ctx context.Context, status string, limit int, after string) ([]*domain.Notification, error)

	// ClaimDue atomically claims due notifications for delivery, charging the
	// attempt at claim time (ADR-CONCURRENCY-006), under FOR UPDATE SKIP LOCKED
	// so no two workers claim the same row (ADR-CONCURRENCY-002/007). Mirrors
	// events.DeliveryStore.ClaimDue.
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*domain.Notification, error)
	// ReleaseClaim returns an unused claim to the queue and gives back the
	// attempt it charged, for a worker stopped between claiming and attempting
	// (demotion, shutdown) — the honest counterpart to ClaimDue's accounting.
	ReleaseClaim(ctx context.Context, id string, claimToken time.Time) error
	// MarkSent records a successful delivery, fenced on the claim token
	// (ADR-CONCURRENCY-005). Returns ErrStaleClaim if the row was reclaimed.
	MarkSent(ctx context.Context, id string, claimToken, sentAt time.Time) error
	// MarkFailed records a failed attempt, fenced on the claim token. A zero
	// `next` means the retry budget is exhausted: no further attempt will ever
	// be claimed (the dead-letter role, without a fourth status). A non-zero
	// `next` schedules a retry. Returns ErrStaleClaim if the row was reclaimed.
	MarkFailed(ctx context.Context, id string, claimToken time.Time, lastErr string, next time.Time) error
}

// ErrStaleClaim is returned by MarkSent/MarkFailed when the row was reclaimed
// by another worker before this one recorded its result: the fencing token no
// longer matches, so nothing was written. It is not a failure — the current
// owner will record the authoritative outcome (ADR-CONCURRENCY-005).
var ErrStaleClaim = errors.New("notification: result fenced — row reclaimed by another worker")

// Metrics receives delivery observability signals. NopMetrics discards them.
type Metrics interface {
	IncDelivered()
	IncFailed()
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

// IncDelivered implements Metrics.
func (NopMetrics) IncDelivered() {}

// IncFailed implements Metrics.
func (NopMetrics) IncFailed() {}

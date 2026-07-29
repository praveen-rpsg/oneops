package events

import (
	"context"
	"time"
)

// ReplayJobStatus is the lifecycle state of a replay job.
type ReplayJobStatus string

// Replay job states.
const (
	JobPending   ReplayJobStatus = "pending"
	JobRunning   ReplayJobStatus = "running"
	JobCompleted ReplayJobStatus = "completed"
	JobFailed    ReplayJobStatus = "failed"
)

// ReplayJob is an operator-requested replay of already-committed events to one
// webhook. It NEVER regenerates events: it either re-enqueues events read from
// the immutable audit log within a time window, or re-sends existing deliveries
// by id. It is executed asynchronously by the ReplayWorker.
type ReplayJob struct {
	ID             string
	WebhookID      string
	From           time.Time // time-window mode (zero when using DeliveryIDs)
	To             time.Time
	DeliveryIDs    []string // delivery-id mode
	Status         ReplayJobStatus
	EventsReplayed int
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// ClaimedAt is the fencing token: the moment this job was claimed by the
	// worker now holding it, carried from ClaimPendingJobs into UpdateJob, which
	// writes only if the job is still claimed under the same token
	// (ADR-CONCURRENCY-007). Zero on jobs never claimed.
	ClaimedAt time.Time
}

// ReplayJobStore persists replay jobs (its own table; never the audit schema).
type ReplayJobStore interface {
	CreateJob(ctx context.Context, job ReplayJob) error
	GetJob(ctx context.Context, id string) (ReplayJob, bool, error)
	ListJobs(ctx context.Context, limit int) ([]ReplayJob, error)
	ClaimPendingJobs(ctx context.Context, limit int) ([]ReplayJob, error)
	UpdateJob(ctx context.Context, job ReplayJob) error
}

// DeliveryOps is the read/admin surface for delivery inspection, recovery, and
// retention. It is additive to the delivery store — it adds no delivery logic.
type DeliveryOps interface {
	GetDelivery(ctx context.Context, id string) (Delivery, bool, error)
	Requeue(ctx context.Context, ids []string) (int, error)
	RequeueDeadLetters(ctx context.Context, webhookID string) (int, error) // webhookID "" = all
	ListDeadLetters(ctx context.Context, webhookID string, limit int) ([]Delivery, error)
	DeleteOlderThan(ctx context.Context, cutoff time.Time, statuses []DeliveryStatus) (int, error)
	CountByStatus(ctx context.Context, status DeliveryStatus) (int, error)
}

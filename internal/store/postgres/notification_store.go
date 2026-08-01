package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/notification"
)

// defaultNotificationPageSize bounds an unbounded List; maxNotificationPageSize
// caps what a caller may ask for. Same shape as the other identity/queue stores.
const (
	defaultNotificationPageSize = 50
	maxNotificationPageSize     = 500
)

const notificationCols = `notification_id, tenant_id, channel, recipient, subject, body, status,
	attempts, last_error, next_attempt_at, claimed_at, row_version, created_at, updated_at`

// NotificationStore administers the notification table: enqueue/read for the
// Service and the admin API, and the atomic claim/fence pair for the delivery
// Worker.
//
// notification is TENANT-OWNED — it is in TenantOwnedTables and carries
// row-level security — but unlike TeamStore this store is also built from the
// PRIVILEGED pool for the delivery worker, exactly as WebhookStore is: the
// worker serves every tenant from one process, so it needs the connection that
// bypasses row-level security, while the admin read API (Get/List) is built
// from the tenant-scoped pool so one tenant cannot read another's
// notifications.
//
// It has no administrative-audit chokepoint for the same reason team and
// webhook do not: ADR-AUDIT-007 §6.2 scopes that chokepoint to five named
// identity-governance tables, and a Notification is neither identity
// governance nor a Configuration Object.
type NotificationStore struct {
	pool *pgxpool.Pool
}

// NewNotificationStore builds the store over the given pool.
func NewNotificationStore(pool *pgxpool.Pool) *NotificationStore {
	return &NotificationStore{pool: pool}
}

var _ notification.Store = (*NotificationStore)(nil)

func clampNotificationPage(limit int) int {
	if limit <= 0 {
		return defaultNotificationPageSize
	}
	if limit > maxNotificationPageSize {
		return maxNotificationPageSize
	}
	return limit
}

func scanNotification(sc rowScanner) (*domain.Notification, error) {
	var (
		n             domain.Notification
		channel       string
		status        string
		nextAttemptAt *time.Time
		claimedAt     *time.Time
	)
	if err := sc.Scan(
		&n.NotificationID, &n.TenantID, &channel, &n.Recipient, &n.Subject, &n.Body, &status,
		&n.Attempts, &n.LastError, &nextAttemptAt, &claimedAt, &n.RowVersion, &n.CreatedAt, &n.UpdatedAt,
	); err != nil {
		return nil, err
	}
	n.Channel = domain.NotificationChannel(channel)
	n.Status = domain.NotificationStatus(status)
	if nextAttemptAt != nil {
		n.NextAttemptAt = *nextAttemptAt
	}
	if claimedAt != nil {
		n.ClaimedAt = *claimedAt
	}
	return &n, nil
}

// Enqueue inserts a pending notification.
//
// tenant_id is taken from n.TenantID, not from context, and deliberately so:
// the delivery worker's Store is built from the privileged pool with no bound
// tenant, and the "notification" policy action calls Enqueue from that
// worker's execution path (via notification.PolicyNotifier). The producer
// already re-derived the authoritative owner (ADR-TENANCY-003) before this is
// called — the same reasoning WebhookStore.Enqueue's doc comment records for
// events.Delivery.Event.TenantID.
func (s *NotificationStore) Enqueue(ctx context.Context, n *domain.Notification) (*domain.Notification, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO notification
			(notification_id, tenant_id, channel, recipient, subject, body, status,
			 attempts, last_error, next_attempt_at, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,0,'',$8,1,now(),now())
		RETURNING `+notificationCols,
		n.NotificationID, n.TenantID, string(n.Channel), n.Recipient, n.Subject, n.Body,
		string(domain.NotificationPending), nullTime(n.NextAttemptAt))
	created, err := scanNotification(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("enqueue notification: %w", err)
	}
	return created, nil
}

// Get returns a notification by id, or domain.ErrNotFound.
func (s *NotificationStore) Get(ctx context.Context, id string) (*domain.Notification, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+notificationCols+` FROM notification WHERE notification_id = $1`, id)
	n, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get notification: %w", err)
	}
	return n, nil
}

// List returns a tenant-scoped page of notifications, keyset-paginated over
// notification_id (a ULID, so ordered by creation), optionally filtered by
// status.
func (s *NotificationStore) List(ctx context.Context, status string, limit int, after string) ([]*domain.Notification, error) {
	limit = clampNotificationPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+notificationCols+`
		  FROM notification
		 WHERE ($1 = '' OR status = $1) AND ($2 = '' OR notification_id > $2)
		 ORDER BY notification_id
		 LIMIT $3`, status, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Notification, 0, limit)
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notifications: %w", err)
	}
	return out, nil
}

// ClaimDue atomically claims due notifications for delivery (ADR-CONCURRENCY-007).
//
// Unlike webhook_delivery there is no distinct "inflight" status: status stays
// pending/failed throughout, and the claim is tracked by claimed_at alone —
// the same lease shape (ADR-CONCURRENCY-002), just without a status value
// dedicated to it. The status transition happening elsewhere (MarkSent /
// MarkFailed) is why claimed_at must be cleared there and not left stale: a row
// left claimed after its outcome is recorded would wrongly block reclaim for a
// full lease even once it is due again.
//
// attempts is incremented here, in the same statement that grants the claim —
// "the claim charges the attempt" (ADR-CONCURRENCY-006) — so a worker that
// crashes after claiming but before recording an outcome still consumes
// budget; the worker (not this query) compares the returned Attempts against
// its configured MaxAttempts to decide retry vs exhaustion.
func (s *NotificationStore) ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*domain.Notification, error) {
	staleBefore := now.Add(-lease)
	rows, err := s.pool.Query(ctx, `
		WITH candidate AS (
		    SELECT notification_id
		      FROM notification
		     WHERE status IN ('pending','failed')
		       AND next_attempt_at IS NOT NULL AND next_attempt_at <= $1
		       AND (claimed_at IS NULL OR claimed_at < $2)
		     ORDER BY next_attempt_at
		     LIMIT $3
		     FOR UPDATE SKIP LOCKED
		),
		claimed AS (
		    UPDATE notification n
		       SET claimed_at = $1, attempts = n.attempts + 1, updated_at = now()
		      FROM candidate c
		     WHERE n.notification_id = c.notification_id
		    RETURNING `+prefixCols(notificationCols, "n")+`
		)
		SELECT `+notificationCols+` FROM claimed`, now, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("claim notifications: %w", err)
	}
	defer rows.Close()

	var out []*domain.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ReleaseClaim returns an unused claim to the queue: claimed_at is cleared and
// the attempt it charged is given back, so a worker stopped between claiming
// and attempting (demotion, shutdown) does not burn retry budget it never
// used. Fenced on the claim token exactly like MarkSent/MarkFailed.
func (s *NotificationStore) ReleaseClaim(ctx context.Context, id string, claimToken time.Time) error {
	if claimToken.IsZero() {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE notification
		   SET claimed_at = NULL, attempts = GREATEST(attempts - 1, 0), updated_at = now()
		 WHERE notification_id = $1 AND claimed_at = $2`, id, claimToken)
	if err != nil {
		return fmt.Errorf("release notification claim: %w", err)
	}
	return nil
}

// MarkSent records a successful delivery, fenced on the claim token
// (ADR-CONCURRENCY-005). claimed_at is cleared: sent is terminal, and a stale
// claim left in place would serve no purpose.
func (s *NotificationStore) MarkSent(ctx context.Context, id string, claimToken, sentAt time.Time) error {
	var tokenPtr *time.Time
	if !claimToken.IsZero() {
		tokenPtr = &claimToken
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE notification
		   SET status = 'sent', claimed_at = NULL, next_attempt_at = NULL,
		       row_version = row_version + 1, updated_at = $3
		 WHERE notification_id = $1 AND ($2::timestamptz IS NULL OR claimed_at = $2)`,
		id, tokenPtr, sentAt)
	if err != nil {
		return fmt.Errorf("mark notification sent: %w", err)
	}
	if tag.RowsAffected() == 0 && tokenPtr != nil {
		return notification.ErrStaleClaim
	}
	return nil
}

// MarkFailed records a failed attempt, fenced on the claim token. A zero
// `next` clears next_attempt_at to NULL — the retry budget is exhausted, and
// ClaimDue's `next_attempt_at IS NOT NULL` predicate excludes the row from
// every future claim, the dead_letter role without a fourth status. claimed_at
// is cleared either way, for the reason ClaimDue's doc comment gives.
func (s *NotificationStore) MarkFailed(ctx context.Context, id string, claimToken time.Time, lastErr string, next time.Time) error {
	var tokenPtr, nextPtr *time.Time
	if !claimToken.IsZero() {
		tokenPtr = &claimToken
	}
	if !next.IsZero() {
		nextPtr = &next
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE notification
		   SET status = 'failed', claimed_at = NULL, last_error = $3,
		       next_attempt_at = $4, row_version = row_version + 1, updated_at = now()
		 WHERE notification_id = $1 AND ($2::timestamptz IS NULL OR claimed_at = $2)`,
		id, tokenPtr, lastErr, nextPtr)
	if err != nil {
		return fmt.Errorf("mark notification failed: %w", err)
	}
	if tag.RowsAffected() == 0 && tokenPtr != nil {
		return notification.ErrStaleClaim
	}
	return nil
}

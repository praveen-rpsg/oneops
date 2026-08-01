package domain

import (
	"strings"
	"time"
)

// NotificationChannel is where a Notification is delivered.
type NotificationChannel string

const (
	NotificationEmail   NotificationChannel = "email"
	NotificationWebhook NotificationChannel = "webhook"
	// NotificationInApp is persisted only — there is no external send, so a
	// Notification on this channel goes from pending to sent the moment the
	// worker claims it (there is nothing that can fail in flight).
	NotificationInApp NotificationChannel = "inapp"
)

// Valid reports whether c is a defined channel.
func (c NotificationChannel) Valid() bool {
	return c == NotificationEmail || c == NotificationWebhook || c == NotificationInApp
}

// NotificationStatus is the lifecycle of a Notification.
//
// Three states, not four: unlike webhook_delivery there is no distinct
// "inflight"/claimed status. A claim is tracked by ClaimedAt alone (a lease,
// ADR-CONCURRENCY-002) while Status stays pending/failed, and a permanently
// exhausted retry budget is expressed by NextAttemptAt going to the zero value
// rather than by a fourth status — the same information without a wider enum.
type NotificationStatus string

const (
	NotificationPending NotificationStatus = "pending"
	NotificationSent    NotificationStatus = "sent"
	NotificationFailed  NotificationStatus = "failed"
)

// Valid reports whether s is a defined status.
func (s NotificationStatus) Valid() bool {
	return s == NotificationPending || s == NotificationSent || s == NotificationFailed
}

// CanTransition reports whether moving from s to next is a legal lifecycle
// move: pending -> sent, pending -> failed, or failed -> failed (a retry that
// failed again). sent and a final failed are terminal; nothing leaves them.
func (s NotificationStatus) CanTransition(next NotificationStatus) bool {
	switch s {
	case NotificationPending:
		return next == NotificationSent || next == NotificationFailed
	case NotificationFailed:
		return next == NotificationSent || next == NotificationFailed
	default:
		return false
	}
}

// Field bounds mirrored by the notification table's CHECK constraints, so an
// invalid request fails with a field-level 422 rather than a constraint
// violation surfacing as a 500.
const (
	MaxNotificationRecipientLength = 320
	MaxNotificationSubjectLength   = 200
	MaxNotificationBodyLength      = 10000
)

// Notification is a tracked, persisted delivery of one message to one
// recipient over one channel — the record webhook_delivery is for outbound
// webhooks, but for the platform's own outbound notifications (policy actions
// today, any future producer tomorrow).
//
// TenantID is carried on the struct rather than resolved from context, exactly
// as events.Delivery carries Event.TenantID: the worker that delivers
// notifications runs on the privileged, cross-tenant connection, so there is no
// bound tenant to read at enqueue time. The producer supplies it, and it must
// be the authoritative owner of whatever triggered the notification — never
// guessed (ADR-TENANCY-003/004).
type Notification struct {
	NotificationID string
	TenantID       string
	Channel        NotificationChannel
	Recipient      string
	Subject        string
	Body           string
	Status         NotificationStatus
	Attempts       int
	LastError      string
	// NextAttemptAt is when the notification becomes due again. The zero value
	// means "never" — either it has not yet been claimed for the first time in a
	// way that matters (a fresh pending row is due immediately, so this is set
	// to CreatedAt) or its retry budget is exhausted and no further attempt will
	// be made — the same role webhook_delivery's dead_letter status plays,
	// without a fourth status value.
	NextAttemptAt time.Time
	// ClaimedAt is the fencing token a delivery worker is handed by ClaimDue and
	// must present back to MarkSent/MarkFailed (ADR-CONCURRENCY-005). Zero means
	// unclaimed.
	ClaimedAt  time.Time
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Terminal reports whether the notification will never be attempted again.
func (n *Notification) Terminal() bool {
	return n.Status == NotificationSent || (n.Status == NotificationFailed && n.NextAttemptAt.IsZero())
}

// Validate enforces the invariants the database also enforces.
func (n *Notification) Validate() error {
	if strings.TrimSpace(n.NotificationID) == "" {
		return newValidation("notification_id", "must not be empty")
	}
	if strings.TrimSpace(n.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if !n.Channel.Valid() {
		return newValidation("channel", "must be one of: email, webhook, inapp")
	}
	recipient := strings.TrimSpace(n.Recipient)
	if recipient == "" {
		return newValidation("recipient", "must not be empty")
	}
	if len(recipient) > MaxNotificationRecipientLength {
		return newValidation("recipient", "must be at most 320 characters")
	}
	if len(n.Subject) > MaxNotificationSubjectLength {
		return newValidation("subject", "must be at most 200 characters")
	}
	if len(n.Body) > MaxNotificationBodyLength {
		return newValidation("body", "must be at most 10000 characters")
	}
	if !n.Status.Valid() {
		return newValidation("status", "must be one of: pending, sent, failed")
	}
	if n.Attempts < 0 {
		return newValidation("attempts", "must not be negative")
	}
	return nil
}

// NewNotification builds a pending notification, due immediately.
//
// tenantID is a parameter for the same reason NewTeam's is: the only correct
// source is the record that already carries it — here, the event or action
// that produced the notification. Deriving it from ambient context would mean
// guessing, and a notification labelled with the wrong tenant is a disclosure
// of one tenant's message to whatever holds another tenant's read access.
func NewNotification(tenantID string, channel NotificationChannel, recipient, subject, body string) (*Notification, error) {
	now := time.Now().UTC()
	n := &Notification{
		NotificationID: NewID(),
		TenantID:       strings.TrimSpace(tenantID),
		Channel:        channel,
		Recipient:      strings.TrimSpace(recipient),
		Subject:        subject,
		Body:           body,
		Status:         NotificationPending,
		NextAttemptAt:  now,
		RowVersion:     1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := n.Validate(); err != nil {
		return nil, err
	}
	return n, nil
}

// Package events implements enterprise event delivery for the OneOps Governance
// Platform. It is fully decoupled from the constitutional execution path: it
// never participates in a governance or audit transaction. Instead it TAILS the
// committed audit_event log (read-only) and fans out matching events to
// registered webhooks. Because an audit event exists only after the atomic
// governance+audit commit (ADR-AUDIT-005), an event is relayed if and only if its
// operation committed — so events are published only after commit, never before,
// and never on rollback. This package changes no governance or audit behavior.
package events

import (
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// DeliveryStatus is the lifecycle state of one webhook delivery.
type DeliveryStatus string

// Delivery states.
const (
	StatusPending    DeliveryStatus = "pending"
	StatusDelivered  DeliveryStatus = "delivered"
	StatusFailed     DeliveryStatus = "failed"
	StatusDeadLetter DeliveryStatus = "dead_letter"
)

// Webhook is a registered subscriber endpoint and its subscription filters.
type Webhook struct {
	ID string
	// TenantID owns the subscription. Delivery compares it against the event's
	// owner, because the relay reads every tenant's subscriptions and every
	// tenant's events from one privileged connection: row-level security is
	// bypassed there by design, so tenant identity has to be re-established in
	// the match rather than assumed from the query.
	TenantID   string
	URL        string
	Secret     string
	Enabled    bool
	Operations []string // empty => all governance operations
	Resources  []string // empty => all resources (configuration object ids)
	MaxRetries int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Matches reports whether this webhook subscribes to the given event. A disabled
// webhook matches nothing; empty Operations/Resources mean "all".
//
// The tenant comparison is first and is not optional. The relay cross-products
// every enabled subscription against every chain it can see, and it can see all
// of them — so without this check a subscription with no Operations and no
// Resources filter, which is the documented way to subscribe to everything,
// received every other tenant's governance events signed with its own HMAC
// secret. Verified against the running service: an attacker's endpoint received
// the victim's chain_id, cfg_id, operation, actor and event_id.
//
// Both sides are compared as empty-safe: an event or subscription that somehow
// carries no owner matches nothing, so a missing value fails closed rather than
// matching everything.
func (w Webhook) Matches(ev Event) bool {
	if !w.Enabled {
		return false
	}
	if w.TenantID == "" || ev.TenantID == "" || w.TenantID != ev.TenantID {
		return false
	}
	if len(w.Operations) > 0 && !contains(w.Operations, ev.Operation) {
		return false
	}
	if len(w.Resources) > 0 && !contains(w.Resources, ev.CfgID) {
		return false
	}
	return true
}

// Event is a committed governance event projected from an audit event.
type Event struct {
	// TenantID owns the event. Populated from the audit row, never inferred.
	TenantID    string
	ChainID     string
	Seq         int64
	EventID     string
	OperationID string
	Operation   string
	Actor       string
	CfgID       string // per-object chain: equals ChainID
	OccurredAt  time.Time
}

// eventFrom projects a committed audit event into a delivery Event.
func eventFrom(a domain.AuditEvent) Event {
	return Event{
		TenantID:    a.TenantID,
		ChainID:     a.ChainID,
		Seq:         a.Seq,
		EventID:     a.EventID,
		OperationID: a.OperationID,
		Operation:   string(a.Operation),
		Actor:       a.Actor,
		CfgID:       a.ChainID,
		OccurredAt:  a.OccurredAt,
	}
}

// Delivery is one webhook delivery attempt record and its status.
type Delivery struct {
	ID             string
	WebhookID      string
	Event          Event
	Status         DeliveryStatus
	RetryCount     int
	LastStatusCode int
	LastAttempt    time.Time
	NextAttemptAt  time.Time
	CreatedAt      time.Time
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

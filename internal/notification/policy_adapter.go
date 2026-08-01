package notification

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// PolicyNotifier adapts a Service to policy.Notifier, so a policy's
// "notification" action produces a tracked, persisted Notification instead of
// the previous fire-and-forget log line
// (internal/policy/actions.go:NotificationAction).
//
// The executor authorises ev.TenantID before any action runs
// (domain.ResolveAndAuthorize, internal/policy/executor.go), so by the time
// Notify is called it is the authoritative owner of the triggering event, not
// a queue-supplied label — it is safe to stamp onto the Notification directly.
type PolicyNotifier struct {
	svc *Service
}

var _ policy.Notifier = (*PolicyNotifier)(nil)

// NewPolicyNotifier builds the adapter over svc.
func NewPolicyNotifier(svc *Service) *PolicyNotifier { return &PolicyNotifier{svc: svc} }

// notificationActionConfig is the "notification" policy action's config shape:
//
//	{"channel": "email|webhook|inapp", "recipient": "...", "subject": "...", "body": "..."}
//
// subject/body are optional; a default naming the triggering event is used
// when absent. Rendering the event's own fields into a template is deferred
// (see the package doc's follow-up list) — today's body is either the
// operator's fixed text or the generated default, never interpolated.
type notificationActionConfig struct {
	Channel   string `json:"channel"`
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

// Notify implements policy.Notifier.
func (a *PolicyNotifier) Notify(ctx context.Context, ev policy.Event, config json.RawMessage) error {
	var cfg notificationActionConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return fmt.Errorf("notification action: bad config: %w", err)
		}
	}
	channel := domain.NotificationChannel(cfg.Channel)
	if channel == "" {
		// inapp is the safe default: no external destination is required, so a
		// policy that names no channel still produces a tracked record instead of
		// failing closed.
		channel = domain.NotificationInApp
	}
	subject := cfg.Subject
	if subject == "" {
		subject = fmt.Sprintf("policy notification: %s", ev.Operation)
	}
	body := cfg.Body
	if body == "" {
		body = fmt.Sprintf("event_id=%s operation=%s cfg_id=%s actor=%s", ev.EventID, ev.Operation, ev.CfgID, ev.Actor)
	}
	recipient := cfg.Recipient
	if recipient == "" && channel == domain.NotificationInApp {
		// An in-app notification with no named recipient still has a subject: the
		// actor whose action triggered the policy.
		recipient = ev.Actor
	}

	n, err := domain.NewNotification(ev.TenantID, channel, recipient, subject, body)
	if err != nil {
		return fmt.Errorf("notification action: %w", err)
	}
	_, err = a.svc.Enqueue(ctx, n)
	return err
}

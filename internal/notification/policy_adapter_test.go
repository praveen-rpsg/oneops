package notification

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

func TestPolicyNotifier_Notify_ProducesATrackedNotification(t *testing.T) {
	store := newFakeStore()
	adapter := NewPolicyNotifier(NewService(store))

	ev := policy.Event{
		TenantID: "t1", EventID: "evt_1", Operation: "ratification", CfgID: "cfg_1", Actor: "user:alice",
	}
	cfg := json.RawMessage(`{"channel":"webhook","recipient":"http://example.test/hook","subject":"hi","body":"there"}`)

	if err := adapter.Notify(context.Background(), ev, cfg); err != nil {
		t.Fatalf("notify: %v", err)
	}

	all := store.all()
	if len(all) != 1 {
		t.Fatalf("notifications persisted = %d, want 1 (fire-and-forget must be replaced by a tracked record)", len(all))
	}
	got := all[0]
	if got.TenantID != "t1" {
		t.Errorf("tenant = %q, want the event's authoritative tenant", got.TenantID)
	}
	if got.Channel != domain.NotificationWebhook || got.Recipient != "http://example.test/hook" {
		t.Errorf("channel/recipient mismatch: %+v", got)
	}
	if got.Status != domain.NotificationPending {
		t.Errorf("status = %q, want pending — the worker delivers it asynchronously", got.Status)
	}
}

func TestPolicyNotifier_Notify_NoConfigDefaultsToInApp(t *testing.T) {
	store := newFakeStore()
	adapter := NewPolicyNotifier(NewService(store))

	ev := policy.Event{TenantID: "t1", EventID: "evt_1", Operation: "ratification", Actor: "user:alice"}

	if err := adapter.Notify(context.Background(), ev, nil); err != nil {
		t.Fatalf("notify: %v", err)
	}
	all := store.all()
	if len(all) != 1 || all[0].Channel != domain.NotificationInApp {
		t.Fatalf("want one in-app notification, got %+v", all)
	}
	if all[0].Recipient != "user:alice" {
		t.Errorf("recipient = %q, want the triggering actor as a fallback", all[0].Recipient)
	}
}

func TestPolicyNotifier_Notify_BadConfigIsAnError(t *testing.T) {
	store := newFakeStore()
	adapter := NewPolicyNotifier(NewService(store))

	err := adapter.Notify(context.Background(), policy.Event{TenantID: "t1"}, json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("malformed config must be refused")
	}
	if len(store.all()) != 0 {
		t.Fatal("a refused config must not produce a notification")
	}
}

func TestPolicyNotifier_Notify_EmailWithNoRecipientIsRefused(t *testing.T) {
	store := newFakeStore()
	adapter := NewPolicyNotifier(NewService(store))

	err := adapter.Notify(context.Background(), policy.Event{TenantID: "t1"},
		json.RawMessage(`{"channel":"email"}`))
	if err == nil {
		t.Fatal("an email/webhook channel with no recipient must be refused, not silently dropped")
	}
}

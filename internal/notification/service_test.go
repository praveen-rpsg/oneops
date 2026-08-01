package notification

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

func TestService_Enqueue_ValidatesBeforePersisting(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	bad := &domain.Notification{NotificationID: "n1"} // missing tenant, channel, recipient
	if _, err := svc.Enqueue(context.Background(), bad); err == nil {
		t.Fatal("an invalid notification must be refused before it reaches the store")
	}
	if len(store.all()) != 0 {
		t.Fatal("a refused notification must never be persisted")
	}
}

func TestService_Enqueue_PersistsAValidNotification(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	n, err := domain.NewNotification("t1", domain.NotificationInApp, "alice", "s", "b")
	if err != nil {
		t.Fatalf("new notification: %v", err)
	}
	created, err := svc.Enqueue(context.Background(), n)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if created.Status != domain.NotificationPending {
		t.Errorf("status = %q, want pending", created.Status)
	}
	got, err := svc.Get(context.Background(), n.NotificationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NotificationID != n.NotificationID {
		t.Errorf("round-trip mismatch: got %+v", got)
	}

	list, err := svc.List(context.Background(), "", 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d items, want 1", len(list))
	}
}

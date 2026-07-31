package domain

import "testing"

func TestNewNotification_BuildsAPendingNotification(t *testing.T) {
	n, err := NewNotification("t1", NotificationEmail, "user@example.com", "hi", "body")
	if err != nil {
		t.Fatalf("new notification: %v", err)
	}
	if n.Status != NotificationPending {
		t.Errorf("status = %q, want pending", n.Status)
	}
	if n.NextAttemptAt.IsZero() {
		t.Error("a fresh notification must be due immediately, not zero")
	}
	if n.RowVersion != 1 {
		t.Errorf("row version = %d, want 1", n.RowVersion)
	}
	if n.NotificationID == "" {
		t.Error("notification id must be minted")
	}
}

func TestNewNotification_TrimsAndValidatesFields(t *testing.T) {
	if _, err := NewNotification("", NotificationEmail, "a@b.com", "s", "b"); err == nil {
		t.Error("empty tenant must be refused")
	}
	if _, err := NewNotification("t1", NotificationChannel("carrier-pigeon"), "a@b.com", "s", "b"); err == nil {
		t.Error("an unknown channel must be refused")
	}
	if _, err := NewNotification("t1", NotificationEmail, "  ", "s", "b"); err == nil {
		t.Error("an empty recipient must be refused")
	}
	n, err := NewNotification("t1", NotificationEmail, "  a@b.com  ", "s", "b")
	if err != nil {
		t.Fatalf("new notification: %v", err)
	}
	if n.Recipient != "a@b.com" {
		t.Errorf("recipient = %q, want trimmed", n.Recipient)
	}
}

func TestNotification_Validate_LengthBounds(t *testing.T) {
	n := &Notification{
		NotificationID: "n1", TenantID: "t1", Channel: NotificationInApp,
		Recipient: "user", Status: NotificationPending,
	}
	long := make([]byte, MaxNotificationSubjectLength+1)
	n.Subject = string(long)
	if err := n.Validate(); err == nil {
		t.Error("an over-length subject must be refused")
	}
	n.Subject = ""

	longBody := make([]byte, MaxNotificationBodyLength+1)
	n.Body = string(longBody)
	if err := n.Validate(); err == nil {
		t.Error("an over-length body must be refused")
	}
	n.Body = ""

	longRecipient := make([]byte, MaxNotificationRecipientLength+1)
	n.Recipient = string(longRecipient)
	if err := n.Validate(); err == nil {
		t.Error("an over-length recipient must be refused")
	}
}

func TestNotification_Validate_NegativeAttemptsRefused(t *testing.T) {
	n := &Notification{
		NotificationID: "n1", TenantID: "t1", Channel: NotificationInApp,
		Recipient: "user", Status: NotificationPending, Attempts: -1,
	}
	if err := n.Validate(); err == nil {
		t.Error("negative attempts must be refused")
	}
}

func TestNotificationStatus_CanTransition(t *testing.T) {
	cases := []struct {
		from, to NotificationStatus
		want     bool
	}{
		{NotificationPending, NotificationSent, true},
		{NotificationPending, NotificationFailed, true},
		{NotificationFailed, NotificationFailed, true}, // a retry that failed again
		{NotificationFailed, NotificationSent, true},   // a retry that succeeded
		{NotificationSent, NotificationFailed, false},  // sent is terminal
		{NotificationSent, NotificationSent, false},
		{NotificationPending, NotificationPending, false},
	}
	for _, c := range cases {
		if got := c.from.CanTransition(c.to); got != c.want {
			t.Errorf("%s -> %s = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestNotification_Terminal(t *testing.T) {
	sent := &Notification{Status: NotificationSent}
	if !sent.Terminal() {
		t.Error("sent must be terminal")
	}
	exhausted := &Notification{Status: NotificationFailed}
	if !exhausted.Terminal() {
		t.Error("failed with no NextAttemptAt (exhausted budget) must be terminal")
	}
	pendingRetry := &Notification{Status: NotificationFailed}
	pendingRetry.NextAttemptAt = pendingRetry.NextAttemptAt.Add(1) // non-zero
	if pendingRetry.Terminal() {
		t.Error("failed with a scheduled retry must not be terminal")
	}
	pending := &Notification{Status: NotificationPending}
	if pending.Terminal() {
		t.Error("pending must not be terminal")
	}
}

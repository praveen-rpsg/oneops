package notification

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore is an in-memory Store. Its ClaimDue/MarkSent/MarkFailed mirror the
// real store's contract closely enough to exercise the worker: the claim
// charges the attempt, a claimed row is excluded until its lease elapses, and
// an outcome write is fenced on the claim token — the exact shape
// events.fakeDeliveries uses for the dispatcher's own tests.
type fakeStore struct {
	mu sync.Mutex
	m  map[string]*domain.Notification

	claimErr   error
	releaseErr error
	staleIDs   map[string]bool
	markErr    map[string]error
}

func newFakeStore(seed ...*domain.Notification) *fakeStore {
	f := &fakeStore{m: map[string]*domain.Notification{}}
	for _, n := range seed {
		cp := *n
		f.m[n.NotificationID] = &cp
	}
	return f
}

func (f *fakeStore) Enqueue(_ context.Context, n *domain.Notification) (*domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *n
	f.m[n.NotificationID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeStore) Get(_ context.Context, id string) (*domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.m[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	out := *n
	return &out, nil
}

func (f *fakeStore) List(_ context.Context, status string, limit int, _ string) ([]*domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*domain.Notification
	for _, n := range f.m {
		if status == "" || string(n.Status) == status {
			cp := *n
			out = append(out, &cp)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) ClaimDue(_ context.Context, now time.Time, lease time.Duration, limit int) ([]*domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	var out []*domain.Notification
	for _, n := range f.m {
		due := !n.NextAttemptAt.IsZero() && !n.NextAttemptAt.After(now)
		unclaimed := n.ClaimedAt.IsZero() || n.ClaimedAt.Before(now.Add(-lease))
		if (n.Status == domain.NotificationPending || n.Status == domain.NotificationFailed) && due && unclaimed {
			n.Attempts++
			n.ClaimedAt = now
			cp := *n
			out = append(out, &cp)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeStore) ReleaseClaim(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return f.releaseErr
	}
	n := f.m[id]
	if n == nil {
		return nil
	}
	if n.Attempts > 0 {
		n.Attempts--
	}
	n.ClaimedAt = time.Time{}
	return nil
}

func (f *fakeStore) MarkSent(_ context.Context, id string, _, sentAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staleIDs[id] {
		return ErrStaleClaim
	}
	if err, ok := f.markErr[id]; ok {
		return err
	}
	n := f.m[id]
	n.Status = domain.NotificationSent
	n.ClaimedAt = time.Time{}
	n.NextAttemptAt = time.Time{}
	n.UpdatedAt = sentAt
	return nil
}

func (f *fakeStore) MarkFailed(_ context.Context, id string, _ time.Time, lastErr string, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staleIDs[id] {
		return ErrStaleClaim
	}
	if err, ok := f.markErr[id]; ok {
		return err
	}
	n := f.m[id]
	n.Status = domain.NotificationFailed
	n.ClaimedAt = time.Time{}
	n.LastError = lastErr
	n.NextAttemptAt = next
	return nil
}

func (f *fakeStore) all() []*domain.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.Notification, 0, len(f.m))
	for _, n := range f.m {
		out = append(out, n)
	}
	return out
}

func (f *fakeStore) get(id string) *domain.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[id]
}

func newPending(id string, channel domain.NotificationChannel, recipient string, due time.Time) *domain.Notification {
	return &domain.Notification{
		NotificationID: id, TenantID: "t-test", Channel: channel, Recipient: recipient,
		Subject: "s", Body: "b", Status: domain.NotificationPending, NextAttemptAt: due,
		RowVersion: 1, CreatedAt: due, UpdatedAt: due,
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}
}

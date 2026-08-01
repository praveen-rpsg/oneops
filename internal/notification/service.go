package notification

import (
	"context"

	"github.com/rpsg/oneops/internal/domain"
)

// Service is the write/read seam every producer and the admin API use. It is
// the one path a notification is created through — enqueuing it directly on
// the store would skip Validate — mirroring how Team and Membership route
// every mutation through a constructor plus Validate before it ever reaches
// SQL.
type Service struct {
	store Store
}

// NewService builds a Service over store.
func NewService(store Store) *Service { return &Service{store: store} }

// Enqueue validates and persists a new notification, pending and due
// immediately. The delivery Worker picks it up on its next pass.
func (s *Service) Enqueue(ctx context.Context, n *domain.Notification) (*domain.Notification, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	return s.store.Enqueue(ctx, n)
}

// Get returns a notification by id, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (*domain.Notification, error) {
	return s.store.Get(ctx, id)
}

// List returns a tenant-scoped page of notifications, optionally filtered by
// status, newest first.
func (s *Service) List(ctx context.Context, status string, limit int, after string) ([]*domain.Notification, error) {
	return s.store.List(ctx, status, limit, after)
}

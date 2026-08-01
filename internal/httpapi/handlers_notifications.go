package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// notificationRegistry is the read surface the admin notification API depends
// on. *notification.Service satisfies it — notifications are produced only by
// the policy Notifier and any future producer, never created directly over
// HTTP, so the admin API is read-only.
type notificationRegistry interface {
	Get(ctx context.Context, id string) (*domain.Notification, error)
	List(ctx context.Context, status string, limit int, after string) ([]*domain.Notification, error)
}

// SetNotifications wires the notification registry. Until it is called the
// endpoints report 501 rather than 404, the same convention SetTeams uses.
func (s *Server) SetNotifications(reg notificationRegistry) { s.notifications = reg }

type notificationDTO struct {
	NotificationID string    `json:"notification_id"`
	Channel        string    `json:"channel"`
	Recipient      string    `json:"recipient"`
	Subject        string    `json:"subject"`
	Body           string    `json:"body"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	LastError      string    `json:"last_error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// toNotificationDTO deliberately omits tenant_id, the same choice toTeamDTO
// and toMembershipDTO make: the caller is already inside exactly one tenant.
func toNotificationDTO(n *domain.Notification) notificationDTO {
	return notificationDTO{
		NotificationID: n.NotificationID, Channel: string(n.Channel), Recipient: n.Recipient,
		Subject: n.Subject, Body: n.Body, Status: string(n.Status), Attempts: n.Attempts,
		LastError: n.LastError, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
	}
}

// listNotifications returns a page of the caller's tenant's notifications,
// optionally filtered by status. Row-level security confines the result to the
// caller's tenant.
func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	if s.notifications == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "notification administration is not configured")
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !domain.NotificationStatus(status).Valid() {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid status", "status must be one of: pending, sent, failed")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}

	items, err := s.notifications.List(r.Context(), status, limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]notificationDTO, 0, len(items))
	for _, n := range items {
		out = append(out, toNotificationDTO(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// getNotification returns one notification by identifier.
func (s *Server) getNotification(w http.ResponseWriter, r *http.Request) {
	if s.notifications == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "notification administration is not configured")
		return
	}
	n, err := s.notifications.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such notification")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toNotificationDTO(n))
}

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// SetOnCallSchedules wires the on-call schedule repository (E5.2a). Until it
// is called the endpoints report 501 rather than 404, the same convention
// every other administration surface in this package establishes.
func (s *Server) SetOnCallSchedules(repo domain.OnCallScheduleRepository) {
	s.onCallSchedules = repo
}

// onCallScheduleDTO deliberately omits tenant_id, the same choice every other
// tenant-scoped DTO in this package makes: the caller is already inside
// exactly one tenant.
type onCallScheduleDTO struct {
	ScheduleID             string    `json:"schedule_id"`
	Name                   string    `json:"name"`
	HandoffIntervalSeconds int       `json:"handoff_interval_seconds"`
	RotationStartAt        time.Time `json:"rotation_start_at"`
	Status                 string    `json:"status"`
	RowVersion             int64     `json:"row_version"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func toOnCallScheduleDTO(s *domain.OnCallSchedule) onCallScheduleDTO {
	return onCallScheduleDTO{
		ScheduleID: s.ScheduleID, Name: s.Name, HandoffIntervalSeconds: s.HandoffIntervalSeconds,
		RotationStartAt: s.RotationStartAt, Status: string(s.Status),
		RowVersion: s.RowVersion, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

type onCallParticipantDTO struct {
	ParticipantID string    `json:"participant_id"`
	ScheduleID    string    `json:"schedule_id"`
	UserID        string    `json:"user_id"`
	Position      int       `json:"position"`
	RowVersion    int64     `json:"row_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toOnCallParticipantDTO(p *domain.OnCallParticipant) onCallParticipantDTO {
	return onCallParticipantDTO{
		ParticipantID: p.ParticipantID, ScheduleID: p.ScheduleID, UserID: p.UserID,
		Position: p.Position, RowVersion: p.RowVersion, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// onCallNowDTO answers "who is on call right now (or at `at`)". user_id/
// display_name are both absent when the schedule has no participants — an
// ordinary, non-error state, not a 404.
type onCallNowDTO struct {
	ScheduleID  string    `json:"schedule_id"`
	At          time.Time `json:"at"`
	UserID      *string   `json:"user_id,omitempty"`
	DisplayName *string   `json:"display_name,omitempty"`
}

func toOnCallNowDTO(r *domain.OnCallResolution) onCallNowDTO {
	return onCallNowDTO{ScheduleID: r.ScheduleID, At: r.At, UserID: r.UserID, DisplayName: r.DisplayName}
}

// createOnCallScheduleRequest carries no schedule_id: minted server-side.
type createOnCallScheduleRequest struct {
	Name                   string    `json:"name"`
	HandoffIntervalSeconds int       `json:"handoff_interval_seconds"`
	RotationStartAt        time.Time `json:"rotation_start_at"`
}

// patchOnCallScheduleRequest changes one or more fields under optimistic
// locking, the same shape patchAlertRuleRequest uses.
type patchOnCallScheduleRequest struct {
	RowVersion             int64      `json:"row_version"`
	Name                   *string    `json:"name"`
	HandoffIntervalSeconds *int       `json:"handoff_interval_seconds"`
	RotationStartAt        *time.Time `json:"rotation_start_at"`
	Status                 *string    `json:"status"`
}

func (req patchOnCallScheduleRequest) anyFieldSet() bool {
	return req.Name != nil || req.HandoffIntervalSeconds != nil || req.RotationStartAt != nil || req.Status != nil
}

// addOnCallParticipantRequest carries no participant_id: minted server-side,
// and no position: AddParticipant always appends at the end.
type addOnCallParticipantRequest struct {
	UserID string `json:"user_id"`
}

// reorderOnCallParticipantsRequest names the schedule's full participant set,
// in the desired new order.
type reorderOnCallParticipantsRequest struct {
	ParticipantIDs []string `json:"participant_ids"`
}

// listOnCallSchedules returns a page of the caller's on-call schedules.
func (s *Server) listOnCallSchedules(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
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
	items, err := s.onCallSchedules.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]onCallScheduleDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toOnCallScheduleDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createOnCallSchedule declares a new on-call rotation.
func (s *Server) createOnCallSchedule(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	var req createOnCallScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller —
	// the same rule createTeam/createMaintenanceWindow enforce.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	sch, err := domain.NewOnCallSchedule(tenant.TenantID, req.Name, req.HandoffIntervalSeconds, req.RotationStartAt)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.onCallSchedules.Create(r.Context(), sch)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toOnCallScheduleDTO(created))
}

// getOnCallSchedule returns one schedule by identifier.
func (s *Server) getOnCallSchedule(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	sch, err := s.onCallSchedules.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such on-call schedule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOnCallScheduleDTO(sch))
}

// patchOnCallSchedule changes one or more fields of a schedule under
// optimistic locking.
func (s *Server) patchOnCallSchedule(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchOnCallScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this schedule"))
		return
	}
	if !req.anyFieldSet() {
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of name, handoff_interval_seconds, rotation_start_at or status"))
		return
	}

	patch := domain.OnCallSchedulePatch{
		Name: req.Name, HandoffIntervalSeconds: req.HandoffIntervalSeconds, RotationStartAt: req.RotationStartAt,
	}
	if req.Status != nil {
		st := domain.OnCallScheduleStatus(strings.TrimSpace(*req.Status))
		patch.Status = &st
	}

	updated, err := s.onCallSchedules.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such on-call schedule")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "on-call schedule was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOnCallScheduleDTO(updated))
}

// deleteOnCallSchedule removes a schedule and, via ON DELETE CASCADE, its
// participants.
func (s *Server) deleteOnCallSchedule(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	err := s.onCallSchedules.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such on-call schedule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listOnCallParticipants returns a page of a schedule's roster, in rotation
// order.
func (s *Server) listOnCallParticipants(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
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
	items, err := s.onCallSchedules.ListParticipants(r.Context(), chi.URLParam(r, "id"), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]onCallParticipantDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toOnCallParticipantDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// addOnCallParticipant appends a user to a schedule's rotation. user_id must
// already be an ACTIVE member of the caller's tenant — re-verified
// server-side (ADR-IDENTITY-002 §3.1) — a cross-tenant or suspended user_id
// is rejected with 404, not a 500.
func (s *Server) addOnCallParticipant(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	var req addOnCallParticipantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		s.mapError(w, r, domain.NewValidationError("user_id", "must not be empty"))
		return
	}

	added, err := s.onCallSchedules.AddParticipant(r.Context(), chi.URLParam(r, "id"), req.UserID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found",
			"no such on-call schedule, or user_id does not name an active member of this tenant")
		return
	case errors.Is(err, domain.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", "user is already a participant on this schedule")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toOnCallParticipantDTO(added))
}

// removeOnCallParticipant removes one participant from a schedule's
// rotation, closing the resulting gap in position.
func (s *Server) removeOnCallParticipant(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	err := s.onCallSchedules.RemoveParticipant(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "participantId"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such on-call schedule or participant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reorderOnCallParticipants atomically replaces a schedule's rotation order.
func (s *Server) reorderOnCallParticipants(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	var req reorderOnCallParticipantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	items, err := s.onCallSchedules.ReorderParticipants(r.Context(), chi.URLParam(r, "id"), req.ParticipantIDs)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such on-call schedule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]onCallParticipantDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toOnCallParticipantDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// getOnCallNow answers "who is on call for this schedule" at an optional
// instant (default now), per docs/PLATFORM-BUILD-PLAN.md E5.2a.
func (s *Server) getOnCallNow(w http.ResponseWriter, r *http.Request) {
	if s.onCallSchedules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "on-call schedule administration is not configured")
		return
	}
	at := time.Now().UTC()
	if v := r.URL.Query().Get("at"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid at", "at must be an RFC3339 timestamp")
			return
		}
		at = parsed.UTC()
	}

	res, err := s.onCallSchedules.OnCall(r.Context(), chi.URLParam(r, "id"), at)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such on-call schedule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOnCallNowDTO(res))
}

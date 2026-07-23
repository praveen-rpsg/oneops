package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/events"
)

// SetWebhookConsume wires the event consumption/replay administration APIs
// (replay jobs, delivery inspection, recovery, dead-letters). It reuses the
// delivery store and replay-job store; it adds no delivery logic.
func (s *Server) SetWebhookConsume(ops events.DeliveryOps, jobs events.ReplayJobStore) {
	s.deliveryOps = ops
	s.replayJobs = jobs
}

type replayRequest struct {
	From        *time.Time `json:"from,omitempty"`
	To          *time.Time `json:"to,omitempty"`
	DeliveryIDs []string   `json:"delivery_ids,omitempty"`
}

type replayJobDTO struct {
	ID             string    `json:"id"`
	WebhookID      string    `json:"webhook_id"`
	From           time.Time `json:"from,omitempty"`
	To             time.Time `json:"to,omitempty"`
	DeliveryIDs    []string  `json:"delivery_ids,omitempty"`
	Status         string    `json:"status"`
	EventsReplayed int       `json:"events_replayed"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toReplayJobDTO(j events.ReplayJob) replayJobDTO {
	return replayJobDTO{
		ID: j.ID, WebhookID: j.WebhookID, From: j.From, To: j.To, DeliveryIDs: j.DeliveryIDs,
		Status: string(j.Status), EventsReplayed: j.EventsReplayed, Error: j.Error,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

type deliveryDetailDTO struct {
	ID             string            `json:"id"`
	WebhookID      string            `json:"webhook_id"`
	Operation      string            `json:"operation"`
	CfgID          string            `json:"cfg_id"`
	Seq            int64             `json:"seq"`
	Status         string            `json:"status"`
	Attempts       int               `json:"attempts"`
	LastStatusCode int               `json:"last_status_code"`
	LastAttempt    time.Time         `json:"last_attempt,omitempty"`
	NextAttemptAt  time.Time         `json:"next_attempt_at,omitempty"`
	Headers        map[string]string `json:"headers"`
	Payload        json.RawMessage   `json:"payload"`
}

func (s *Server) consumeReady(w http.ResponseWriter, r *http.Request) bool {
	if s.webhooks == nil || s.deliveryOps == nil || s.replayJobs == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "event consumption unavailable")
		return false
	}
	return true
}

// replayWebhook serves POST /v1/admin/webhooks/{id}/replay. It creates a replay
// job (executed asynchronously by the replay worker); it never regenerates events.
func (s *Server) replayWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.consumeReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.webhooks.Get(r.Context(), id); err != nil {
		s.mapError(w, r, err)
		return
	}
	var req replayRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
			return
		}
	}
	job := events.ReplayJob{
		ID: events.NewReplayJobID(), WebhookID: id, DeliveryIDs: req.DeliveryIDs, Status: events.JobPending,
	}
	if req.From != nil {
		job.From = *req.From
	}
	if req.To != nil {
		job.To = *req.To
	}
	if err := s.replayJobs.CreateJob(r.Context(), job); err != nil {
		s.mapError(w, r, err)
		return
	}
	s.log.Info("replay job created", "job_id", job.ID, "webhook_id", id, "request_id", RequestIDFrom(r.Context()))
	writeJSON(w, http.StatusAccepted, toReplayJobDTO(job))
}

// getDelivery serves GET /v1/admin/webhooks/{id}/deliveries/{deliveryID}.
func (s *Server) getDelivery(w http.ResponseWriter, r *http.Request) {
	if !s.consumeReady(w, r) {
		return
	}
	id, deliveryID := chi.URLParam(r, "id"), chi.URLParam(r, "deliveryID")
	wh, err := s.webhooks.Get(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	d, ok, err := s.deliveryOps.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if !ok || d.WebhookID != id {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such delivery for this webhook")
		return
	}
	payload, headers, err := events.DeliveryView(d, wh.Secret, time.Now().UTC())
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "could not render delivery")
		return
	}
	writeJSON(w, http.StatusOK, deliveryDetailDTO{
		ID: d.ID, WebhookID: d.WebhookID, Operation: d.Event.Operation, CfgID: d.Event.CfgID,
		Seq: d.Event.Seq, Status: string(d.Status), Attempts: d.RetryCount, LastStatusCode: d.LastStatusCode,
		LastAttempt: d.LastAttempt, NextAttemptAt: d.NextAttemptAt, Headers: headers, Payload: payload,
	})
}

// retryDelivery serves POST /v1/admin/webhooks/{id}/deliveries/{deliveryID}/retry.
func (s *Server) retryDelivery(w http.ResponseWriter, r *http.Request) {
	if !s.consumeReady(w, r) {
		return
	}
	id, deliveryID := chi.URLParam(r, "id"), chi.URLParam(r, "deliveryID")
	d, ok, err := s.deliveryOps.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if !ok || d.WebhookID != id {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such delivery for this webhook")
		return
	}
	n, err := s.deliveryOps.Requeue(r.Context(), []string{deliveryID})
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requeued": n})
}

// listDeadLetters serves GET /v1/admin/webhooks/deadletters.
func (s *Server) listDeadLetters(w http.ResponseWriter, r *http.Request) {
	if !s.consumeReady(w, r) {
		return
	}
	ds, err := s.deliveryOps.ListDeadLetters(r.Context(), r.URL.Query().Get("webhook_id"), s.pageLimit(r))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]deliveryDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, deliveryDTO{
			ID: d.ID, WebhookID: d.WebhookID, Operation: d.Event.Operation, CfgID: d.Event.CfgID,
			Seq: d.Event.Seq, Status: string(d.Status), RetryCount: d.RetryCount,
			LastStatusCode: d.LastStatusCode, LastAttempt: d.LastAttempt, NextAttemptAt: d.NextAttemptAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// retryDeadLetters serves POST /v1/admin/webhooks/deadletters/retry.
func (s *Server) retryDeadLetters(w http.ResponseWriter, r *http.Request) {
	if !s.consumeReady(w, r) {
		return
	}
	var req struct {
		WebhookID string `json:"webhook_id,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	n, err := s.deliveryOps.RequeueDeadLetters(r.Context(), req.WebhookID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	s.log.Info("dead-letters requeued", "count", n, "webhook_id", req.WebhookID, "request_id", RequestIDFrom(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{"requeued": n})
}

// listReplayJobs serves GET /v1/admin/webhooks/replay/jobs.
func (s *Server) listReplayJobs(w http.ResponseWriter, r *http.Request) {
	if !s.consumeReady(w, r) {
		return
	}
	jobs, err := s.replayJobs.ListJobs(r.Context(), s.pageLimit(r))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]replayJobDTO, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toReplayJobDTO(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// getReplayJob serves GET /v1/admin/webhooks/replay/jobs/{jobID}.
func (s *Server) getReplayJob(w http.ResponseWriter, r *http.Request) {
	if !s.consumeReady(w, r) {
		return
	}
	j, ok, err := s.replayJobs.GetJob(r.Context(), chi.URLParam(r, "jobID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such replay job")
		return
	}
	writeJSON(w, http.StatusOK, toReplayJobDTO(j))
}

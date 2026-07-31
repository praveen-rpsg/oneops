package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

// auditReadPort is the read-only audit query surface the query APIs depend on.
// *postgres.AuditStore satisfies it; declaring it here keeps the transport layer
// decoupled and testable, and adds no audit logic (verification stays in the
// frozen Verifier, reached via audit.ChainVerifier).
type auditReadPort interface {
	HeadOf(ctx context.Context, chainID string) (seq int64, hash []byte, found bool, err error)
	ListEvents(ctx context.Context, chainID string, cursor int64, desc bool, limit int, operation string) ([]domain.AuditEvent, error)
}

// SchedulerView is the decoupled scheduler snapshot the verification endpoint
// surfaces; the wiring adapts ops.SchedulerStatus into it (no ops import here).
type SchedulerView struct {
	Enabled     bool
	HasRun      bool
	Running     bool
	LastRunAt   time.Time
	LastHealthy bool
	Stalled     bool
	ChainsTotal int
	ChainsOK    int
	Failures    int
	Errors      int
}

// SetGovernanceQuery wires the read-only governance/audit query dependencies. It
// is additive and reuses existing components: the audit store's read methods, the
// frozen chain verifier, and the operational scheduler status. sched may be nil.
func (s *Server) SetGovernanceQuery(read auditReadPort, verifier audit.ChainVerifier, sched func() SchedulerView) {
	s.auditRead = read
	s.auditVerifier = verifier
	s.schedStatus = sched
}

// ---- response models --------------------------------------------------------

type governanceStateResponse struct {
	CfgID           string    `json:"cfg_id"`
	Lifecycle       string    `json:"lifecycle"`
	RetentionClass  string    `json:"retention_class"`
	RetentionPolicy string    `json:"retention_policy,omitempty"`
	Authority       string    `json:"authority"`
	RowVersion      int64     `json:"row_version"`
	RatifiedBy      string    `json:"ratified_by,omitempty"`
	ReviewCycle     string    `json:"review_cycle,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type governanceHistoryItem struct {
	Seq            int64               `json:"seq"`
	Operation      string              `json:"operation"`
	Actor          string              `json:"actor"`
	OccurredAt     time.Time           `json:"occurred_at"`
	OperationID    string              `json:"operation_id"`
	Removed        bool                `json:"removed"`
	ResultingState *governanceStateDTO `json:"resulting_state,omitempty"`
}

type historyResponse struct {
	Items      []governanceHistoryItem `json:"items"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

type auditChainResponse struct {
	ChainID       string `json:"chain_id"`
	Verified      bool   `json:"verified"`
	Checked       int64  `json:"checked"`
	HeadSeq       int64  `json:"head_seq"`
	HeadHash      string `json:"head_hash,omitempty"`
	FirstBreakSeq *int64 `json:"first_break_seq,omitempty"`
	BreakReason   string `json:"break_reason,omitempty"`
}

type auditEventItem struct {
	Seq         int64     `json:"seq"`
	EventID     string    `json:"event_id"`
	OperationID string    `json:"operation_id"`
	Operation   string    `json:"operation"`
	Actor       string    `json:"actor"`
	OccurredAt  time.Time `json:"occurred_at"`
	PrevHash    string    `json:"prev_hash,omitempty"`
	ThisHash    string    `json:"this_hash"`
}

type auditEventsResponse struct {
	Items      []auditEventItem `json:"items"`
	Order      string           `json:"order"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type verificationSchedulerView struct {
	Enabled     bool      `json:"enabled"`
	HasRun      bool      `json:"has_run"`
	Running     bool      `json:"running"`
	LastRunAt   time.Time `json:"last_run_at,omitempty"`
	LastHealthy bool      `json:"last_healthy"`
	Stalled     bool      `json:"stalled"`
	Failures    int       `json:"failures"`
	Errors      int       `json:"errors"`
}

type verificationResponse struct {
	ChainID       string                     `json:"chain_id"`
	IntegrityOK   bool                       `json:"integrity_ok"`
	Checked       int64                      `json:"checked"`
	FirstBreakSeq *int64                     `json:"first_break_seq,omitempty"`
	BreakReason   string                     `json:"break_reason,omitempty"`
	Scheduler     *verificationSchedulerView `json:"scheduler,omitempty"`
}

// auditPayloadView unmarshals the governance-owned audit payload for presentation
// of the resulting state. It reads the JSON descriptor only; it recomputes nothing.
type auditPayloadView struct {
	Removed      bool   `json:"removed"`
	NewLifecycle string `json:"new_lifecycle"`
	NewRetention string `json:"new_retention"`
	NewAuthority string `json:"new_authority"`
	RowVersion   int64  `json:"row_version"`
}

// ---- helpers ----------------------------------------------------------------

func (s *Server) pageLimit(r *http.Request) int {
	limit := s.cfg.DefaultPageSize
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > s.cfg.MaxPageSize {
		limit = s.cfg.MaxPageSize
	}
	return limit
}

func parseCursor(r *http.Request, def int64) int64 {
	if v := r.URL.Query().Get("cursor"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// requireObject reuses the registry repository to 404 unknown ids consistently.
func (s *Server) requireObject(w http.ResponseWriter, r *http.Request, id string) bool {
	if _, err := s.repo.Get(r.Context(), id); err != nil {
		s.mapError(w, r, err)
		return false
	}
	return true
}

// ---- handlers ---------------------------------------------------------------

// getGovernanceState serves GET /v1/governance/{id}.
func (s *Server) getGovernanceState(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	obj, err := s.repo.Get(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(obj.RowVersion))
	writeJSON(w, http.StatusOK, governanceStateResponse{
		CfgID:           obj.CfgID,
		Lifecycle:       string(obj.Lifecycle),
		RetentionClass:  string(obj.RetentionClass),
		RetentionPolicy: obj.RetentionPolicy,
		Authority:       string(obj.Authority),
		RowVersion:      obj.RowVersion,
		RatifiedBy:      obj.RatifiedBy,
		ReviewCycle:     obj.ReviewCycle,
		CreatedAt:       obj.CreatedAt,
		UpdatedAt:       obj.UpdatedAt,
	})
}

// getGovernanceHistory serves GET /v1/governance/{id}/history (chronological).
func (s *Server) getGovernanceHistory(w http.ResponseWriter, r *http.Request) {
	if s.auditRead == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "governance query unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	if !s.requireObject(w, r, id) {
		return
	}
	operation, ok := s.filterOperation(w, r)
	if !ok {
		return
	}
	limit := s.pageLimit(r)
	cursor := parseCursor(r, 0)

	events, err := s.auditRead.ListEvents(r.Context(), domain.AuditChainID(id), cursor, false, limit, operation)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	resp := historyResponse{Items: make([]governanceHistoryItem, 0, len(events))}
	for i := range events {
		e := events[i]
		item := governanceHistoryItem{
			Seq:         e.Seq,
			Operation:   string(e.Operation),
			Actor:       e.Actor,
			OccurredAt:  e.OccurredAt,
			OperationID: e.OperationID,
		}
		if pv, ok := decodePayload(e.PayloadCanonical); ok {
			item.Removed = pv.Removed
			if !pv.Removed {
				item.ResultingState = &governanceStateDTO{
					Lifecycle:      pv.NewLifecycle,
					RetentionClass: pv.NewRetention,
					Authority:      pv.NewAuthority,
				}
			}
		}
		resp.Items = append(resp.Items, item)
	}
	if len(events) == limit && len(events) > 0 {
		resp.NextCursor = strconv.FormatInt(events[len(events)-1].Seq, 10)
	}
	writeJSON(w, http.StatusOK, resp)
}

// getAuditChain serves GET /v1/governance/{id}/audit.
func (s *Server) getAuditChain(w http.ResponseWriter, r *http.Request) {
	if s.auditRead == nil || s.auditVerifier == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "governance query unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	if !s.requireObject(w, r, id) {
		return
	}
	chainID := domain.AuditChainID(id)

	seq, hash, found, err := s.auditRead.HeadOf(r.Context(), chainID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	res, err := s.auditVerifier.VerifyChain(r.Context(), chainID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	resp := auditChainResponse{
		ChainID:       chainID,
		Verified:      res.OK,
		Checked:       res.Checked,
		HeadSeq:       seq,
		FirstBreakSeq: res.FirstBreakSeq,
		BreakReason:   res.BreakReason,
	}
	if found && len(hash) > 0 {
		resp.HeadHash = hex.EncodeToString(hash)
	}
	writeJSON(w, http.StatusOK, resp)
}

// getAuditEvents serves GET /v1/governance/{id}/audit/events.
func (s *Server) getAuditEvents(w http.ResponseWriter, r *http.Request) {
	if s.auditRead == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "governance query unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	if !s.requireObject(w, r, id) {
		return
	}
	operation, ok := s.filterOperation(w, r)
	if !ok {
		return
	}
	desc := r.URL.Query().Get("order") == "desc"
	limit := s.pageLimit(r)
	cursor := parseCursor(r, 0)
	if desc && r.URL.Query().Get("cursor") == "" {
		cursor = math.MaxInt64
	}

	events, err := s.auditRead.ListEvents(r.Context(), domain.AuditChainID(id), cursor, desc, limit, operation)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	order := "asc"
	if desc {
		order = "desc"
	}
	resp := auditEventsResponse{Items: make([]auditEventItem, 0, len(events)), Order: order}
	for i := range events {
		e := events[i]
		resp.Items = append(resp.Items, auditEventItem{
			Seq:         e.Seq,
			EventID:     e.EventID,
			OperationID: e.OperationID,
			Operation:   string(e.Operation),
			Actor:       e.Actor,
			OccurredAt:  e.OccurredAt,
			PrevHash:    hex.EncodeToString(e.PrevHash),
			ThisHash:    hex.EncodeToString(e.ThisHash),
		})
	}
	if len(events) == limit && len(events) > 0 {
		resp.NextCursor = strconv.FormatInt(events[len(events)-1].Seq, 10)
	}
	writeJSON(w, http.StatusOK, resp)
}

// getVerification serves GET /v1/governance/{id}/verification.
func (s *Server) getVerification(w http.ResponseWriter, r *http.Request) {
	if s.auditVerifier == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "governance query unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	if !s.requireObject(w, r, id) {
		return
	}
	chainID := domain.AuditChainID(id)

	res, err := s.auditVerifier.VerifyChain(r.Context(), chainID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	resp := verificationResponse{
		ChainID:       chainID,
		IntegrityOK:   res.OK,
		Checked:       res.Checked,
		FirstBreakSeq: res.FirstBreakSeq,
		BreakReason:   res.BreakReason,
	}
	if s.schedStatus != nil {
		v := s.schedStatus()
		resp.Scheduler = &verificationSchedulerView{
			Enabled:     v.Enabled,
			HasRun:      v.HasRun,
			Running:     v.Running,
			LastRunAt:   v.LastRunAt,
			LastHealthy: v.LastHealthy,
			Stalled:     v.Stalled,
			Failures:    v.Failures,
			Errors:      v.Errors,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// approverDTO is one recorded distinct approval (ADR-GOV-005).
type approverDTO struct {
	ApproverUserID string    `json:"approver_user_id"`
	ApprovedAt     time.Time `json:"approved_at"`
}

type governanceApprovalsResponse struct {
	CfgID     string        `json:"cfg_id"`
	Approvers []approverDTO `json:"approvers"`
	Count     int           `json:"count"`
	Required  int           `json:"required"`
	Met       bool          `json:"met"`
}

// getGovernanceApprovals serves GET /v1/governance/{id}/approvals: who has
// recorded a distinct approval so far, against the tenant's configured
// multi-approver quorum (ADR-GOV-005). It is read-only — recording an
// approval is a constitutional mutation and goes through POST .../approve.
func (s *Server) getGovernanceApprovals(w http.ResponseWriter, r *http.Request) {
	if s.approvals == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "approval quorum tracking is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if !s.requireObject(w, r, id) {
		return
	}
	approvers, err := s.approvals.ListApprovers(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	required, err := s.requiredApprovals(r.Context())
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	resp := governanceApprovalsResponse{
		CfgID:     id,
		Approvers: make([]approverDTO, 0, len(approvers)),
		Count:     len(approvers),
		Required:  required,
		Met:       len(approvers) >= required,
	}
	for _, a := range approvers {
		resp.Approvers = append(resp.Approvers, approverDTO{ApproverUserID: a.ApproverUserID, ApprovedAt: a.CreatedAt})
	}
	writeJSON(w, http.StatusOK, resp)
}

// filterOperation reads an optional operation filter and validates it against the
// known §8 operations, returning 400 for an unknown value.
func (s *Server) filterOperation(w http.ResponseWriter, r *http.Request) (string, bool) {
	op := r.URL.Query().Get("operation")
	if op != "" && !domain.ConfigurationOperation(op).Valid() {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "unknown operation filter: "+op)
		return "", false
	}
	return op, true
}

func decodePayload(b []byte) (auditPayloadView, bool) {
	var pv auditPayloadView
	if len(b) == 0 {
		return pv, false
	}
	if err := json.Unmarshal(b, &pv); err != nil {
		return pv, false
	}
	return pv, true
}

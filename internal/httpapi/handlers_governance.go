package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
)

// governanceExecutor is the transport's view of the Governance Engine: it runs
// exactly one constitutional operation atomically and returns the result. It is
// interface-only so the HTTP layer stays decoupled and testable, and so the
// engine remains the sole owner of all business logic. *governance.Engine
// satisfies it.
type governanceExecutor interface {
	Execute(ctx context.Context, cmd governance.Command) (governance.Result, error)
}

// governanceRequest is the optional JSON body for a constitutional operation.
// Optimistic concurrency uses the If-Match header (like the registry); idempotency
// uses the Idempotency-Key header — neither is duplicated in the body.
type governanceRequest struct {
	// TargetRetention is required only for archive; the engine enforces the rest.
	TargetRetention string `json:"target_retention,omitempty"`
}

type governanceStateDTO struct {
	Lifecycle      string `json:"lifecycle"`
	RetentionClass string `json:"retention_class"`
	Authority      string `json:"authority"`
}

// governanceAuditDTO is the read-only audit metadata surfaced to clients. It
// exposes no internal implementation detail (no hashes, sequences, or event ids):
// a successful atomic operation always records exactly one audit event
// (ADR-AUDIT-005), so Recorded is true and the client's own OperationID is echoed.
type governanceAuditDTO struct {
	OperationID string `json:"operation_id"`
	ChainID     string `json:"chain_id"`
	Recorded    bool   `json:"recorded"`
}

type governanceResponse struct {
	Operation  string              `json:"operation"`
	CfgID      string              `json:"cfg_id"`
	Actor      string              `json:"actor"`
	OccurredAt time.Time           `json:"occurred_at"`
	Removed    bool                `json:"removed"`
	RowVersion int64               `json:"row_version"`
	State      *governanceStateDTO `json:"state,omitempty"`
	Audit      governanceAuditDTO  `json:"audit"`
}

func toGovernanceResponse(res governance.Result, operationID string) governanceResponse {
	resp := governanceResponse{
		Operation:  string(res.Operation),
		CfgID:      res.CfgID,
		Actor:      res.Actor,
		OccurredAt: res.OccurredAt,
		Removed:    res.Removed,
		RowVersion: res.RowVersion,
		Audit: governanceAuditDTO{
			OperationID: operationID,
			ChainID:     domain.AuditChainID(res.CfgID),
			Recorded:    true,
		},
	}
	if !res.Removed {
		resp.State = &governanceStateDTO{
			Lifecycle:      string(res.NewLifecycle),
			RetentionClass: string(res.NewRetention),
			Authority:      string(res.NewAuthority),
		}
	}
	return resp
}

// mapGovernanceError maps governance-specific errors to HTTP, then delegates to
// the shared registry mapper for domain errors (404 / 412 / 422 / 500),
// preserving existing error semantics.
func mapGovernanceError(w http.ResponseWriter, r *http.Request, err error) {
	var te *governance.TransitionError
	switch {
	case errors.As(err, &te):
		// Operation not permitted from the object's current state.
		writeProblem(w, r, http.StatusConflict, "conflict", te.Error())
	case errors.Is(err, governance.ErrHasDependents):
		writeProblem(w, r, http.StatusConflict, "conflict", "object has dependents")
	case errors.Is(err, governance.ErrUnsupportedOperation):
		writeProblem(w, r, http.StatusUnprocessableEntity, "unsupported operation", err.Error())
	default:
		mapError(w, r, err)
	}
}

// Constitutional operation handlers. Each maps 1:1 to a §8 operation; all logic,
// transition rules, authorization hooks, and audit emission live in the engine.
func (s *Server) ratifyGovernance(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpRatification)
}
func (s *Server) approveGovernance(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpApproval)
}
func (s *Server) suspendGovernance(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpSuspension)
}
func (s *Server) deprecateGovernance(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpDeprecation)
}
func (s *Server) withdrawGovernance(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpWithdrawal)
}
func (s *Server) archiveGovernance(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpArchiving)
}
func (s *Server) deleteGovernance(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpDeletion)
}

// execGovernance performs transport concerns only — identity, idempotency,
// optimistic-concurrency, and body parsing — then delegates the constitutional
// decision entirely to the engine.
func (s *Server) execGovernance(w http.ResponseWriter, r *http.Request, op domain.ConfigurationOperation) {
	if s.governance == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "governance engine unavailable")
		return
	}
	claims, ok := ClaimsFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "no identity")
		return
	}
	operationID := r.Header.Get("Idempotency-Key")
	if operationID == "" {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "Idempotency-Key header is required")
		return
	}

	cmd := governance.Command{
		Operation:   op,
		CfgID:       chi.URLParam(r, "id"),
		Actor:       claims.Subject,
		OperationID: operationID,
	}

	// Optional optimistic concurrency, using the same If-Match/ETag convention as
	// the registry. Absent header => no version check (engine treats 0 as skip).
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
		expected, err := parseETag(ifMatch)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid If-Match value")
			return
		}
		cmd.ExpectedRowVersion = expected
	}

	// Parse the optional body (only archive needs target_retention).
	if r.Body != nil {
		var req governanceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
			return
		}
		if req.TargetRetention != "" {
			cmd.TargetRetention = domain.RetentionClass(req.TargetRetention)
		}
	}
	if op == domain.OpArchiving && cmd.TargetRetention == "" {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "target_retention is required for archive")
		return
	}

	res, err := s.governance.Execute(r.Context(), cmd)
	if err != nil {
		s.log.Warn("governance operation failed",
			"operation", op, "cfg_id", cmd.CfgID,
			"request_id", RequestIDFrom(r.Context()), "err", err.Error())
		mapGovernanceError(w, r, err)
		return
	}

	if !res.Removed {
		w.Header().Set("ETag", etag(res.RowVersion))
	}
	s.log.Info("governance operation",
		"operation", res.Operation, "cfg_id", res.CfgID, "actor", res.Actor,
		"row_version", res.RowVersion, "removed", res.Removed,
		"request_id", RequestIDFrom(r.Context()))
	writeJSON(w, http.StatusOK, toGovernanceResponse(res, operationID))
}

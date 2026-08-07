package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// SetVulnFindings wires the vulnerability-finding repository (E8.3a). Until
// it is called the endpoints report 501 rather than 404, the same convention
// SetSecurityRules/SetIOCs establish.
func (s *Server) SetVulnFindings(repo domain.VulnFindingRepository) { s.vulnFindings = repo }

type vulnFindingDTO struct {
	FindingID   string    `json:"finding_id"`
	AssetID     string    `json:"asset_id"`
	VulnID      string    `json:"vuln_id"`
	Title       string    `json:"title"`
	Severity    string    `json:"severity"`
	Status      string    `json:"status"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	Scanner     string    `json:"scanner"`
	Description string    `json:"description"`
	// RemediationIncidentID projects VulnFinding.RemediationIncidentID
	// (E8.3b) — read-only: there is no PATCH field for it. Set only by POST
	// .../remediate. omitempty keeps a not-yet-remediated finding's JSON
	// identical to what it was before E8.3b.
	RemediationIncidentID *string   `json:"remediation_incident_id,omitempty"`
	RowVersion            int64     `json:"row_version"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// toVulnFindingDTO deliberately omits tenant_id, the same choice
// toSecurityRuleDTO/toIOCDTO make: the caller is already inside exactly one
// tenant.
func toVulnFindingDTO(f *domain.VulnFinding) vulnFindingDTO {
	return vulnFindingDTO{
		FindingID: f.FindingID, AssetID: f.AssetID, VulnID: f.VulnID, Title: f.Title,
		Severity: string(f.Severity), Status: string(f.Status),
		FirstSeen: f.FirstSeen, LastSeen: f.LastSeen,
		Scanner: f.Scanner, Description: f.Description,
		RemediationIncidentID: f.RemediationIncidentID,
		RowVersion:            f.RowVersion, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

// ingestVulnFindingRequest is one finding of a scan-ingest batch. AssetID
// names an EXISTING Configuration Item — the store re-verifies it against
// the caller's own tenant before any row naming it is written (ADR-ASSET-001
// §6, mirroring ingestSecurityObservationRequest). first_seen/last_seen/
// status are absent: they are store-managed — see domain.VulnFinding's doc
// comment and domain.VulnFindingRepository.Upsert's exact UPSERT rule.
type ingestVulnFindingRequest struct {
	AssetID     string `json:"asset_id"`
	VulnID      string `json:"vuln_id"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Scanner     string `json:"scanner"`
	Description string `json:"description"`
}

type ingestVulnFindingsRequest struct {
	Findings []ingestVulnFindingRequest `json:"findings"`
}

// vulnFindingUpsertResultDTO is one input finding's outcome, always one per
// input finding and at that finding's input index — mirrors
// securityObservationWriteResultDTO's shape.
type vulnFindingUpsertResultDTO struct {
	Index     int    `json:"index"`
	Outcome   string `json:"outcome"`
	FindingID string `json:"finding_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type ingestVulnFindingsResponse struct {
	Results []vulnFindingUpsertResultDTO `json:"results"`
}

// ingestVulnFindings bulk-UPSERTs vulnerability findings from one scan
// (E8.3a). Each finding is validated and asset-verified independently and
// reported at its own index — one bad finding does not abort the batch
// around it, the same shape ingestSecurityObservations already established.
// Bounded to domain.MaxVulnFindingIngestBatch findings per call. See
// domain.VulnFindingRepository.Upsert's doc comment for the exact dedup/
// reappearance rule this endpoint's writes follow.
func (s *Server) ingestVulnFindings(w http.ResponseWriter, r *http.Request) {
	if s.vulnFindings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "vuln finding ingestion is not configured")
		return
	}
	var req ingestVulnFindingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}
	if len(req.Findings) == 0 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed", "findings must not be empty")
		return
	}
	if len(req.Findings) > domain.MaxVulnFindingIngestBatch {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed",
			fmt.Sprintf("findings must not exceed %d per ingest call", domain.MaxVulnFindingIngestBatch))
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	results := make([]vulnFindingUpsertResultDTO, len(req.Findings))
	var pending []domain.VulnFinding
	var pendingIdx []int
	for i, row := range req.Findings {
		f, err := domain.NewVulnFinding(tenant.TenantID, row.AssetID, row.VulnID, row.Title,
			domain.VulnFindingSeverity(row.Severity), row.Scanner, row.Description)
		if err != nil {
			results[i] = vulnFindingUpsertResultDTO{Index: i, Outcome: "rejected", Reason: err.Error()}
			continue
		}
		pending = append(pending, *f)
		pendingIdx = append(pendingIdx, i)
	}
	if len(pending) == 0 {
		writeJSON(w, http.StatusOK, ingestVulnFindingsResponse{Results: results})
		return
	}

	written, err := s.vulnFindings.Upsert(r.Context(), pending)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	for j, i := range pendingIdx {
		if written[j].Accepted {
			results[i] = vulnFindingUpsertResultDTO{
				Index: i, Outcome: "accepted", FindingID: written[j].FindingID, Status: string(written[j].Status),
			}
		} else {
			results[i] = vulnFindingUpsertResultDTO{Index: i, Outcome: "rejected", Reason: written[j].Reason}
		}
	}
	writeJSON(w, http.StatusOK, ingestVulnFindingsResponse{Results: results})
}

// listVulnFindings returns a page of the caller's findings, optionally
// filtered by asset_id/status/severity.
func (s *Server) listVulnFindings(w http.ResponseWriter, r *http.Request) {
	if s.vulnFindings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "vuln finding administration is not configured")
		return
	}
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}
	items, err := s.vulnFindings.List(r.Context(), limit, q.Get("after"), q.Get("asset_id"),
		domain.VulnFindingStatus(q.Get("status")), domain.VulnFindingSeverity(q.Get("severity")))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]vulnFindingDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toVulnFindingDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// getVulnFinding returns one finding by identifier.
func (s *Server) getVulnFinding(w http.ResponseWriter, r *http.Request) {
	if s.vulnFindings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "vuln finding administration is not configured")
		return
	}
	f, err := s.vulnFindings.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such vuln finding")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toVulnFindingDTO(f))
}

// patchVulnFindingRequest performs a lifecycle transition under optimistic
// locking — the ONLY mutation this story's PATCH exposes. Severity/title/
// scanner/description are scan-supplied facts refreshed by ingestion, not
// operator-editable fields — see domain.VulnFindingRepository's doc comment
// on why there is no general field-Update method here (unlike
// SecurityRulePatch/IOCPatch).
type patchVulnFindingRequest struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
}

// patchVulnFinding performs the ONLY lifecycle move this API exposes for a
// finding — the single authority for status changes
// (domain.VulnFindingStatus.CanTransitionTo), mirroring transitionIncident's
// error-ordering shape exactly.
func (s *Server) patchVulnFinding(w http.ResponseWriter, r *http.Request) {
	if s.vulnFindings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "vuln finding administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchVulnFindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this vuln finding"))
		return
	}
	status := domain.VulnFindingStatus(strings.TrimSpace(req.Status))
	if !status.Valid() {
		s.mapError(w, r, domain.NewValidationError("status",
			"must be one of: open, remediated, accepted_risk, false_positive"))
		return
	}

	updated, err := s.vulnFindings.SetStatus(r.Context(), id, req.RowVersion, status)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such vuln finding")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "vuln finding was modified concurrently; re-read and retry")
		return
	case errors.Is(err, domain.ErrInvalidTransition):
		// The target state is well-formed; it is the move that is refused —
		// a conflict with the record's current state, mirrors mapError's own
		// ErrInvalidTransition branch (errors.go), applied explicitly here so
		// the 404/409 ordering above stays readable as one switch (mirrors
		// transitionIncident exactly).
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toVulnFindingDTO(updated))
}

// prioritizedVulnFindingDTO is one row of GET
// /v1/admin/vuln-findings/prioritized (E8.3b): the finding, the asset
// criticality it was joined against, and the computed score/label — see
// domain.PrioritizedVulnFinding's own doc comment. Nothing here is stored.
type prioritizedVulnFindingDTO struct {
	Finding          vulnFindingDTO `json:"finding"`
	AssetCriticality string         `json:"asset_criticality"`
	PriorityScore    int            `json:"priority_score"`
	Priority         string         `json:"priority"`
}

func toPrioritizedVulnFindingDTO(p *domain.PrioritizedVulnFinding) prioritizedVulnFindingDTO {
	return prioritizedVulnFindingDTO{
		Finding:          toVulnFindingDTO(&p.Finding),
		AssetCriticality: string(p.Criticality),
		PriorityScore:    p.Score,
		Priority:         string(p.Priority),
	}
}

type vulnFindingsPrioritizedResponse struct {
	Items []prioritizedVulnFindingDTO `json:"items"`
}

// listPrioritizedVulnFindings serves GET /v1/admin/vuln-findings/prioritized
// (E8.3b, ADR-SOC-007): a bounded, read-only projection of the caller's OPEN
// findings ranked by business risk (domain.VulnFindingRepository.
// Prioritized's own doc comment states the exact ranking). Row-level
// security on both vuln_finding and asset is the ONLY isolation mechanism —
// there is no explicit tenant predicate, mirroring GET /admin/noc/overview
// (ADR-NOC-001).
func (s *Server) listPrioritizedVulnFindings(w http.ResponseWriter, r *http.Request) {
	if s.vulnFindings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "vuln finding administration is not configured")
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
	items, err := s.vulnFindings.Prioritized(r.Context(), limit)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]prioritizedVulnFindingDTO, 0, len(items))
	for i := range items {
		out = append(out, toPrioritizedVulnFindingDTO(&items[i]))
	}
	writeJSON(w, http.StatusOK, vulnFindingsPrioritizedResponse{Items: out})
}

// remediateVulnFindingRequest carries the row_version the caller last read
// for the finding — the same optimistic-locking shape patchVulnFindingRequest
// uses, guarding the LINK write exactly as PATCH guards a status transition.
type remediateVulnFindingRequest struct {
	RowVersion int64 `json:"row_version"`
}

// remediateVulnFindingResponse returns both halves of the link: the Incident
// opened (or reused) and the finding's own updated view of the link.
type remediateVulnFindingResponse struct {
	VulnFinding vulnFindingDTO `json:"vuln_finding"`
	Incident    incidentDTO    `json:"incident"`
}

// remediateVulnFinding serves POST /v1/admin/vuln-findings/{id}/remediate
// (E8.3b, ADR-SOC-007): opens, or reuses, a tracked remediation Incident for
// one finding — see domain.VulnFindingRepository.Remediate's doc comment for
// the full idempotency/concurrency contract this handler exposes unchanged.
func (s *Server) remediateVulnFinding(w http.ResponseWriter, r *http.Request) {
	if s.vulnFindings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "vuln finding administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req remediateVulnFindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this vuln finding"))
		return
	}

	finding, incident, err := s.vulnFindings.Remediate(r.Context(), id, req.RowVersion)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such vuln finding")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "vuln finding was modified concurrently; re-read and retry")
		return
	case errors.Is(err, domain.ErrVulnFindingNotOpen):
		// The finding exists and the version matches; it is its CURRENT
		// status that refuses remediation — a conflict with the record's
		// state, not a malformed request, mirrors patchVulnFinding's own
		// ErrInvalidTransition branch immediately above.
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, remediateVulnFindingResponse{
		VulnFinding: toVulnFindingDTO(finding),
		Incident:    toIncidentDTO(incident),
	})
}

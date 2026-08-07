package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// SetSecurityObservations wires the security observation repository (E8.1a).
// Until it is called the endpoints report 501 rather than 404, the same
// convention SetTelemetry establishes.
func (s *Server) SetSecurityObservations(repo domain.SecurityObservationRepository) {
	s.securityObservations = repo
}

// ingestSecurityObservationRequest is one observation of a batch ingest call.
// AssetID names an EXISTING Configuration Item (unlike createAssetRequest,
// this is a reference, not a new entity's identity) — the store re-verifies
// it against the caller's own tenant before any row naming it is written
// (ADR-TELEMETRY-001, ADR-SOC-001). ObservedAt is RFC3339; an empty/missing
// Attributes is stored as no attributes.
type ingestSecurityObservationRequest struct {
	AssetID         string            `json:"asset_id"`
	ObservationType string            `json:"observation_type"`
	Source          string            `json:"source"`
	Severity        string            `json:"severity"`
	ObservedAt      string            `json:"observed_at"`
	Attributes      map[string]string `json:"attributes"`
}

type ingestSecurityObservationsRequest struct {
	Observations []ingestSecurityObservationRequest `json:"observations"`
}

// securityObservationWriteResultDTO is one observation's outcome, always one
// per input observation and at that observation's input index — the same
// shape sampleWriteResultDTO uses for telemetry ingest.
type securityObservationWriteResultDTO struct {
	Index   int    `json:"index"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type ingestSecurityObservationsResponse struct {
	Results []securityObservationWriteResultDTO `json:"results"`
}

// ingestSecurityObservations bulk-writes security observations tied to
// Configuration Items (E8.1a). Each observation is validated and
// asset-verified independently and reported at its own index — one bad
// observation does not abort the batch around it, the same shape
// ingestTelemetry already established. Bounded to
// domain.MaxSecurityObservationIngestBatch observations per call.
func (s *Server) ingestSecurityObservations(w http.ResponseWriter, r *http.Request) {
	if s.securityObservations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security observation ingestion is not configured")
		return
	}
	var req ingestSecurityObservationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}
	if len(req.Observations) == 0 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed", "observations must not be empty")
		return
	}
	if len(req.Observations) > domain.MaxSecurityObservationIngestBatch {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed",
			fmt.Sprintf("observations must not exceed %d per ingest call", domain.MaxSecurityObservationIngestBatch))
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	results := make([]securityObservationWriteResultDTO, len(req.Observations))
	var pending []domain.SecurityObservation
	var pendingIdx []int
	for i, row := range req.Observations {
		ts, err := time.Parse(time.RFC3339, row.ObservedAt)
		if err != nil {
			results[i] = securityObservationWriteResultDTO{Index: i, Outcome: "rejected",
				Reason: "observed_at must be an RFC3339 timestamp, e.g. 2026-08-01T00:00:00Z"}
			continue
		}
		o, err := domain.NewSecurityObservation(tenant.TenantID, row.AssetID, row.ObservationType, row.Source,
			domain.ObservationSeverity(row.Severity), ts, row.Attributes)
		if err != nil {
			results[i] = securityObservationWriteResultDTO{Index: i, Outcome: "rejected", Reason: err.Error()}
			continue
		}
		// Only an observation that survives transport-level validation reaches
		// WriteObservations at all — a bad timestamp or a domain.NewSecurityObservation
		// failure is reported here without a round trip to the store.
		pending = append(pending, *o)
		pendingIdx = append(pendingIdx, i)
	}
	if len(pending) == 0 {
		writeJSON(w, http.StatusOK, ingestSecurityObservationsResponse{Results: results})
		return
	}

	written, err := s.securityObservations.WriteObservations(r.Context(), pending)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	for j, i := range pendingIdx {
		if written[j].Accepted {
			results[i] = securityObservationWriteResultDTO{Index: i, Outcome: "accepted"}
		} else {
			results[i] = securityObservationWriteResultDTO{Index: i, Outcome: "rejected", Reason: written[j].Reason}
		}
	}
	writeJSON(w, http.StatusOK, ingestSecurityObservationsResponse{Results: results})
}

// securityObservationDTO is one security observation on the wire.
type securityObservationDTO struct {
	AssetID         string            `json:"asset_id"`
	ObservationType string            `json:"observation_type"`
	Source          string            `json:"source"`
	Severity        string            `json:"severity"`
	ObservedAt      time.Time         `json:"observed_at"`
	Attributes      map[string]string `json:"attributes"`
}

// toSecurityObservationDTO deliberately omits tenant_id, the same choice
// toSampleDTO makes: the caller is already inside exactly one tenant.
func toSecurityObservationDTO(o domain.SecurityObservation) securityObservationDTO {
	return securityObservationDTO{
		AssetID: o.AssetID, ObservationType: o.ObservationType, Source: o.Source,
		Severity: string(o.Severity), ObservedAt: o.ObservedAt, Attributes: o.Attributes,
	}
}

// querySecurityObservations serves GET /admin/security-observations: a
// bounded, keyset-paginated range query over one asset's one observation
// type. asset_id, observation_type, from and to are required; limit and
// after are optional — mirrors queryTelemetry exactly. A caller with more
// observations than one page fits re-queries with after set to the last
// item's observed_at.
func (s *Server) querySecurityObservations(w http.ResponseWriter, r *http.Request) {
	if s.securityObservations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security observation query is not configured")
		return
	}
	q := r.URL.Query()

	assetID := q.Get("asset_id")
	if assetID == "" {
		s.mapError(w, r, domain.NewValidationError("asset_id", "must not be empty"))
		return
	}
	observationType := q.Get("observation_type")
	if observationType == "" {
		s.mapError(w, r, domain.NewValidationError("observation_type", "must not be empty"))
		return
	}
	from, err := domain.ParseTelemetryTimestamp("from", q.Get("from"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	to, err := domain.ParseTelemetryTimestamp("to", q.Get("to"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if to.Before(from) {
		s.mapError(w, r, domain.NewValidationError("to", "must not be before from"))
		return
	}

	var after time.Time
	if raw := q.Get("after"); raw != "" {
		after, err = domain.ParseTelemetryTimestamp("after", raw)
		if err != nil {
			s.mapError(w, r, err)
			return
		}
	}

	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}

	items, err := s.securityObservations.QueryRange(r.Context(), assetID, observationType, from, to, limit, after)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]securityObservationDTO, 0, len(items))
	for _, o := range items {
		out = append(out, toSecurityObservationDTO(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

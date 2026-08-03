package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// SetTelemetry wires the telemetry repository (E2.1). Until it is called the
// endpoints report 501 rather than 404, the same convention SetAssets
// establishes for the CMDB.
func (s *Server) SetTelemetry(repo domain.TelemetryRepository) { s.telemetry = repo }

// ingestSampleRequest is one sample of a batch ingest call. AssetID names an
// EXISTING Configuration Item (unlike createAssetRequest, this is a
// reference, not a new entity's identity) — TelemetryStore re-verifies it
// against the caller's own tenant before any row naming it is written
// (ADR-TELEMETRY-001). Timestamp is RFC3339; an empty/missing Labels is
// stored as no labels.
type ingestSampleRequest struct {
	AssetID   string            `json:"asset_id"`
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp string            `json:"timestamp"`
	Labels    map[string]string `json:"labels"`
}

type ingestTelemetryRequest struct {
	Samples []ingestSampleRequest `json:"samples"`
}

// sampleWriteResultDTO is one sample's outcome, always one per input sample
// and at that sample's input index, the same shape assetImportResultDTO uses
// for bulk asset import — a caller correlates a failure back to the request
// it sent without re-transmitting the payload.
type sampleWriteResultDTO struct {
	Index   int    `json:"index"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type ingestTelemetryResponse struct {
	Results []sampleWriteResultDTO `json:"results"`
}

// ingestTelemetry bulk-writes metric samples tied to Configuration Items
// (E2.1). Each sample is validated and asset-verified independently and
// reported at its own index — one bad sample does not abort the batch
// around it, the same shape importAssets already established. Bounded to
// domain.MaxTelemetryIngestBatch samples per call.
func (s *Server) ingestTelemetry(w http.ResponseWriter, r *http.Request) {
	if s.telemetry == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "telemetry ingestion is not configured")
		return
	}
	var req ingestTelemetryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}
	if len(req.Samples) == 0 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed", "samples must not be empty")
		return
	}
	if len(req.Samples) > domain.MaxTelemetryIngestBatch {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed",
			fmt.Sprintf("samples must not exceed %d per ingest call", domain.MaxTelemetryIngestBatch))
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	results := make([]sampleWriteResultDTO, len(req.Samples))
	var pending []domain.Sample
	var pendingIdx []int
	for i, row := range req.Samples {
		ts, err := time.Parse(time.RFC3339, row.Timestamp)
		if err != nil {
			results[i] = sampleWriteResultDTO{Index: i, Outcome: "rejected",
				Reason: "timestamp must be an RFC3339 timestamp, e.g. 2026-08-01T00:00:00Z"}
			continue
		}
		sm, err := domain.NewSample(tenant.TenantID, row.AssetID, row.Metric, row.Value, ts, row.Labels)
		if err != nil {
			results[i] = sampleWriteResultDTO{Index: i, Outcome: "rejected", Reason: err.Error()}
			continue
		}
		// Only a sample that survives transport-level validation reaches
		// WriteSamples at all — a bad timestamp or a domain.NewSample failure
		// is reported here without a round trip to the store.
		pending = append(pending, *sm)
		pendingIdx = append(pendingIdx, i)
	}
	if len(pending) == 0 {
		writeJSON(w, http.StatusOK, ingestTelemetryResponse{Results: results})
		return
	}

	written, err := s.telemetry.WriteSamples(r.Context(), pending)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	for j, i := range pendingIdx {
		if written[j].Accepted {
			results[i] = sampleWriteResultDTO{Index: i, Outcome: "accepted"}
		} else {
			results[i] = sampleWriteResultDTO{Index: i, Outcome: "rejected", Reason: written[j].Reason}
		}
	}
	writeJSON(w, http.StatusOK, ingestTelemetryResponse{Results: results})
}

// sampleDTO is one telemetry observation on the wire.
type sampleDTO struct {
	AssetID   string            `json:"asset_id"`
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp time.Time         `json:"timestamp"`
	Labels    map[string]string `json:"labels"`
}

// toSampleDTO deliberately omits tenant_id, the same choice toAssetDTO makes:
// the caller is already inside exactly one tenant.
func toSampleDTO(sm domain.Sample) sampleDTO {
	return sampleDTO{AssetID: sm.AssetID, Metric: sm.Metric, Value: sm.Value, Timestamp: sm.Timestamp, Labels: sm.Labels}
}

// queryTelemetry serves GET /admin/telemetry: a bounded, keyset-paginated
// range query over one asset's one metric. asset_id, metric, from and to are
// required; limit and after are optional. A caller with more samples than
// one page fits re-queries with after set to the last item's timestamp.
func (s *Server) queryTelemetry(w http.ResponseWriter, r *http.Request) {
	if s.telemetry == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "telemetry query is not configured")
		return
	}
	q := r.URL.Query()

	assetID := q.Get("asset_id")
	if assetID == "" {
		s.mapError(w, r, domain.NewValidationError("asset_id", "must not be empty"))
		return
	}
	metric := q.Get("metric")
	if metric == "" {
		s.mapError(w, r, domain.NewValidationError("metric", "must not be empty"))
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

	items, err := s.telemetry.QueryRange(r.Context(), assetID, metric, from, to, limit, after)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]sampleDTO, 0, len(items))
	for _, sm := range items {
		out = append(out, toSampleDTO(sm))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

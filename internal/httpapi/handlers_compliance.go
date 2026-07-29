package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/compliance"
)

// complianceService is the read-only compliance & evidence engine.
// *compliance.Service satisfies it.
type complianceService interface {
	Summary(ctx context.Context, govID string) (compliance.Summary, error)
	Evidence(ctx context.Context, govID string) (compliance.Evidence, error)
	Checks(ctx context.Context, govID string) ([]compliance.Check, error)
	Reports(ctx context.Context, cursor string, limit int) (compliance.ReportPage, error)
}

// SetCompliance wires the read-only compliance endpoints. exportInc counts
// evidence exports (reusing the compliance metrics; may be nil).
func (s *Server) SetCompliance(svc complianceService, exportInc func()) {
	s.compliance = svc
	s.complianceExport = exportInc
}

func (s *Server) complianceReady(w http.ResponseWriter, r *http.Request) bool {
	if s.compliance == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "compliance unavailable")
		return false
	}
	return true
}

// getComplianceSummary serves GET /v1/admin/compliance/{governanceID}.
func (s *Server) getComplianceSummary(w http.ResponseWriter, r *http.Request) {
	if !s.complianceReady(w, r) {
		return
	}
	sum, err := s.compliance.Summary(r.Context(), chi.URLParam(r, "governanceID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// getComplianceChecks serves GET /v1/admin/compliance/{governanceID}/checks.
func (s *Server) getComplianceChecks(w http.ResponseWriter, r *http.Request) {
	if !s.complianceReady(w, r) {
		return
	}
	checks, err := s.compliance.Checks(r.Context(), chi.URLParam(r, "governanceID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": checks})
}

// getComplianceEvidence serves GET /v1/admin/compliance/{governanceID}/evidence.
// It returns JSON by default, or a reproducible ZIP bundle for ?format=zip.
func (s *Server) getComplianceEvidence(w http.ResponseWriter, r *http.Request) {
	if !s.complianceReady(w, r) {
		return
	}
	id := chi.URLParam(r, "governanceID")
	ev, err := s.compliance.Evidence(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "zip" {
		payload, zerr := compliance.BuildZIP(ev)
		if zerr != nil {
			writeProblem(w, r, http.StatusInternalServerError, "internal error", "could not build evidence archive")
			return
		}
		if s.complianceExport != nil {
			s.complianceExport()
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="evidence-`+id+`.zip"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
		return
	}
	if s.complianceExport != nil {
		s.complianceExport()
	}
	writeJSON(w, http.StatusOK, ev)
}

// getComplianceReports serves GET /v1/admin/compliance/reports.
func (s *Server) getComplianceReports(w http.ResponseWriter, r *http.Request) {
	if !s.complianceReady(w, r) {
		return
	}
	page, err := s.compliance.Reports(r.Context(), r.URL.Query().Get("cursor"), s.pageLimit(r))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

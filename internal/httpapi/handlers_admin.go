package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/rpsg/oneops/internal/diag"
	"github.com/rpsg/oneops/pkg/version"
)

// AdminIntegrityRun is the result of an on-demand verification sweep. It mirrors
// the scheduler's IntegrityReport without importing ops; the wiring adapts it.
type AdminIntegrityRun struct {
	StartedAt   time.Time `json:"started_at"`
	DurationMS  int64     `json:"duration_ms"`
	ChainsTotal int       `json:"chains_total"`
	ChainsOK    int       `json:"chains_ok"`
	Failures    int       `json:"failures"`
	Errors      int       `json:"errors"`
	Healthy     bool      `json:"healthy"`
}

// SetAdmin wires the administration APIs. snapshot reuses the diagnostics builder
// (already redacted); runIntegrity reuses the existing verification scheduler.
// Both may be nil (endpoints then report unavailable). No new operational logic.
func (s *Server) SetAdmin(snapshot func(context.Context) diag.Snapshot, runIntegrity func(context.Context) AdminIntegrityRun) {
	s.adminSnapshot = snapshot
	s.adminRunIntegrity = runIntegrity
}

type adminStatusResponse struct {
	Service          string                  `json:"service"`
	Version          string                  `json:"version"`
	Commit           string                  `json:"commit"`
	BuildTime        string                  `json:"build_time"`
	Env              string                  `json:"env"`
	UptimeSeconds    float64                 `json:"uptime_seconds"`
	MigrationVersion string                  `json:"migration_version"`
	Scheduler        diag.SchedulerStatus    `json:"scheduler"`
	VerifierHealthy  bool                    `json:"verifier_healthy"`
	Dependencies     []diag.DependencyStatus `json:"dependencies"`
	Healthy          bool                    `json:"healthy"`
}

type adminIntegrityResponse struct {
	Enabled     bool      `json:"enabled"`
	Running     bool      `json:"running"`
	HasRun      bool      `json:"has_run"`
	LastRunAt   time.Time `json:"last_run_at,omitempty"`
	LastHealthy bool      `json:"last_healthy"`
	Stalled     bool      `json:"stalled"`
	ChainsTotal int       `json:"chains_total"`
	ChainsOK    int       `json:"chains_ok"`
	Failures    int       `json:"failures"`
	Errors      int       `json:"errors"`
}

type adminConfigResponse struct {
	Modules map[string]bool    `json:"modules"`
	Config  diag.ConfigSummary `json:"config"`
}

type adminReportResponse struct {
	Diagnostics diag.Snapshot      `json:"diagnostics"`
	Metrics     map[string]float64 `json:"metrics"`
}

// adminStatus serves GET /v1/admin/status. Reuses the diagnostics snapshot.
func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	if s.adminSnapshot == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "admin diagnostics unavailable")
		return
	}
	snap := s.adminSnapshot(r.Context())
	writeJSON(w, http.StatusOK, adminStatusResponse{
		Service:          snap.Service,
		Version:          snap.Version,
		Commit:           snap.Commit,
		BuildTime:        version.Date,
		Env:              snap.Env,
		UptimeSeconds:    snap.UptimeSeconds,
		MigrationVersion: snap.MigrationVersion,
		Scheduler:        snap.Scheduler,
		VerifierHealthy:  snap.Scheduler.LastHealthy,
		Dependencies:     snap.Dependencies,
		Healthy:          snap.Healthy,
	})
}

// adminIntegrity serves GET /v1/admin/integrity. Reuses the scheduler status.
func (s *Server) adminIntegrity(w http.ResponseWriter, r *http.Request) {
	if s.schedStatus == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "scheduler status unavailable")
		return
	}
	v := s.schedStatus()
	writeJSON(w, http.StatusOK, adminIntegrityResponse{
		Enabled:     v.Enabled,
		Running:     v.Running,
		HasRun:      v.HasRun,
		LastRunAt:   v.LastRunAt,
		LastHealthy: v.LastHealthy,
		Stalled:     v.Stalled,
		ChainsTotal: v.ChainsTotal,
		ChainsOK:    v.ChainsOK,
		Failures:    v.Failures,
		Errors:      v.Errors,
	})
}

// adminIntegrityRun serves POST /v1/admin/integrity/run. It triggers ONE sweep
// via the existing scheduler (no new verification logic) and reports the result.
func (s *Server) adminIntegrityRun(w http.ResponseWriter, r *http.Request) {
	if s.adminRunIntegrity == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "verification scheduler unavailable")
		return
	}
	run := s.adminRunIntegrity(r.Context())
	s.log.Info("admin integrity sweep triggered",
		"chains", run.ChainsTotal, "ok", run.ChainsOK, "failures", run.Failures,
		"errors", run.Errors, "request_id", RequestIDFrom(r.Context()))
	writeJSON(w, http.StatusOK, run)
}

// adminMetrics serves GET /v1/admin/metrics. It summarizes the existing registry.
func (s *Server) adminMetrics(w http.ResponseWriter, r *http.Request) {
	summary, err := s.metrics.Summary()
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "could not read metrics")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counters": summary})
}

// adminConfig serves GET /v1/admin/config. Reuses the redacted diagnostics config.
func (s *Server) adminConfig(w http.ResponseWriter, r *http.Request) {
	if s.adminSnapshot == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "admin diagnostics unavailable")
		return
	}
	snap := s.adminSnapshot(r.Context())
	writeJSON(w, http.StatusOK, adminConfigResponse{Modules: snap.Modules, Config: snap.Config})
}

// adminReport serves GET /v1/admin/report: diagnostics + metrics in one document.
func (s *Server) adminReport(w http.ResponseWriter, r *http.Request) {
	if s.adminSnapshot == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "admin diagnostics unavailable")
		return
	}
	snap := s.adminSnapshot(r.Context())
	summary, err := s.metrics.Summary()
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "could not read metrics")
		return
	}
	writeJSON(w, http.StatusOK, adminReportResponse{Diagnostics: snap, Metrics: summary})
}

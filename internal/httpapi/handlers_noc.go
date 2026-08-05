package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// nocOnCallScheduleCap bounds how many of the caller's on-call schedules the
// overview projection below will resolve "who's on call" for — the first N
// by schedule_id (OnCallScheduleRepository.List's own keyset order), the
// same "bounded, documented, not unlimited" shape AssetHealthCategory's
// sample cap already establishes for E1.5. A tenant with more than this many
// schedules gets a partial on_call section rather than an unbounded
// per-request fan-out of N OnCall resolutions (each itself a query) — an
// acceptable v1 bound for a read-only projection, revisited if a real
// deployment ever approaches it.
const nocOnCallScheduleCap = 100

// nocEscalationReader is the narrow read the NOC overview projection needs
// from escalation_state (E7.1): how many rows are currently active.
// *postgres.EscalationStateStore, built over the TENANT-SCOPED appPool (a
// second, read-only role alongside its privileged Seeder/Worker instance —
// see that type's own doc comment), satisfies it.
type nocEscalationReader interface {
	CountActive(ctx context.Context) (int, error)
}

// SetNOCEscalations wires the escalation-state read the NOC overview
// projection needs. Until it is called, GET /admin/noc/overview reports 501,
// the same convention every other administration surface in this package
// establishes.
func (s *Server) SetNOCEscalations(r nocEscalationReader) { s.nocEscalations = r }

// incidentStatusCountsDTO is the open-class incident breakdown by status —
// see domain.IncidentOverviewCounts' own doc comment for exactly which
// statuses are in scope.
type incidentStatusCountsDTO struct {
	Open          int `json:"open"`
	Acknowledged  int `json:"acknowledged"`
	Investigating int `json:"investigating"`
}

// incidentSeverityCountsDTO is the open-class incident breakdown by
// severity.
type incidentSeverityCountsDTO struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

// incidentGroupingDTO is E4.2's root/collateral summary over the same
// open-class incident set — see domain.IncidentOverviewCounts' own doc
// comment for what root_count/collateral_count each count.
type incidentGroupingDTO struct {
	RootCount       int `json:"root_count"`
	CollateralCount int `json:"collateral_count"`
}

// nocIncidentsDTO is the overview's "incidents" section.
type nocIncidentsDTO struct {
	OpenTotal  int                       `json:"open_total"`
	ByStatus   incidentStatusCountsDTO   `json:"by_status"`
	BySeverity incidentSeverityCountsDTO `json:"by_severity"`
	Grouped    incidentGroupingDTO       `json:"grouped"`
}

func toNOCIncidentsDTO(c *domain.IncidentOverviewCounts) nocIncidentsDTO {
	return nocIncidentsDTO{
		OpenTotal: c.OpenTotal,
		ByStatus: incidentStatusCountsDTO{
			Open:          c.ByStatus[domain.IncidentOpen],
			Acknowledged:  c.ByStatus[domain.IncidentAcknowledged],
			Investigating: c.ByStatus[domain.IncidentInvestigating],
		},
		BySeverity: incidentSeverityCountsDTO{
			Critical: c.BySeverity[domain.IncidentSeverityCritical],
			High:     c.BySeverity[domain.IncidentSeverityHigh],
			Medium:   c.BySeverity[domain.IncidentSeverityMedium],
			Low:      c.BySeverity[domain.IncidentSeverityLow],
		},
		Grouped: incidentGroupingDTO{
			RootCount:       c.RootCount,
			CollateralCount: c.CollateralCount,
		},
	}
}

// alertSeverityCountsDTO is the firing-rule breakdown by severity.
type alertSeverityCountsDTO struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// nocAlertsDTO is the overview's "alerts" section: DERIVED firing state
// (docs/PLATFORM-BUILD-PLAN.md §4), never a reified Alert row.
type nocAlertsDTO struct {
	FiringTotal int                    `json:"firing_total"`
	BySeverity  alertSeverityCountsDTO `json:"by_severity"`
}

func toNOCAlertsDTO(c *domain.AlertFiringCounts) nocAlertsDTO {
	return nocAlertsDTO{
		FiringTotal: c.Total,
		BySeverity: alertSeverityCountsDTO{
			Critical: c.BySeverity[domain.AlertSeverityCritical],
			Warning:  c.BySeverity[domain.AlertSeverityWarning],
			Info:     c.BySeverity[domain.AlertSeverityInfo],
		},
	}
}

// nocAssetsDTO is the overview's "assets" section: category COUNTS only from
// AssetRepository.Health() (E1.5) — the bounded per-category sample list
// that endpoint also returns is not reproduced here, since the overview is a
// summary, not a drill-down (GET /admin/assets/health remains the place to
// see which CIs, per docs/PLATFORM-BUILD-PLAN.md E7.1).
type nocAssetsDTO struct {
	Stale                    int `json:"stale"`
	OrphanedAssets           int `json:"orphaned_assets"`
	OrphanedBusinessServices int `json:"orphaned_business_services"`
	Incomplete               int `json:"incomplete"`
}

func toNOCAssetsDTO(r *domain.AssetHealthReport) nocAssetsDTO {
	return nocAssetsDTO{
		Stale:                    r.Stale.Count,
		OrphanedAssets:           r.OrphanedAssets.Count,
		OrphanedBusinessServices: r.OrphanedBusinessServices.Count,
		Incomplete:               r.Incomplete.Count,
	}
}

// nocOnCallEntryDTO is one active schedule's current on-call resolution.
// user_id/display_name are both null when the schedule's roster is empty —
// an ordinary state, not an error (mirrors OnCallResolution's own doc
// comment).
type nocOnCallEntryDTO struct {
	ScheduleID   string  `json:"schedule_id"`
	ScheduleName string  `json:"schedule_name"`
	UserID       *string `json:"user_id"`
	DisplayName  *string `json:"display_name"`
}

// nocEscalationsDTO is the overview's "escalations" section: the size of the
// escalation_state work-queue right now (E5.2b-2), never a reified entity of
// its own.
type nocEscalationsDTO struct {
	ActiveTotal int `json:"active_total"`
}

// nocOverviewDTO is the transient response of GET /admin/noc/overview
// (E7.1): a computed-at-request-time projection over the incident/alert/
// asset/on-call/escalation stores, never a stored Dashboard/Report/Overview
// entity (docs/PLATFORM-BUILD-PLAN.md §4, Vol III §3.4). Nothing here is
// persisted; recomputed on every call, exactly like GET /admin/assets/health
// and GET /admin/on-call-schedules/{id}/on-call.
type nocOverviewDTO struct {
	Incidents   nocIncidentsDTO     `json:"incidents"`
	Alerts      nocAlertsDTO        `json:"alerts"`
	Assets      nocAssetsDTO        `json:"assets"`
	OnCall      []nocOnCallEntryDTO `json:"on_call"`
	Escalations nocEscalationsDTO   `json:"escalations"`
	GeneratedAt time.Time           `json:"generated_at"`
}

// nocOverview serves GET /admin/noc/overview: the current operational state
// of the whole NOC loop, aggregated at request time from the tenant-scoped
// (RLS-enforced) stores this server already holds — see this file's own
// package-level documentation and ADR-NOC-001 for the query shapes, their
// indexes, and why row-level security alone is sufficient tenant isolation
// here (no explicit tenant predicate, no privileged pool). Every dependency
// must be wired for this endpoint to serve; a missing one reports 501,
// rather than a partial response silently missing a whole section.
func (s *Server) nocOverview(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil || s.alertRules == nil || s.assets == nil ||
		s.onCallSchedules == nil || s.nocEscalations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "the NOC overview is not fully configured")
		return
	}
	ctx := r.Context()

	incidentCounts, err := s.incidents.OverviewCounts(ctx)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	alertCounts, err := s.alertRules.CountFiring(ctx)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	assetHealth, err := s.assets.Health(ctx, domain.DefaultAssetStaleAfter)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	onCall, err := s.buildNOCOnCall(ctx)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	activeEscalations, err := s.nocEscalations.CountActive(ctx)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, nocOverviewDTO{
		Incidents:   toNOCIncidentsDTO(incidentCounts),
		Alerts:      toNOCAlertsDTO(alertCounts),
		Assets:      toNOCAssetsDTO(assetHealth),
		OnCall:      onCall,
		Escalations: nocEscalationsDTO{ActiveTotal: activeEscalations},
		GeneratedAt: time.Now().UTC(),
	})
}

// buildNOCOnCall lists up to nocOnCallScheduleCap of the caller's schedules
// (List's own keyset order, unfiltered by status — there is no status filter
// on that method) and resolves "who's on call now" for each ACTIVE one via
// the existing OnCall(now), exactly as GET .../on-call-schedules/{id}/on-call
// does for one schedule. An archived schedule is skipped, not resolved: it
// has nothing currently on call by definition.
func (s *Server) buildNOCOnCall(ctx context.Context) ([]nocOnCallEntryDTO, error) {
	schedules, err := s.onCallSchedules.List(ctx, nocOnCallScheduleCap, "")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]nocOnCallEntryDTO, 0, len(schedules))
	for _, sch := range schedules {
		if !sch.Active() {
			continue
		}
		res, err := s.onCallSchedules.OnCall(ctx, sch.ScheduleID, now)
		if err != nil {
			return nil, err
		}
		out = append(out, nocOnCallEntryDTO{
			ScheduleID:   sch.ScheduleID,
			ScheduleName: sch.Name,
			UserID:       res.UserID,
			DisplayName:  res.DisplayName,
		})
	}
	return out, nil
}

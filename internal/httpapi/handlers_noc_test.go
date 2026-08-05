package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeAlertRulesForNOC is a minimal domain.AlertRuleRepository fake: only
// CountFiring is exercised by the NOC overview, so every other method is a
// no-op spy — mirroring fakeIncidents/fakeOnCallSchedules' own shape for the
// methods their own handlers don't touch.
type fakeAlertRulesForNOC struct {
	countFirings   int
	firingCounts   *domain.AlertFiringCounts
	countFiringErr error
}

func (f *fakeAlertRulesForNOC) Create(context.Context, *domain.AlertRule) (*domain.AlertRule, error) {
	return nil, nil
}
func (f *fakeAlertRulesForNOC) Get(context.Context, string) (*domain.AlertRule, error) {
	return nil, nil
}
func (f *fakeAlertRulesForNOC) List(context.Context, int, string) ([]*domain.AlertRule, error) {
	return nil, nil
}
func (f *fakeAlertRulesForNOC) Update(
	context.Context, string, int64, domain.AlertRulePatch,
) (*domain.AlertRule, error) {
	return nil, nil
}
func (f *fakeAlertRulesForNOC) Delete(context.Context, string) error { return nil }
func (f *fakeAlertRulesForNOC) CountFiring(context.Context) (*domain.AlertFiringCounts, error) {
	f.countFirings++
	if f.countFiringErr != nil {
		return nil, f.countFiringErr
	}
	if f.firingCounts != nil {
		return f.firingCounts, nil
	}
	return &domain.AlertFiringCounts{BySeverity: map[domain.AlertSeverity]int{}}, nil
}

var _ domain.AlertRuleRepository = (*fakeAlertRulesForNOC)(nil)

// fakeEscalationReader is the nocEscalationReader fake.
type fakeEscalationReader struct {
	calls  int
	active int
	err    error
}

func (f *fakeEscalationReader) CountActive(context.Context) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return f.active, nil
}

var _ nocEscalationReader = (*fakeEscalationReader)(nil)

// wireNOCOverview wires every dependency GET /admin/noc/overview needs, so
// tests can override just the fakes they care about.
func wireNOCOverview(srv *Server, incidents *fakeIncidents, alerts *fakeAlertRulesForNOC,
	assets *fakeAssets, onCall *fakeOnCallSchedules, esc *fakeEscalationReader,
) {
	srv.SetIncidents(incidents)
	srv.SetAlertRules(alerts)
	srv.SetAssets(assets)
	srv.SetOnCallSchedules(onCall)
	srv.SetNOCEscalations(esc)
}

func TestNOCOverview_Authorization(t *testing.T) {
	cases := []struct {
		name  string
		token func(t *testing.T) string
		want  int
	}{
		{"no token", func(*testing.T) string { return "" }, http.StatusUnauthorized},
		{"read-only principal", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-reader"})
		}, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			incidents := &fakeIncidents{}
			alerts := &fakeAlertRulesForNOC{}
			assets := &fakeAssets{}
			onCall := &fakeOnCallSchedules{}
			esc := &fakeEscalationReader{}
			wireNOCOverview(srv, incidents, alerts, assets, onCall, esc)

			req := httptest.NewRequest(http.MethodGet, "/v1/admin/noc/overview", nil)
			if tok := tc.token(t); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if incidents.touched() != 0 || alerts.countFirings != 0 || assets.healths != 0 ||
				onCall.touched() != 0 || esc.calls != 0 {
				t.Error("a refused request reached one of the overview's stores")
			}
		})
	}
}

func TestNOCOverview_NotImplementedWhenUnwired(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	// Only some dependencies wired — the endpoint must not serve a partial
	// response silently missing a whole section.
	srv.SetIncidents(&fakeIncidents{})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/noc/overview", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// TestNOCOverview_AssemblesDTO is the DTO-assembly unit test: every section
// of the response is traced back to a specific fake return value, and an
// archived schedule is proven to be excluded from on_call (buildNOCOnCall's
// own skip-if-not-Active branch).
func TestNOCOverview_AssemblesDTO(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	incidents := &fakeIncidents{overviewCounts: &domain.IncidentOverviewCounts{
		OpenTotal: 5,
		ByStatus: map[domain.IncidentStatus]int{
			domain.IncidentOpen:          3,
			domain.IncidentAcknowledged:  2,
			domain.IncidentInvestigating: 0,
		},
		BySeverity: map[domain.IncidentSeverity]int{
			domain.IncidentSeverityCritical: 1,
			domain.IncidentSeverityHigh:     4,
		},
		RootCount:       1,
		CollateralCount: 2,
	}}
	alerts := &fakeAlertRulesForNOC{firingCounts: &domain.AlertFiringCounts{
		Total: 3,
		BySeverity: map[domain.AlertSeverity]int{
			domain.AlertSeverityCritical: 2,
			domain.AlertSeverityWarning:  1,
		},
	}}
	assets := &fakeAssets{healthReport: &domain.AssetHealthReport{
		Stale:                    domain.AssetHealthCategory{Count: 7},
		OrphanedAssets:           domain.AssetHealthCategory{Count: 2},
		OrphanedBusinessServices: domain.AssetHealthCategory{Count: 1},
		Incomplete:               domain.AssetHealthCategory{Count: 4},
	}}
	activeUser := "u-1"
	activeName := "Alice"
	onCall := &fakeOnCallSchedules{
		listSchedules: []*domain.OnCallSchedule{
			{ScheduleID: "sch-active", Name: "Primary", Status: domain.OnCallScheduleActive},
			{ScheduleID: "sch-archived", Name: "Old", Status: domain.OnCallScheduleArchived},
		},
		onCallRes: &domain.OnCallResolution{UserID: &activeUser, DisplayName: &activeName},
	}
	esc := &fakeEscalationReader{active: 4}
	wireNOCOverview(srv, incidents, alerts, assets, onCall, esc)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/noc/overview", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got nocOverviewDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got.Incidents.OpenTotal != 5 {
		t.Errorf("open_total = %d, want 5", got.Incidents.OpenTotal)
	}
	if got.Incidents.ByStatus.Open != 3 || got.Incidents.ByStatus.Acknowledged != 2 || got.Incidents.ByStatus.Investigating != 0 {
		t.Errorf("by_status = %+v, want {3 2 0}", got.Incidents.ByStatus)
	}
	if got.Incidents.BySeverity.Critical != 1 || got.Incidents.BySeverity.High != 4 {
		t.Errorf("by_severity = %+v", got.Incidents.BySeverity)
	}
	if got.Incidents.Grouped.RootCount != 1 || got.Incidents.Grouped.CollateralCount != 2 {
		t.Errorf("grouped = %+v, want {1 2}", got.Incidents.Grouped)
	}
	if got.Alerts.FiringTotal != 3 || got.Alerts.BySeverity.Critical != 2 || got.Alerts.BySeverity.Warning != 1 {
		t.Errorf("alerts = %+v", got.Alerts)
	}
	if got.Assets.Stale != 7 || got.Assets.OrphanedAssets != 2 || got.Assets.OrphanedBusinessServices != 1 || got.Assets.Incomplete != 4 {
		t.Errorf("assets = %+v", got.Assets)
	}
	if len(got.OnCall) != 1 {
		t.Fatalf("on_call has %d entries, want 1 (the archived schedule must be excluded)", len(got.OnCall))
	}
	if got.OnCall[0].ScheduleID != "sch-active" || got.OnCall[0].ScheduleName != "Primary" {
		t.Errorf("on_call[0] = %+v", got.OnCall[0])
	}
	if got.OnCall[0].UserID == nil || *got.OnCall[0].UserID != "u-1" {
		t.Errorf("on_call[0].user_id = %v, want u-1", got.OnCall[0].UserID)
	}
	if got.Escalations.ActiveTotal != 4 {
		t.Errorf("escalations.active_total = %d, want 4", got.Escalations.ActiveTotal)
	}
	if time.Since(got.GeneratedAt) > time.Minute || time.Since(got.GeneratedAt) < -time.Minute {
		t.Errorf("generated_at = %v, not close to now", got.GeneratedAt)
	}

	if onCall.onCalls != 1 {
		t.Errorf("OnCall resolved %d time(s), want exactly 1 (only the active schedule)", onCall.onCalls)
	}
}

// TestNOCOverview_EmptyTenantReturnsCleanZeroes proves the empty-tenant
// bound: every section renders zeroed/empty, never a 500 or a null panic.
func TestNOCOverview_EmptyTenantReturnsCleanZeroes(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	wireNOCOverview(srv, &fakeIncidents{}, &fakeAlertRulesForNOC{}, &fakeAssets{}, &fakeOnCallSchedules{}, &fakeEscalationReader{})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/noc/overview", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"on_call":[]`) && !strings.Contains(rec.Body.String(), `"on_call":null`) {
		t.Errorf("expected an empty on_call array, got: %s", rec.Body.String())
	}

	var got nocOverviewDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Incidents.OpenTotal != 0 || got.Alerts.FiringTotal != 0 || got.Escalations.ActiveTotal != 0 {
		t.Errorf("expected a zeroed overview for an empty tenant, got %+v", got)
	}
}

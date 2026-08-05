//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// nocOverviewHarness wires GET /admin/noc/overview against the REAL stores —
// a tenant-scoped appPool (row-level security enforced, exactly as
// production's composition root builds it) plus a privileged pool used ONLY
// for tenant registration and E4.2-style grouping fixture setup, never for
// anything the overview itself reads. This is the wiring the handler unit
// tests (fakes) cannot prove: that RLS, not a predicate the query happens to
// carry, is what isolates one tenant's aggregate from another's.
type nocOverviewHarness struct {
	router     http.Handler
	priv       *pgxpool.Pool
	appPool    *pgxpool.Pool
	assets     *postgres.AssetStore
	incidents  *postgres.IncidentStore
	alertRules *postgres.AlertRuleStore
	onCall     *postgres.OnCallScheduleStore
	policies   *postgres.EscalationPolicyStore
	tenants    *postgres.TenantStore
	grouping   *postgres.IncidentGroupingStore // privileged: E4.2's only write path
	privInc    *postgres.IncidentStore         // privileged: FindOrCreateOpenAlertIncident
	escSeed    *postgres.EscalationStateStore  // privileged: Seeder's own instance
	users      *postgres.UserStore             // privileged: app_user fixture rows only
}

func realNOCOverviewHarness(t *testing.T) *nocOverviewHarness {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "options=-c%20search_path%3Dhttpapi_itest"
	ctx := context.Background()

	priv, err := postgres.NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("privileged pool: %v", err)
	}
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = priv.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("db not ready: %v", pingErr)
	}
	if _, err := priv.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS httpapi_itest`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := migrate.Up(ctx, priv); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(priv.Close)

	appPool, err := postgres.NewTenantScopedPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("tenant-scoped pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), priv.Ping)

	h := &nocOverviewHarness{
		priv: priv, appPool: appPool,
		assets:     postgres.NewAssetStore(appPool),
		incidents:  postgres.NewIncidentStore(appPool),
		alertRules: postgres.NewAlertRuleStore(appPool),
		onCall:     postgres.NewOnCallScheduleStore(appPool),
		policies:   postgres.NewEscalationPolicyStore(appPool),
		tenants:    postgres.NewTenantStore(priv),
		grouping:   postgres.NewIncidentGroupingStore(priv),
		privInc:    postgres.NewIncidentStore(priv),
		escSeed:    postgres.NewEscalationStateStore(priv),
		users:      postgres.NewUserStore(priv),
	}
	s.SetTenants(h.tenants)
	s.SetAssets(h.assets)
	s.SetIncidents(h.incidents)
	s.SetAlertRules(h.alertRules)
	s.SetOnCallSchedules(h.onCall)
	s.SetEscalationPolicies(h.policies)
	s.SetNOCEscalations(postgres.NewEscalationStateStore(appPool))
	h.router = s.Router()
	return h
}

// nocTenant registers a real tenant on the privileged pool.
func (h *nocOverviewHarness) nocTenant(t *testing.T, slug string) *domain.Tenant {
	t.Helper()
	slug = uniqueSlug(slug)
	tn, err := h.tenants.Create(domain.WithActor(context.Background(), "test-actor"), &domain.Tenant{
		TenantID: domain.NewID(), Slug: slug, Name: slug, ExternalID: "ext-" + slug,
		Status: domain.TenantActive,
	})
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

func (h *nocOverviewHarness) ctx(tn *domain.Tenant) context.Context {
	return domain.WithActor(domain.WithTenant(context.Background(), tn), "test-actor")
}

// nocGet issues an authenticated GET /v1/admin/noc/overview as tn's admin.
func (h *nocOverviewHarness) nocGet(t *testing.T, tn *domain.Tenant) (*httptest.ResponseRecorder, nocOverviewDTO) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/noc/overview", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	var got nocOverviewDTO
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode overview: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, got
}

// seedFullNOCFixture populates one incident/rule/asset/on-call/escalation
// picture for tn, real end to end: two collateral incidents grouped under a
// third root (E4.2's own write path), a firing critical alert rule and a
// disabled-but-firing rule that must be excluded, one active schedule with a
// roster and one archived schedule that must be excluded, and one seeded
// active escalation_state row via the real Seeder path.
func (h *nocOverviewHarness) seedFullNOCFixture(t *testing.T, tn *domain.Tenant) {
	t.Helper()
	ctx := h.ctx(tn)

	host, err := domain.NewAsset(tn.TenantID, "server", "noc-host-"+tn.Slug, nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	createdHost, err := h.assets.Create(ctx, host)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}

	// Three open, manual incidents: two collateral under the third (E4.2).
	root := h.createIncident(t, ctx, tn, "root cause", domain.IncidentSeverityHigh)
	childA := h.createIncident(t, ctx, tn, "collateral A", domain.IncidentSeverityMedium)
	childB := h.createIncident(t, ctx, tn, "collateral B", domain.IncidentSeverityLow)
	if err := h.grouping.SetRootIncidentID(context.Background(), tn.TenantID, childA.IncidentID, &root.IncidentID); err != nil {
		t.Fatalf("set root on child A: %v", err)
	}
	if err := h.grouping.SetRootIncidentID(context.Background(), tn.TenantID, childB.IncidentID, &root.IncidentID); err != nil {
		t.Fatalf("set root on child B: %v", err)
	}
	// One acknowledged incident, to prove by_status distinguishes it from open.
	acked := h.createIncident(t, ctx, tn, "acked one", domain.IncidentSeverityCritical)
	if _, err := h.incidents.SetStatus(ctx, acked.IncidentID, acked.RowVersion, domain.IncidentAcknowledged); err != nil {
		t.Fatalf("acknowledge incident: %v", err)
	}

	// Firing critical rule (counted) + disabled-but-firing rule (excluded).
	h.createFiringRule(t, ctx, createdHost.AssetID, domain.AlertSeverityCritical, true)
	h.createFiringRule(t, ctx, createdHost.AssetID, domain.AlertSeverityWarning, false)

	// One active schedule with a roster, one archived (must be excluded).
	active, err := domain.NewOnCallSchedule(tn.TenantID, "Primary", 3600, time.Now().UTC())
	if err != nil {
		t.Fatalf("new schedule: %v", err)
	}
	createdActive, err := h.onCall.Create(ctx, active)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	h.addRosterUser(t, tn, createdActive.ScheduleID, "u-"+tn.Slug)

	archived, err := domain.NewOnCallSchedule(tn.TenantID, "Retired", 3600, time.Now().UTC())
	if err != nil {
		t.Fatalf("new archived schedule: %v", err)
	}
	createdArchived, err := h.onCall.Create(ctx, archived)
	if err != nil {
		t.Fatalf("create archived schedule: %v", err)
	}
	archivedStatus := domain.OnCallScheduleArchived
	if _, err := h.onCall.Update(ctx, createdArchived.ScheduleID, createdArchived.RowVersion,
		domain.OnCallSchedulePatch{Status: &archivedStatus}); err != nil {
		t.Fatalf("archive schedule: %v", err)
	}

	// One active escalation_state row via the real config+seed path: an
	// active policy, an OPEN alert-sourced incident, then Seed.
	pol, err := domain.NewEscalationPolicy(tn.TenantID, "Default")
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	if _, err := h.policies.Create(ctx, pol); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	want, err := domain.NewAlertIncident(tn.TenantID, "cpu high", "", domain.IncidentSeverityCritical, createdHost.AssetID)
	if err != nil {
		t.Fatalf("new alert incident: %v", err)
	}
	if _, err := h.privInc.FindOrCreateOpenAlertIncident(context.Background(), want, "test-actor", "alert fired"); err != nil {
		t.Fatalf("find-or-create alert incident: %v", err)
	}
	if _, _, err := h.escSeed.Seed(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("seed escalation state: %v", err)
	}
}

// addRosterUser creates a real app_user row and attaches it to scheduleID at
// position 0, directly via the privileged pool rather than
// OnCallScheduleRepository.AddParticipant: AddParticipant re-verifies an
// ACTIVE membership, which requires a real Organization (its own,
// independently-provisioned tenant — domain.NewOrganization's own doc
// comment), incompatible with this harness's directly-registered tenants
// (needed so a plain external id resolves a real JWT, which Organization's
// creation path does not expose). That business rule is already
// mutation-verified by TestOnCallScheduleStore_AddParticipantRejectsNonActiveMember;
// this harness only needs a real roster row for OnCall's own read to resolve
// against.
func (h *nocOverviewHarness) addRosterUser(t *testing.T, tn *domain.Tenant, scheduleID, userID string) {
	t.Helper()
	u, err := domain.NewUser(userID+"@example.com", "Roster User")
	if err != nil {
		t.Fatalf("new user: %v", err)
	}
	u.UserID = userID
	if _, err := h.users.Create(domain.WithActor(context.Background(), "test-actor"), u); err != nil {
		t.Fatalf("create app_user %s: %v", userID, err)
	}
	if _, err := h.priv.Exec(context.Background(), `
		INSERT INTO on_call_participant (participant_id, tenant_id, schedule_id, user_id, position)
		VALUES ($1, $2, $3, $4, 0)`,
		domain.NewID(), tn.TenantID, scheduleID, userID); err != nil {
		t.Fatalf("insert on_call_participant: %v", err)
	}
}

func (h *nocOverviewHarness) createIncident(
	t *testing.T, ctx context.Context, tn *domain.Tenant, title string, sev domain.IncidentSeverity,
) *domain.Incident {
	t.Helper()
	inc, err := domain.NewIncident(tn.TenantID, title, "", sev, nil, nil)
	if err != nil {
		t.Fatalf("new incident %s: %v", title, err)
	}
	created, err := h.incidents.Create(ctx, inc)
	if err != nil {
		t.Fatalf("create incident %s: %v", title, err)
	}
	return created
}

func (h *nocOverviewHarness) createFiringRule(
	t *testing.T, ctx context.Context, assetID string, sev domain.AlertSeverity, enabled bool,
) {
	t.Helper()
	r, err := domain.NewAlertRule(domain.TenantIDFrom(ctx), assetID, "cpu_utilization", domain.ComparatorGT, 90, 60, sev)
	if err != nil {
		t.Fatalf("new alert rule: %v", err)
	}
	r.Enabled = enabled
	created, err := h.alertRules.Create(ctx, r)
	if err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	if _, err := h.alertRules.RecordTransition(
		ctx, created.RuleID, created.RowVersion, domain.AlertRuleStateFiring, time.Now().UTC(), nil,
	); err != nil {
		t.Fatalf("record firing transition: %v", err)
	}
}

// TestNOCOverviewAPI_AggregatesAgainstRealStores is the aggregate-correctness
// proof: every section of a real GET response is traced back to fixtures
// seeded through the ordinary tenant-scoped write paths (plus E4.2's
// grouping write and E5.2b-2's Seeder, both privileged by design), never a
// hand-crafted expectation.
func TestNOCOverviewAPI_AggregatesAgainstRealStores(t *testing.T) {
	h := realNOCOverviewHarness(t)
	tn := h.nocTenant(t, "noc-agg")
	h.seedFullNOCFixture(t, tn)

	rec, got := h.nocGet(t, tn)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// incidents: 3 open-class root/collateral incidents + 1 acknowledged + 1
	// open alert-sourced incident from the escalation fixture = 5 open-class
	// total (acked counts as open-class too).
	if got.Incidents.OpenTotal != 5 {
		t.Errorf("open_total = %d, want 5: %+v", got.Incidents.OpenTotal, got.Incidents)
	}
	if got.Incidents.ByStatus.Open != 4 {
		t.Errorf("by_status.open = %d, want 4", got.Incidents.ByStatus.Open)
	}
	if got.Incidents.ByStatus.Acknowledged != 1 {
		t.Errorf("by_status.acknowledged = %d, want 1", got.Incidents.ByStatus.Acknowledged)
	}
	if got.Incidents.Grouped.CollateralCount != 2 {
		t.Errorf("grouped.collateral_count = %d, want 2", got.Incidents.Grouped.CollateralCount)
	}
	if got.Incidents.Grouped.RootCount != 1 {
		t.Errorf("grouped.root_count = %d, want 1 (one distinct root)", got.Incidents.Grouped.RootCount)
	}

	// alerts: exactly one ENABLED firing rule counted; the disabled one is not.
	if got.Alerts.FiringTotal != 1 {
		t.Errorf("firing_total = %d, want 1 (disabled rule excluded)", got.Alerts.FiringTotal)
	}
	if got.Alerts.BySeverity.Critical != 1 {
		t.Errorf("by_severity.critical = %d, want 1", got.Alerts.BySeverity.Critical)
	}
	if got.Alerts.BySeverity.Warning != 0 {
		t.Errorf("by_severity.warning = %d, want 0 (that rule was disabled)", got.Alerts.BySeverity.Warning)
	}

	// on_call: exactly the active schedule, never the archived one.
	if len(got.OnCall) != 1 {
		t.Fatalf("on_call has %d entries, want 1: %+v", len(got.OnCall), got.OnCall)
	}
	if got.OnCall[0].ScheduleName != "Primary" {
		t.Errorf("on_call[0].schedule_name = %q, want Primary", got.OnCall[0].ScheduleName)
	}
	if got.OnCall[0].UserID == nil || *got.OnCall[0].UserID != "u-"+tn.Slug {
		t.Errorf("on_call[0].user_id = %v, want u-%s", got.OnCall[0].UserID, tn.Slug)
	}

	// escalations: exactly the one row the real Seeder enrolled.
	if got.Escalations.ActiveTotal != 1 {
		t.Errorf("escalations.active_total = %d, want 1", got.Escalations.ActiveTotal)
	}
}

// TestNOCOverviewAPI_TenantIsolation is the test the whole story exists to
// make bite: tenant A's overview must never include tenant B's incidents,
// firing alerts, on-call roster, or active escalations, even though every
// query below carries NO explicit tenant_id predicate at all — isolation is
// row-level security on the tenant-scoped connection, full stop.
func TestNOCOverviewAPI_TenantIsolation(t *testing.T) {
	h := realNOCOverviewHarness(t)
	tnA := h.nocTenant(t, "noc-iso-a")
	tnB := h.nocTenant(t, "noc-iso-b")

	h.seedFullNOCFixture(t, tnA)
	h.seedFullNOCFixture(t, tnB)
	// A third tenant with even more volume, to prove A's counts do not simply
	// happen to equal the sum of everyone else's by coincidence of fixture size.
	h.seedFullNOCFixture(t, h.nocTenant(t, "noc-iso-c"))

	_, gotA := h.nocGet(t, tnA)
	_, gotB := h.nocGet(t, tnB)

	if gotA.Incidents.OpenTotal != 5 || gotB.Incidents.OpenTotal != 5 {
		t.Fatalf("A/B open_total = %d/%d, want 5/5 (each tenant sees only its own)",
			gotA.Incidents.OpenTotal, gotB.Incidents.OpenTotal)
	}
	if gotA.Alerts.FiringTotal != 1 || gotB.Alerts.FiringTotal != 1 {
		t.Fatalf("A/B firing_total = %d/%d, want 1/1", gotA.Alerts.FiringTotal, gotB.Alerts.FiringTotal)
	}
	if len(gotA.OnCall) != 1 || len(gotB.OnCall) != 1 {
		t.Fatalf("A/B on_call length = %d/%d, want 1/1", len(gotA.OnCall), len(gotB.OnCall))
	}
	if gotA.OnCall[0].UserID == nil || *gotA.OnCall[0].UserID != "u-"+tnA.Slug {
		t.Errorf("tenant A's overview shows on-call user %v, want u-%s (tenant B's or C's roster leaked)",
			gotA.OnCall[0].UserID, tnA.Slug)
	}
	if gotB.OnCall[0].UserID == nil || *gotB.OnCall[0].UserID != "u-"+tnB.Slug {
		t.Errorf("tenant B's overview shows on-call user %v, want u-%s (tenant A's or C's roster leaked)",
			gotB.OnCall[0].UserID, tnB.Slug)
	}
	if gotA.Escalations.ActiveTotal != 1 || gotB.Escalations.ActiveTotal != 1 {
		t.Fatalf("A/B escalations.active_total = %d/%d, want 1/1", gotA.Escalations.ActiveTotal, gotB.Escalations.ActiveTotal)
	}
	if gotA.Incidents.Grouped.RootCount != 1 || gotA.Incidents.Grouped.CollateralCount != 2 {
		t.Errorf("tenant A grouped = %+v, want {root:1 collateral:2} (unaffected by B/C's own grouping)", gotA.Incidents.Grouped)
	}
}

// TestNOCOverviewAPI_EmptyTenantIsCleanZeroes proves the empty-tenant bound:
// a brand-new tenant with no incidents/rules/assets/schedules/escalations at
// all gets 200 with every section zeroed/empty, never a 500 or a null panic.
func TestNOCOverviewAPI_EmptyTenantIsCleanZeroes(t *testing.T) {
	h := realNOCOverviewHarness(t)
	tn := h.nocTenant(t, "noc-empty")

	rec, got := h.nocGet(t, tn)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got.Incidents.OpenTotal != 0 {
		t.Errorf("open_total = %d, want 0", got.Incidents.OpenTotal)
	}
	if got.Incidents.Grouped.RootCount != 0 || got.Incidents.Grouped.CollateralCount != 0 {
		t.Errorf("grouped = %+v, want zeroes", got.Incidents.Grouped)
	}
	if got.Alerts.FiringTotal != 0 {
		t.Errorf("firing_total = %d, want 0", got.Alerts.FiringTotal)
	}
	if len(got.OnCall) != 0 {
		t.Errorf("on_call = %+v, want empty", got.OnCall)
	}
	if got.Escalations.ActiveTotal != 0 {
		t.Errorf("escalations.active_total = %d, want 0", got.Escalations.ActiveTotal)
	}
	if got.Assets.Stale != 0 || got.Assets.Incomplete != 0 {
		t.Errorf("assets = %+v, want zeroes", got.Assets)
	}
}

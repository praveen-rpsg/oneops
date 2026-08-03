//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// E5.1: Incident lifecycle + append-only timeline.

// The whole legal lifecycle, live: open -> acknowledged -> investigating ->
// resolved -> reopened -> investigating -> resolved -> closed, each
// transition checked against the database, not merely the in-memory state
// machine domain/incident_test.go already covers.
func TestIncidentStore_SetStatus_WalksTheLegalLifecycle(t *testing.T) {
	priv := testPool(t)
	tn := incidentTenant(t, NewTenantStore(priv), "incident-lifecycle-walk")

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctx := incidentTestCtx(tn)

	inc, _ := domain.NewIncident(tn.TenantID, "lifecycle", "", domain.IncidentSeverityHigh, nil, nil)
	created, err := store.Create(ctx, inc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cur := created
	for _, next := range []domain.IncidentStatus{
		domain.IncidentAcknowledged, domain.IncidentInvestigating, domain.IncidentResolved,
		domain.IncidentReopened, domain.IncidentInvestigating, domain.IncidentResolved, domain.IncidentClosed,
	} {
		updated, err := store.SetStatus(ctx, cur.IncidentID, cur.RowVersion, next)
		if err != nil {
			t.Fatalf("transition %s -> %s: %v", cur.Status, next, err)
		}
		if updated.Status != next {
			t.Fatalf("status = %q, want %q", updated.Status, next)
		}
		if updated.RowVersion != cur.RowVersion+1 {
			t.Fatalf("row_version = %d, want %d", updated.RowVersion, cur.RowVersion+1)
		}
		cur = updated
	}

	if cur.AcknowledgedAt == nil {
		t.Error("acknowledged_at was never set")
	}
	if cur.ResolvedAt == nil {
		t.Error("resolved_at was never set")
	}
	if cur.ClosedAt == nil {
		t.Error("closed_at was never set")
	}
}

// THIS MUST BITE: an illegal transition is refused by the database-backed
// store, not merely by the in-memory domain guard, and no row is mutated by
// the refused attempt.
func TestIncidentStore_SetStatus_RejectsIllegalTransition(t *testing.T) {
	priv := testPool(t)
	tn := incidentTenant(t, NewTenantStore(priv), "incident-illegal-transition")

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctx := incidentTestCtx(tn)

	inc, _ := domain.NewIncident(tn.TenantID, "illegal", "", domain.IncidentSeverityHigh, nil, nil)
	created, err := store.Create(ctx, inc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// open -> resolved skips the pipeline; not a legal edge.
	_, err = store.SetStatus(ctx, created.IncidentID, created.RowVersion, domain.IncidentResolved)
	var transErr *domain.IncidentTransitionError
	if !errors.As(err, &transErr) {
		t.Fatalf("open -> resolved = %v, want *domain.IncidentTransitionError", err)
	}
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Error("the error does not unwrap to domain.ErrInvalidTransition")
	}

	// The refused attempt left no trace: still open, still at its original
	// row_version, and no acknowledged_at/resolved_at set.
	still, err := store.Get(ctx, created.IncidentID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if still.Status != domain.IncidentOpen || still.RowVersion != created.RowVersion {
		t.Errorf("a refused transition mutated the row: %+v", still)
	}
	if still.ResolvedAt != nil {
		t.Errorf("resolved_at was set despite a refused transition: %+v", still)
	}

	// And it wrote no timeline row either — an illegal move is not a change.
	timeline, err := store.Timeline(ctx, created.IncidentID, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	for _, e := range timeline {
		if e.Kind == domain.IncidentEventStatusTransitioned {
			t.Errorf("a refused transition still wrote a timeline row: %+v", e)
		}
	}

	// closed is terminal: once there, nothing moves it, including reopened.
	ackd, err := store.SetStatus(ctx, created.IncidentID, created.RowVersion, domain.IncidentAcknowledged)
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	inv, err := store.SetStatus(ctx, ackd.IncidentID, ackd.RowVersion, domain.IncidentInvestigating)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	res, err := store.SetStatus(ctx, inv.IncidentID, inv.RowVersion, domain.IncidentResolved)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	closed, err := store.SetStatus(ctx, res.IncidentID, res.RowVersion, domain.IncidentClosed)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := store.SetStatus(ctx, closed.IncidentID, closed.RowVersion, domain.IncidentReopened); !errors.As(err, &transErr) {
		t.Errorf("closed -> reopened = %v, want *domain.IncidentTransitionError (closed is terminal)", err)
	}
}

// THIS MUST BITE: a stale row_version on a transition is refused, exactly as
// it is for Update.
func TestIncidentStore_SetStatus_DetectsVersionConflict(t *testing.T) {
	priv := testPool(t)
	tn := incidentTenant(t, NewTenantStore(priv), "incident-transition-conflict")

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctx := incidentTestCtx(tn)

	inc, _ := domain.NewIncident(tn.TenantID, "conflict", "", domain.IncidentSeverityHigh, nil, nil)
	created, err := store.Create(ctx, inc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := store.SetStatus(ctx, created.IncidentID, created.RowVersion, domain.IncidentAcknowledged); err != nil {
		t.Fatalf("first transition: %v", err)
	}

	// created.RowVersion is now stale (the incident moved to row_version 2).
	if _, err := store.SetStatus(ctx, created.IncidentID, created.RowVersion, domain.IncidentInvestigating); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale transition = %v, want ErrVersionMismatch", err)
	}
}

// THIS MUST BITE: Create/SetStatus/Assign each write the timeline row(s)
// their doc comments promise, with the correct old->new value and the real
// actor — not a fixture constant this test happens to reuse for the write.
func TestIncidentStore_Timeline_RecordsCreateTransitionAndAssignment(t *testing.T) {
	priv := testPool(t)
	tn := incidentTenant(t, NewTenantStore(priv), "incident-timeline-record")

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	creator := incidentTestCtxAs(tn, "user-creator")
	transitioner := incidentTestCtxAs(tn, "user-transitioner")

	inc, _ := domain.NewIncident(tn.TenantID, "timeline host", "", domain.IncidentSeverityMedium, nil, nil)
	created, err := store.Create(creator, inc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	acked, err := store.SetStatus(transitioner, created.IncidentID, created.RowVersion, domain.IncidentAcknowledged)
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	timeline, err := store.Timeline(creator, created.IncidentID, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(timeline) != 2 {
		t.Fatalf("timeline has %d row(s), want 2 (created, status_transitioned): %+v", len(timeline), timeline)
	}

	createdRow, transitionRow := timeline[0], timeline[1]
	if createdRow.Kind != domain.IncidentEventCreated || createdRow.Actor != "user-creator" ||
		createdRow.OldValue != nil || createdRow.NewValue != nil || createdRow.RowVersion != 1 {
		t.Errorf("created row wrong: %+v", createdRow)
	}
	if transitionRow.Kind != domain.IncidentEventStatusTransitioned || transitionRow.Field != "status" ||
		transitionRow.Actor != "user-transitioner" ||
		transitionRow.OldValue == nil || *transitionRow.OldValue != "open" ||
		transitionRow.NewValue == nil || *transitionRow.NewValue != "acknowledged" ||
		transitionRow.RowVersion != acked.RowVersion {
		t.Errorf("transition row wrong: %+v", transitionRow)
	}
}

// THIS MUST BITE: incident_event is append-only at the schema level, exactly
// like asset_change_history — a direct UPDATE/DELETE/TRUNCATE, issued on the
// tenant-scoped connection that wrote the row, is refused by the database
// itself, not merely by store discipline. And session_replication_role does
// not bypass it: the guards are ENABLE ALWAYS from this table's first
// migration, exactly like asset_change_history's.
func TestIncidentEvent_IsAppendOnly(t *testing.T) {
	priv := testPool(t)
	tn := incidentTenant(t, NewTenantStore(priv), "incident-timeline-append-only")

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctx := incidentTestCtx(tn)

	inc, _ := domain.NewIncident(tn.TenantID, "append-only host", "", domain.IncidentSeverityLow, nil, nil)
	created, err := store.Create(ctx, inc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	timeline, err := store.Timeline(ctx, created.IncidentID, 0, "")
	if err != nil || len(timeline) == 0 {
		t.Fatalf("timeline: %v (%d rows)", err, len(timeline))
	}
	eventID := timeline[0].EventID

	if _, err := scoped.Exec(ctx,
		`UPDATE incident_event SET actor = 'tampered' WHERE event_id = $1`, eventID); err == nil {
		t.Error("incident_event must refuse UPDATE on the request-path connection")
	}
	if _, err := scoped.Exec(ctx,
		`DELETE FROM incident_event WHERE event_id = $1`, eventID); err == nil {
		t.Error("incident_event must refuse DELETE on the request-path connection")
	}
	if _, err := scoped.Exec(ctx, `TRUNCATE incident_event`); err == nil {
		t.Error("incident_event must refuse TRUNCATE on the request-path connection")
	}

	after, err := store.Timeline(ctx, created.IncidentID, 0, "")
	if err != nil {
		t.Fatalf("re-read timeline: %v", err)
	}
	if len(after) == 0 || after[0].Actor == "tampered" {
		t.Errorf("timeline was mutated despite three refused attempts: %+v", after)
	}

	// Also proven at the privileged connection, using ENABLE ALWAYS (not the
	// default origin-mode trigger a session_replication_role='replica' would
	// suppress) — the same hardening asset_change_history/audit_event carry.
	conn, err := priv.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(context.Background(), `SET session_replication_role = 'replica'`); err != nil {
		t.Fatalf("set replication role: %v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SET session_replication_role = 'origin'`)
	}()
	if _, err := conn.Exec(context.Background(),
		`UPDATE incident_event SET actor = 'tampered' WHERE event_id = $1`, eventID); err == nil {
		t.Error("session_replication_role='replica' bypassed incident_event's guard — it is not ENABLE ALWAYS")
	}
}

// THIS MUST BITE: tenant B cannot see tenant A's timeline, either through the
// store's own Timeline method or through a direct query on the tenant-scoped
// connection — row-level security, not a WHERE clause the store remembered
// to add.
func TestIncidentEvent_RLSIsolatesByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("incident-tl-alpha", "ext-incident-tl-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("incident-tl-bravo", "ext-incident-tl-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctxA := incidentTestCtx(a)
	ctxB := incidentTestCtx(b)

	incA, _ := domain.NewIncident(a.TenantID, "tenant-a-incident", "", domain.IncidentSeverityLow, nil, nil)
	createdA, err := store.Create(ctxA, incA)
	if err != nil {
		t.Fatalf("create for tenant a: %v", err)
	}

	timelineAsB, err := store.Timeline(ctxB, createdA.IncidentID, 0, "")
	if err != nil {
		t.Fatalf("timeline as tenant b: %v", err)
	}
	if len(timelineAsB) != 0 {
		t.Fatalf("tenant B's Timeline() call for tenant A's incident returned rows: %+v", timelineAsB)
	}

	var count int
	if err := scoped.QueryRow(ctxB,
		`SELECT count(*) FROM incident_event WHERE incident_id = $1`, createdA.IncidentID).Scan(&count); err != nil {
		t.Fatalf("direct query as tenant b: %v", err)
	}
	if count != 0 {
		t.Errorf("tenant B's own connection saw %d of tenant A's timeline row(s) directly", count)
	}

	timelineAsA, err := store.Timeline(ctxA, createdA.IncidentID, 0, "")
	if err != nil {
		t.Fatalf("timeline as tenant a: %v", err)
	}
	if len(timelineAsA) == 0 {
		t.Error("tenant A cannot see its own incident's timeline")
	}
}

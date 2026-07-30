//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

// OPS-S037's acceptance criterion, executed against the real runtime rather
// than argued from structure: a tenant holding a catch-all subscription
// receives zero administrative events, while the administrative record itself
// is written and readable.
//
// The subscription is deliberately the widest one the platform will accept. An
// empty Operations list matches every operation and an empty Resources list
// matches every resource — that is a published promise in the OpenAPI contract,
// not an accident, and it is precisely why ADR-AUDIT-007 §2.4 identifies a
// catch-all subscriber as the hazard. A test with a narrow subscription would
// prove nothing: the filter would do the work, and §6.5 requires the isolation
// to come from where the rows live.
func TestAdminAudit_CatchAllSubscriberReceivesNothing(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	// A real tenant with a real, enabled, catch-all webhook.
	tenants := NewTenantStore(pool)
	tenantID := ulid.Make().String()
	if _, err := tenants.Create(ctx, &domain.Tenant{
		TenantID: tenantID,
		Slug:     strings.ToLower("leak-" + ulid.Make().String()[:10]),
		Name:     "Leak Probe",
		Status:   domain.TenantActive,
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	tenantCtx := domain.WithActor(
		domain.WithTenant(context.Background(), &domain.Tenant{
			TenantID: tenantID, Slug: "leak", Name: "Leak Probe", Status: domain.TenantActive,
		}), "test-platform-admin")

	webhooks := NewWebhookStore(pool)
	if err := webhooks.Create(tenantCtx, events.Webhook{
		ID: "wh_leak_" + ulid.Make().String(), URL: "https://subscriber.invalid/hook",
		Secret: "s3cr3t", Enabled: true,
		Operations: nil, Resources: nil, // catch-all: matches every operation
		MaxRetries: 3,
	}); err != nil {
		t.Fatalf("create catch-all webhook: %v", err)
	}

	deliveriesBefore := countDeliveries(t, pool)
	chainsBefore := len(listChains(t, pool))

	// Perform genuine administrative acts through the real chokepoint: a user
	// creation on the platform chain, and an organisation creation which writes
	// two facts across two chains.
	users := NewUserStore(pool)
	u := newTestUser(t)
	if _, err := users.Create(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	orgs := NewOrganizationStore(pool)
	o, err := domain.NewOrganization("Leak Probe Org", strings.ToLower("leakorg-"+ulid.Make().String()[:10]))
	if err != nil {
		t.Fatalf("new org: %v", err)
	}
	if _, err := orgs.Create(ctx, o); err != nil {
		t.Fatalf("create org: %v", err)
	}

	// The administrative consumer still receives the events: the records exist.
	var adminRecords int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM admin_audit_event
		 WHERE subject_user_id = $1 OR subject_org_id = $2 OR subject_tenant_id = $3`, u.UserID, o.OrgID, o.TenantID,
	).Scan(&adminRecords); err != nil {
		t.Fatalf("count admin records: %v", err)
	}
	if adminRecords < 3 {
		t.Fatalf("administrative records written = %d, want at least 3 (user.created, "+
			"tenant.created, organization.created) — the acts must still be audited", adminRecords)
	}

	// Run the delivery path exactly as production does, against the same store
	// the composition root hands it.
	auditStore := NewAuditStore(pool)
	relay := events.NewRelay(auditStore, webhooks, webhooks, webhooks, nil, nil, events.RelayConfig{})
	relay.RunOnce(context.Background())

	// THE ASSERTION: the catch-all subscriber received nothing.
	if after := countDeliveries(t, pool); after != deliveriesBefore {
		t.Errorf("catch-all subscriber received %d delivery/deliveries after administrative acts; "+
			"administrative audit is not an event source (ADR-AUDIT-007 §6.5)",
			after-deliveriesBefore)
	}

	// And the reason is structural: the constitutional chain registry never saw
	// the administrative chains, so there was nothing for the relay to walk.
	if after := len(listChains(t, pool)); after != chainsBefore {
		t.Errorf("the constitutional chain registry grew by %d after purely administrative acts; "+
			"administrative chains must live in their own registry, invisible to discovery",
			after-chainsBefore)
	}

	// PROOF THAT FILTERING IS IRRELEVANT HERE, which is the whole point of
	// ADR-AUDIT-007 rejecting Option D: administrative events never reach the
	// matcher, so no subscription predicate — catch-all or narrow — is load
	// bearing. Disabling operation matching or ownership matching cannot leak an
	// administrative event, because none is ever offered to them.
	adminRows, err := pool.Query(ctx, `SELECT chain_id FROM admin_audit_chain_head`)
	if err != nil {
		t.Fatalf("list admin chains: %v", err)
	}
	adminChains := map[string]bool{}
	for adminRows.Next() {
		var id string
		if err := adminRows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		adminChains[id] = true
	}
	adminRows.Close()
	if len(adminChains) == 0 {
		t.Fatal("no administrative chain exists — the isolation proof would be vacuous")
	}
	for _, discovered := range listChains(t, pool) {
		if adminChains[discovered] {
			t.Errorf("chain %q is administrative yet discoverable by the relay; every subscription "+
				"predicate downstream is now the only thing standing between it and a subscriber, "+
				"which is the filter-based isolation ADR-AUDIT-007 §4 Option D rejects", discovered)
		}
	}

	// THE SECOND BARRIER, proven independently of the first. Even if discovery
	// were subverted — a bug, a refactor, or an operator handing the relay an
	// administrative chain id directly — the event source would still return
	// nothing, because administrative events are not in the table it reads.
	// Isolation here is two structural facts, not one, and neither is a filter.
	for chainID := range adminChains {
		evs, err := auditStore.ListEvents(ctx, chainID, 0, false, 100, "")
		if err != nil {
			t.Fatalf("list events for administrative chain %q: %v", chainID, err)
		}
		if len(evs) != 0 {
			t.Errorf("the constitutional event source returned %d event(s) for administrative "+
				"chain %q; administrative events must not be in the table the relay reads",
				len(evs), chainID)
		}
	}

	// Replay re-reads the same window and must enqueue nothing either.
	relay.RunOnce(context.Background())
	if after := countDeliveries(t, pool); after != deliveriesBefore {
		t.Errorf("a second delivery pass produced %d delivery/deliveries", after-deliveriesBefore)
	}
}

// countDeliveries returns the total number of queued webhook deliveries. A
// count rather than a diff of one subscriber's rows, so a delivery to ANY
// subscriber is caught.
func countDeliveries(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM webhook_delivery`).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	return n
}

// listChains returns the constitutional chain registry — the table the relay,
// the replay worker and the policy consumer all discover work from.
func listChains(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	ids, err := NewAuditStore(pool).ListChainIDs(context.Background())
	if err != nil {
		t.Fatalf("list chains: %v", err)
	}
	return ids
}

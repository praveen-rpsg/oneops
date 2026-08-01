//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/policy"
)

// tenantScopedPool returns a pool that assumes oneops_app and binds the tenant
// carried by the acquiring context, exactly as the request path does.
//
// Callers must have obtained a privileged pool via testPool first: that is what
// creates the schema and runs the migrations. This helper deliberately does not
// call testPool itself, because testPool truncates — doing so here would empty
// the tables between seeding a fixture and reading it back, which reads exactly
// like an isolation failure and is not one.
func tenantScopedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := NewTenantScopedPool(context.Background(), itestDSN(t), 4)
	if err != nil {
		t.Fatalf("tenant-scoped pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedForTenant writes one configuration object owned by tenantID using the
// privileged pool, so the fixture exists regardless of what the scoped pool can
// see.
func seedForTenant(ctx context.Context, t *testing.T, priv *pgxpool.Pool, tenantID, artifact string) string {
	t.Helper()
	id := ulid.Make().String()
	if _, err := priv.Exec(ctx, `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, tenant_id)
		VALUES ($1, $2, '1.0.0', 'reference', 'draft', 'working_material', $3)`,
		id, artifact, tenantID); err != nil {
		t.Fatalf("seed %s for %s: %v", artifact, tenantID, err)
	}
	return id
}

// This is the test the whole tenancy effort exists to make pass. A connection
// bound to tenant A must not retrieve a row belonging to tenant B — enforced by
// the database, not by a predicate the query happened to include.
func TestRLS_CrossTenantReadReturnsNothing(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-alpha", "ext-rls-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(ctx, newTenant("rls-bravo", "ext-rls-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	seedForTenant(ctx, t, priv, a.TenantID, "alpha-secret.md")
	seedForTenant(ctx, t, priv, b.TenantID, "bravo-secret.md")

	scoped := tenantScopedPool(t)

	// Bound to A: sees A's row, and only A's row.
	ctxA := domain.WithTenant(ctx, a)
	var count int
	if err := scoped.QueryRow(ctxA,
		`SELECT count(*) FROM configuration_object WHERE artifact = 'alpha-secret.md'`,
	).Scan(&count); err != nil {
		t.Fatalf("read own row: %v", err)
	}
	if count != 1 {
		t.Errorf("tenant A sees %d of its own rows, want 1", count)
	}

	if err := scoped.QueryRow(ctxA,
		`SELECT count(*) FROM configuration_object WHERE artifact = 'bravo-secret.md'`,
	).Scan(&count); err != nil {
		t.Fatalf("read foreign row: %v", err)
	}
	if count != 0 {
		t.Fatalf("tenant A retrieved %d of tenant B's rows — isolation is not enforced", count)
	}

	// And symmetrically, so the result is not an artefact of ordering.
	ctxB := domain.WithTenant(ctx, b)
	if err := scoped.QueryRow(ctxB,
		`SELECT count(*) FROM configuration_object WHERE artifact = 'alpha-secret.md'`,
	).Scan(&count); err != nil {
		t.Fatalf("read foreign row as B: %v", err)
	}
	if count != 0 {
		t.Fatalf("tenant B retrieved %d of tenant A's rows — isolation is not enforced", count)
	}
}

// An unqualified SELECT — the shape a forgotten predicate produces — must still
// be confined to the bound tenant.
func TestRLS_UnqualifiedSelectIsStillConfined(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-scan-a", "ext-rls-scan-a"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	seedForTenant(ctx, t, priv, a.TenantID, "scan-mine.md")
	seedForTenant(ctx, t, priv, domain.SystemTenantID, "scan-theirs.md")

	scoped := tenantScopedPool(t)

	rows, err := scoped.Query(domain.WithTenant(ctx, a),
		`SELECT tenant_id FROM configuration_object`)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if owner != a.TenantID {
			t.Errorf("unqualified scan returned a row owned by %q", owner)
		}
		seen++
	}
	if seen == 0 {
		t.Error("expected the bound tenant's own rows to be visible")
	}
}

// The policy is fail-closed: a connection that declares no tenant sees nothing.
// The alternative — treating an unset tenant as "see everything" — would turn
// any forgotten assignment into total cross-tenant disclosure.
func TestRLS_UnboundConnectionSeesNothing(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()
	seedForTenant(ctx, t, priv, domain.SystemTenantID, "unbound-probe.md")

	scoped := tenantScopedPool(t)

	// A context carrying no tenant resolves to the system tenant, so to test
	// the truly-unset case the setting is cleared on the connection directly.
	conn, err := scoped.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT set_config('app.tenant_id', '', false)"); err != nil {
		t.Fatalf("clear tenant: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM configuration_object`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("an unbound connection saw %d rows; the policy is not fail-closed", count)
	}
}

// WITH CHECK stops a tenant labelling a row with someone else's id. Without it
// a caller could write into another tenant's boundary even though it could not
// read from it.
func TestRLS_CannotWriteIntoAnotherTenant(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-write-a", "ext-rls-write-a"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	scoped := tenantScopedPool(t)

	_, err = scoped.Exec(domain.WithTenant(ctx, a), `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, tenant_id)
		VALUES ($1, 'smuggled.md', '1.0.0', 'reference', 'draft', 'working_material', $2)`,
		ulid.Make().String(), domain.SystemTenantID)
	if err == nil {
		t.Fatal("a tenant must not be able to insert a row owned by another tenant")
	}
}

// The privileged pool keeps its bypass: background workers process every tenant
// from one process and would otherwise silently stop — the integrity sweeper in
// particular would report healthy precisely because it could no longer see
// anything to check.
func TestRLS_PrivilegedPoolStillSeesEveryTenant(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-worker-a", "ext-rls-worker-a"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	seedForTenant(ctx, t, priv, a.TenantID, "worker-a.md")
	seedForTenant(ctx, t, priv, domain.SystemTenantID, "worker-sys.md")

	var count int
	if err := priv.QueryRow(ctx,
		`SELECT count(DISTINCT tenant_id) FROM configuration_object`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count < 2 {
		t.Fatalf("the worker pool sees %d tenants, want at least 2 — workers are blinded by RLS", count)
	}
}

// Isolation must hold on every table the ADR designates, not only the one the
// other tests happen to exercise. A table with RLS enabled but no policy denies
// everything; a table with neither is wide open. Both are caught here.
func TestRLS_EveryOwnedTableIsProtected(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	for _, table := range tenantOwnedTables {
		var enabled, forced bool
		if err := pool.QueryRow(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = $1 AND c.relname = $2`,
			itestSchema, table).Scan(&enabled, &forced); err != nil {
			t.Errorf("%s: %v", table, err)
			continue
		}
		if !enabled {
			t.Errorf("%s does not have row-level security enabled", table)
		}
		// Without FORCE the owning role bypasses the policy, and the
		// application connects as the owner — the policy would never run.
		if !forced {
			t.Errorf("%s does not FORCE row-level security; the owner would bypass it", table)
		}

		var policies int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_policies
			 WHERE schemaname = $1 AND tablename = $2 AND policyname = 'tenant_isolation'`,
			itestSchema, table).Scan(&policies); err != nil {
			t.Errorf("%s policies: %v", table, err)
			continue
		}
		if policies != 1 {
			t.Errorf("%s has %d tenant_isolation policies, want 1", table, policies)
		}
	}
}

// A tenant must be able to write through the repository, not merely read.
//
// The tenant_id column carries DEFAULT 'system' so that paths predating
// tenancy still produce valid rows. Under row-level security that default is
// actively wrong for every other tenant: the row is labelled `system` while the
// connection is bound to the caller, the policy's WITH CHECK rejects it, and
// the write fails with SQLSTATE 42501. Observed live before the repository was
// changed to write ownership explicitly.
func TestRLS_TenantCanWriteThroughRepository(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-repo-write", "ext-rls-repo-write"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	repo := NewConfigObjectRepo(tenantScopedPool(t))
	ctxA := domain.WithTenant(ctx, a)

	obj := sample("repo-write.md", "1.0.0")
	obj.Metadata = map[string]string{"owner": "acme"}
	created, err := repo.Create(ctxA, obj)
	if err != nil {
		t.Fatalf("a bound tenant must be able to create: %v", err)
	}

	// The row is owned by the writing tenant, not by the column default.
	var owner string
	if err := priv.QueryRow(ctx,
		`SELECT tenant_id FROM configuration_object WHERE cfg_id = $1`, created.CfgID).Scan(&owner); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if owner != a.TenantID {
		t.Errorf("row owned by %q, want %q", owner, a.TenantID)
	}

	// Metadata must be labelled too, or it becomes invisible to its own owner.
	var metaOwner string
	if err := priv.QueryRow(ctx,
		`SELECT tenant_id FROM configuration_metadata WHERE cfg_id = $1`, created.CfgID).Scan(&metaOwner); err != nil {
		t.Fatalf("read metadata owner: %v", err)
	}
	if metaOwner != a.TenantID {
		t.Errorf("metadata owned by %q, want %q", metaOwner, a.TenantID)
	}

	// And it reads back through the same scoped connection.
	got, err := repo.Get(ctxA, created.CfgID)
	if err != nil {
		t.Fatalf("read back own row: %v", err)
	}
	if got.Artifact != "repo-write.md" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

// Webhook subscriptions are tenant data, not platform data.
//
// The administration API and the delivery workers shared one store instance,
// so the API inherited the workers' RLS bypass. Verified against the running
// service: a second tenant could list every tenant's endpoint URLs, rotate
// their HMAC secrets — returned in the response, so forged signed deliveries
// became possible — and disable their delivery. Read, credential disclosure
// and denial of service across the tenant boundary from one wiring mistake.
func TestRLS_WebhooksAreTenantIsolated(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-wh-a", "ext-rls-wh-a"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	scoped := tenantScopedPool(t)
	sysStore := NewWebhookStore(scoped)
	otherStore := NewWebhookStore(scoped)

	sysCtx := domain.WithTenant(ctx, &domain.Tenant{
		TenantID: domain.SystemTenantID, Status: domain.TenantActive})
	if err := sysStore.Create(sysCtx, events.Webhook{
		ID: "wh_sys_secret", URL: "https://system-internal.example.com/hook",
		Secret: "system-hmac-key", Enabled: true, MaxRetries: 5,
	}); err != nil {
		t.Fatalf("system creates webhook: %v", err)
	}

	// The other tenant must not see it.
	aCtx := domain.WithTenant(ctx, a)
	list, err := otherStore.List(aCtx)
	if err != nil {
		t.Fatalf("list as other tenant: %v", err)
	}
	for _, w := range list {
		if w.ID == "wh_sys_secret" {
			t.Fatalf("tenant %s can see another tenant's webhook %q (url %q)",
				a.TenantID, w.ID, w.URL)
		}
	}

	// Nor fetch it directly, which is what the PATCH and DELETE handlers do
	// before mutating — the path that allowed secret rotation.
	if _, err := otherStore.Get(aCtx, "wh_sys_secret"); err == nil {
		t.Fatal("another tenant could fetch the webhook, so it could re-secret or disable it")
	}

	// The owner still sees its own.
	own, err := sysStore.List(sysCtx)
	if err != nil {
		t.Fatalf("owner list: %v", err)
	}
	found := false
	for _, w := range own {
		if w.ID == "wh_sys_secret" {
			found = true
		}
	}
	if !found {
		t.Error("the owning tenant must still see its own webhook")
	}
}

// Team is tenant data, not platform data, exactly like membership: a team
// name and its membership list describe one customer's organisational
// structure and must not be visible to another tenant's connection.
func TestRLS_TeamsAreTenantIsolated(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	orgs := NewOrganizationStore(priv)
	a, err := orgs.Create(ctx, mustNewOrg(t, "rls-team-a"))
	if err != nil {
		t.Fatalf("create org a: %v", err)
	}
	b, err := orgs.Create(ctx, mustNewOrg(t, "rls-team-b"))
	if err != nil {
		t.Fatalf("create org b: %v", err)
	}
	u := newTestUser(t)
	if _, err := NewUserStore(priv).Create(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewTeamStore(scoped)

	aCtx := domain.WithTenant(ctx, &domain.Tenant{TenantID: a.TenantID, Status: domain.TenantActive})
	teamA, err := domain.NewTeam(a.OrgID, a.TenantID, "Alpha Secret Team")
	if err != nil {
		t.Fatalf("build team a: %v", err)
	}
	created, err := store.Create(aCtx, teamA)
	if err != nil {
		t.Fatalf("tenant a creates team: %v", err)
	}
	m, err := domain.NewTeamMembership(created.TeamID, a.TenantID, u.UserID)
	if err != nil {
		t.Fatalf("build membership: %v", err)
	}
	if _, err := store.AddMember(aCtx, m); err != nil {
		t.Fatalf("tenant a adds member: %v", err)
	}

	// Tenant B must not see tenant A's team when listing its own organisation...
	bCtx := domain.WithTenant(ctx, &domain.Tenant{TenantID: b.TenantID, Status: domain.TenantActive})
	listAsB, err := store.ListByOrg(bCtx, a.OrgID, 50, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listAsB) != 0 {
		t.Fatalf("tenant b listed %d of tenant a's teams under a's org_id — isolation is not enforced", len(listAsB))
	}

	// ...nor fetch it directly by id...
	if _, err := store.Get(bCtx, created.TeamID); err == nil {
		t.Fatal("tenant b could fetch tenant a's team by id")
	}

	// ...nor read its membership.
	membersAsB, err := store.ListMembers(bCtx, created.TeamID, 50, "")
	if err != nil {
		t.Fatalf("list members as tenant b: %v", err)
	}
	if len(membersAsB) != 0 {
		t.Fatalf("tenant b read %d of tenant a's team members — isolation is not enforced", len(membersAsB))
	}

	// The owning tenant still sees its own team and member, so the refusals
	// above are about isolation and not about a path that never works.
	own, err := store.ListByOrg(aCtx, a.OrgID, 50, "")
	if err != nil {
		t.Fatalf("owner list: %v", err)
	}
	found := false
	for _, tm := range own {
		if tm.TeamID == created.TeamID {
			found = true
		}
	}
	if !found {
		t.Error("the owning tenant must still see its own team")
	}
}

// Notification is tenant data, not platform data, exactly like webhook and
// team: it can carry an operator recipient address and message content that
// must not leak across the tenant boundary.
func TestRLS_NotificationsAreTenantIsolated(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-notif-a", "ext-rls-notif-a"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(ctx, newTenant("rls-notif-b", "ext-rls-notif-b"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewNotificationStore(scoped)

	aCtx := domain.WithTenant(ctx, a)
	n, err := domain.NewNotification(a.TenantID, domain.NotificationInApp, "alice-secret", "s", "b")
	if err != nil {
		t.Fatalf("build notification: %v", err)
	}
	created, err := store.Enqueue(aCtx, n)
	if err != nil {
		t.Fatalf("tenant a enqueues: %v", err)
	}

	// Tenant B must not see it in a list...
	bCtx := domain.WithTenant(ctx, b)
	listAsB, err := store.List(bCtx, "", 50, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	for _, item := range listAsB {
		if item.NotificationID == created.NotificationID {
			t.Fatal("tenant b listed tenant a's notification — isolation is not enforced")
		}
	}

	// ...nor fetch it directly by id...
	if _, err := store.Get(bCtx, created.NotificationID); err == nil {
		t.Fatal("tenant b could fetch tenant a's notification by id")
	}

	// ...nor claim it for delivery through the tenant-scoped connection (the
	// worker itself uses the privileged pool precisely so it is NOT scoped this
	// way, but a scoped connection must still see nothing to claim).
	dueAsB, err := store.ClaimDue(bCtx, time.Now().UTC(), time.Minute, 10)
	if err != nil {
		t.Fatalf("claim as tenant b: %v", err)
	}
	for _, d := range dueAsB {
		if d.NotificationID == created.NotificationID {
			t.Fatal("tenant b's scoped connection claimed tenant a's notification")
		}
	}

	// The owning tenant still sees its own.
	own, err := store.List(aCtx, "", 50, "")
	if err != nil {
		t.Fatalf("owner list: %v", err)
	}
	found := false
	for _, item := range own {
		if item.NotificationID == created.NotificationID {
			found = true
		}
	}
	if !found {
		t.Error("the owning tenant must still see its own notification")
	}

	// The privileged worker pool sees both tenants' notifications, because
	// delivery is cross-tenant by design.
	dueAsWorker, err := NewNotificationStore(priv).ClaimDue(ctx, time.Now().UTC(), time.Minute, 100)
	if err != nil {
		t.Fatalf("claim as worker: %v", err)
	}
	foundAsWorker := false
	for _, d := range dueAsWorker {
		if d.NotificationID == created.NotificationID {
			foundAsWorker = true
		}
	}
	if !foundAsWorker {
		t.Error("the privileged delivery worker must be able to claim any tenant's due notification")
	}
}

// mustNewOrg builds a valid organisation for RLS fixtures with a collision-free
// slug.
func mustNewOrg(t *testing.T, prefix string) *domain.Organization {
	t.Helper()
	o, err := domain.NewOrganization(prefix, strings.ToLower(prefix+"-"+ulid.Make().String()[:10]))
	if err != nil {
		t.Fatalf("new organisation: %v", err)
	}
	return o
}

// Policies carry conditions and action configuration, which can embed
// endpoints and credentials. Same wiring, same exposure.
func TestRLS_PoliciesAreTenantIsolated(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-pol-a", "ext-rls-pol-a"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewPolicyStore(scoped)

	sysCtx := domain.WithTenant(ctx, &domain.Tenant{
		TenantID: domain.SystemTenantID, Status: domain.TenantActive})
	if err := store.Create(sysCtx, policy.Policy{
		ID: "pol_sys_secret", Name: "system-only", Enabled: true, MaxRetries: 3,
		Action: policy.ActionSpec{Type: "webhook",
			Config: json.RawMessage(`{"url":"https://system-internal.example.com/policy"}`)},
	}); err != nil {
		t.Fatalf("system creates policy: %v", err)
	}

	list, err := store.List(domain.WithTenant(ctx, a))
	if err != nil {
		t.Fatalf("list as other tenant: %v", err)
	}
	for _, p := range list {
		if p.ID == "pol_sys_secret" {
			t.Fatalf("tenant %s can see another tenant's policy %q", a.TenantID, p.ID)
		}
	}
}

// A real tenant must be able to create a replay job, and must not see another
// tenant's. The RLS rollout left webhook_replay_job.CreateJob without a
// tenant_id, so under the tenant-scoped pool the insert failed WITH CHECK and
// every non-system tenant got HTTP 500 on POST .../replay — replay was broken
// for real customers and, incidentally, unusable as an attack. Verified against
// the running service before the fix.
func TestRLS_ReplayJobsAreTenantIsolated(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rls-replay-a", "ext-rls-replay-a"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewWebhookStore(scoped)

	// Tenant A creates a replay job under its own scoped connection.
	aCtx := domain.WithTenant(ctx, a)
	job := events.ReplayJob{
		ID: "rpl_tenant_a", WebhookID: "wh_a",
		DeliveryIDs: []string{"d1"}, Status: events.JobPending,
	}
	if err := store.CreateJob(aCtx, job); err != nil {
		t.Fatalf("a real tenant must be able to create a replay job: %v", err)
	}

	// A second tenant must not see it.
	b, err := tenants.Create(ctx, newTenant("rls-replay-b", "ext-rls-replay-b"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}
	bCtx := domain.WithTenant(ctx, b)
	if _, found, err := store.GetJob(bCtx, "rpl_tenant_a"); err == nil && found {
		t.Fatal("another tenant could read the replay job")
	}

	// The privileged worker still sees it, because replay processing spans
	// tenants from one process.
	pend, err := NewWebhookStore(priv).ClaimPendingJobs(ctx, 100)
	if err != nil {
		t.Fatalf("claim pending: %v", err)
	}
	found := false
	for _, j := range pend {
		if j.ID == "rpl_tenant_a" {
			found = true
		}
	}
	if !found {
		t.Error("the privileged replay worker must still see the tenant's pending job")
	}
}

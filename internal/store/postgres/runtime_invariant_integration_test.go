//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/ops"
)

// Tenant isolation must not survive only as long as nobody has weakened it since
// boot.
//
// The schema validator (ADR-TENANCY-007) proves that row-level security is
// enabled AND forced, with a policy, on every tenant-owned table — and then the
// process serves traffic for days or weeks. The validator's own comment states
// the threat exactly: "A migration or an operator can weaken any of these, and
// nothing else at runtime would notice — a disabled RLS policy is a silent,
// total cross-tenant leak." The threat it names is a *runtime* event. The
// enforcement it provides is *boot-time only*.
//
// These tests perform that runtime event against the same tenant-scoped pool the
// request path uses, and measure what actually breaks.
//
// They mutate schema-level state and restore it, so they must not run in
// parallel with other tests, and every mutation is scoped to the current test
// schema (the test database hosts several).
func TestRuntimeInvariant_RLSDisabledAfterStartupIsDetectedContinuously(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("rt-alpha", "ext-rt-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(ctx, newTenant("rt-bravo", "ext-rt-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}
	seedForTenant(ctx, t, priv, a.TenantID, "rt-alpha-secret.md")
	seedForTenant(ctx, t, priv, b.TenantID, "rt-bravo-secret.md")

	scoped := tenantScopedPool(t)
	ctxA := domain.WithTenant(ctx, a)

	countBravo := func(what string) int {
		t.Helper()
		var n int
		if err := scoped.QueryRow(ctxA,
			`SELECT count(*) FROM configuration_object WHERE artifact = 'rt-bravo-secret.md'`,
		).Scan(&n); err != nil {
			t.Fatalf("%s: read: %v", what, err)
		}
		return n
	}

	// Control: with the invariant intact, A cannot see B's row. This is the
	// boundary the Trust Register records as constitutional.
	if n := countBravo("control"); n != 0 {
		t.Fatalf("precondition failed: isolation already broken before the attack (leaked=%d)", n)
	}
	t.Log("control (invariant intact): tenant A sees 0 of tenant B's rows")

	// The runtime event: one operator ALTER, long after startup. DISABLE removes
	// row-level security for every role, not just the owner — it is precisely
	// what the validator's `!enabled` check exists to catch, and it is applied
	// here at a moment when no validator will ever run again.
	if _, err := priv.Exec(ctx, `ALTER TABLE configuration_object DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatalf("weaken invariant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := priv.Exec(context.Background(),
			`ALTER TABLE configuration_object ENABLE ROW LEVEL SECURITY`); err != nil {
			t.Fatalf("RESTORE FAILED — the test schema is left with isolation disabled: %v", err)
		}
	})

	// The damage, recorded for the record: the database really does stop
	// isolating tenants. The platform cannot prevent this — an operator with DDL
	// rights can always disable RLS — which is precisely why it must *detect* it.
	leaked := countBravo("after weakening")
	t.Logf("after runtime weakening (RLS disabled): tenant A sees %d of tenant B's rows "+
		"— the database is no longer isolating tenants", leaked)
	if leaked == 0 {
		t.Skip("disabling RLS did not produce a cross-tenant read in this environment; " +
			"the detection assertion below would prove nothing")
	}

	// The property this investigation exists to establish: the platform detects
	// the breach for itself, continuously — not once at boot (ADR-SECURITY-002).
	sentinel := ops.NewSentinel("schema invariants",
		func(c context.Context) ([]string, error) { return NewSchemaValidator(priv).Validate(c) },
		20*time.Millisecond, nil, nil)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	go sentinel.Run(sctx)

	// Wait for a *named breach*, not merely "not healthy": an unverified sentinel
	// is also unhealthy, so polling Healthy() alone would pass even if detection
	// never worked.
	if !waitUntil(2*time.Second, func() bool { return len(sentinel.Breach()) > 0 }) {
		t.Fatalf("UNDETECTED BREACH: tenant A can read tenant B's rows, and the platform's "+
			"continuous verification reported no problem after %s — the boundary is gone and "+
			"nothing knows it", 2*time.Second)
	}
	breach := sentinel.Breach()
	t.Logf("sentinel detected the breach: %v", breach)

	if !strings.Contains(strings.Join(breach, "; "), "row-level security") {
		t.Errorf("the detected problem does not name the disabled boundary: %v", breach)
	}
	if err := sentinel.Err(); err == nil {
		t.Error("sentinel reports no error while tenant isolation is disabled — serving would continue")
	}

	// And it must clear once the boundary is repaired, so recovery needs no
	// redeploy.
	if _, err := priv.Exec(ctx, `ALTER TABLE configuration_object ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !waitUntil(2*time.Second, sentinel.Healthy) {
		t.Error("sentinel did not recover after the invariant was repaired — an operator's fix " +
			"would require a restart to take effect")
	}
	if n := countBravo("after repair"); n != 0 {
		t.Errorf("isolation did not return after repair: tenant A still sees %d of B's rows", n)
	}
	t.Log("sentinel recovered after repair; isolation restored")
}

// waitUntil polls cond until it holds or the limit elapses.
func waitUntil(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// The same question for the audit-immutability triggers, which ADR-TENANCY-003/004
// ownership authority rests on: the validator proves the triggers exist at boot,
// and a single DROP TRIGGER afterwards is invisible to the running process.
//
// This test establishes the *detection* half directly: it drops the guard, asks
// the validator whether it would have noticed, and restores. A non-empty problem
// list proves the validator is capable — which makes "it is only ever called
// once, at startup" the whole of the defect.
func TestRuntimeInvariant_AuditImmutabilityDropIsDetectableButUnwatched(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	problems, err := NewSchemaValidator(priv).Validate(ctx)
	if err != nil {
		t.Fatalf("baseline validate: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("precondition: schema already has %d problem(s): %v", len(problems), problems)
	}

	// Scope strictly to this test's schema: the test database hosts several, and
	// an unscoped DROP would weaken a schema this test does not own.
	var victim, victimDDL string
	err = priv.QueryRow(ctx, `
		SELECT t.tgname, pg_get_triggerdef(t.oid)
		  FROM pg_trigger t
		  JOIN pg_class c ON c.oid = t.tgrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = current_schema()
		   AND c.relname = 'audit_event'
		   AND NOT t.tgisinternal
		   AND t.tgname = 'trg_audit_event_no_row_mutate'`).Scan(&victim, &victimDDL)
	if err != nil {
		t.Skipf("no row-mutation guard on audit_event in %s: %v", "current schema", err)
	}

	if _, err := priv.Exec(ctx, `DROP TRIGGER `+victim+` ON audit_event`); err != nil {
		t.Fatalf("drop trigger %s: %v", victim, err)
	}

	problems, err = NewSchemaValidator(priv).Validate(ctx)

	// Restore before asserting anything, so no assertion path can leave the
	// schema weakened. The partition inherits the guard from the parent.
	if _, rerr := priv.Exec(context.Background(), victimDDL); rerr != nil {
		t.Fatalf("RESTORE FAILED for %s — repair before trusting this database: %v", victim, rerr)
	}
	// pg_get_triggerdef() does not emit the ENABLE ALWAYS firing mode, so the
	// restore above recreates the guard in origin mode. audit_event now requires
	// ENABLE ALWAYS (ADR-AUDIT-008); without this re-arm the schema is left
	// downgraded and every later test that first validates clean fails. The
	// parent ALTER recurses to the partition.
	if _, rerr := priv.Exec(context.Background(),
		`ALTER TABLE audit_event ENABLE ALWAYS TRIGGER `+victim); rerr != nil {
		t.Fatalf("RE-ARM FAILED for %s — repair before trusting this database: %v", victim, rerr)
	}
	if err != nil {
		t.Fatalf("post-drop validate: %v", err)
	}
	t.Logf("re-validation after DROP TRIGGER %s reports %d problem(s): %v", victim, len(problems), problems)

	if len(problems) == 0 {
		t.Errorf("the validator did not detect a dropped audit-immutability trigger (%s) — "+
			"audit-derived ownership authority (ADR-TENANCY-003/004) would rest on a log that "+
			"is no longer append-only", victim)
	}
}

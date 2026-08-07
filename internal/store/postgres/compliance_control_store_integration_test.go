//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// complianceControlTestCtx carries both the tenant binding
// ComplianceControlStore's RLS-scoped pool requires and the actor identity
// AddEvidence requires to attribute a control_evidence row — mirrors
// incidentTestCtx.
func complianceControlTestCtx(tn *domain.Tenant) context.Context {
	return domain.WithActor(domain.WithTenant(context.Background(), tn), "test-compliance-actor")
}

// TestComplianceControlStore_CreateGetList proves the basic CRUD path: a
// freshly created control is not_implemented, server-minted, and appears in
// the caller's own List.
func TestComplianceControlStore_CreateGetList(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-crud")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	c, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "Logical access controls", "restrict prod access")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	created, err := store.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ControlID == "" {
		t.Fatal("control_id must be server-minted")
	}
	if created.Status != domain.ComplianceControlNotImplemented {
		t.Errorf("Status = %q, want not_implemented", created.Status)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}

	got, err := store.Get(ctx, created.ControlID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Logical access controls" || got.Framework != "SOC2" || got.ControlRef != "CC6.1" {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := store.List(ctx, 0, "", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ControlID != created.ControlID {
		t.Errorf("list = %+v, want exactly the created control", list)
	}
}

// TestComplianceControlStore_CreateRejectsDuplicateFrameworkControlRef
// proves UNIQUE(tenant_id, framework, control_ref) is enforced.
func TestComplianceControlStore_CreateRejectsDuplicateFrameworkControlRef(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-dup")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	first, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "Logical access controls", "")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	if _, err := store.Create(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}

	second, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "Duplicate control", "")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	if _, err := store.Create(ctx, second); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("create duplicate (framework, control_ref) err = %v, want ErrConflict", err)
	}

	// A DIFFERENT framework with the same control_ref is unrelated and must
	// succeed.
	third, err := domain.NewComplianceControl(tn.TenantID, "ISO27001", "CC6.1", "Different framework", "")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	if _, err := store.Create(ctx, third); err != nil {
		t.Errorf("create with a different framework must succeed: %v", err)
	}
}

// TestComplianceControlStore_UpdateFields proves an ordinary field edit
// under optimistic locking; framework/control_ref are immutable (no patch
// field exists for either).
func TestComplianceControlStore_UpdateFields(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-update")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	c, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "Logical access controls", "v1 description")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	created, err := store.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newTitle := "Logical access controls (revised)"
	newDescription := "v2 description"
	updated, err := store.Update(ctx, created.ControlID, 1, domain.ComplianceControlPatch{
		Title: &newTitle, Description: &newDescription,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != newTitle || updated.Description != newDescription {
		t.Errorf("updated = %+v, want title/description changed", updated)
	}
	if updated.Framework != "SOC2" || updated.ControlRef != "CC6.1" {
		t.Errorf("framework/control_ref must be immutable: %+v", updated)
	}
	if updated.RowVersion != 2 {
		t.Errorf("row_version = %d, want 2", updated.RowVersion)
	}

	// Stale row_version is refused.
	if _, err := store.Update(ctx, created.ControlID, 1, domain.ComplianceControlPatch{Title: &newTitle}); err != domain.ErrVersionMismatch {
		t.Errorf("stale row_version err = %v, want ErrVersionMismatch", err)
	}

	// Unknown control_id.
	if _, err := store.Update(ctx, "no-such-control", 1, domain.ComplianceControlPatch{Title: &newTitle}); err != domain.ErrNotFound {
		t.Errorf("unknown control_id err = %v, want ErrNotFound", err)
	}
}

// TestComplianceControlStore_UpdateRevalidatesTheWholeEntity proves Title's
// non-blank rule is enforced even though it is applied via a
// merge-then-Validate shape.
func TestComplianceControlStore_UpdateRevalidatesTheWholeEntity(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-revalidate")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	c, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "x", "")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	created, err := store.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	blank := "   "
	if _, err := store.Update(ctx, created.ControlID, 1, domain.ComplianceControlPatch{Title: &blank}); err == nil {
		t.Fatal("update with a blank title must fail")
	} else {
		ve, ok := domain.AsValidation(err)
		if !ok || ve.Field != "title" {
			t.Errorf("err = %v, want a title ValidationError", err)
		}
	}
}

// TestComplianceControlStore_SetStatusTransitions proves the legal edges
// write through the store and illegal edges are refused with the transition
// error, and that a stale row_version is refused with ErrVersionMismatch.
func TestComplianceControlStore_SetStatusTransitions(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-transitions")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	c, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "x", "")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	created, err := store.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created.ControlID

	// Illegal: not_implemented -> not_implemented is a self-transition, refused.
	if _, err := store.SetStatus(ctx, id, 1, domain.ComplianceControlNotImplemented); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("not_implemented->not_implemented err = %v, want ErrInvalidTransition", err)
	}

	// Legal: not_implemented -> in_progress.
	updated, err := store.SetStatus(ctx, id, 1, domain.ComplianceControlInProgress)
	if err != nil {
		t.Fatalf("not_implemented->in_progress: %v", err)
	}
	if updated.RowVersion != 2 {
		t.Errorf("row_version = %d, want 2", updated.RowVersion)
	}

	// Legal: in_progress -> implemented.
	if _, err := store.SetStatus(ctx, id, 2, domain.ComplianceControlImplemented); err != nil {
		t.Fatalf("in_progress->implemented: %v", err)
	}

	// Illegal: implemented -> not_applicable directly (must step back through in_progress).
	if _, err := store.SetStatus(ctx, id, 3, domain.ComplianceControlNotApplicable); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("implemented->not_applicable err = %v, want ErrInvalidTransition", err)
	}

	// Legal: implemented -> in_progress -> not_applicable -> not_implemented.
	if _, err := store.SetStatus(ctx, id, 3, domain.ComplianceControlInProgress); err != nil {
		t.Fatalf("implemented->in_progress: %v", err)
	}
	if _, err := store.SetStatus(ctx, id, 4, domain.ComplianceControlNotApplicable); err != nil {
		t.Fatalf("in_progress->not_applicable: %v", err)
	}
	final, err := store.SetStatus(ctx, id, 5, domain.ComplianceControlNotImplemented)
	if err != nil {
		t.Fatalf("not_applicable->not_implemented: %v", err)
	}
	if final.Status != domain.ComplianceControlNotImplemented {
		t.Errorf("Status = %q, want not_implemented", final.Status)
	}

	// Stale row_version.
	if _, err := store.SetStatus(ctx, id, 2, domain.ComplianceControlInProgress); err != domain.ErrVersionMismatch {
		t.Errorf("stale row_version err = %v, want ErrVersionMismatch", err)
	}

	// Not found.
	if _, err := store.SetStatus(ctx, "no-such-control", 1, domain.ComplianceControlInProgress); err != domain.ErrNotFound {
		t.Errorf("unknown control_id err = %v, want ErrNotFound", err)
	}
}

// TestComplianceControlStore_ListFilters proves the framework/status filters
// each narrow the page independently.
func TestComplianceControlStore_ListFilters(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-listfilter")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	mk := func(framework, ref, title string) *domain.ComplianceControl {
		c, err := domain.NewComplianceControl(tn.TenantID, framework, ref, title, "")
		if err != nil {
			t.Fatalf("new compliance control: %v", err)
		}
		created, err := store.Create(ctx, c)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return created
	}

	soc2Control := mk("SOC2", "CC6.1", "soc2 control")
	mk("SOC2", "CC6.2", "another soc2 control")
	mk("ISO27001", "A.9.2.3", "iso control")

	if _, err := store.SetStatus(ctx, soc2Control.ControlID, 1, domain.ComplianceControlInProgress); err != nil {
		t.Fatalf("set status: %v", err)
	}

	byFramework, err := store.List(ctx, 0, "", "SOC2", "")
	if err != nil {
		t.Fatalf("list by framework: %v", err)
	}
	if len(byFramework) != 2 {
		t.Errorf("list by framework=SOC2 = %d, want 2", len(byFramework))
	}

	byStatus, err := store.List(ctx, 0, "", "", domain.ComplianceControlInProgress)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].ControlID != soc2Control.ControlID {
		t.Errorf("list by status=in_progress = %+v, want exactly the in_progress control", byStatus)
	}
}

// TestComplianceControlStore_AddEvidenceAndList proves evidence appends
// correctly, in kind/value/actor, and is returned oldest-first.
func TestComplianceControlStore_AddEvidenceAndList(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-evidence")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	c, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "x", "")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	created, err := store.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	first, err := store.AddEvidence(ctx, created.ControlID, domain.ControlEvidenceKindURL, "https://example.com/proof.pdf")
	if err != nil {
		t.Fatalf("add evidence (url): %v", err)
	}
	if first.EvidenceID == "" {
		t.Error("evidence_id must be server-minted")
	}
	if first.RecordedBy != "test-compliance-actor" {
		t.Errorf("RecordedBy = %q, want the context actor", first.RecordedBy)
	}
	if first.ControlID != created.ControlID {
		t.Errorf("ControlID = %q, want %q", first.ControlID, created.ControlID)
	}

	second, err := store.AddEvidence(ctx, created.ControlID, domain.ControlEvidenceKindAttestation, "I confirm quarterly review")
	if err != nil {
		t.Fatalf("add evidence (attestation): %v", err)
	}

	trail, err := store.Evidence(ctx, created.ControlID, 0)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(trail) != 2 {
		t.Fatalf("evidence trail = %d rows, want 2", len(trail))
	}
	if trail[0].EvidenceID != first.EvidenceID || trail[1].EvidenceID != second.EvidenceID {
		t.Errorf("evidence trail order = %+v, want oldest first", trail)
	}
	if trail[0].Kind != domain.ControlEvidenceKindURL || trail[1].Kind != domain.ControlEvidenceKindAttestation {
		t.Errorf("evidence kinds = %q/%q, want url/attestation", trail[0].Kind, trail[1].Kind)
	}

	// Evidence for an unknown control is empty, not an error.
	empty, err := store.Evidence(ctx, "no-such-control", 0)
	if err != nil {
		t.Fatalf("evidence for unknown control: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("evidence for unknown control = %+v, want empty", empty)
	}
}

// TestComplianceControlStore_AddEvidenceRejectsUnknownControl proves
// AddEvidence returns ErrNotFound for a control_id that does not exist,
// exactly as it does for a cross-tenant one — see the isolation test below
// for the cross-tenant case specifically.
func TestComplianceControlStore_AddEvidenceRejectsUnknownControl(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-evidence-404")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	if _, err := store.AddEvidence(ctx, "no-such-control", domain.ControlEvidenceKindNote, "x"); err != domain.ErrNotFound {
		t.Errorf("add evidence to unknown control err = %v, want ErrNotFound", err)
	}
}

// TestComplianceControlStore_AddEvidenceRejectsInvalidKindOrValue proves
// domain validation runs before the row is written.
func TestComplianceControlStore_AddEvidenceRejectsInvalidKindOrValue(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "compctl-evidence-invalid")

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctx := complianceControlTestCtx(tn)

	c, err := domain.NewComplianceControl(tn.TenantID, "SOC2", "CC6.1", "x", "")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	created, err := store.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := store.AddEvidence(ctx, created.ControlID, "bogus", "x"); err == nil {
		t.Fatal("add evidence with an unknown kind must fail")
	} else if ve, ok := domain.AsValidation(err); !ok || ve.Field != "kind" {
		t.Errorf("err = %v, want a kind ValidationError", err)
	}

	if _, err := store.AddEvidence(ctx, created.ControlID, domain.ControlEvidenceKindNote, "   "); err == nil {
		t.Fatal("add evidence with a blank value must fail")
	} else if ve, ok := domain.AsValidation(err); !ok || ve.Field != "value" {
		t.Errorf("err = %v, want a value ValidationError", err)
	}
}

// TestComplianceControlIsolation_RLSByTenant is the security gate for this
// story: tenant B can never see, list, patch or transition tenant A's
// controls, cannot append evidence to a control it does not own, and the
// same (framework, control_ref) pair is independent per tenant — mirrors
// TestRiskIsolation_RLSByTenant.
func TestComplianceControlIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("compctl-iso-alpha", "ext-compctl-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("compctl-iso-bravo", "ext-compctl-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewComplianceControlStore(scoped)
	ctxA := complianceControlTestCtx(a)
	ctxB := complianceControlTestCtx(b)

	cA, err := domain.NewComplianceControl(a.TenantID, "SOC2", "CC6.1", "alpha's control", "")
	if err != nil {
		t.Fatalf("new compliance control a: %v", err)
	}
	createdA, err := store.Create(ctxA, cA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	// SAME (framework, control_ref) as tenant A's — must succeed for tenant
	// B: the uniqueness constraint is tenant-scoped.
	cB, err := domain.NewComplianceControl(b.TenantID, "SOC2", "CC6.1", "bravo's control", "")
	if err != nil {
		t.Fatalf("new compliance control b: %v", err)
	}
	createdB, err := store.Create(ctxB, cB)
	if err != nil {
		t.Fatalf("create as tenant b with the same (framework, control_ref): %v", err)
	}

	// READ: tenant B cannot get tenant A's control by id, and does not see
	// it in its own list.
	if _, err := store.Get(ctxB, createdA.ControlID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's control: err = %v, want ErrNotFound", err)
	}
	listB, err := store.List(ctxB, 0, "", "", "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 1 || listB[0].ControlID != createdB.ControlID {
		t.Errorf("tenant B's list = %+v, want exactly its own control", listB)
	}

	// WRITE: tenant B cannot patch or transition tenant A's control, even
	// with the correct row_version.
	newTitle := "hijacked"
	if _, err := store.Update(ctxB, createdA.ControlID, 1, domain.ComplianceControlPatch{Title: &newTitle}); err != domain.ErrNotFound {
		t.Errorf("tenant B updated tenant A's control: err = %v, want ErrNotFound", err)
	}
	if _, err := store.SetStatus(ctxB, createdA.ControlID, 1, domain.ComplianceControlInProgress); err != domain.ErrNotFound {
		t.Errorf("tenant B transitioned tenant A's control: err = %v, want ErrNotFound", err)
	}

	// EVIDENCE: tenant B cannot append evidence to tenant A's control.
	if _, err := store.AddEvidence(ctxB, createdA.ControlID, domain.ControlEvidenceKindNote, "hijacked evidence"); err != domain.ErrNotFound {
		t.Errorf("tenant B appended evidence to tenant A's control: err = %v, want ErrNotFound", err)
	}

	// Tenant A files its own evidence, undisturbed by tenant B's attempt.
	evA, err := store.AddEvidence(ctxA, createdA.ControlID, domain.ControlEvidenceKindNote, "alpha's own evidence")
	if err != nil {
		t.Fatalf("tenant a add evidence: %v", err)
	}

	// EVIDENCE LEAKAGE: tenant B's read of tenant A's evidence trail (by
	// tenant A's own control_id, which tenant B does not have any legitimate
	// way to name, but the property must hold regardless) returns nothing —
	// row-level security, not a knowledge-of-the-id check, is what protects
	// this.
	trailAsB, err := store.Evidence(ctxB, createdA.ControlID, 0)
	if err != nil {
		t.Fatalf("tenant b read tenant a's evidence: %v", err)
	}
	if len(trailAsB) != 0 {
		t.Errorf("tenant B saw %d of tenant A's evidence row(s): %+v", len(trailAsB), trailAsB)
	}

	// Direct query on tenant B's own tenant-scoped connection: the property
	// that actually matters is RLS, not a predicate the store happened to
	// add.
	var count int
	if err := scoped.QueryRow(ctxB,
		`SELECT count(*) FROM control_evidence WHERE evidence_id = $1`, evA.EvidenceID).Scan(&count); err != nil {
		t.Fatalf("direct query as tenant b: %v", err)
	}
	if count != 0 {
		t.Errorf("tenant B's own connection saw %d of tenant A's evidence row(s) directly", count)
	}

	// Tenant A still sees its own, undisturbed control, at its original
	// row_version, and its own evidence trail.
	stillThere, err := store.Get(ctxA, createdA.ControlID)
	if err != nil {
		t.Fatalf("tenant A lost its own control: %v", err)
	}
	if stillThere.RowVersion != 1 || stillThere.Title != "alpha's control" {
		t.Errorf("tenant A's control was mutated by tenant B's attempt: %+v", stillThere)
	}
	trailA, err := store.Evidence(ctxA, createdA.ControlID, 0)
	if err != nil {
		t.Fatalf("tenant a evidence: %v", err)
	}
	if len(trailA) != 1 || trailA[0].EvidenceID != evA.EvidenceID {
		t.Errorf("tenant A's evidence trail = %+v, want exactly its own evidence", trailA)
	}
}

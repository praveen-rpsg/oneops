package governance

import (
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// §8 Deletion: "Never permitted for Constitution/Governance/Validation/Evidence/
// Audit or anything with dependents." The role prohibition is ABSOLUTE — it must
// hold for every retention class, including working_material, which is the case
// that was deletable before this guard.
func TestDeletionForbiddenForProtectedRoles(t *testing.T) {
	protected := []domain.Role{
		domain.RoleConstitution, domain.RoleGovernance,
		domain.RoleValidation, domain.RoleEvidence, domain.RoleAudit,
	}
	retentions := []domain.RetentionClass{
		domain.RetentionWorkingMaterial, // the previously-exploitable combination
		domain.RetentionCurrentBaseline,
		domain.RetentionHistoricalRecord,
		domain.RetentionAuditRecord,
	}
	for _, role := range protected {
		for _, rc := range retentions {
			o := obj(domain.LifecycleDraft, rc, domain.AuthorityNonNormative)
			o.Role = role
			if _, err := planTransition(domain.OpDeletion, o, Command{}); err == nil {
				t.Errorf("role %s + retention %s: deletion permitted, want refusal", role, rc)
			}
		}
	}
}

// Permitted roles keep their existing behaviour exactly: working_material is
// deletable, everything else is not.
func TestDeletionUnchangedForPermittedRoles(t *testing.T) {
	permitted := []domain.Role{
		domain.RoleEngSpec, domain.RolePlanning, domain.RoleReference, domain.RoleWorking,
	}
	for _, role := range permitted {
		o := obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)
		o.Role = role
		p, err := planTransition(domain.OpDeletion, o, Command{})
		if err != nil || !p.Remove {
			t.Errorf("role %s working_material: got err=%v remove=%v, want deletable", role, err, p.Remove)
		}

		// Retention precondition unchanged.
		o2 := obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive)
		o2.Role = role
		if _, err := planTransition(domain.OpDeletion, o2, Command{}); err == nil {
			t.Errorf("role %s current_baseline: deletion permitted, want the retention refusal", role)
		}
	}
}

// The role set is the single source of truth shared with the persistence layer.
func TestProtectedDeletionRolesSet(t *testing.T) {
	got := domain.ProtectedDeletionRoles()
	if len(got) != 5 {
		t.Fatalf("ProtectedDeletionRoles() = %v, want 5 roles", got)
	}
	for _, r := range []domain.Role{domain.RoleReference, domain.RoleWorking, domain.RolePlanning, domain.RoleEngSpec} {
		if r.ProtectedFromDeletion() {
			t.Errorf("role %s must not be protected", r)
		}
	}
}

// Package domain holds the Configuration Registry domain model: entities,
// value objects, business rules, and repository contracts. It has no
// dependency on transport, storage, or framework code.
package domain

// Role is the Configuration Role dimension (Configuration State Model §5).
type Role string

const (
	RoleConstitution Role = "constitution"
	RoleGovernance   Role = "governance"
	RoleEngSpec      Role = "eng_spec"
	RoleValidation   Role = "validation"
	RoleEvidence     Role = "evidence"
	RoleAudit        Role = "audit"
	RolePlanning     Role = "planning"
	RoleReference    Role = "reference"
	RoleWorking      Role = "working"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleConstitution, RoleGovernance, RoleEngSpec, RoleValidation,
		RoleEvidence, RoleAudit, RolePlanning, RoleReference, RoleWorking:
		return true
	default:
		return false
	}
}

// Lifecycle is the Lifecycle dimension (Configuration State Model §3).
type Lifecycle string

const (
	LifecycleDraft      Lifecycle = "draft"
	LifecycleInReview   Lifecycle = "in_review"
	LifecycleRatified   Lifecycle = "ratified"
	LifecycleApproved   Lifecycle = "approved"
	LifecycleInProgress Lifecycle = "in_progress"
	LifecycleComplete   Lifecycle = "complete"
	LifecycleSuspended  Lifecycle = "suspended"
	LifecycleDeprecated Lifecycle = "deprecated"
	LifecycleWithdrawn  Lifecycle = "withdrawn"
)

// Valid reports whether l is a known lifecycle state.
func (l Lifecycle) Valid() bool {
	switch l {
	case LifecycleDraft, LifecycleInReview, LifecycleRatified, LifecycleApproved,
		LifecycleInProgress, LifecycleComplete, LifecycleSuspended,
		LifecycleDeprecated, LifecycleWithdrawn:
		return true
	default:
		return false
	}
}

// Authority is the Constitutional Authority dimension. In M1 it is a stored,
// validated value; the transitive computation from the dependency graph is
// implemented in M3 (Authority Resolver), which maintains this field.
type Authority string

const (
	AuthorityActive       Authority = "active"
	AuthorityHistorical   Authority = "historical"
	AuthorityNonNormative Authority = "non_normative"
)

// Valid reports whether a is a known authority value.
func (a Authority) Valid() bool {
	switch a {
	case AuthorityActive, AuthorityHistorical, AuthorityNonNormative:
		return true
	default:
		return false
	}
}

// RetentionClass is the Retention dimension (Configuration State Model §4).
type RetentionClass string

const (
	RetentionCurrentBaseline    RetentionClass = "current_baseline"
	RetentionCurrentPlanning    RetentionClass = "current_planning"
	RetentionHistoricalRecord   RetentionClass = "historical_record"
	RetentionSupersededPlan     RetentionClass = "superseded_planning"
	RetentionHistoricalEvidence RetentionClass = "historical_evidence"
	RetentionAuditRecord        RetentionClass = "audit_record"
	RetentionWorkingMaterial    RetentionClass = "working_material"
)

// Valid reports whether rc is a known retention class.
func (rc RetentionClass) Valid() bool {
	switch rc {
	case RetentionCurrentBaseline, RetentionCurrentPlanning, RetentionHistoricalRecord,
		RetentionSupersededPlan, RetentionHistoricalEvidence, RetentionAuditRecord,
		RetentionWorkingMaterial:
		return true
	default:
		return false
	}
}

// Format is the artifact body format.
type Format string

const (
	FormatMarkdown Format = "md"
	FormatMDX      Format = "mdx"
	FormatJSON     Format = "json"
)

// Valid reports whether f is a known format.
func (f Format) Valid() bool {
	switch f {
	case FormatMarkdown, FormatMDX, FormatJSON:
		return true
	default:
		return false
	}
}

// protectedDeletionRoles are the Configuration Roles that Configuration State
// Model §8 (Deletion) never permits to be deleted: "Never permitted for
// Constitution/Governance/Validation/Evidence/Audit or anything with
// dependents."
//
// The prohibition is on the ROLE alone. It is independent of Retention,
// Lifecycle and Authority, so an object of one of these roles is undeletable
// even while it is Working Material with no dependents.
//
// Role is immutable — no API, patch or SQL statement writes the role column
// after creation — so this property cannot be evaded by mutating the object.
var protectedDeletionRoles = []Role{
	RoleConstitution, RoleGovernance, RoleValidation, RoleEvidence, RoleAudit,
}

// ProtectedFromDeletion reports whether §8 forbids deleting an object of this
// role outright, regardless of its other dimensions.
func (r Role) ProtectedFromDeletion() bool {
	for _, p := range protectedDeletionRoles {
		if r == p {
			return true
		}
	}
	return false
}

// ProtectedDeletionRoles returns the §8-protected roles as strings, for
// persistence adapters that must express the prohibition in a query. It is the
// single source of truth for the set.
func ProtectedDeletionRoles() []string {
	out := make([]string, 0, len(protectedDeletionRoles))
	for _, p := range protectedDeletionRoles {
		out = append(out, string(p))
	}
	return out
}

// constitutionalMetadataKeys are the metadata keys that are NOT descriptive:
// they are read by the §9.1 evaluators and therefore determine computed
// Authority and the four-part Replacement Test.
//
//	responsibilities → owns(new, allResponsibilities(old))
//	citations        → noActiveArtifactCites(old)
//	coverage         → noGapIfRemoved(old)
//
// They are constitutional inputs carried in the metadata map, so a surface that
// may write arbitrary metadata must exclude them explicitly.
var constitutionalMetadataKeys = []string{"responsibilities", "citations", "coverage"}

// IsConstitutionalMetadataKey reports whether a metadata key is a §9.1 input
// rather than descriptive data.
func IsConstitutionalMetadataKey(key string) bool {
	for _, k := range constitutionalMetadataKeys {
		if key == k {
			return true
		}
	}
	return false
}

// ConstitutionalMetadataKeys returns the §9.1 input keys. It is the single
// source of truth for the set.
func ConstitutionalMetadataKeys() []string {
	out := make([]string, len(constitutionalMetadataKeys))
	copy(out, constitutionalMetadataKeys)
	return out
}

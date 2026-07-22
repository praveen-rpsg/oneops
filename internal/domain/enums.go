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

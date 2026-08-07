package domain

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// ComplianceControlStatus is the governed implementation lifecycle of a
// ComplianceControl (E8.4b) — mirrors RiskStatus/VulnFindingStatus's
// state-machine treatment exactly, applied to "how far along is this control"
// rather than "how far along is this finding/risk".
type ComplianceControlStatus string

const (
	// ComplianceControlNotImplemented is the status every control is minted
	// in: named/tracked, but no implementation work has started.
	ComplianceControlNotImplemented ComplianceControlStatus = "not_implemented"
	// ComplianceControlInProgress: implementation work is under way.
	ComplianceControlInProgress ComplianceControlStatus = "in_progress"
	// ComplianceControlImplemented: the operator considers the control in
	// place. Not terminal — see complianceControlTransitions: a later drift
	// or re-review can move it back to in_progress or not_implemented.
	ComplianceControlImplemented ComplianceControlStatus = "implemented"
	// ComplianceControlNotApplicable: the operator has judged this control
	// does not apply to their environment (e.g. a cloud-only control against
	// an on-prem-only estate). Reachable back to not_implemented only — an
	// operator who decides a control DOES apply after all starts the
	// implementation lifecycle over, the same "no shortcut back into active
	// work" rule RiskStatus.riskTransitions applies to nothing (risk has no
	// not_applicable-shaped state); here it exists because a framework's own
	// control list is fixed but not every control fits every environment.
	ComplianceControlNotApplicable ComplianceControlStatus = "not_applicable"
)

// Valid reports whether s is a defined status.
func (s ComplianceControlStatus) Valid() bool {
	switch s {
	case ComplianceControlNotImplemented, ComplianceControlInProgress,
		ComplianceControlImplemented, ComplianceControlNotApplicable:
		return true
	default:
		return false
	}
}

// complianceControlTransitions is the whole lifecycle, as data rather than a
// chain of conditionals — mirrors riskTransitions/vulnFindingTransitions. A
// transition absent here, including every self-transition, is refused.
//
//	not_implemented -> {in_progress, not_applicable}
//	in_progress     -> {implemented, not_applicable, not_implemented}
//	implemented     -> {in_progress, not_implemented}
//	not_applicable  -> {not_implemented}
//
// implemented/in_progress can fall back to not_implemented (a control found
// to have drifted, or a re-review that concludes the work was never actually
// done) but NEVER directly to not_applicable from implemented — an operator
// who once implemented a control cannot silently mark it inapplicable
// without first stepping back through in_progress, the same "no shortcut"
// discipline RiskStatus draws between mitigating and accepted.
var complianceControlTransitions = map[ComplianceControlStatus][]ComplianceControlStatus{
	ComplianceControlNotImplemented: {ComplianceControlInProgress, ComplianceControlNotApplicable},
	ComplianceControlInProgress: {
		ComplianceControlImplemented, ComplianceControlNotApplicable, ComplianceControlNotImplemented,
	},
	ComplianceControlImplemented:   {ComplianceControlInProgress, ComplianceControlNotImplemented},
	ComplianceControlNotApplicable: {ComplianceControlNotImplemented},
}

// CanTransitionTo reports whether from -> to is a permitted lifecycle move.
func (s ComplianceControlStatus) CanTransitionTo(to ComplianceControlStatus) bool {
	for _, allowed := range complianceControlTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ComplianceControlTransitionError is a refused lifecycle move — mirrors
// RiskTransitionError/VulnFindingTransitionError exactly, scoped to
// ComplianceControlStatus.
type ComplianceControlTransitionError struct {
	From ComplianceControlStatus
	To   ComplianceControlStatus
}

func (e *ComplianceControlTransitionError) Error() string {
	return "invalid lifecycle transition: " + string(e.From) + " -> " + string(e.To)
}

// Unwrap makes errors.Is(err, ErrInvalidTransition) true for this type.
func (e *ComplianceControlTransitionError) Unwrap() error { return ErrInvalidTransition }

// NewComplianceControlTransitionError builds a *ComplianceControlTransitionError naming both ends.
func NewComplianceControlTransitionError(from, to ComplianceControlStatus) *ComplianceControlTransitionError {
	return &ComplianceControlTransitionError{From: from, To: to}
}

// MaxComplianceControlFrameworkLength bounds Framework.
const MaxComplianceControlFrameworkLength = 100

// complianceControlFrameworkPattern is the format Framework must satisfy.
// Unlike Risk.Category (an organisation's OWN lower-snake-case taxonomy word,
// e.g. "operational"), a framework name is an external standard's OWN label
// (SOC2, ISO27001, PCI-DSS, HIPAA, NIST-800-53) — operators write it in
// whatever case the standard itself uses, so this pattern is case-preserving,
// unlike riskCategoryPattern/assetTypePattern. It is still bounded to a safe,
// index/filter/console-label charset: letters, digits, underscore, hyphen —
// no whitespace or punctuation that could break a query-string filter.
var complianceControlFrameworkPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// MaxComplianceControlRefLength bounds ControlRef.
const MaxComplianceControlRefLength = 50

// complianceControlRefPattern is the format ControlRef must satisfy — a
// compact control identifier WITHIN a framework (e.g. "CC6.1", "A.9.2.3",
// "3.4.1"). Wider than complianceControlFrameworkPattern: framework control
// numbering conventionally uses dots to express a hierarchical section
// number, so '.' joins '-'/'_' in the allowed charset; still no whitespace.
var complianceControlRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// MaxComplianceControlTitleLength bounds Title, mirroring MaxRiskTitleLength.
const MaxComplianceControlTitleLength = 200

// MaxComplianceControlDescriptionLength bounds the optional free-text
// narrative, mirroring MaxRiskDescriptionLength.
const MaxComplianceControlDescriptionLength = 10000

// ComplianceControl is one entry in the tenant's compliance-control register
// (E8.4b): a legitimate stateful reified entity — like Risk/Incident/
// VulnFinding, NOT a false noun — because a control is operator-tracked,
// carries a governed implementation lifecycle (ComplianceControlStatus), and
// accumulates append-only evidence over time (ControlEvidence). Finishes
// E8.4 (continuous-audit automation — mapping controls to live checks — is
// deferred as speculative on a customer-less product; see ADR-SOC-008/009).
//
// Framework/ControlRef together name WHICH control this is (e.g. framework
// "SOC2", control_ref "CC6.1") and are immutable after Create: changing
// either would silently reassign which compliance obligation this row
// tracks, so ComplianceControlRepository.Update does not accept them —
// mirrors Incident.Source's identical "provenance is a fact, not an edit"
// treatment. UNIQUE(tenant_id, framework, control_ref): a control appears at
// most once per framework per tenant.
//
// OwnerTeamID/OwnerUserID (accountability) are DELIBERATELY OMITTED from
// this first cut, for the identical reason Risk's doc comment gives for its
// own omission (ADR-SOC-008): this story's value is the register + its
// lifecycle + evidence trail, not an ownership workflow, and adding them
// would mean two more cross-tenant existence checks
// (verifyOwnerTeamExists/verifyOwnerUserExists) on every write for a
// capability nothing here needs yet. A follow-up story can add them as a
// pure additive PATCH field.
type ComplianceControl struct {
	ControlID string
	TenantID  string
	Framework string
	// ControlRef is the control's own identifier WITHIN Framework (e.g.
	// "CC6.1" under "SOC2").
	ControlRef string
	Title      string
	// Description is optional, operator-supplied free text: scope, context,
	// interpretation notes.
	Description string
	Status      ComplianceControlStatus
	RowVersion  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate enforces the invariants the database also enforces, so a bad
// control fails with a field-level reason before it reaches the store.
func (c *ComplianceControl) Validate() error {
	if strings.TrimSpace(c.ControlID) == "" {
		return newValidation("control_id", "must not be empty")
	}
	if strings.TrimSpace(c.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	framework := strings.TrimSpace(c.Framework)
	if framework == "" {
		return newValidation("framework", "must not be empty")
	}
	if len(framework) > MaxComplianceControlFrameworkLength {
		return newValidation("framework", "must be at most 100 characters")
	}
	if !complianceControlFrameworkPattern.MatchString(framework) {
		return newValidation("framework", "must start with a letter and contain only letters, digits, '_' or '-', e.g. SOC2, ISO27001, PCI-DSS")
	}
	controlRef := strings.TrimSpace(c.ControlRef)
	if controlRef == "" {
		return newValidation("control_ref", "must not be empty")
	}
	if len(controlRef) > MaxComplianceControlRefLength {
		return newValidation("control_ref", "must be at most 50 characters")
	}
	if !complianceControlRefPattern.MatchString(controlRef) {
		return newValidation("control_ref", "must start with a letter or digit and contain only letters, digits, '.', '_' or '-', e.g. CC6.1")
	}
	title := strings.TrimSpace(c.Title)
	if title == "" {
		return newValidation("title", "must not be empty")
	}
	if len(title) > MaxComplianceControlTitleLength {
		return newValidation("title", "must be at most 200 characters")
	}
	if len(c.Description) > MaxComplianceControlDescriptionLength {
		return newValidation("description", "must be at most 10000 characters")
	}
	if !c.Status.Valid() {
		return newValidation("status", "must be one of: not_implemented, in_progress, implemented, not_applicable")
	}
	return nil
}

// NewComplianceControl builds a validated, freshly-minted control, always
// not_implemented. tenantID is always taken from the bound connection's
// context, never from request data, the same rule NewRisk/NewIncident
// follow.
func NewComplianceControl(tenantID, framework, controlRef, title, description string) (*ComplianceControl, error) {
	c := &ComplianceControl{
		ControlID:   NewID(),
		TenantID:    strings.TrimSpace(tenantID),
		Framework:   strings.TrimSpace(framework),
		ControlRef:  strings.TrimSpace(controlRef),
		Title:       strings.TrimSpace(title),
		Description: description,
		Status:      ComplianceControlNotImplemented,
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ComplianceControlPatch carries the fields ComplianceControlRepository.
// Update may change; a nil pointer leaves that field unchanged. Status is
// deliberately absent — the ONLY way to move a control's lifecycle is
// SetStatus, mirroring RiskPatch/AssetPatch's identical split. Framework/
// ControlRef are likewise absent: see ComplianceControl's own doc comment on
// why they are immutable after Create.
type ComplianceControlPatch struct {
	Title       *string
	Description *string
}

// ComplianceControlRepository administers compliance_control: the
// tenant-owned register entity, its governed implementation lifecycle, and
// its append-only evidence trail (E8.4b, ADR-SOC-009). Continuous-audit
// automation (mapping controls to live checks) is OUT of scope here — a
// deliberately separate, later story, exactly as ADR-SOC-008 records for
// E8.4a.
//
// compliance_control and control_evidence are TENANT-OWNED: both are in
// postgres.TenantOwnedTables and carry row-level security, so this
// repository takes no tenant argument anywhere — the bound connection
// already is the boundary (ADR-TENANCY-002), exactly like RiskRepository/
// IncidentRepository.
//
// There is NO Delete on the control. Like Risk/Incident/VulnFinding, a
// tracked control is operational history the moment it is registered — even
// a since-retired control records what was once tracked — so "removing" one
// is a lifecycle transition (SetStatus to not_applicable), not a row
// deletion (ADR-HARD-003). Evidence has no Delete OR Update at all: it is an
// append-only audit trail (ControlEvidence's own doc comment).
type ComplianceControlRepository interface {
	// Create inserts a control. Returns ErrConflict when (tenant_id,
	// framework, control_ref) is already taken — mirrors
	// VulnFindingRepository's own uniqueness-violation mapping.
	Create(ctx context.Context, c *ComplianceControl) (*ComplianceControl, error)

	Get(ctx context.Context, controlID string) (*ComplianceControl, error)

	// List returns a page of the caller's controls, keyset-paginated over
	// control_id. framework/status, each non-empty, filter to exactly that
	// value; the zero value for each returns every match on the other
	// filter — mirrors RiskRepository.List's filter shape.
	List(ctx context.Context, limit int, after string, framework string, status ComplianceControlStatus) ([]*ComplianceControl, error)

	// Update changes one or more non-lifecycle, non-identity fields of patch
	// under optimistic locking. Returns ErrVersionMismatch on a stale
	// rowVersion, ErrNotFound if controlID does not name a control visible
	// to this tenant.
	Update(ctx context.Context, controlID string, rowVersion int64, patch ComplianceControlPatch) (*ComplianceControl, error)

	// SetStatus performs a lifecycle transition under optimistic locking,
	// guarded by ComplianceControlStatus.CanTransitionTo — the single
	// authority for status changes. An illegal move returns
	// *ComplianceControlTransitionError (ErrInvalidTransition).
	SetStatus(ctx context.Context, controlID string, rowVersion int64, status ComplianceControlStatus) (*ComplianceControl, error)

	// AddEvidence appends one ControlEvidence row to controlID's trail.
	// controlID is re-verified against this store's own tenant-scoped,
	// RLS-enforced connection BEFORE the row is written — a controlID this
	// tenant cannot see returns ErrNotFound, exactly as if it did not exist.
	// The acting identity (RecordedBy) comes from ActorFrom(ctx); an
	// unattributable call returns ErrNoActor.
	AddEvidence(ctx context.Context, controlID string, kind ControlEvidenceKind, value string) (*ControlEvidence, error)

	// Evidence returns controlID's append-only evidence trail, oldest first,
	// bounded to at most limit rows — the same "bounded projection" shape
	// RiskRepository.Register uses, since this is served embedded in GET
	// .../{id} rather than as its own paginated endpoint. Row-level security
	// alone confines this to rows the caller's tenant actually wrote; a
	// controlID belonging to another tenant returns an empty slice, the same
	// "RLS makes it invisible" behaviour IncidentRepository.Timeline has for
	// a mismatched incidentID.
	Evidence(ctx context.Context, controlID string, limit int) ([]*ControlEvidence, error)
}

// ControlEvidenceKind is the closed vocabulary of what a ControlEvidence
// row's Value carries (E8.4b).
type ControlEvidenceKind string

const (
	// ControlEvidenceKindURL: Value is a URL pointing at externally-stored
	// proof (a screenshot, a scan report, a signed document).
	ControlEvidenceKindURL ControlEvidenceKind = "url"
	// ControlEvidenceKindNote: Value is free-text operator narrative.
	ControlEvidenceKindNote ControlEvidenceKind = "note"
	// ControlEvidenceKindAttestation: Value is a formal statement an
	// accountable person is putting on record (e.g. "I confirm access
	// reviews ran quarterly through Q3").
	ControlEvidenceKindAttestation ControlEvidenceKind = "attestation"
)

// Valid reports whether k is a defined kind.
func (k ControlEvidenceKind) Valid() bool {
	switch k {
	case ControlEvidenceKindURL, ControlEvidenceKindNote, ControlEvidenceKindAttestation:
		return true
	default:
		return false
	}
}

// MaxControlEvidenceValueLength bounds Value — wide enough for a URL, a
// short narrative, or a formal attestation statement, but still finite; the
// same "prose but not unbounded" treatment MaxRiskDescriptionLength gives,
// scaled down since evidence is a single filed record, not a running
// narrative.
const MaxControlEvidenceValueLength = 4000

// ControlEvidence is one append-only row of a ComplianceControl's evidence
// trail (E8.4b) — the proof an operator files for a control. It is NOT a
// reified entity: it carries no lifecycle of its own and is never updated or
// deleted after it is written, mirroring IncidentEvent's exact treatment
// (an attached record on a stateful parent, not a stateful thing in its own
// right). Enforced append-only at the SCHEMA level (BEFORE UPDATE OR DELETE
// trigger, mirroring incident_event/asset_change_history's hardened
// pattern) — not merely by this repository's own missing Update/Delete
// methods.
//
// This is a PLAIN append-only table, not a second instance of the audit
// hash-chain (audit_event/admin_audit_event's chain_id/seq/this_hash/
// prev_hash machinery, ADR-AUDIT-003/004): control_evidence carries no
// chain-of-custody linkage between rows, only RLS isolation plus the
// immutability trigger — the same "append-only but not chained" treatment
// incident_event/asset_change_history already have. See
// ComplianceControlRepository.AddEvidence's doc comment for why one FOR
// UPDATE lock (on the PARENT compliance_control row) still governs the
// append path — that lock exists to serialise the tenant re-verification
// read, not to order a chain sequence number, since ControlEvidence has
// none.
type ControlEvidence struct {
	EvidenceID string
	TenantID   string
	ControlID  string
	Kind       ControlEvidenceKind
	Value      string
	// RecordedBy is the actor who filed this evidence — sourced from
	// ActorFrom(ctx) at write time, never taken from request data, the same
	// rule IncidentEvent.Actor follows.
	RecordedBy string
	RecordedAt time.Time
}

// Validate enforces the invariants the database also enforces, so a bad
// evidence record fails with a field-level reason before it reaches the
// store.
func (e *ControlEvidence) Validate() error {
	if strings.TrimSpace(e.EvidenceID) == "" {
		return newValidation("evidence_id", "must not be empty")
	}
	if strings.TrimSpace(e.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(e.ControlID) == "" {
		return newValidation("control_id", "must not be empty")
	}
	if !e.Kind.Valid() {
		return newValidation("kind", "must be one of: url, note, attestation")
	}
	value := strings.TrimSpace(e.Value)
	if value == "" {
		return newValidation("value", "must not be empty")
	}
	if len(value) > MaxControlEvidenceValueLength {
		return newValidation("value", "must be at most 4000 characters")
	}
	if strings.TrimSpace(e.RecordedBy) == "" {
		return newValidation("recorded_by", "must not be empty")
	}
	return nil
}

// NewControlEvidence builds a validated, freshly-minted evidence record.
// tenantID/controlID/recordedBy are always taken from the store's own
// tenant-verified control row and ActorFrom(ctx), never from request data —
// see ComplianceControlRepository.AddEvidence's doc comment.
func NewControlEvidence(tenantID, controlID string, kind ControlEvidenceKind, value, recordedBy string) (*ControlEvidence, error) {
	e := &ControlEvidence{
		EvidenceID: NewID(),
		TenantID:   strings.TrimSpace(tenantID),
		ControlID:  strings.TrimSpace(controlID),
		Kind:       kind,
		Value:      strings.TrimSpace(value),
		RecordedBy: strings.TrimSpace(recordedBy),
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

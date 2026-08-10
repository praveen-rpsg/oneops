package domain

import (
	"context"
	"strings"
	"time"
)

// IncidentSeverity is the operational impact tier of an Incident. Closed, like
// AssetCriticality: it is the vocabulary triage/routing and, later, SLA
// tracking (E5.3) reason over directly, so a new tier is a deliberate schema
// decision, not a value a caller happens to send. Unlike AssetCriticality
// there is no "unknown" member — an Incident is opened BECAUSE something is
// known to be wrong, so a caller always states how badly.
type IncidentSeverity string

const (
	IncidentSeverityCritical IncidentSeverity = "critical"
	IncidentSeverityHigh     IncidentSeverity = "high"
	IncidentSeverityMedium   IncidentSeverity = "medium"
	IncidentSeverityLow      IncidentSeverity = "low"
)

// Valid reports whether s is a defined severity tier.
func (s IncidentSeverity) Valid() bool {
	switch s {
	case IncidentSeverityCritical, IncidentSeverityHigh, IncidentSeverityMedium, IncidentSeverityLow:
		return true
	default:
		return false
	}
}

// incidentSeverityRank orders IncidentSeverity from least (low) to most
// (critical) severe — the numeric backing for AtLeast below. An unrecognised
// value ranks below every defined severity so a caller comparing against
// garbage input never treats it as satisfying a threshold.
func incidentSeverityRank(s IncidentSeverity) int {
	switch s {
	case IncidentSeverityLow:
		return 1
	case IncidentSeverityMedium:
		return 2
	case IncidentSeverityHigh:
		return 3
	case IncidentSeverityCritical:
		return 4
	default:
		return 0
	}
}

// AtLeast reports whether s is at least as severe as threshold — critical >
// high > medium > low, e.g. IncidentSeverityHigh.AtLeast(IncidentSeverityMedium)
// is true. Introduced for SecurityResponseRule's MinSeverity threshold match
// (E8.5, ADR-SOC-010), the first caller in this codebase to compare two
// IncidentSeverity values by ORDER rather than by equality.
func (s IncidentSeverity) AtLeast(threshold IncidentSeverity) bool {
	return incidentSeverityRank(s) >= incidentSeverityRank(threshold)
}

// IncidentStatus is the lifecycle of an Incident (E5.1, extending the ITSM
// core sketched in docs/PLATFORM-BUILD-PLAN.md E5). Incident is the ratified
// reduced noun for a stateful ITSM work item — it is modelled as a state
// machine, exactly like AssetStatus/UserStatus, not as a reified generic
// "Ticket".
type IncidentStatus string

const (
	// IncidentOpen is the status every Incident is created in. There is no
	// "pre-opened" state to choose at creation — unlike Asset (which may be
	// registered "planned", ahead of going live), an Incident exists because
	// something has already happened, so its history starts the moment it is
	// filed.
	IncidentOpen IncidentStatus = "open"
	// IncidentAcknowledged: a responder has seen it and taken ownership.
	IncidentAcknowledged IncidentStatus = "acknowledged"
	// IncidentInvestigating: active work toward a resolution is under way.
	IncidentInvestigating IncidentStatus = "investigating"
	// IncidentResolved: the responder believes the underlying problem is
	// fixed. Not terminal — see IncidentReopened.
	IncidentResolved IncidentStatus = "resolved"
	// IncidentClosed is terminal: the resolution has been confirmed and
	// nothing further will happen to this Incident. Mirrors
	// UserStatus.deactivated's terminality, not AssetStatus.retired's
	// reversibility — an Incident does not get "reinstated", it gets
	// IncidentReopened instead, which is a distinct state with its own
	// history, not a return to an earlier one.
	IncidentClosed IncidentStatus = "closed"
	// IncidentReopened: a resolution did not hold. Distinct from IncidentOpen
	// (a fresh Incident) precisely because reopening is itself a fact worth
	// recording — the timeline shows the resolution attempt that failed.
	IncidentReopened IncidentStatus = "reopened"
)

// Valid reports whether s is a defined status.
func (s IncidentStatus) Valid() bool {
	switch s {
	case IncidentOpen, IncidentAcknowledged, IncidentInvestigating, IncidentResolved, IncidentClosed, IncidentReopened:
		return true
	default:
		return false
	}
}

// IncidentSource distinguishes an incident an operator filed directly
// (manual, the only kind before E4) from one internal/alerting's evaluator
// created or linked off an alert-rule firing (alert, E4.1), one
// internal/security's detector created or linked off a security-rule firing
// (security, E8.1b-2), or one VulnFindingRepository.Remediate opened to track
// fixing a vulnerability finding (vuln, E8.3b, ADR-SOC-007). It is the ONLY
// thing that gates auto-correlation: FindOrCreateOpenAlertIncident only ever
// considers incidents whose Source is alert, and
// FindOrCreateOpenSecurityIncident only ever considers incidents whose
// Source is security, so a later firing on the same CI can never silently
// annex an operator's own manually-filed incident, and an alert firing and a
// security firing on the SAME asset always produce two SEPARATE open
// incidents rather than colliding into one — nothing else about the three
// shapes would otherwise tell them apart. vuln does not participate in that
// per-asset correlation at all: Remediate's own idempotency is keyed by the
// FINDING's own RemediationIncidentID link, not by (tenant, asset, source),
// so a vuln-sourced incident carries no partial-unique-index analog to
// ux_incident_open_alert_per_asset/ux_incident_open_security_per_asset (see
// VulnFindingRepository.Remediate's doc comment).
type IncidentSource string

const (
	IncidentSourceManual   IncidentSource = "manual"
	IncidentSourceAlert    IncidentSource = "alert"
	IncidentSourceSecurity IncidentSource = "security"
	// IncidentSourceVuln is written ONLY by VulnFindingRepository.Remediate
	// (E8.3b) — an operator-triggered action, not an evaluator/detector
	// background correlation, but still distinguished from manual so a
	// remediation-tracking incident is filterable/reportable as its own
	// provenance, exactly as alert/security are.
	IncidentSourceVuln IncidentSource = "vuln"
)

// Valid reports whether s is a defined source.
func (s IncidentSource) Valid() bool {
	switch s {
	case IncidentSourceManual, IncidentSourceAlert, IncidentSourceSecurity, IncidentSourceVuln:
		return true
	default:
		return false
	}
}

// incidentTransitions is the whole lifecycle, as data rather than as a chain
// of conditionals — mirrors assetTransitions/userTransitions. A transition
// absent here is refused, so adding a state means stating every edge it has
// instead of discovering the omissions in production.
//
// The forward path is exactly open -> acknowledged -> investigating ->
// resolved -> closed (E5.1's stated shape). resolved -> reopened is the one
// documented exception to strict forward motion: a resolution that did not
// hold. reopened -> investigating puts a reopened Incident straight back into
// active work — not through acknowledged again, since it was already
// acknowledged the first time round — so the same forward edge
// (investigating -> resolved -> closed) carries it the rest of the way.
// closed is terminal, exactly like UserStatus.deactivated: once confirmed
// resolved and closed, an Incident is not reopened from there — reopening a
// CLOSED incident (as opposed to one merely resolved) is deliberately out of
// this edge set; a closed incident that turns out not to be fixed is filed as
// a new Incident that references it, which is out of scope for E5.1 (no
// "linked incidents" yet).
var incidentTransitions = map[IncidentStatus][]IncidentStatus{
	IncidentOpen:          {IncidentAcknowledged},
	IncidentAcknowledged:  {IncidentInvestigating},
	IncidentInvestigating: {IncidentResolved},
	IncidentResolved:      {IncidentClosed, IncidentReopened},
	IncidentReopened:      {IncidentInvestigating},
	IncidentClosed:        {},
}

// CanTransitionTo reports whether from -> to is a permitted lifecycle move. A
// transition to the current state is refused: it is a no-op that would
// consume a row version and write a timeline record recording nothing.
func (s IncidentStatus) CanTransitionTo(to IncidentStatus) bool {
	for _, allowed := range incidentTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// IncidentTransitionError is a refused lifecycle move, naming both ends so the
// message says which move was attempted rather than only that one was. It
// wraps the shared ErrInvalidTransition (user.go), so callers may match the
// class with errors.Is regardless of which entity's lifecycle refused the
// move — mirrors AssetTransitionError exactly, scoped to IncidentStatus
// because Go's type system will not let one struct carry both.
type IncidentTransitionError struct {
	From IncidentStatus
	To   IncidentStatus
}

func (e *IncidentTransitionError) Error() string {
	return "invalid lifecycle transition: " + string(e.From) + " -> " + string(e.To)
}

// Unwrap makes errors.Is(err, ErrInvalidTransition) true for this type.
func (e *IncidentTransitionError) Unwrap() error { return ErrInvalidTransition }

// NewIncidentTransitionError builds an *IncidentTransitionError naming both ends.
func NewIncidentTransitionError(from, to IncidentStatus) *IncidentTransitionError {
	return &IncidentTransitionError{From: from, To: to}
}

// MaxIncidentTitleLength bounds a free-text field that reaches consoles, logs
// and notifications — the same bound Asset.Name carries.
const MaxIncidentTitleLength = 200

// MaxIncidentDescriptionLength bounds the free-text narrative field. Wider
// than Title: a description is prose, not a label, but still finite —
// unbounded text in a row is a storage and rendering hazard the same way an
// unbounded page size is (docs/PLATFORM-BUILD-PLAN.md §3).
const MaxIncidentDescriptionLength = 10000

// Incident is a stateful ITSM work item tracking operational work against a
// CI: the ratified reduced noun for this kind of thing is Incident, not a
// reified generic "Ticket" (docs/PLATFORM-BUILD-PLAN.md E5). It carries a
// governed lifecycle (IncidentStatus), an optional assignee and an optional
// link to the affected Configuration Item, and an append-only timeline
// (IncidentEvent) recording how it got to its current state.
//
// Foundation for E5.2 (on-call/paging), E5.3 (SLA), E5.4 (problem
// management) and E5.5 (change management), all of which reference an
// Incident rather than duplicating its shape. Alert-firing -> Incident wiring
// is E4; incidents are created manually via the API until then.
type Incident struct {
	IncidentID  string
	TenantID    string
	Title       string
	Description string
	Severity    IncidentSeverity
	Status      IncidentStatus
	// Source distinguishes an operator-filed incident from one E4.1's
	// correlation pipeline created or linked off an alert-rule firing.
	// Written once at Create and never changed afterward — there is no
	// Update path for it, the same immutable-after-create treatment
	// Asset.Source(E1.4) does NOT get (that one is mutable via reconciliation);
	// an incident's provenance, unlike a CMDB record's, is a fact about how it
	// came to exist, not a value a later import can correct.
	Source IncidentSource
	// AssetID is the affected Configuration Item, or nil when the incident is
	// not (yet) tied to one. When set, IncidentRepository re-verifies it
	// against the caller's own tenant-scoped connection before writing — see
	// AssetRepository.Create's doc comment for why the foreign key alone
	// cannot be trusted (ADR-ASSET-001 §6).
	AssetID *string
	// AssigneeUserID is the responder currently accountable for this
	// Incident, or nil when unassigned. app_user is GLOBAL (ADR-IDENTITY-002
	// §3.1); membership is the tenant fact, so IncidentRepository verifies an
	// ACTIVE membership row for this user in the caller's tenant before
	// writing it — the same check AssetStore.verifyOwnerUserExists applies to
	// Asset.OwnerUserID (E1.2), applied here to an assignee instead of an
	// owner.
	AssigneeUserID *string
	RowVersion     int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// AcknowledgedAt/ResolvedAt/ClosedAt are set by IncidentRepository.
	// SetStatus at the moment the corresponding state is (re-)entered — never
	// by a caller directly, the same "derived write-time fact" treatment
	// Asset.LastSeen gets. Each is overwritten, not latched, on a later
	// re-entry (e.g. resolved -> reopened -> investigating -> resolved sets
	// ResolvedAt to the SECOND resolution's time): SLA tracking against the
	// current open period is E5.3's concern, not this one's; these fields
	// answer "when did this last happen", not "when did this first happen".
	AcknowledgedAt *time.Time
	ResolvedAt     *time.Time
	ClosedAt       *time.Time
	// RootIncidentID is the topology-aware grouping LINK (E4.2,
	// ADR-ALERTING-004): a column, not a reified Group/Correlation entity —
	// the same reduced-concept treatment AlertRule.CurrentIncidentID gets
	// (E4.1). NULL means this incident is itself a root or standalone; a
	// non-NULL value names the OPEN, alert-sourced incident this one is
	// grouped under because its own asset transitively depends
	// (depends_on/runs_on only, cycle-safe) on the root's asset, which is
	// itself "down" (has its own open, alert-sourced incident). Set/cleared
	// EXCLUSIVELY by internal/grouping's leader-gated reconciliation pass —
	// there is no HTTP write path, mirroring alert_rule.last_state's
	// evaluator-only write discipline. An incident points to AT MOST ONE
	// root; "the group" a caller sees is derived (every incident sharing a
	// RootIncidentID, plus that root), never a stored membership row.
	RootIncidentID *string
	// SymptomClass mirrors alert_rule.SymptomClass's own three-value taxonomy
	// (E3.4/ADR-ALERTING-005) onto the incident an alert firing correlates
	// into (E4.3, ADR-ALERTING-007) — the grouping-side counterpart of E3.5's
	// (ADR-ALERTING-006) dependency-suppression consumption of the same
	// primitive. It is set ONCE, at create, by internal/alerting's evaluator
	// (FindOrCreateOpenAlertIncident), copied from the firing rule's own
	// SymptomClass — never re-synced if the rule is later re-classified, the
	// same "fact about how this incident came to exist" treatment Source
	// already gets. There is no HTTP write path for it. A manual/security/vuln
	// incident always carries DefaultAlertSymptomClass (unspecified) — only an
	// alert-sourced incident's class is ever meaningful. internal/grouping's
	// reconciler is the sole reader: a resource-class incident is never
	// rooted under a down dependency, however deep, because a resource
	// symptom on X is not caused by an upstream dependency being down.
	SymptomClass AlertSymptomClass
}

// Open reports whether the incident is still active work (not closed).
func (i *Incident) Open() bool { return i.Status != IncidentClosed }

// IsGroupRoot reports whether this incident anchors a topology-aware group
// (E4.2): it has no root of its own, whether or not anything is currently
// grouped under it — the same "NULL means unlinked" reading
// AlertRule.CurrentIncidentID's own doc comment uses, applied here to the
// inverse direction (a root names nothing; a collateral incident names its
// root).
func (i *Incident) IsGroupRoot() bool { return i.RootIncidentID == nil }

// Validate enforces the invariants the database also enforces, so a bad
// request fails with a field-level 422 rather than a constraint-violation 500.
func (i *Incident) Validate() error {
	if strings.TrimSpace(i.IncidentID) == "" {
		return newValidation("incident_id", "must not be empty")
	}
	if strings.TrimSpace(i.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	title := strings.TrimSpace(i.Title)
	if title == "" {
		return newValidation("title", "must not be empty")
	}
	if len(title) > MaxIncidentTitleLength {
		return newValidation("title", "must be at most 200 characters")
	}
	if len(i.Description) > MaxIncidentDescriptionLength {
		return newValidation("description", "must be at most 10000 characters")
	}
	if !i.Severity.Valid() {
		return newValidation("severity", "must be one of: critical, high, medium, low")
	}
	if !i.Status.Valid() {
		return newValidation("status", "must be one of: open, acknowledged, investigating, resolved, closed, reopened")
	}
	if !i.Source.Valid() {
		return newValidation("source", "must be one of: manual, alert, security, vuln")
	}
	if (i.Source == IncidentSourceAlert || i.Source == IncidentSourceSecurity || i.Source == IncidentSourceVuln) &&
		(i.AssetID == nil || strings.TrimSpace(*i.AssetID) == "") {
		return newValidation("asset_id", "must be set for an alert-, security- or vuln-sourced incident — correlation/remediation always names the CI the firing rule/finding watches")
	}
	if i.AssetID != nil && strings.TrimSpace(*i.AssetID) == "" {
		return newValidation("asset_id", "must not be blank when supplied; omit it to leave the incident unlinked")
	}
	if i.AssigneeUserID != nil && strings.TrimSpace(*i.AssigneeUserID) == "" {
		return newValidation("assignee_user_id", "must not be blank when supplied; omit it to leave the incident unassigned")
	}
	if !i.SymptomClass.Valid() {
		return newValidation("symptom_class", "must be one of: availability, resource, unspecified")
	}
	return nil
}

// NewIncident builds a freshly opened, manually-filed Incident. Status is
// always IncidentOpen — there is no caller-chosen initial status (see
// IncidentOpen's doc comment): an Incident is filed because something has
// already happened. Source is always IncidentSourceManual — this is the
// caller-facing constructor the HTTP admin API uses; NewAlertIncident is
// E4.1's counterpart for the evaluator's own correlation pipeline.
func NewIncident(tenantID, title, description string, severity IncidentSeverity, assetID, assigneeUserID *string) (*Incident, error) {
	inc := &Incident{
		IncidentID:     NewID(),
		TenantID:       strings.TrimSpace(tenantID),
		Title:          strings.TrimSpace(title),
		Description:    description,
		Severity:       severity,
		Status:         IncidentOpen,
		Source:         IncidentSourceManual,
		AssetID:        assetID,
		AssigneeUserID: assigneeUserID,
		SymptomClass:   DefaultAlertSymptomClass,
	}
	if err := inc.Validate(); err != nil {
		return nil, err
	}
	return inc, nil
}

// NewAlertIncident builds a freshly opened, alert-sourced Incident — the
// shape internal/alerting's evaluator creates on an ok->firing transition
// that finds no existing OPEN alert-sourced incident for the firing rule's
// own asset (E4.1). Unlike NewIncident's optional AssetID, assetID is
// required here: a correlation always names the CI the firing rule watches,
// and IncidentSourceAlert without one is refused by Validate.
//
// SymptomClass defaults to DefaultAlertSymptomClass (unspecified) here —
// there is no parameter for it, the same "default inside the constructor,
// not a parameter every call site must supply" shape NewAlertRule already
// uses for its own SymptomClass default. The evaluator's own correlate
// (E4.3, ADR-ALERTING-007) overwrites it with the firing rule's actual
// SymptomClass on the CREATE path, before calling
// IncidentCorrelator.FindOrCreateOpenAlertIncident — never on a LINK, which
// leaves an already-open incident's class exactly as it was set at create.
func NewAlertIncident(tenantID, title, description string, severity IncidentSeverity, assetID string) (*Incident, error) {
	trimmed := strings.TrimSpace(assetID)
	inc := &Incident{
		IncidentID:   NewID(),
		TenantID:     strings.TrimSpace(tenantID),
		Title:        strings.TrimSpace(title),
		Description:  description,
		Severity:     severity,
		Status:       IncidentOpen,
		Source:       IncidentSourceAlert,
		AssetID:      &trimmed,
		SymptomClass: DefaultAlertSymptomClass,
	}
	if err := inc.Validate(); err != nil {
		return nil, err
	}
	return inc, nil
}

// NewSecurityIncident builds a freshly opened, security-sourced Incident —
// the shape internal/security's detector creates on an ok->firing transition
// that finds no existing OPEN security-sourced incident for the firing
// rule's own asset (E8.1b-2). It is NewAlertIncident's exact analog for the
// SOC detection path: assetID is required for the identical reason (a
// correlation always names the CI the firing rule watches), and
// IncidentSourceSecurity without one is refused by Validate. A security
// incident and an alert incident on the SAME asset are two SEPARATE rows —
// Source distinguishes them, so neither correlation path ever discovers or
// annexes the other's incident.
func NewSecurityIncident(tenantID, title, description string, severity IncidentSeverity, assetID string) (*Incident, error) {
	trimmed := strings.TrimSpace(assetID)
	inc := &Incident{
		IncidentID:   NewID(),
		TenantID:     strings.TrimSpace(tenantID),
		Title:        strings.TrimSpace(title),
		Description:  description,
		Severity:     severity,
		Status:       IncidentOpen,
		Source:       IncidentSourceSecurity,
		AssetID:      &trimmed,
		SymptomClass: DefaultAlertSymptomClass,
	}
	if err := inc.Validate(); err != nil {
		return nil, err
	}
	return inc, nil
}

// NewVulnRemediationIncident builds a freshly opened, vuln-sourced Incident —
// the shape VulnFindingRepository.Remediate opens to track fixing one
// VulnFinding (E8.3b, ADR-SOC-007). assetID is required for the identical
// reason NewAlertIncident/NewSecurityIncident require one: a correlation
// always names the CI in question, and IncidentSourceVuln without one is
// refused by Validate. Unlike the alert/security paths, there is no
// per-(tenant,asset) uniqueness here — Remediate's own idempotency is keyed
// by the finding's own RemediationIncidentID link (see that method's doc
// comment), so calling this twice for two DIFFERENT findings on the SAME
// asset is expected to produce two separate incidents.
func NewVulnRemediationIncident(tenantID, title, description string, severity IncidentSeverity, assetID string) (*Incident, error) {
	trimmed := strings.TrimSpace(assetID)
	inc := &Incident{
		IncidentID:   NewID(),
		TenantID:     strings.TrimSpace(tenantID),
		Title:        strings.TrimSpace(title),
		Description:  description,
		Severity:     severity,
		Status:       IncidentOpen,
		Source:       IncidentSourceVuln,
		AssetID:      &trimmed,
		SymptomClass: DefaultAlertSymptomClass,
	}
	if err := inc.Validate(); err != nil {
		return nil, err
	}
	return inc, nil
}

// IncidentPatch carries the fields IncidentRepository.Update may change; a
// nil pointer leaves that field unchanged.
//
// Status and AssigneeUserID are deliberately ABSENT, exactly as Status is
// absent from AssetPatch: SetStatus and Assign are the single authorities for
// those two moves (mirrors AssetRepository.SetStatus's doc comment) — folding
// either back into Update would let a caller change it without going through
// the transition guard or the assignee tenant-verification, or hide either
// change inside a multi-field patch that also touches title/description/
// severity.
//
// AssetID is tri-state, encoded as *string exactly as Asset.OwnerTeamID/
// OwnerUserID are (see AssetPatch's doc comment): nil leaves the link
// unchanged, a pointer to "" clears it, and a pointer to a non-empty id sets
// it, re-verified against the caller's tenant first.
type IncidentPatch struct {
	Title       *string
	Description *string
	Severity    *IncidentSeverity
	AssetID     *string
}

// IncidentEventKind classifies what an incident_event row records.
//
// This is the timeline's own, deliberately narrower, vocabulary — NOT
// asset_change_history's "one row per field changed" shape. A Patch to
// title/description/severity/asset_id is not recorded here: the timeline is
// the case narrative an operator reads top to bottom (what happened, who
// acted, when), not a compliance field-audit. General operator comment
// support (a free-text note an operator drops into the timeline) remains a
// documented member of the target vocabulary with no HTTP write path — E5.1's
// SCOPE(in) §5 still enumerates exactly create/get/list/patch/transition/
// assign/timeline. IncidentEventAlertNote (E4.1) is NOT that: it is written
// exclusively by internal/alerting's evaluator, never by an HTTP handler, so
// it does not open the general comment surface — a later increment that adds
// an operator-facing "add a comment" endpoint still wires into this same
// append-only table rather than opening a second one.
type IncidentEventKind string

const (
	// IncidentEventCreated is the single row written when an incident is
	// first filed. It carries no field/old_value/new_value — there is no
	// "old" state to diff against — only the fact and its actor.
	IncidentEventCreated IncidentEventKind = "created"
	// IncidentEventStatusTransitioned is written by SetStatus. Field is
	// always "status".
	IncidentEventStatusTransitioned IncidentEventKind = "status_transitioned"
	// IncidentEventAssigned is written by Assign. Field is always
	// "assignee_user_id"; old/new are user ids, or nil for unassigned.
	IncidentEventAssigned IncidentEventKind = "assigned"
	// IncidentEventAlertNote is a free-text timeline note the alert-
	// correlation pipeline appends (E4.1): "alert <metric> fired" when a
	// second rule's firing links to an already-open alert incident, and
	// "alert <metric> recovered" when the linked rule's firing clears. Field
	// is always "alert"; NewValue carries the note text; OldValue is always
	// nil — there is no "old" note to diff against, the same shape
	// IncidentEventCreated uses. Actor is always the evaluator's own system
	// identity, never ActorFrom(ctx) — this is a background write, not a
	// request-attributed one.
	IncidentEventAlertNote IncidentEventKind = "alert_note"
	// IncidentEventSecurityNote is IncidentEventAlertNote's exact analog for
	// internal/security's detector (E8.1b-2): "security rule <observation_type>
	// fired" when a second rule's firing links to an already-open security
	// incident, and "security rule <observation_type> recovered" when the
	// linked rule's firing clears. Field is always "security"; NewValue
	// carries the note text; OldValue is always nil. Actor is always the
	// detector's own system identity, never ActorFrom(ctx) — this is a
	// background write, not a request-attributed one.
	IncidentEventSecurityNote IncidentEventKind = "security_note"
)

// Valid reports whether k is a value the database will accept.
func (k IncidentEventKind) Valid() bool {
	switch k {
	case IncidentEventCreated, IncidentEventStatusTransitioned, IncidentEventAssigned,
		IncidentEventAlertNote, IncidentEventSecurityNote:
		return true
	default:
		return false
	}
}

// IncidentEvent is one append-only row of an Incident's timeline (E5.1). It
// is written by the same transaction that performs the mutation it records —
// see IncidentStore.Create/SetStatus/Assign — so a change and its timeline
// entry commit together or neither does. Mirrors AssetChangeEntry's shape and
// same-transaction discipline exactly.
//
// IncidentID DOES carry a foreign key to incident (see the migration) —
// unlike AssetChangeEntry's deliberate omission of one to asset. That
// omission exists because an asset CAN be hard-deleted (AssetRepository.
// Delete) and its history must survive that; an Incident has NO Delete
// method at all (E5.1 decision — see IncidentRepository's doc comment), so
// the "history must outlive a deleted subject" concern that drives
// asset_change_history's FK-less design does not apply here: the subject is
// never deleted, so a normal referential-integrity FK is strictly safer (it
// catches a bug that would otherwise write an orphaned timeline row) and
// costs nothing.
type IncidentEvent struct {
	EventID    string
	TenantID   string
	IncidentID string
	Kind       IncidentEventKind
	Field      string
	OldValue   *string
	NewValue   *string
	Actor      string
	RowVersion int64
	OccurredAt time.Time
}

// IncidentRepository administers Incidents: the work item, its lifecycle, its
// assignment, and its append-only timeline. This is the tenant-scoped,
// request-path surface (the HTTP admin API). E4.1's alert-correlation
// pipeline does NOT go through it: internal/alerting.IncidentCorrelator is a
// separate, narrower port implemented by the same *postgres.IncidentStore
// built over the PRIVILEGED pool instead — the same dual-role split
// AlertRuleStore/alerting.Store already uses — because a privileged,
// cross-tenant background worker cannot rely on this interface's ambient,
// RLS-enforced tenant boundary (ADR-TENANCY-012).
//
// incident and incident_event are TENANT-OWNED: both are in
// postgres.TenantOwnedTables and carry row-level security, so this
// repository takes no tenant argument anywhere — the bound connection
// already is the boundary (ADR-TENANCY-002), exactly like AssetRepository.
//
// There is NO Delete. An Incident is operational history the moment it
// exists — even a mis-filed one is a fact about what an operator believed at
// the time — so, like Team's archive-only lifecycle, "removing" one is
// SetStatus to IncidentClosed, not a row deletion. This is also what makes
// incident_event's foreign key to incident safe (see IncidentEvent's doc
// comment): the referenced row is never gone.
type IncidentRepository interface {
	// Create inserts an incident and its IncidentEventCreated timeline row in
	// one transaction. When inc.AssetID is set, it is re-verified against
	// this store's own tenant-scoped connection (ADR-ASSET-001 §6); a
	// non-nil id the tenant cannot see returns ErrNotFound. When
	// inc.AssigneeUserID is set, it is re-verified via an ACTIVE membership
	// row in the caller's tenant (mirrors AssetStore.verifyOwnerUserExists);
	// a user with no active membership here also returns ErrNotFound. The
	// acting identity comes from ActorFrom(ctx); an unattributable call
	// returns ErrNoActor.
	Create(ctx context.Context, inc *Incident) (*Incident, error)
	Get(ctx context.Context, incidentID string) (*Incident, error)
	// List returns a page of the caller's incidents, keyset-paginated over
	// incident_id. status, when non-empty, filters to exactly that status;
	// the zero value returns every status (unlike AssetRepository.List,
	// there is no soft-retire-style default exclusion — a closed incident is
	// still operationally relevant history, not noise to hide by default).
	List(ctx context.Context, limit int, after string, status IncidentStatus) ([]*Incident, error)
	// Update changes one or more non-lifecycle, non-assignee fields under
	// optimistic locking, per patch. Writes NO timeline row (see
	// IncidentEventKind's doc comment on why the timeline is narrower than
	// asset_change_history). AssetID, when touched to a non-empty value, is
	// re-verified exactly as Create re-verifies one. The acting identity
	// comes from ActorFrom(ctx); an unattributable call returns ErrNoActor.
	Update(ctx context.Context, incidentID string, rowVersion int64, patch IncidentPatch) (*Incident, error)
	// SetStatus performs a lifecycle transition under optimistic locking,
	// guarded by IncidentStatus.CanTransitionTo — the single authority for
	// status changes (mirrors AssetStore.SetStatus/UserStore.SetStatus). An
	// illegal move returns *IncidentTransitionError (ErrInvalidTransition).
	// Records one IncidentEventStatusTransitioned timeline row in the same
	// transaction as the UPDATE, and sets AcknowledgedAt/ResolvedAt/ClosedAt
	// when the corresponding state is entered (see those fields' doc
	// comments). The acting identity comes from ActorFrom(ctx); an
	// unattributable call returns ErrNoActor.
	SetStatus(ctx context.Context, incidentID string, rowVersion int64, status IncidentStatus) (*Incident, error)
	// Assign changes the incident's assignee under optimistic locking — the
	// single authority for that field (mirrors SetStatus's exclusivity from
	// Update). assigneeUserID nil clears the assignment; non-nil is
	// re-verified via an ACTIVE membership row in the caller's tenant before
	// being written, exactly as Create's own re-verification does — a user
	// with no active membership here returns ErrNotFound. Records one
	// IncidentEventAssigned timeline row in the same transaction as the
	// UPDATE. The acting identity comes from ActorFrom(ctx); an
	// unattributable call returns ErrNoActor.
	Assign(ctx context.Context, incidentID string, rowVersion int64, assigneeUserID *string) (*Incident, error)
	// Timeline returns a page of incidentID's append-only history,
	// keyset-paginated over event_id (a ULID, and therefore chronological
	// ascending) — the same shape AssetRepository.History uses. Never
	// returns a row this store, or any caller, can update or delete:
	// incident_event is append-only at the schema level.
	Timeline(ctx context.Context, incidentID string, limit int, after string) ([]*IncidentEvent, error)
	// OverviewCounts returns the bounded incident aggregate the NOC overview
	// projection needs (E7.1): open-class (open/acknowledged/investigating)
	// counts by status and by severity, plus the root/collateral grouping
	// summary derived from root_incident_id (E4.2). Two GROUP BY queries,
	// never a List — see IncidentOverviewCounts' own doc comment for the
	// grouping definitions.
	OverviewCounts(ctx context.Context) (*IncidentOverviewCounts, error)
	// Trends returns the RAW, ungrouped-by-bucket rows behind GET
	// /admin/dashboards/incident-trends (E9.1, ADR-NOC-008): opened rows
	// bucketed by (bucket, severity, source) over created_at, and resolved
	// rows bucketed alone over resolved_at, both confined to
	// [q.From, q.To) — half-open, so contiguous requests never double-count
	// the boundary instant. Neither return is a contiguous, zero-filled
	// series: a bucket with no matching incidents is simply absent from
	// either slice. The caller (internal/httpapi's handler) assembles the
	// contiguous series a client sees from IncidentTrendsQuery.BucketStarts,
	// after q.Validate has already bounded the request. Bucket boundaries are
	// computed by Postgres' own date_trunc forced to UTC
	// (`date_trunc($bucket, ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`),
	// which is exactly what BucketStarts reproduces on the Go side.
	Trends(ctx context.Context, q IncidentTrendsQuery) ([]IncidentOpenedTrendPoint, []IncidentResolvedTrendPoint, error)
}

// IncidentOverviewCounts is the bounded aggregate behind the NOC overview
// projection's "incidents" section (E7.1) — a computed read, never a stored
// noun (docs/PLATFORM-BUILD-PLAN.md §4). Every field is scoped to
// OPEN-CLASS incidents only (IncidentOpen, IncidentAcknowledged,
// IncidentInvestigating): resolved/closed/reopened is history the loop has
// already moved past and is deliberately excluded, unlike
// IncidentRepository.List's own no-default-exclusion shape.
type IncidentOverviewCounts struct {
	// OpenTotal is the count of every open-class incident, regardless of
	// severity — the sum of ByStatus's values.
	OpenTotal int
	// ByStatus breaks OpenTotal down by IncidentOpen/IncidentAcknowledged/
	// IncidentInvestigating. A status with zero open-class incidents is
	// simply absent, not a zero-valued entry.
	ByStatus map[IncidentStatus]int
	// BySeverity breaks OpenTotal down by severity across the same
	// open-class set. Absent, not zero, for a severity with no matches.
	BySeverity map[IncidentSeverity]int
	// CollateralCount is the number of open-class incidents whose
	// root_incident_id is non-NULL (E4.2): incidents the topology-aware
	// grouping reconciler has attributed to another, deeper root.
	CollateralCount int
	// RootCount is the number of DISTINCT root_incident_id values named by
	// CollateralCount's incidents — how many separate roots currently have
	// at least one open collateral incident grouped under them. It is not
	// "how many open incidents are themselves a root" (an incident can be a
	// root of nothing, i.e. standalone, without appearing here at all).
	RootCount int
}

// IncidentTrendBucket is the closed vocabulary of time-bucket granularities
// GET /admin/dashboards/incident-trends accepts (E9.1, ADR-NOC-008). Closed,
// not open text: bucket width is what MaxIncidentTrendBuckets bounds a
// request by, and an arbitrary caller-chosen duration would make that bound
// meaningless — the same "closed because a downstream decision keys off it"
// reasoning IncidentSource's own doc comment gives.
type IncidentTrendBucket string

const (
	IncidentTrendBucketHour IncidentTrendBucket = "hour"
	IncidentTrendBucketDay  IncidentTrendBucket = "day"
)

// Valid reports whether b is a defined bucket granularity.
func (b IncidentTrendBucket) Valid() bool {
	switch b {
	case IncidentTrendBucketHour, IncidentTrendBucketDay:
		return true
	default:
		return false
	}
}

// Duration is the wall-clock width of one bucket of this granularity. It is
// the ONLY place that width is stated; IncidentTrendsQuery.BucketCount/
// BucketStarts both call it rather than re-deriving it, and it is what the
// Postgres store's date_trunc argument corresponds to.
func (b IncidentTrendBucket) Duration() time.Duration {
	switch b {
	case IncidentTrendBucketHour:
		return time.Hour
	case IncidentTrendBucketDay:
		return 24 * time.Hour
	default:
		return 0
	}
}

// MaxIncidentTrendBuckets bounds how many contiguous buckets one request may
// span — a month of hourly buckets (24*31), the same order-of-magnitude
// honesty ADR-NOC-001 §5 records for its own v1 on-call cap. Enforced by
// IncidentTrendsQuery.Validate BEFORE any query runs: a caller-chosen
// combination of a fine bucket and a wide window is refused as a bounding
// problem (422), never answered as an oversized response.
const MaxIncidentTrendBuckets = 744

// IncidentTrendsQuery is GET /admin/dashboards/incident-trends' validated,
// bounded request (E9.1): a half-open [From, To) window at Bucket
// granularity. Half-open so a caller paging contiguous windows back-to-back
// (e.g. week by week) never double-counts the shared boundary instant.
type IncidentTrendsQuery struct {
	From   time.Time
	To     time.Time
	Bucket IncidentTrendBucket
}

// Validate enforces every invariant the store depends on already having been
// checked — the same validate-before-store discipline AdminAuditFilter.
// Validate establishes: a bad request is refused at the API's own edge
// (422), not discovered by the store running an unboundedly wide GROUP BY.
func (q *IncidentTrendsQuery) Validate() error {
	if !q.Bucket.Valid() {
		return NewValidationError("bucket", "must be one of: hour, day")
	}
	if q.From.IsZero() {
		return NewValidationError("from", "is required")
	}
	if q.To.IsZero() {
		return NewValidationError("to", "is required")
	}
	if !q.To.After(q.From) {
		return NewValidationError("to", "must be after from")
	}
	if q.BucketCount() > MaxIncidentTrendBuckets {
		return NewValidationError("to",
			"the requested window is too wide for this bucket size — at most 744 buckets "+
				"(a month of hourly buckets) are returned per request; narrow the window or choose a coarser bucket")
	}
	return nil
}

// BucketCount returns the number of contiguous buckets [From, To) produces at
// Bucket granularity. The FIRST bucket is anchored to From's own truncated
// boundary — the same truncation Postgres' own
// `date_trunc(bucket, created_at AT TIME ZONE 'UTC')` performs — not to the
// exact, possibly off-boundary, instant a caller supplied. Used both to
// enforce MaxIncidentTrendBuckets before any query runs and to size the
// zero-filled series BucketStarts produces. Zero for an invalid/zero-value
// query (Bucket unset, or To not after From).
func (q IncidentTrendsQuery) BucketCount() int {
	step := q.Bucket.Duration()
	if step <= 0 {
		return 0
	}
	from := q.From.UTC().Truncate(step)
	if !q.To.After(from) {
		return 0
	}
	diff := q.To.UTC().Sub(from)
	n := int(diff / step)
	if diff%step != 0 {
		n++
	}
	return n
}

// BucketStarts returns the contiguous, boundary-aligned bucket_start values
// [From, To) produces, ascending — the "shape" of a zero-filled series before
// any count is attached to it. nil when BucketCount is 0.
func (q IncidentTrendsQuery) BucketStarts() []time.Time {
	n := q.BucketCount()
	if n == 0 {
		return nil
	}
	step := q.Bucket.Duration()
	start := q.From.UTC().Truncate(step)
	out := make([]time.Time, n)
	for i := range out {
		out[i] = start.Add(time.Duration(i) * step)
	}
	return out
}

// IncidentOpenedTrendPoint is one (bucket, severity, source) row of the
// opened series behind GET /admin/dashboards/incident-trends (E9.1) — the
// store's raw, ungrouped-by-bucket output. BucketStart is already the
// boundary-aligned value Postgres' date_trunc produced (UTC).
type IncidentOpenedTrendPoint struct {
	BucketStart time.Time
	Severity    IncidentSeverity
	Source      IncidentSource
	Count       int
}

// IncidentResolvedTrendPoint is one bucket's resolved count (E9.1) — bucketed
// over resolved_at alone, with no severity/source breakdown (the response DTO
// mirrors this: resolved_total is a single number per bucket, see
// docs/PLATFORM-BUILD-PLAN.md E9.1).
type IncidentResolvedTrendPoint struct {
	BucketStart time.Time
	Count       int
}

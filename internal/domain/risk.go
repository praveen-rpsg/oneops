package domain

import (
	"context"
	"strings"
	"time"
)

// RiskLikelihood is the closed, five-member "how likely" scale of the
// classic likelihood x impact risk matrix (E8.4a). It is its own vocabulary
// — NOT a reuse of VulnFindingSeverity/AssetCriticality/IncidentSeverity —
// for the same reason VulnFindingSeverity's own doc comment gives for not
// reusing IncidentSeverity/ObservationSeverity: it names a fourth,
// independent scale (an operator's own probability judgment about a named
// risk), not a scanner-reported CVSS rating, a CI's business-impact tier, or
// an incident's operational severity.
type RiskLikelihood string

const (
	RiskLikelihoodRare          RiskLikelihood = "rare"
	RiskLikelihoodUnlikely      RiskLikelihood = "unlikely"
	RiskLikelihoodPossible      RiskLikelihood = "possible"
	RiskLikelihoodLikely        RiskLikelihood = "likely"
	RiskLikelihoodAlmostCertain RiskLikelihood = "almost_certain"
)

// Valid reports whether l is a defined likelihood.
func (l RiskLikelihood) Valid() bool {
	switch l {
	case RiskLikelihoodRare, RiskLikelihoodUnlikely, RiskLikelihoodPossible,
		RiskLikelihoodLikely, RiskLikelihoodAlmostCertain:
		return true
	default:
		return false
	}
}

// Rank is the 1-BASED position of l in its own natural order — see
// RiskRepository.Register's doc comment for why 1-based (never 0-based):
// starting at 1 keeps every likelihood x impact product strictly positive, so
// a "rare" likelihood never zeroes out a "severe" impact's own weight. Rank
// returns 0 for an invalid value; callers that reach this store-side always
// hold an already-Valid RiskLikelihood.
func (l RiskLikelihood) Rank() int {
	switch l {
	case RiskLikelihoodRare:
		return 1
	case RiskLikelihoodUnlikely:
		return 2
	case RiskLikelihoodPossible:
		return 3
	case RiskLikelihoodLikely:
		return 4
	case RiskLikelihoodAlmostCertain:
		return 5
	default:
		return 0
	}
}

// RiskImpact is the closed, five-member "how bad" scale of the risk matrix
// (E8.4a) — its own vocabulary, independent of RiskLikelihood and every
// other severity-shaped scale in this codebase, for the identical reason
// RiskLikelihood's own doc comment states.
type RiskImpact string

const (
	RiskImpactNegligible RiskImpact = "negligible"
	RiskImpactMinor      RiskImpact = "minor"
	RiskImpactModerate   RiskImpact = "moderate"
	RiskImpactMajor      RiskImpact = "major"
	RiskImpactSevere     RiskImpact = "severe"
)

// Valid reports whether i is a defined impact.
func (i RiskImpact) Valid() bool {
	switch i {
	case RiskImpactNegligible, RiskImpactMinor, RiskImpactModerate, RiskImpactMajor, RiskImpactSevere:
		return true
	default:
		return false
	}
}

// Rank is the 1-based position of i in its own natural order — see
// RiskLikelihood.Rank's doc comment for why 1-based.
func (i RiskImpact) Rank() int {
	switch i {
	case RiskImpactNegligible:
		return 1
	case RiskImpactMinor:
		return 2
	case RiskImpactModerate:
		return 3
	case RiskImpactMajor:
		return 4
	case RiskImpactSevere:
		return 5
	default:
		return 0
	}
}

// RiskTreatment is the closed, OPTIONAL disposition an operator records for
// how a risk is being handled (ISO-31000-style: mitigate/accept/transfer/
// avoid). Optional — a freshly registered risk may carry no treatment
// decision yet — so Risk.Treatment is *RiskTreatment, not RiskTreatment.
type RiskTreatment string

const (
	RiskTreatmentMitigate RiskTreatment = "mitigate"
	RiskTreatmentAccept   RiskTreatment = "accept"
	RiskTreatmentTransfer RiskTreatment = "transfer"
	RiskTreatmentAvoid    RiskTreatment = "avoid"
)

// Valid reports whether t is a defined treatment.
func (t RiskTreatment) Valid() bool {
	switch t {
	case RiskTreatmentMitigate, RiskTreatmentAccept, RiskTreatmentTransfer, RiskTreatmentAvoid:
		return true
	default:
		return false
	}
}

// RiskStatus is the lifecycle of a Risk — a stateful reified entity (like
// Incident/VulnFinding/Asset, NOT a false noun): an identified risk persists
// in the register, moving through operator-driven review, until it is
// closed. Mirrors VulnFindingStatus/IncidentStatus's state-machine
// treatment.
type RiskStatus string

const (
	// RiskOpen is the status every risk is minted in: identified, not yet
	// under active treatment or accepted.
	RiskOpen RiskStatus = "open"
	// RiskMitigating: an operator has active mitigation work under way
	// against this risk. Not terminal — see riskTransitions.
	RiskMitigating RiskStatus = "mitigating"
	// RiskAccepted: an operator has made a standing decision to accept this
	// risk as-is (RiskTreatmentAccept is the usual, but not required,
	// companion Treatment value). Reachable back to open — an acceptance is
	// a decision that can be revisited, not a permanent write-off.
	RiskAccepted RiskStatus = "accepted"
	// RiskClosed: the risk no longer applies (resolved, materialised and
	// handled elsewhere, or superseded). Reachable back to open — a closed
	// risk can be reopened, mirroring IncidentStatus's own closed->reopened
	// edge, rather than requiring a fresh row for a risk that resurfaces.
	RiskClosed RiskStatus = "closed"
)

// Valid reports whether s is a defined status.
func (s RiskStatus) Valid() bool {
	switch s {
	case RiskOpen, RiskMitigating, RiskAccepted, RiskClosed:
		return true
	default:
		return false
	}
}

// Open reports whether s counts as part of the OPEN register
// (RiskRepository.Register) — every status except closed. Named to mirror
// Incident.Open's identical "not closed" reading, applied here to the status
// value itself rather than to a struct method, since Register needs the
// predicate at the SQL layer too (see riskOpenSQL).
func (s RiskStatus) OpenForRegister() bool { return s != RiskClosed }

// riskTransitions is the whole lifecycle, as data rather than a chain of
// conditionals — mirrors vulnFindingTransitions/incidentTransitions. A
// transition absent here, including every self-transition, is refused.
var riskTransitions = map[RiskStatus][]RiskStatus{
	RiskOpen:       {RiskMitigating, RiskAccepted, RiskClosed},
	RiskMitigating: {RiskAccepted, RiskClosed, RiskOpen},
	RiskAccepted:   {RiskOpen, RiskClosed},
	RiskClosed:     {RiskOpen},
}

// CanTransitionTo reports whether from -> to is a permitted lifecycle move.
func (s RiskStatus) CanTransitionTo(to RiskStatus) bool {
	for _, allowed := range riskTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// RiskTransitionError is a refused lifecycle move — mirrors
// VulnFindingTransitionError/IncidentTransitionError exactly, scoped to
// RiskStatus.
type RiskTransitionError struct {
	From RiskStatus
	To   RiskStatus
}

func (e *RiskTransitionError) Error() string {
	return "invalid lifecycle transition: " + string(e.From) + " -> " + string(e.To)
}

// Unwrap makes errors.Is(err, ErrInvalidTransition) true for this type.
func (e *RiskTransitionError) Unwrap() error { return ErrInvalidTransition }

// NewRiskTransitionError builds a *RiskTransitionError naming both ends.
func NewRiskTransitionError(from, to RiskStatus) *RiskTransitionError {
	return &RiskTransitionError{From: from, To: to}
}

// MaxRiskTitleLength bounds Title, mirroring MaxIncidentTitleLength — a
// short, operator-authored label, not prose.
const MaxRiskTitleLength = 200

// MaxRiskDescriptionLength bounds the optional free-text narrative,
// mirroring MaxIncidentDescriptionLength — a risk's write-up (context,
// evidence, rationale) is closer in shape to an incident's narrative than to
// a vuln finding's short scanner-supplied blurb.
const MaxRiskDescriptionLength = 10000

// MaxRiskCategoryLength bounds Category, mirroring MaxAssetTypeLength — an
// open-but-bounded label, not a closed enum (a risk taxonomy is an
// organisational choice this layer should not have to be amended to accept).
const MaxRiskCategoryLength = 100

// riskCategoryPattern reuses assetTypePattern: the same lower-case
// snake_case discipline, for the same reason (safe to appear in a filter, an
// index, a console label), applied here only when Category is non-blank —
// unlike Asset.Type, Category is OPTIONAL, so the blank value ("no category
// given") is itself valid and skips this pattern check.
var riskCategoryPattern = assetTypePattern

// MaxRiskScore is likelihoodRank x impactRank's own maximum (5 x 5 — almost
// certain likelihood, severe impact; see RiskRepository.Register's doc
// comment for both 1-based rank tables). RiskSeverityBandOf splits the
// closed range [1, MaxRiskScore] into four equal-width bands (ceil(25/4) = 7
// wide), the identical banding discipline VulnFindingPriorityOf uses over
// its own, differently-sourced [1, 25] range — the two happen to share a
// numeric range because both factors are 1..5 scales, not because either
// vocabulary is reused.
const MaxRiskScore = 25

// RiskSeverityBand is the closed, human-facing LABEL GET
// /v1/admin/risks/register attaches to each row (E8.4a) — a value COMPUTED
// at request time from a RiskRegisterEntry's own Score, never a persisted
// column (there is no risk.severity_band in the schema). It deliberately
// reuses NEITHER VulnFindingPriority NOR any other closed vocabulary in this
// codebase, for the exact reason VulnFindingPriority's own doc comment gives
// for not reusing IncidentSeverity/VulnFindingSeverity: it names its own
// scale, derived from RiskLikelihood x RiskImpact, that happens to share the
// same four adjacent names every operational-urgency tier in this codebase
// already uses.
type RiskSeverityBand string

const (
	RiskSeverityBandCritical RiskSeverityBand = "critical"
	RiskSeverityBandHigh     RiskSeverityBand = "high"
	RiskSeverityBandMedium   RiskSeverityBand = "medium"
	RiskSeverityBandLow      RiskSeverityBand = "low"
)

// Valid reports whether b is a defined band label.
func (b RiskSeverityBand) Valid() bool {
	switch b {
	case RiskSeverityBandCritical, RiskSeverityBandHigh, RiskSeverityBandMedium, RiskSeverityBandLow:
		return true
	default:
		return false
	}
}

// RiskSeverityBandOf maps a risk score (as RiskRepository.Register computes
// and RiskRegisterEntry.Score carries) to its human-facing label: four
// equal-width bands over [1, 25] — low 1-6, medium 7-12, high 13-18,
// critical 19-25 — the identical boundaries VulnFindingPriorityOf uses over
// its own, differently-sourced range.
func RiskSeverityBandOf(score int) RiskSeverityBand {
	switch {
	case score >= 19:
		return RiskSeverityBandCritical
	case score >= 13:
		return RiskSeverityBandHigh
	case score >= 7:
		return RiskSeverityBandMedium
	default:
		return RiskSeverityBandLow
	}
}

// Risk is one entry in the tenant's risk register (E8.4a): a legitimate
// stateful reified entity — like Incident/VulnFinding/Asset, NOT a false
// noun — because a risk is operator-authored, persists through review and
// treatment, and carries a governed lifecycle (RiskStatus). It is the
// foundation of risk management (E8.4); compliance controls/evidence
// (E8.4b) and continuous-audit automation are separate, later stories
// (ADR-SOC-008).
//
// Unlike VulnFinding, a Risk carries no natural dedup key: it is
// operator-authored, not scan-deduped, so risk_id (server-minted) is the
// only identity a caller ever supplies.
type Risk struct {
	RiskID   string
	TenantID string
	Title    string
	// Description is optional, operator-supplied free text: context,
	// evidence, rationale.
	Description string
	// Category is an optional, open-but-bounded label (e.g. "operational",
	// "financial", "compliance", "security") — NOT a closed enum: an
	// organisation's own risk taxonomy is not this layer's decision to make,
	// mirroring Asset.Source's identical "open but validated" treatment.
	// Blank means uncategorised.
	Category   string
	Likelihood RiskLikelihood
	Impact     RiskImpact
	Status     RiskStatus
	// Treatment is the operator's OPTIONAL disposition decision (mitigate/
	// accept/transfer/avoid), or nil when not yet decided.
	Treatment *RiskTreatment
	// AssetID names the Configuration Item this risk concerns, or nil when
	// the risk is not tied to one (an organisational/process risk, e.g.
	// "single vendor dependency", concerns no single CI). When set, it is
	// re-verified against the caller's own tenant-scoped connection before
	// being written — the same defense Incident.AssetID's doc comment
	// describes (ADR-ASSET-001 §6). ON DELETE SET NULL on the FK: a
	// hard-deleted CI clears the link, it does not delete the risk — a risk
	// is itself a standalone record with its own value, mirroring
	// Incident.AssetID exactly.
	//
	// SourceFindingID (a risk raised FROM a vuln_finding) is deliberately
	// OMITTED from this first cut — not a trivial addition, since it would
	// need its own cross-tenant re-verification against vuln_finding plus a
	// decision about what happens to the link across a finding's own
	// lifecycle transitions, and nothing in E8.4a's scope requires it (a
	// risk raised from a finding can simply name the same asset_id and say
	// so in Description today). A follow-up story can add it without
	// migrating this one.
	//
	// OwnerTeamID/OwnerUserID (accountability, mirroring Asset's) are
	// likewise OMITTED from this first cut: E8.4a's value is the register
	// and its scoring projection, not ownership workflow, and adding them
	// would mean two more cross-tenant existence checks
	// (verifyOwnerTeamExists/verifyOwnerUserExists) on every write for a
	// capability this story does not need. A follow-up story can add them as
	// a pure additive PATCH field.
	AssetID    *string
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Validate enforces the invariants the database also enforces, so a bad risk
// fails with a field-level reason before it reaches the store.
func (r *Risk) Validate() error {
	if strings.TrimSpace(r.RiskID) == "" {
		return newValidation("risk_id", "must not be empty")
	}
	if strings.TrimSpace(r.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	title := strings.TrimSpace(r.Title)
	if title == "" {
		return newValidation("title", "must not be empty")
	}
	if len(title) > MaxRiskTitleLength {
		return newValidation("title", "must be at most 200 characters")
	}
	if len(r.Description) > MaxRiskDescriptionLength {
		return newValidation("description", "must be at most 10000 characters")
	}
	category := strings.TrimSpace(r.Category)
	if category != "" {
		if len(category) > MaxRiskCategoryLength {
			return newValidation("category", "must be at most 100 characters")
		}
		if !riskCategoryPattern.MatchString(category) {
			return newValidation("category", "must be lower-case snake_case, e.g. operational, compliance, security")
		}
	}
	if !r.Likelihood.Valid() {
		return newValidation("likelihood", "must be one of: rare, unlikely, possible, likely, almost_certain")
	}
	if !r.Impact.Valid() {
		return newValidation("impact", "must be one of: negligible, minor, moderate, major, severe")
	}
	if !r.Status.Valid() {
		return newValidation("status", "must be one of: open, mitigating, accepted, closed")
	}
	if r.Treatment != nil && !r.Treatment.Valid() {
		return newValidation("treatment", "must be one of: mitigate, accept, transfer, avoid")
	}
	if r.AssetID != nil && strings.TrimSpace(*r.AssetID) == "" {
		return newValidation("asset_id", "must not be blank when supplied; omit it to leave the risk unlinked")
	}
	return nil
}

// NewRisk builds a validated, freshly-minted, open Risk. tenantID is always
// taken from the bound connection's context, never from request data, the
// same rule NewVulnFinding/NewIncident follow. category/treatment/assetID
// are each optional; category is trimmed, treatment and assetID are taken
// as-is (nil means "not supplied").
func NewRisk(
	tenantID, title, description, category string,
	likelihood RiskLikelihood, impact RiskImpact, treatment *RiskTreatment, assetID *string,
) (*Risk, error) {
	r := &Risk{
		RiskID:      NewID(),
		TenantID:    strings.TrimSpace(tenantID),
		Title:       strings.TrimSpace(title),
		Description: description,
		Category:    strings.TrimSpace(category),
		Likelihood:  likelihood,
		Impact:      impact,
		Status:      RiskOpen,
		Treatment:   treatment,
		AssetID:     assetID,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// RiskPatch carries the fields RiskRepository.Update may change; a nil
// pointer leaves that field unchanged. Status is deliberately absent — the
// ONLY way to move a risk's lifecycle is RiskRepository.SetStatus, mirroring
// AssetPatch/IncidentPatch's identical split of "ordinary field edit" from
// "lifecycle transition" (never both in the same call, enforced by the
// transport layer, not here).
//
// Category/Treatment/AssetID follow the tri-state rule Asset.OwnerTeamID
// established: absent (nil) leaves the field unchanged; present as "" clears
// it (Category to uncategorised, Treatment/AssetID to unset); present as a
// non-empty value sets it (Treatment/AssetID validated/re-verified first).
type RiskPatch struct {
	Title       *string
	Description *string
	Category    *string
	Likelihood  *RiskLikelihood
	Impact      *RiskImpact
	Treatment   *string
	AssetID     *string
}

// RiskRegisterEntry is one row of GET /v1/admin/risks/register (E8.4a): a
// Risk plus its computed score/band — see RiskRepository.Register's doc
// comment. Never a stored row — a transient projection assembled at request
// time (ADR-NOC-001's reduced-concept pattern, applied here, mirroring
// PrioritizedVulnFinding exactly): no "score"/"band" column exists on risk,
// and both are recomputed identically on every call.
type RiskRegisterEntry struct {
	Risk Risk
	// Score is likelihoodRank(Risk.Likelihood) * impactRank(Risk.Impact) —
	// see RiskRepository.Register's doc comment for both rank tables and why
	// they are 1-based.
	Score int
	// Band is RiskSeverityBandOf(Score) — the human-facing label.
	Band RiskSeverityBand
}

// RiskRepository administers risk: the tenant-owned risk-register stateful
// entity, its governed lifecycle, and the computed likelihood x impact
// scoring projection (E8.4a, ADR-SOC-008). Compliance controls/evidence
// (E8.4b) and continuous-audit automation are OUT of scope here — a
// deliberately separate, later story.
//
// risk is TENANT-OWNED: it is in postgres.TenantOwnedTables and carries
// row-level security, so this repository takes no tenant argument anywhere
// — the bound connection already is the boundary (ADR-TENANCY-002), exactly
// like VulnFindingRepository/IncidentRepository.
//
// There is NO Delete. Like Incident/VulnFinding, a risk is operational
// history the moment it is registered — even a since-superseded risk
// records what was once judged true — so "removing" one is a lifecycle
// transition (SetStatus to closed), not a row deletion (ADR-HARD-003).
type RiskRepository interface {
	// Create inserts a risk. When r.AssetID is set, it is re-verified
	// against this store's own tenant-scoped connection before the row is
	// written (ADR-ASSET-001 §6, mirroring IncidentRepository.Create); an
	// asset_id the tenant cannot see returns ErrNotFound.
	Create(ctx context.Context, r *Risk) (*Risk, error)

	Get(ctx context.Context, riskID string) (*Risk, error)

	// List returns a page of the caller's risks, keyset-paginated over
	// risk_id. status/category, each non-empty, filter to exactly that
	// value; the zero value for each returns every match on the other
	// filter — mirrors VulnFindingRepository.List's filter shape.
	List(ctx context.Context, limit int, after string, status RiskStatus, category string) ([]*Risk, error)

	// Update changes one or more non-lifecycle fields of patch under
	// optimistic locking. Returns ErrVersionMismatch on a stale rowVersion,
	// ErrNotFound if riskID does not name a risk visible to this tenant, or
	// if patch.AssetID names an asset the caller's tenant cannot see.
	Update(ctx context.Context, riskID string, rowVersion int64, patch RiskPatch) (*Risk, error)

	// SetStatus performs a lifecycle transition under optimistic locking,
	// guarded by RiskStatus.CanTransitionTo — the single authority for
	// status changes (mirrors VulnFindingRepository.SetStatus/
	// AssetStore.SetStatus). An illegal move returns *RiskTransitionError
	// (ErrInvalidTransition).
	SetStatus(ctx context.Context, riskID string, rowVersion int64, status RiskStatus) (*Risk, error)

	// Register returns the caller's OPEN (non-closed: open, mitigating or
	// accepted) risks ranked by risk score — the classic likelihood x impact
	// risk matrix — computed FRESH on every call. This is a PROJECTION,
	// never stored: see RiskRegisterEntry's own doc comment for why nothing
	// here is a persisted column.
	//
	// Ranking definition, exactly:
	//
	//   score = likelihoodRank(risk.Likelihood) * impactRank(risk.Impact)
	//
	// where both ranks are 1-BASED (not 0-based) over their own five-member
	// closed vocabularies, in each vocabulary's own natural order:
	//
	//   likelihoodRank: rare=1, unlikely=2, possible=3, likely=4, almost_certain=5
	//   impactRank:     negligible=1, minor=2, moderate=3, major=4, severe=5
	//
	// Both scales start at 1, deliberately — the identical reasoning
	// VulnFindingRepository.Prioritized's doc comment gives for its own two
	// rank tables, applied here: a 0-based "rare"/"negligible" rank would
	// make the PRODUCT zero regardless of the other factor, silently sinking
	// a severe-impact risk merely judged rare to the very bottom of the
	// register alongside genuinely trivial rows. Starting at 1 keeps every
	// combination strictly positive.
	//
	// Ties (equal score) are broken by updated_at DESC (a risk touched more
	// recently outranks one that has gone stale), then risk_id ascending —
	// a total, deterministic order, never "whatever the database felt like
	// returning this time."
	//
	// Bounded to at most limit rows (clamped exactly like List's own page
	// size); this is a ranked TOP-N view, not a page-through-everything
	// list, mirroring VulnFindingRepository.Prioritized's identical
	// bounded-projection shape.
	Register(ctx context.Context, limit int) ([]RiskRegisterEntry, error)
}

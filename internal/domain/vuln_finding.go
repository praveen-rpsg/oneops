package domain

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// VulnFindingSeverity is the CVSS-derived severity rating of a vulnerability
// finding (E8.3a). It is its own closed vocabulary — NEITHER IncidentSeverity
// NOR ObservationSeverity — because it names a THIRD, independent scale: the
// industry-standard CVSS qualitative rating (NVD: none/low/medium/high/
// critical), which a scanner reports as a fact about the vulnerability
// itself, not an operational-impact tier (IncidentSeverity, no "none" member
// — an Incident is opened BECAUSE something is known to be wrong) nor a SIEM
// observation's severity (ObservationSeverity, "info" not "none" — a fact
// about how notable an observed EVENT was, not a CVSS score). Reusing either
// would conflate three different things that happen to share adjacent names.
type VulnFindingSeverity string

const (
	VulnFindingSeverityNone     VulnFindingSeverity = "none"
	VulnFindingSeverityLow      VulnFindingSeverity = "low"
	VulnFindingSeverityMedium   VulnFindingSeverity = "medium"
	VulnFindingSeverityHigh     VulnFindingSeverity = "high"
	VulnFindingSeverityCritical VulnFindingSeverity = "critical"
)

// Valid reports whether s is a defined CVSS-derived severity.
func (s VulnFindingSeverity) Valid() bool {
	switch s {
	case VulnFindingSeverityNone, VulnFindingSeverityLow, VulnFindingSeverityMedium,
		VulnFindingSeverityHigh, VulnFindingSeverityCritical:
		return true
	default:
		return false
	}
}

// VulnFindingStatus is the lifecycle of a VulnFinding — a stateful reified
// entity (like Incident/Asset, NOT a false noun): a vulnerability persists on
// an asset until remediated, is deduped by (tenant_id, asset_id, vuln_id),
// and carries a governed lifecycle, mirroring IncidentStatus/UserStatus'
// state-machine treatment.
type VulnFindingStatus string

const (
	// VulnFindingOpen is the status every finding is minted in (fresh from
	// ingestion) and the status a remediated/accepted-risk finding RETURNS to
	// when a later scan sees it again — see the ingestion UPSERT rule on
	// VulnFindingRepository.Upsert's doc comment.
	VulnFindingOpen VulnFindingStatus = "open"
	// VulnFindingRemediated: an operator (or a later, separate remediation
	// workflow — E8.3b) believes the vulnerability is fixed. Not terminal: a
	// later scan that still detects it transitions it straight back to open
	// — a remediation that did not hold is itself a fact worth surfacing
	// immediately, unlike Incident's resolved->reopened distinction (there is
	// no separate "remediation failed" state; it is simply open again).
	VulnFindingRemediated VulnFindingStatus = "remediated"
	// VulnFindingAcceptedRisk: an operator has decided not to fix this
	// finding for now. Like remediated, a later scan reappearance returns it
	// to open — an accepted risk is a standing decision about TODAY's
	// finding, not a blanket suppression of every future scan that detects
	// the same vuln_id on the same asset; re-accepting it is a fresh,
	// deliberate act the operator repeats via SetStatus.
	VulnFindingAcceptedRisk VulnFindingStatus = "accepted_risk"
	// VulnFindingFalsePositive: an operator has determined the scanner is
	// simply wrong about this finding. UNLIKE remediated/accepted_risk, a
	// scan reappearance does NOT reopen it — see Upsert's doc comment: a
	// scanner re-reporting the same false alarm is not new evidence, and
	// silently overturning the operator's own judgment on every re-scan
	// would make the status meaningless. It is reachable back to open only
	// through a deliberate manual SetStatus (a human re-triage decision),
	// never through ingestion.
	VulnFindingFalsePositive VulnFindingStatus = "false_positive"
)

// Valid reports whether s is a defined status.
func (s VulnFindingStatus) Valid() bool {
	switch s {
	case VulnFindingOpen, VulnFindingRemediated, VulnFindingAcceptedRisk, VulnFindingFalsePositive:
		return true
	default:
		return false
	}
}

// vulnFindingTransitions is the whole lifecycle, as data rather than a chain
// of conditionals — mirrors incidentTransitions/assetTransitions. A
// transition absent here is refused.
//
// Every non-open state transitions back to open, but by two different
// authorities: remediated/accepted_risk are also reopened AUTOMATICALLY by
// Upsert on a scan reappearance (see its doc comment), while
// false_positive->open is reachable ONLY through this map's manual SetStatus
// path — Upsert never calls it. false_positive is deliberately NOT modelled
// as fully terminal (unlike IncidentClosed): a human who marked a finding as
// a false positive must have a way to correct that later re-triage without
// this table also growing a Delete method, which the design does not call
// for (E8.3a scope — see VulnFindingRepository's doc comment).
var vulnFindingTransitions = map[VulnFindingStatus][]VulnFindingStatus{
	VulnFindingOpen:          {VulnFindingRemediated, VulnFindingAcceptedRisk, VulnFindingFalsePositive},
	VulnFindingRemediated:    {VulnFindingOpen},
	VulnFindingAcceptedRisk:  {VulnFindingOpen},
	VulnFindingFalsePositive: {VulnFindingOpen},
}

// CanTransitionTo reports whether from -> to is a permitted lifecycle move. A
// transition to the current state is refused: it is a no-op that would
// consume a row version for nothing, mirroring IncidentStatus.CanTransitionTo.
func (s VulnFindingStatus) CanTransitionTo(to VulnFindingStatus) bool {
	for _, allowed := range vulnFindingTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// VulnFindingTransitionError is a refused lifecycle move — mirrors
// IncidentTransitionError exactly, scoped to VulnFindingStatus.
type VulnFindingTransitionError struct {
	From VulnFindingStatus
	To   VulnFindingStatus
}

func (e *VulnFindingTransitionError) Error() string {
	return "invalid lifecycle transition: " + string(e.From) + " -> " + string(e.To)
}

// Unwrap makes errors.Is(err, ErrInvalidTransition) true for this type.
func (e *VulnFindingTransitionError) Unwrap() error { return ErrInvalidTransition }

// NewVulnFindingTransitionError builds a *VulnFindingTransitionError naming
// both ends.
func NewVulnFindingTransitionError(from, to VulnFindingStatus) *VulnFindingTransitionError {
	return &VulnFindingTransitionError{From: from, To: to}
}

// MaxVulnFindingVulnIDLength bounds VulnID. Generous enough for the longest
// common scanner identifiers (CVE-YYYY-NNNNNNN, GHSA-xxxx-xxxx-xxxx,
// RHSA-YYYY:NNNN, vendor advisory codes) while still bounded, the same
// "bounded, not unbounded" discipline every other identifier-like field in
// this epic follows.
const MaxVulnFindingVulnIDLength = 100

// vulnFindingVulnIDPattern is deliberately NOT a CVE-only pattern — scanners
// emit non-CVE identifiers too (GHSA-*, RHSA-*, vendor-specific advisory
// codes, internal plugin ids) — only a safe, generous charset: it must start
// alphanumeric and may contain letters, digits, '.', '_', ':' and '-'
// afterward (no whitespace/control characters, since this value reaches an
// index, a filter and a console label).
var vulnFindingVulnIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// MaxVulnFindingTitleLength bounds Title. Wider than MaxIncidentTitleLength:
// scanner-reported plugin/finding names run longer than an operator-authored
// incident title, but are still a label, not prose.
const MaxVulnFindingTitleLength = 300

// MaxVulnFindingScannerLength bounds Scanner (the ingesting tool's name,
// e.g. "nessus", "qualys", "trivy") — open-but-bounded, mirroring
// MaxObservationSourceLength/CollectorCheck.Target's split.
const MaxVulnFindingScannerLength = 200

// MaxVulnFindingDescriptionLength bounds the optional free-text field,
// mirroring MaxIOCDescriptionLength.
const MaxVulnFindingDescriptionLength = 2000

// MaxVulnFindingIngestBatch bounds one scan ingest call, mirroring
// MaxSecurityObservationIngestBatch — a single host scan can easily report
// hundreds of CVEs, so this is generous, but a batch is still bounded, never
// unbounded.
const MaxVulnFindingIngestBatch = 1000

// VulnFinding is one deduplicated vulnerability-on-asset finding (E8.3a): a
// legitimate stateful reified entity — like Incident/Asset, NOT a false noun
// — because a vulnerability PERSISTS on an asset until remediated, is
// deduped by (tenant_id, asset_id, vuln_id), and carries a governed
// lifecycle (VulnFindingStatus). It is the foundation of vulnerability
// management (E8.3); prioritization by CI criticality and remediation ->
// Incident linkage are E8.3b, a later, separate story — nothing here ranks
// findings or opens an Incident.
type VulnFinding struct {
	FindingID string
	TenantID  string
	// AssetID names the affected Configuration Item. It is re-verified
	// against the caller's own tenant-scoped connection before being
	// written — the same defense AlertRule.AssetID/SecurityRule.AssetID
	// carry (ADR-ASSET-001 §6).
	AssetID string
	// VulnID names the vulnerability itself (e.g. a CVE like
	// "CVE-2024-1234", but deliberately not CVE-format-only — see
	// vulnFindingVulnIDPattern's doc comment). Together with (TenantID,
	// AssetID) this is the row's dedup key (UNIQUE (tenant_id, asset_id,
	// vuln_id)) — one finding per asset per vulnerability.
	VulnID string
	// Title is a short, human-readable label for the finding (the scanner's
	// plugin/finding name, or the vulnerability's own title).
	Title string
	// Severity is the CVSS-derived rating — see VulnFindingSeverity's doc
	// comment for why it is its own vocabulary.
	Severity VulnFindingSeverity
	Status   VulnFindingStatus
	// FirstSeen/LastSeen are store-managed facts, not caller-set fields —
	// the same "derived write-time fact" treatment Asset.LastSeen and
	// Incident.AcknowledgedAt/ResolvedAt/ClosedAt get. FirstSeen is set once,
	// at INSERT; LastSeen is refreshed on every ingestion UPSERT that touches
	// this row (see VulnFindingRepository.Upsert's doc comment), including
	// the one exception that leaves every other field untouched
	// (false_positive).
	FirstSeen time.Time
	LastSeen  time.Time
	// Scanner names the producer that reported this finding (e.g. "nessus",
	// "qualys", "trivy") — open-but-bounded provenance, mirroring
	// SecurityObservation.Source, and REQUIRED for the identical reason: a
	// finding with no stated source is not a usable fact.
	Scanner string
	// Description is optional, scanner- or operator-supplied free text.
	Description string
	RowVersion  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate enforces the invariants the database also enforces, so a bad
// finding fails with a field-level reason before it reaches the store.
// FirstSeen/LastSeen/CreatedAt/UpdatedAt are store-managed and deliberately
// NOT checked here — the same omission SecurityRule.Validate makes for its
// own CreatedAt/UpdatedAt/LastTransitionAt.
func (f *VulnFinding) Validate() error {
	if strings.TrimSpace(f.FindingID) == "" {
		return newValidation("finding_id", "must not be empty")
	}
	if strings.TrimSpace(f.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(f.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	vulnID := strings.TrimSpace(f.VulnID)
	if vulnID == "" {
		return newValidation("vuln_id", "must not be empty")
	}
	if len(vulnID) > MaxVulnFindingVulnIDLength {
		return newValidation("vuln_id", "must be at most 100 characters")
	}
	if !vulnFindingVulnIDPattern.MatchString(vulnID) {
		return newValidation("vuln_id",
			"must start with a letter or digit and contain only letters, digits, '.', '_', ':' or '-', e.g. CVE-2024-1234")
	}
	title := strings.TrimSpace(f.Title)
	if title == "" {
		return newValidation("title", "must not be empty")
	}
	if len(title) > MaxVulnFindingTitleLength {
		return newValidation("title", "must be at most 300 characters")
	}
	if !f.Severity.Valid() {
		return newValidation("severity", "must be one of: none, low, medium, high, critical")
	}
	if !f.Status.Valid() {
		return newValidation("status", "must be one of: open, remediated, accepted_risk, false_positive")
	}
	scanner := strings.TrimSpace(f.Scanner)
	if scanner == "" {
		return newValidation("scanner", "must not be empty")
	}
	if len(scanner) > MaxVulnFindingScannerLength {
		return newValidation("scanner", "must be at most 200 characters")
	}
	if len(f.Description) > MaxVulnFindingDescriptionLength {
		return newValidation("description", "must be at most 2000 characters")
	}
	return nil
}

// NewVulnFinding builds a validated, freshly-minted, open VulnFinding ready
// for VulnFindingRepository.Upsert. tenantID is always taken from the bound
// connection's context, never from request data, the same rule
// NewSecurityRule/NewIOC follow. FirstSeen/LastSeen are left zero — they are
// store-managed (see VulnFinding's doc comment); the store, not this
// constructor, decides them at write time (now(), or unchanged on an
// existing row).
func NewVulnFinding(tenantID, assetID, vulnID, title string, severity VulnFindingSeverity, scanner, description string) (*VulnFinding, error) {
	f := &VulnFinding{
		FindingID:   NewID(),
		TenantID:    strings.TrimSpace(tenantID),
		AssetID:     strings.TrimSpace(assetID),
		VulnID:      strings.TrimSpace(vulnID),
		Title:       title,
		Severity:    severity,
		Status:      VulnFindingOpen,
		Scanner:     strings.TrimSpace(scanner),
		Description: description,
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return f, nil
}

// VulnFindingUpsertResult is the outcome of writing one VulnFinding from a
// scan-ingest batch — always one per input finding, at that finding's input
// index, mirroring SecurityObservationWriteResult's shape exactly: one bad
// finding must not abort the findings around it.
type VulnFindingUpsertResult struct {
	Accepted bool
	// FindingID is set when Accepted: the CANONICAL finding_id for this
	// (tenant, asset, vuln_id) — the pre-existing one on a dedup match, a
	// freshly-minted one on first sight. A caller cannot assume this equals
	// the finding_id domain.NewVulnFinding minted for its own input row.
	FindingID string
	// Status is set when Accepted: the finding's status AFTER this write —
	// see VulnFindingRepository.Upsert's doc comment. A caller watching for
	// "open" here on a vuln_id it previously saw move to remediated/
	// accepted_risk has just observed a reappearance.
	Status VulnFindingStatus
	// Reason is set when not Accepted: the validation failure, or an
	// asset_id the tenant cannot see.
	Reason string
}

// VulnFindingRepository administers vuln_finding: the vulnerability-finding
// stateful entity (E8.3a) — scan ingestion (UPSERT/dedup) and lifecycle
// CRUD. Prioritization by CI criticality and remediation -> Incident linkage
// are E8.3b, a LATER, SEPARATE story — nothing here ranks findings or opens
// an Incident; auto-close-on-absence (an open finding not named in a new
// full-scope scan) is likewise deferred (it needs full-scan-scope semantics
// this story does not have) — a finding is only ever reopened or left alone
// by Upsert, never auto-closed by it.
//
// vuln_finding is TENANT-OWNED: it is in postgres.TenantOwnedTables and
// carries row-level security, so this repository takes no tenant argument
// anywhere — the bound connection already is the boundary (ADR-TENANCY-002),
// exactly like SecurityRuleRepository/IOCRepository.
//
// There is NO Delete. Like Incident, a finding is operational history the
// moment it exists — even a mistaken one records what a scan actually
// reported — so "removing" one is a lifecycle transition (remediated/
// accepted_risk/false_positive via SetStatus), not a row deletion.
type VulnFindingRepository interface {
	// Upsert validates and writes a batch from one scan ingest call,
	// returning one VulnFindingUpsertResult per input finding, in input
	// order. AssetID is re-verified against this store's own tenant-scoped
	// connection before any row naming it is written: an asset_id the
	// caller's tenant cannot see is reported as a rejected finding, never as
	// a row naming a cross-tenant asset (ADR-ASSET-001 §6).
	//
	// Dedup key: UNIQUE (tenant_id, asset_id, vuln_id) — one finding per
	// asset per vulnerability. The UPSERT rule, exactly:
	//
	//   1. No existing row -> INSERT: status=open, first_seen=last_seen=now(),
	//      a freshly-minted finding_id, fields taken from the input.
	//   2. An existing row whose status is open, remediated, or
	//      accepted_risk -> UPDATE: last_seen=now(), title/severity/scanner/
	//      description refreshed from the input, status forced to open
	//      (a remediated or accepted-risk finding reappearing in a later
	//      scan is exactly the fact "this was not actually fixed / the
	//      accepted risk is still present" — see VulnFindingRemediated/
	//      VulnFindingAcceptedRisk's own doc comments). first_seen is left
	//      untouched.
	//   3. An existing row whose status is false_positive -> UPDATE:
	//      last_seen=now() ONLY. title/severity/scanner/description/status
	//      are left exactly as the operator last judged them. A scanner
	//      re-reporting the same alarm is not new evidence that overturns an
	//      operator's determination that the scanner was simply wrong — see
	//      VulnFindingFalsePositive's own doc comment; last_seen still moves
	//      so an operator reviewing the entry can see it is still being
	//      detected.
	//
	// Idempotent: re-ingesting the identical scan a second time reproduces
	// case 2 or 3 above (never a duplicate row — the UNIQUE constraint plus
	// ON CONFLICT guarantee it), refreshing last_seen/row_version but
	// creating nothing new.
	//
	// finding_id on the input findings is used ONLY for the fresh-insert
	// branch; on a dedup match the pre-existing row's own finding_id is
	// returned instead (VulnFindingUpsertResult.FindingID) — see that type's
	// doc comment.
	Upsert(ctx context.Context, findings []VulnFinding) ([]VulnFindingUpsertResult, error)

	Get(ctx context.Context, findingID string) (*VulnFinding, error)

	// List returns a page of the caller's findings, keyset-paginated over
	// finding_id. assetID/status/severity, each non-empty, filter to exactly
	// that value; the zero value for each returns every match on the other
	// filters — mirrors IncidentRepository.List's own filter shape.
	List(ctx context.Context, limit int, after, assetID string, status VulnFindingStatus, severity VulnFindingSeverity) ([]*VulnFinding, error)

	// SetStatus performs a lifecycle transition under optimistic locking,
	// guarded by VulnFindingStatus.CanTransitionTo — the single authority
	// for status changes (mirrors AssetStore.SetStatus/IncidentStore.
	// SetStatus). An illegal move returns *VulnFindingTransitionError
	// (ErrInvalidTransition). This is the ONLY path that can move a finding
	// OUT of false_positive — Upsert never does (see its own doc comment).
	SetStatus(ctx context.Context, findingID string, rowVersion int64, status VulnFindingStatus) (*VulnFinding, error)
}

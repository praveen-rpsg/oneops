package domain

import (
	"context"
	"strings"
	"time"
)

// IOCIndicatorType is the closed set of indicator kinds a watchlist entry
// may hold. It stays closed — rather than an open string like
// SecurityObservation.Source — because a future detector (E8.2b) dispatches
// its match logic on this field, the same reason AlertComparator/
// ObservationSeverity are closed rather than open.
type IOCIndicatorType string

const (
	IOCIndicatorTypeIP       IOCIndicatorType = "ip"
	IOCIndicatorTypeDomain   IOCIndicatorType = "domain"
	IOCIndicatorTypeURL      IOCIndicatorType = "url"
	IOCIndicatorTypeFileHash IOCIndicatorType = "file_hash"
	IOCIndicatorTypeEmail    IOCIndicatorType = "email"
)

// Valid reports whether t is a defined indicator type.
func (t IOCIndicatorType) Valid() bool {
	switch t {
	case IOCIndicatorTypeIP, IOCIndicatorTypeDomain, IOCIndicatorTypeURL, IOCIndicatorTypeFileHash, IOCIndicatorTypeEmail:
		return true
	default:
		return false
	}
}

// MaxIOCIndicatorValueLength bounds IndicatorValue: generous enough for a
// long URL indicator while still bounded, the same "bounded, not unbounded"
// discipline MaxObservationSourceLength states for security_observation.
const MaxIOCIndicatorValueLength = 2048

// MaxIOCDescriptionLength and MaxIOCSourceLength bound Description/Source —
// open, operator-authored text with no shared grammar, mirroring
// MaxMaintenanceWindowReasonLength and MaxObservationSourceLength
// respectively.
const (
	MaxIOCDescriptionLength = 2000
	MaxIOCSourceLength      = 200
)

// NormalizeIOCIndicatorValue applies the per-type canonicalisation the
// uniqueness constraint (tenant_id, indicator_type, indicator_value) relies
// on: two spellings of the SAME indicator must collide into one watchlist
// row, not silently coexist as two rows a future detector (E8.2b) would then
// have to match separately. domain/url/email are case-insensitive by their
// own grammar (RFC 1035, RFC 3986, RFC 5321) so they are lower-cased; ip and
// file_hash are NOT — an IPv6 literal's compressed form and a hash's hex
// digits are each already a fixed, case-significant representation the
// operator supplied deliberately (some feeds publish upper-case hex), and
// lower-casing either would silently accept the wrong indicator — so both
// are only trimmed.
func NormalizeIOCIndicatorValue(indicatorType IOCIndicatorType, value string) string {
	v := strings.TrimSpace(value)
	switch indicatorType {
	case IOCIndicatorTypeDomain, IOCIndicatorTypeURL, IOCIndicatorTypeEmail:
		return strings.ToLower(v)
	default:
		return v
	}
}

// IOC is one entry in a tenant's indicator-of-compromise watchlist (E8.2a):
// a curated value (an IP, domain, URL, file hash or email address) an
// operator wants matched against future security_observation facts. It is
// REFERENCE DATA an operator maintains — the same legitimate-stored-data
// category alert_rule and maintenance_window occupy — deliberately NOT a
// reified Threat/Event/Detection noun: this row carries no match state of
// its own (no last_matched_at, no current_incident_id). Whichever store
// eventually reads this watchlist to compare it against security_observation
// and raise an Incident is E8.2b, a later, separate story — this row is
// config only (docs/PLATFORM-BUILD-PLAN.md §4, ADR-SOC-004).
type IOC struct {
	IOCID    string
	TenantID string
	// IndicatorType classifies what IndicatorValue names — see
	// IOCIndicatorType's own doc comment. Fixed at creation: not patchable
	// (see IOCPatch's doc comment) — delete and recreate to change it.
	IndicatorType IOCIndicatorType
	// IndicatorValue is the indicator itself, normalized per IndicatorType —
	// see NormalizeIOCIndicatorValue's doc comment. Fixed at creation, the
	// same reason IndicatorType is.
	IndicatorValue string
	// Severity is the Incident severity a future detector (E8.2b) will raise
	// on a match — the INCIDENT severity vocabulary (critical/high/medium/
	// low), NOT ObservationSeverity: the same split SecurityRule.
	// IncidentSeverity draws against SecurityRule.MinSeverity.
	Severity IncidentSeverity
	Enabled  bool
	// Description is optional, operator-authored context for why this
	// indicator is on the watchlist.
	Description string
	// Source names where this indicator came from (e.g. "misp",
	// "alienvault-otx", "manual") — open-but-bounded, the same split
	// SecurityObservation.Source draws.
	Source     string
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Validate enforces the invariants the database also enforces, so a bad IOC
// fails with a field-level reason before it reaches the store.
func (i *IOC) Validate() error {
	if strings.TrimSpace(i.IOCID) == "" {
		return newValidation("ioc_id", "must not be empty")
	}
	if strings.TrimSpace(i.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if !i.IndicatorType.Valid() {
		return newValidation("indicator_type", "must be one of: ip, domain, url, file_hash, email")
	}
	if i.IndicatorValue == "" {
		return newValidation("indicator_value", "must not be empty")
	}
	if len(i.IndicatorValue) > MaxIOCIndicatorValueLength {
		return newValidation("indicator_value", "must be at most 2048 characters")
	}
	if i.IndicatorValue != NormalizeIOCIndicatorValue(i.IndicatorType, i.IndicatorValue) {
		return newValidation("indicator_value",
			"must already be normalized: trimmed, and lower-cased for domain/url/email")
	}
	if !i.Severity.Valid() {
		return newValidation("severity", "must be one of: critical, high, medium, low")
	}
	if len(i.Description) > MaxIOCDescriptionLength {
		return newValidation("description", "must be at most 2000 characters")
	}
	if len(i.Source) > MaxIOCSourceLength {
		return newValidation("source", "must be at most 200 characters")
	}
	return nil
}

// NewIOC builds a validated, enabled IOC, freshly minted with no
// Description/Source set — the caller may set either before Create, and
// either is patchable afterwards (see IOCPatch's doc comment). tenantID is
// always taken from the bound connection's context, never from request data,
// the same rule NewSecurityRule/NewAlertRule follow. indicatorValue is
// normalized here, once, so every downstream comparison (Validate, the
// unique constraint) sees the canonical form.
func NewIOC(tenantID string, indicatorType IOCIndicatorType, indicatorValue string, severity IncidentSeverity) (*IOC, error) {
	i := &IOC{
		IOCID:          NewID(),
		TenantID:       strings.TrimSpace(tenantID),
		IndicatorType:  indicatorType,
		IndicatorValue: NormalizeIOCIndicatorValue(indicatorType, indicatorValue),
		Severity:       severity,
		Enabled:        true,
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}

// IOCPatch carries the fields IOCRepository.Update may change; a nil pointer
// leaves that field unchanged. IndicatorType and IndicatorValue are
// deliberately absent — like SecurityRulePatch's ObservationType/
// AlertRulePatch's Metric — what an entry watches for is fixed at creation,
// not a field to repoint later: delete and recreate instead.
type IOCPatch struct {
	Severity    *IncidentSeverity
	Enabled     *bool
	Description *string
	Source      *string
}

// IOCRepository administers ioc: E8.2a's indicator-of-compromise watchlist.
// CONFIG ONLY — no detector, worker or correlation reads this repository or
// security_observation to look for a match; that is E8.2b, a later, separate
// story.
//
// ioc is TENANT-OWNED: it is in TenantOwnedTables and carries row-level
// security, so this repository takes no tenant argument anywhere — the
// bound connection already is the boundary (ADR-TENANCY-002), exactly like
// SecurityRuleRepository.
type IOCRepository interface {
	// Create inserts an entry. Returns domain.ErrConflict if this tenant
	// already holds an entry with the same (IndicatorType, IndicatorValue) —
	// the UNIQUE (tenant_id, indicator_type, indicator_value) constraint,
	// tenant-scoped so two tenants may independently hold the same
	// indicator.
	Create(ctx context.Context, i *IOC) (*IOC, error)
	Get(ctx context.Context, iocID string) (*IOC, error)
	// List returns a page of the caller's entries, keyset-paginated over
	// ioc_id.
	List(ctx context.Context, limit int, after string) ([]*IOC, error)
	// Update changes one or more fields under optimistic locking. Returns
	// ErrVersionMismatch on a stale rowVersion, ErrNotFound if iocID does not
	// name an entry visible to this tenant.
	Update(ctx context.Context, iocID string, rowVersion int64, patch IOCPatch) (*IOC, error)
	// Delete removes an entry, or returns ErrNotFound. Carries no
	// row_version — see ADR-HARD-003, which applies here unchanged.
	Delete(ctx context.Context, iocID string) error
}

package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MaxMaintenanceWindowReasonLength bounds the operator-supplied label
// describing why a window was declared (e.g. "patching", "network
// maintenance") — free text, not a vocabulary this package interprets.
const MaxMaintenanceWindowReasonLength = 500

// MaintenanceWindow is an operator-declared, one-shot [StartsAt, EndsAt)
// suppression window over a single asset (E3.3a, ADR-ALERTING-002): while
// `now` falls inside it, the alerting evaluator's ok->firing transition for
// that asset does not create/link an Incident and does not notify — planned
// maintenance must not page.
//
// This is a real, first-class operational object an operator declares — not
// one of the reduced false-nouns (docs/PLATFORM-BUILD-PLAN.md §4, Vol II
// §5.3). It suppresses a SIDE EFFECT of a firing, never the firing's own
// derivation: sustainedBreach's verdict over telemetry (E3.1) and the E3.2
// flap-dwell bookkeeping it composes with are both entirely unaffected by
// this type existing — see internal/alerting.Evaluator.evaluateRule's doc
// comment for exactly where and how it is consulted.
//
// The target is a single AssetID (E3.3a's decision, ADR-ALERTING-002 §1):
// dependency-aware suppression (silencing a CI's downstream/dependent CIs
// too) is E3.3b, deliberately out of scope here — no CMDB graph traversal
// happens anywhere in this package. Recurrence is likewise out of scope: a
// window is a single, explicit [StartsAt, EndsAt) interval; a recurring
// maintenance schedule is future work.
type MaintenanceWindow struct {
	WindowID string
	TenantID string
	// AssetID names the Configuration Item this window covers. Re-verified
	// against the caller's own tenant-scoped connection before being
	// written — the same defense ADR-ASSET-001 §6 already applies to
	// AlertRule.AssetID, CollectorCheck.AssetID, etc.
	AssetID string
	// StartsAt/EndsAt bound the window as a half-open interval [StartsAt,
	// EndsAt): a firing at exactly StartsAt IS suppressed, a firing at
	// exactly EndsAt is NOT (ADR-ALERTING-002 §3) — the same half-open
	// convention TelemetryRepository.QueryRange's [from, to) already uses,
	// applied here so a window's boundary has one unambiguous, already-
	// familiar meaning in this codebase.
	StartsAt time.Time
	EndsAt   time.Time
	// Reason is an optional, free-text operator label (e.g. "patching
	// window", "network maintenance") — not a vocabulary this package
	// interprets or validates beyond a length bound.
	Reason string
	// CreatedBy is the authenticated actor who declared this window, set by
	// the repository from ActorFrom(ctx) at Create time — never accepted
	// from caller-supplied request data, the same discipline
	// IncidentStore.Create already applies to its own timeline actor.
	CreatedBy string
	// SuppressedCount/LastSuppressedAt are the visible, queryable record
	// that a suppression happened — never a silent drop
	// (docs/PLATFORM-BUILD-PLAN.md's success criterion for E3.3a). Both are
	// set only by the evaluator's privileged background path
	// (alerting.MaintenanceWindowChecker.Suppress), incremented once per
	// evaluation tick that found this window active for a would-be firing,
	// never through the caller-facing Create/Delete path.
	SuppressedCount  int64
	LastSuppressedAt time.Time // zero means never suppressed
	RowVersion       int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validate enforces the invariants the database also enforces, so a bad
// window fails with a field-level reason before it reaches the store.
func (w *MaintenanceWindow) Validate() error {
	if strings.TrimSpace(w.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(w.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	if w.StartsAt.IsZero() {
		return newValidation("starts_at", "must not be zero")
	}
	if w.EndsAt.IsZero() {
		return newValidation("ends_at", "must not be zero")
	}
	if !w.EndsAt.After(w.StartsAt) {
		return newValidation("ends_at", "must be strictly after starts_at — the window is half-open [starts_at, ends_at)")
	}
	if len(w.Reason) > MaxMaintenanceWindowReasonLength {
		return newValidation("reason", fmt.Sprintf("must be at most %d characters", MaxMaintenanceWindowReasonLength))
	}
	return nil
}

// NewMaintenanceWindow builds a validated window, freshly minted and never
// yet suppressing anything (SuppressedCount=0, LastSuppressedAt zero).
// CreatedBy is deliberately absent here — see MaintenanceWindow's own doc
// comment — the repository's Create sets it from the authenticated caller.
func NewMaintenanceWindow(tenantID, assetID, reason string, startsAt, endsAt time.Time) (*MaintenanceWindow, error) {
	w := &MaintenanceWindow{
		WindowID: NewID(),
		TenantID: strings.TrimSpace(tenantID),
		AssetID:  strings.TrimSpace(assetID),
		StartsAt: startsAt,
		EndsAt:   endsAt,
		Reason:   strings.TrimSpace(reason),
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

// MaintenanceWindowRepository administers maintenance_window: E3.3a's
// operator-facing CRUD surface.
//
// maintenance_window is TENANT-OWNED: it is in TenantOwnedTables and carries
// row-level security, so this repository takes no tenant argument
// anywhere — the bound connection already is the boundary (ADR-TENANCY-002),
// exactly like AlertRuleRepository/CollectorCheckRepository. The evaluator's
// separate, privileged-pool read (whether a window is currently active for a
// firing) is alerting.MaintenanceWindowChecker, not this interface — the
// same dual-role split *postgres.AlertRuleStore already draws between
// domain.AlertRuleRepository and alerting.Store.
type MaintenanceWindowRepository interface {
	// Create inserts a window. AssetID must already be visible to the
	// caller's tenant — re-verified against this store's own tenant-scoped
	// connection before the row is written (ADR-ASSET-001 §6, extended
	// here); an asset_id the tenant cannot see returns ErrNotFound.
	// CreatedBy is taken from ActorFrom(ctx), never from w — an
	// unauthenticated call returns ErrNoActor.
	Create(ctx context.Context, w *MaintenanceWindow) (*MaintenanceWindow, error)
	// Get returns a window by identifier, or ErrNotFound.
	Get(ctx context.Context, windowID string) (*MaintenanceWindow, error)
	// List returns a page of the caller's windows, keyset-paginated over
	// window_id.
	List(ctx context.Context, limit int, after string) ([]*MaintenanceWindow, error)
	// Delete removes a window — an operator's way to cancel/withdraw a
	// declared maintenance window at any time, before or during it. There is
	// no other lifecycle: a window either exists (and is consulted by its
	// [StartsAt, EndsAt) bounds) or it does not. Returns ErrNotFound
	// otherwise.
	Delete(ctx context.Context, windowID string) error
}

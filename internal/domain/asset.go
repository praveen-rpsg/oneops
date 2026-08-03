package domain

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// AssetStatus is the lifecycle of a Configuration Item (E1.3, extending
// ADR-ASSET-001 §5).
//
// §5 originally modelled this as a two-value active/retired pair mirroring
// TeamStatus. E1.3 widens it to a real lifecycle — planned -> active ->
// retired, with maintenance as a reversible detour off active — because a
// CMDB CI has states TeamStatus never needed: a server can be racked and
// named before it ever serves traffic (planned), and taken down for patching
// without being decommissioned (maintenance). The "keep the row, never hard
// delete on a status change" reasoning §5 gives for retirement is unchanged
// and now applies to every state, not just the terminal one.
type AssetStatus string

const (
	// AssetPlanned is a Configuration Item that has been registered but is
	// not yet in service — provisioned but not live. It is the default only
	// for a caller that asks for it explicitly at creation; NewAsset itself
	// still defaults to active (see its doc comment).
	AssetPlanned AssetStatus = "planned"
	// AssetActive is a Configuration Item in ordinary service.
	AssetActive AssetStatus = "active"
	// AssetMaintenance is a temporary, reversible detour off active — the CI
	// is out of service for planned work, not decommissioned.
	AssetMaintenance AssetStatus = "maintenance"
	// AssetRetired is a decommissioned Configuration Item. The row survives
	// (ADR-ASSET-001 §5) so a relationship, monitor or incident that already
	// names it is not orphaned. Unlike UserStatus.deactivated, retirement is
	// NOT terminal here: a decommissioned server can be redeployed, so
	// retired -> active ("reinstate") is a permitted transition — see
	// assetTransitions.
	AssetRetired AssetStatus = "retired"
)

// Valid reports whether s is a defined status.
func (s AssetStatus) Valid() bool {
	switch s {
	case AssetPlanned, AssetActive, AssetMaintenance, AssetRetired:
		return true
	default:
		return false
	}
}

// assetTransitions is the whole lifecycle, as data rather than as a chain of
// conditionals — mirrors userTransitions (internal/domain/user.go). A
// transition absent here is refused, so adding a state means stating every
// edge it has instead of discovering the omissions in production.
//
// retired -> active is "reinstate": deliberately permitted, unlike
// UserStatus's terminal deactivated, because an Asset's retirement is an
// operational fact (decommissioned hardware/software), not a governance
// act — the same CI can be redeployed and go back into service. Reinstating
// goes straight to active, not back through planned: the CI already has a
// history, so replanning it would misrepresent it as new.
var assetTransitions = map[AssetStatus][]AssetStatus{
	AssetPlanned:     {AssetActive, AssetRetired},
	AssetActive:      {AssetMaintenance, AssetRetired},
	AssetMaintenance: {AssetActive, AssetRetired},
	AssetRetired:     {AssetActive},
}

// initialAssetStatuses are the statuses a caller may request AT CREATION.
// maintenance and retired are reachable only by transition (SetStatus): an
// asset created already "under maintenance" or "retired" has no history to
// justify either state.
var initialAssetStatuses = map[AssetStatus]bool{
	AssetPlanned: true,
	AssetActive:  true,
}

// CanTransitionTo reports whether from -> to is a permitted lifecycle move.
// A transition to the current state is refused: it is a no-op that would
// consume a row version and write a history record recording nothing.
func (s AssetStatus) CanTransitionTo(to AssetStatus) bool {
	for _, allowed := range assetTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidInitialStatus reports whether s may be set directly at creation,
// bypassing the transition guard entirely. planned and active are
// creation-time facts about a CI that did not exist a moment ago; maintenance
// and retired describe something that happened to a CI already in service,
// so they are reachable only through SetStatus.
func (s AssetStatus) ValidInitialStatus() bool { return initialAssetStatuses[s] }

// AssetTransitionError is a refused lifecycle move, naming both ends so the
// message says which move was attempted rather than only that one was. It
// wraps the shared ErrInvalidTransition (defined in user.go), so callers may
// match the class with errors.Is regardless of which entity's lifecycle
// refused the move — mirrors user.go's TransitionError exactly, scoped to
// AssetStatus because Go's type system will not let one struct carry both.
type AssetTransitionError struct {
	From AssetStatus
	To   AssetStatus
}

func (e *AssetTransitionError) Error() string {
	return "invalid lifecycle transition: " + string(e.From) + " -> " + string(e.To)
}

// Unwrap makes errors.Is(err, ErrInvalidTransition) true for this type.
func (e *AssetTransitionError) Unwrap() error { return ErrInvalidTransition }

// NewAssetTransitionError builds an *AssetTransitionError naming both ends.
func NewAssetTransitionError(from, to AssetStatus) *AssetTransitionError {
	return &AssetTransitionError{From: from, To: to}
}

// MaxAssetNameLength bounds a free-text field that reaches consoles and logs,
// the same bound Team.Name carries.
const MaxAssetNameLength = 200

// MaxAssetTypeLength bounds the type identifier.
const MaxAssetTypeLength = 100

// assetTypePattern is the format Asset.Type must satisfy. The set of types is
// deliberately open — server, service, network_device, application, database
// and whatever downstream monitoring/ITSM increments need next, none of which
// this layer should have to be amended to accept — but the shape is not: a
// lower-case snake_case identifier, the same discipline
// configuration_object's own identifiers hold elsewhere in this schema, so a
// type is fit to appear in a filter, a metric label, or a route without
// escaping.
var assetTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// RelationshipType is the kind of edge the CMDB graph carries between two
// Assets. Unlike Asset.Type, this set is closed: it is the vocabulary the
// dependency-graph engine (internal/graph) and downstream monitoring/incident
// correlation reason over, so adding a member is a decision this layer makes
// deliberately, not a value a caller happens to send.
type RelationshipType string

const (
	// RelationshipDependsOn: the source asset depends on the target to function.
	RelationshipDependsOn RelationshipType = "depends_on"
	// RelationshipRunsOn: the source asset is hosted/executed on the target.
	RelationshipRunsOn RelationshipType = "runs_on"
	// RelationshipConnectedTo: the two assets have a network/physical link.
	RelationshipConnectedTo RelationshipType = "connected_to"
	// RelationshipMemberOf: the source asset belongs to the target grouping
	// (e.g. a host that is a member of a cluster).
	RelationshipMemberOf RelationshipType = "member_of"
)

// Valid reports whether t is a known relationship type.
func (t RelationshipType) Valid() bool {
	switch t {
	case RelationshipDependsOn, RelationshipRunsOn, RelationshipConnectedTo, RelationshipMemberOf:
		return true
	default:
		return false
	}
}

// AssetEnvironment classifies which deployment environment an asset runs in.
// Downstream NOC health rollups reason over this directly (a production
// asset's health matters differently than a development one's), so — like
// RelationshipType, and unlike Asset.Type — this set is CLOSED: a new tier
// is a deliberate schema decision, not a value a caller happens to send
// (E1.2, docs/PLATFORM-BUILD-PLAN.md).
type AssetEnvironment string

const (
	AssetEnvironmentProduction  AssetEnvironment = "production"
	AssetEnvironmentStaging     AssetEnvironment = "staging"
	AssetEnvironmentDevelopment AssetEnvironment = "development"
	// AssetEnvironmentUnknown is the default for an asset nobody has
	// classified yet — an unclassified asset is a fact worth surfacing to a
	// health rollup, not an error at creation time.
	AssetEnvironmentUnknown AssetEnvironment = "unknown"
)

// Valid reports whether e is a defined environment.
func (e AssetEnvironment) Valid() bool {
	switch e {
	case AssetEnvironmentProduction, AssetEnvironmentStaging, AssetEnvironmentDevelopment, AssetEnvironmentUnknown:
		return true
	default:
		return false
	}
}

// AssetCriticality is the business-impact tier SOC vulnerability
// prioritization and ITSM routing key off directly. Closed for the same
// reason AssetEnvironment is.
type AssetCriticality string

const (
	AssetCriticalityCritical AssetCriticality = "critical"
	AssetCriticalityHigh     AssetCriticality = "high"
	AssetCriticalityMedium   AssetCriticality = "medium"
	AssetCriticalityLow      AssetCriticality = "low"
	// AssetCriticalityUnknown is the default for an asset nobody has tiered
	// yet, mirroring AssetEnvironmentUnknown.
	AssetCriticalityUnknown AssetCriticality = "unknown"
)

// Valid reports whether c is a defined criticality tier.
func (c AssetCriticality) Valid() bool {
	switch c {
	case AssetCriticalityCritical, AssetCriticalityHigh, AssetCriticalityMedium, AssetCriticalityLow, AssetCriticalityUnknown:
		return true
	default:
		return false
	}
}

// Asset is a Configuration Item: the operational unit every downstream
// NOC/SOC/ITSM capability (monitoring, alerting, incidents, tickets, service
// catalog) will reference. It is deliberately distinct from
// configuration_object (ADR-ASSET-001 §2): that entity is a governance
// artifact under the Configuration State Model's constitutional lifecycle,
// and an Asset carries none of that — no Authority, no Retention, no §8
// operations. It is tenant-owned, operational data.
type Asset struct {
	AssetID     string
	TenantID    string
	Type        string
	Name        string
	Attributes  map[string]any
	Status      AssetStatus
	Environment AssetEnvironment
	Criticality AssetCriticality
	// OwnerTeamID/OwnerUserID name the team/user accountable for this asset.
	// Both nil is a valid, common state — ownership is optional, assigned as
	// the CMDB matures. When set, AssetRepository re-verifies the id against
	// the caller's own tenant-scoped connection before writing it: see
	// AssetRepository.Create's doc comment for why the foreign key alone
	// cannot be trusted (ADR-ASSET-001 §6, extended to ownership).
	OwnerTeamID *string
	OwnerUserID *string
	RowVersion  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Active reports whether the asset is in ordinary use.
func (a *Asset) Active() bool { return a.Status == AssetActive }

// Validate enforces the invariants the database also enforces, so a bad
// request fails with a field-level 422 rather than a constraint-violation 500.
func (a *Asset) Validate() error {
	if strings.TrimSpace(a.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	if strings.TrimSpace(a.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	assetType := strings.TrimSpace(a.Type)
	if assetType == "" {
		return newValidation("type", "must not be empty")
	}
	if len(assetType) > MaxAssetTypeLength {
		return newValidation("type", "must be at most 100 characters")
	}
	if !assetTypePattern.MatchString(assetType) {
		return newValidation("type", "must be lower-case snake_case, e.g. server, network_device")
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return newValidation("name", "must not be empty")
	}
	if len(name) > MaxAssetNameLength {
		return newValidation("name", "must be at most 200 characters")
	}
	if !a.Status.Valid() {
		return newValidation("status", "must be one of: planned, active, maintenance, retired")
	}
	if !a.Environment.Valid() {
		return newValidation("environment", "must be one of: production, staging, development, unknown")
	}
	if !a.Criticality.Valid() {
		return newValidation("criticality", "must be one of: critical, high, medium, low, unknown")
	}
	if a.OwnerTeamID != nil && strings.TrimSpace(*a.OwnerTeamID) == "" {
		return newValidation("owner_team_id", "must not be blank when supplied; omit it to leave the asset unowned")
	}
	if a.OwnerUserID != nil && strings.TrimSpace(*a.OwnerUserID) == "" {
		return newValidation("owner_user_id", "must not be blank when supplied; omit it to leave the asset unowned")
	}
	return nil
}

// NewAsset builds an active asset. Active, not planned, remains the default
// (E1.3 decision, unchanged from E1.1): the common case registering a CMDB
// entry is describing infrastructure that already exists and is already
// serving, not one being pre-provisioned. A caller that wants a planned CI
// sets Status explicitly before calling AssetRepository.Create, which
// accepts any AssetStatus.ValidInitialStatus() value.
//
// attributes may be nil, which is stored as an empty object rather than SQL
// NULL — a caller reading it back always gets a map, never a nil-check
// surprise.
func NewAsset(tenantID, assetType, name string, attributes map[string]any) (*Asset, error) {
	if attributes == nil {
		attributes = map[string]any{}
	}
	a := &Asset{
		AssetID:     NewID(),
		TenantID:    strings.TrimSpace(tenantID),
		Type:        strings.TrimSpace(assetType),
		Name:        strings.TrimSpace(name),
		Attributes:  attributes,
		Status:      AssetActive,
		Environment: AssetEnvironmentUnknown,
		Criticality: AssetCriticalityUnknown,
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// ApplyClassification sets environment, criticality and ownership from
// request-shaped input and validates the result. An empty environment or
// criticality string defaults to "unknown" — the same default NewAsset and
// the migration's column default both apply — so a caller that never
// mentions classification gets exactly what a caller who explicitly asked
// for "unknown" gets. ownerTeamID/ownerUserID are assigned as given (nil
// means unowned); AssetRepository re-verifies a non-nil id before it is
// persisted, because "known to the tenant" is a database fact this
// constructor cannot decide (mirrors NewAssetRelationship's own doc comment).
func (a *Asset) ApplyClassification(environment, criticality string, ownerTeamID, ownerUserID *string) error {
	env := strings.TrimSpace(environment)
	if env == "" {
		env = string(AssetEnvironmentUnknown)
	}
	crit := strings.TrimSpace(criticality)
	if crit == "" {
		crit = string(AssetCriticalityUnknown)
	}
	a.Environment = AssetEnvironment(env)
	a.Criticality = AssetCriticality(crit)
	a.OwnerTeamID = ownerTeamID
	a.OwnerUserID = ownerUserID
	return a.Validate()
}

// AssetRelationship is a directed, typed edge (FromAssetID → ToAssetID) of the
// CMDB graph. It is the storage-layer entity the dependency-graph engine
// traverses (ADR-ASSET-001 §4); it carries no authority or lifecycle of its
// own.
type AssetRelationship struct {
	RelationshipID string
	TenantID       string
	FromAssetID    string
	ToAssetID      string
	Type           RelationshipType
	RowVersion     int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Validate enforces the invariants the database also enforces: both endpoints
// present, no self-relationship, and a known relationship type.
func (r *AssetRelationship) Validate() error {
	if strings.TrimSpace(r.RelationshipID) == "" {
		return newValidation("relationship_id", "must not be empty")
	}
	if strings.TrimSpace(r.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	from := strings.TrimSpace(r.FromAssetID)
	if from == "" {
		return newValidation("from_asset_id", "must not be empty")
	}
	to := strings.TrimSpace(r.ToAssetID)
	if to == "" {
		return newValidation("to_asset_id", "must not be empty")
	}
	if from == to {
		return newValidation("to_asset_id", "an asset cannot have a relationship with itself")
	}
	if !r.Type.Valid() {
		return newValidation("type", "must be one of: depends_on, runs_on, connected_to, member_of")
	}
	return nil
}

// NewAssetRelationship builds a relationship between two assets already known
// to belong to the tenant. The store is responsible for confirming that —
// see AssetRepository.CreateRelationship — because "known to the tenant" is a
// database fact, not something this constructor can decide.
func NewAssetRelationship(tenantID, fromAssetID, toAssetID string, relType RelationshipType) (*AssetRelationship, error) {
	r := &AssetRelationship{
		RelationshipID: NewID(),
		TenantID:       strings.TrimSpace(tenantID),
		FromAssetID:    strings.TrimSpace(fromAssetID),
		ToAssetID:      strings.TrimSpace(toAssetID),
		Type:           relType,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// AssetPatch carries the fields AssetRepository.Update may change; a nil
// pointer leaves that field unchanged, exactly as name/attributes already
// worked before this type existed.
//
// Status is deliberately ABSENT (E1.3): a lifecycle move is no longer a field
// like any other. AssetRepository.SetStatus is now the single place a
// transition is checked (mirrors UserRepository.SetStatus/UserRepository.
// UpdateProfile's split) — folding it back into Update would let a caller
// change status without going through CanTransitionTo, or hide an illegal
// move inside a multi-field patch.
//
// OwnerTeamID/OwnerUserID are doubly-optional, encoded in a single *string
// rather than a **string because the JSON layer already gives us the
// distinction for free: a key absent from the request body decodes to a nil
// pointer ("leave unchanged"); a key present with a non-empty string decodes
// to a pointer to it ("set to this id, re-verified tenant-side"); a key
// present as "" decodes to a pointer to the empty string ("clear the
// owner"). A JSON `null` is indistinguishable from an absent key by design —
// a caller that wants to clear ownership sends "", not null.
type AssetPatch struct {
	Name        *string
	Attributes  map[string]any
	Environment *AssetEnvironment
	Criticality *AssetCriticality
	OwnerTeamID *string
	OwnerUserID *string
}

// AssetChangeKind classifies what an asset_change_history row records.
type AssetChangeKind string

const (
	// AssetChangeCreated is the single row written when an asset is first
	// inserted. It carries no field/old_value/new_value — there is no "old"
	// state to diff against — only the fact and its actor.
	AssetChangeCreated AssetChangeKind = "created"
	// AssetChangeUpdated is one row per field changed by AssetRepository.
	// Update. A single call that changes three fields writes three rows,
	// sharing the same resulting RowVersion and OccurredAt: this is the same
	// shape a field-history table (e.g. ServiceNow's sys_history_line) uses,
	// and it is what makes "what changed" queryable per field rather than
	// requiring every reader to diff two JSON blobs.
	AssetChangeUpdated AssetChangeKind = "updated"
	// AssetChangeTransitioned is the row written by SetStatus. Field is
	// always "status".
	AssetChangeTransitioned AssetChangeKind = "status_transitioned"
)

// Valid reports whether k is a value the database will accept.
func (k AssetChangeKind) Valid() bool {
	switch k {
	case AssetChangeCreated, AssetChangeUpdated, AssetChangeTransitioned:
		return true
	default:
		return false
	}
}

// AssetChangeEntry is one append-only row of an asset's change history
// (E1.3). It is written by the same transaction that performs the mutation
// it records — see AssetStore.Create/Update/SetStatus — so a change and its
// history entry commit together or neither does.
//
// AssetID deliberately carries no foreign key to asset (see the migration):
// an administrative or forensic record must outlive its subject, mirroring
// why admin_audit_event's subject columns carry no foreign key either
// (ADR-AUDIT-007 §6.3). A hard Delete of the asset therefore leaves its
// history rows in place, addressable by asset_id even though the asset no
// longer is.
type AssetChangeEntry struct {
	ChangeID   string
	TenantID   string
	AssetID    string
	Kind       AssetChangeKind
	Field      string
	OldValue   *string
	NewValue   *string
	Actor      string
	RowVersion int64
	OccurredAt time.Time
}

// AssetRepository administers the CMDB: assets, the relationships between
// them, and each asset's append-only change history.
//
// asset, asset_relationship and asset_change_history are TENANT-OWNED: all
// three are in TenantOwnedTables and carry row-level security, so this
// repository takes no tenant argument anywhere — the bound connection
// already is the boundary (ADR-TENANCY-002). Mutations here do NOT pass
// through withAdminAudit, for the same reason Team does not: ADR-AUDIT-007
// §6.2 scopes that chokepoint to five named identity-governance tables, and
// Asset is not one of them; asset_change_history is this entity's own
// equivalent record, not a use of that one.
type AssetRepository interface {
	// Create inserts an asset and its AssetChangeCreated history row in one
	// transaction. a.Status must be an initial status (AssetStatus.
	// ValidInitialStatus) — planned or active; maintenance and retired
	// describe something that happened to an asset already in service and
	// are reachable only through SetStatus. When a.OwnerTeamID/a.OwnerUserID
	// is set, the implementation MUST re-verify it against its own
	// tenant-scoped connection before writing — see the package doc on
	// AssetPatch for why the foreign key alone is not enough (ADR-ASSET-001
	// §6, extended to ownership). A non-nil id the tenant cannot see returns
	// ErrNotFound. The acting identity comes from ActorFrom(ctx); an
	// unattributable call returns ErrNoActor.
	Create(ctx context.Context, a *Asset) (*Asset, error)
	Get(ctx context.Context, assetID string) (*Asset, error)
	// List returns a page of the caller's assets, keyset-paginated over
	// asset_id. status filters to exactly that status; the zero value
	// excludes AssetRetired (a retired CI is soft-deleted from the default
	// view, per E1.3's soft-retire decision) but includes every other
	// status. A retired asset remains individually reachable via Get
	// regardless of this filter.
	List(ctx context.Context, limit int, after string, status AssetStatus) ([]*Asset, error)
	// Update changes one or more non-lifecycle fields under optimistic
	// locking, per patch, recording one AssetChangeUpdated history row per
	// field actually changed, in the same transaction as the UPDATE. Owner
	// references are re-verified exactly as Create re-verifies them. The
	// acting identity comes from ActorFrom(ctx); an unattributable call
	// returns ErrNoActor.
	Update(ctx context.Context, assetID string, rowVersion int64, patch AssetPatch) (*Asset, error)
	// SetStatus performs a lifecycle transition under optimistic locking,
	// guarded by AssetStatus.CanTransitionTo — the single authority for
	// status changes (mirrors UserRepository.SetStatus). An illegal move
	// returns *AssetTransitionError (ErrInvalidTransition). Records one
	// AssetChangeTransitioned history row in the same transaction as the
	// UPDATE. The acting identity comes from ActorFrom(ctx); an
	// unattributable call returns ErrNoActor.
	SetStatus(ctx context.Context, assetID string, rowVersion int64, status AssetStatus) (*Asset, error)
	// Delete removes an asset. Its relationships are removed with it
	// (ON DELETE CASCADE) — an asset cannot be half-referenced by a dangling
	// edge. Its change history is NOT removed: see AssetChangeEntry's doc
	// comment on why the record must outlive its subject.
	Delete(ctx context.Context, assetID string) error
	// History returns a page of assetID's append-only change history, newest
	// first is NOT guaranteed — ordering is keyset-paginated over change_id
	// (a ULID, and therefore chronological ascending), the same shape List
	// uses. Never returns a row this store, or any caller, can update or
	// delete: asset_change_history is append-only at the schema level.
	History(ctx context.Context, assetID string, limit int, after string) ([]*AssetChangeEntry, error)

	// CreateRelationship inserts a relationship. Both endpoints must already
	// be visible to the caller's tenant; a from/to id the tenant cannot see
	// (because it does not exist, or belongs to another tenant) returns
	// ErrNotFound rather than creating a cross-tenant edge. See
	// ADR-ASSET-001 §6 for why this check cannot be left to the foreign key
	// alone.
	CreateRelationship(ctx context.Context, r *AssetRelationship) (*AssetRelationship, error)
	// DeleteRelationship removes a relationship by id, or returns ErrNotFound.
	DeleteRelationship(ctx context.Context, relationshipID string) error
	// RelationshipsFrom returns the direct out-edges of assetID.
	RelationshipsFrom(ctx context.Context, assetID string) ([]*AssetRelationship, error)
	// RelationshipsTo returns the direct in-edges of assetID.
	RelationshipsTo(ctx context.Context, assetID string) ([]*AssetRelationship, error)
}

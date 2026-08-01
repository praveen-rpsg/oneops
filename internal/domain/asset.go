package domain

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// AssetStatus is the lifecycle of a Configuration Item.
//
// Modelled on TeamStatus rather than a hard delete: a retired asset keeps its
// row so a relationship, and eventually a monitor or an incident, that already
// names it is not orphaned (ADR-ASSET-001 §5).
type AssetStatus string

const (
	AssetActive  AssetStatus = "active"
	AssetRetired AssetStatus = "retired"
)

// Valid reports whether s is a defined status.
func (s AssetStatus) Valid() bool {
	return s == AssetActive || s == AssetRetired
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
		return newValidation("status", "must be one of: active, retired")
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

// NewAsset builds an active asset. attributes may be nil, which is stored as
// an empty object rather than SQL NULL — a caller reading it back always gets
// a map, never a nil-check surprise.
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
// pointer leaves that field unchanged, exactly as name/attributes/status
// already worked before this type existed.
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
	Status      *AssetStatus
	Environment *AssetEnvironment
	Criticality *AssetCriticality
	OwnerTeamID *string
	OwnerUserID *string
}

// AssetRepository administers the CMDB: assets and the relationships between
// them.
//
// asset and asset_relationship are TENANT-OWNED: both are in
// TenantOwnedTables and carry row-level security, so this repository takes no
// tenant argument anywhere — the bound connection already is the boundary
// (ADR-TENANCY-002). Mutations here do NOT pass through withAdminAudit, for
// the same reason Team does not: ADR-AUDIT-007 §6.2 scopes that chokepoint to
// five named identity-governance tables, and Asset is not one of them.
type AssetRepository interface {
	// Create inserts an asset. When a.OwnerTeamID/a.OwnerUserID is set, the
	// implementation MUST re-verify it against its own tenant-scoped
	// connection before writing — see the package doc on AssetPatch for why
	// the foreign key alone is not enough (ADR-ASSET-001 §6, extended to
	// ownership). A non-nil id the tenant cannot see returns ErrNotFound.
	Create(ctx context.Context, a *Asset) (*Asset, error)
	Get(ctx context.Context, assetID string) (*Asset, error)
	// List returns a page of the caller's assets, keyset-paginated over
	// asset_id.
	List(ctx context.Context, limit int, after string) ([]*Asset, error)
	// Update changes one or more fields under optimistic locking, per patch.
	// Unlike Team, Asset has no authorisation-adjacent status split to
	// protect: retiring an asset is not a governance act, so there is one
	// update path, not two. Owner references are re-verified exactly as
	// Create re-verifies them.
	Update(ctx context.Context, assetID string, rowVersion int64, patch AssetPatch) (*Asset, error)
	// Delete removes an asset. Its relationships are removed with it
	// (ON DELETE CASCADE) — an asset cannot be half-referenced by a dangling
	// edge.
	Delete(ctx context.Context, assetID string) error

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

package domain

import "context"

// Filter narrows a ListConfigObjects query. Zero values mean "no constraint".
type Filter struct {
	Role      Role
	Lifecycle Lifecycle
	Authority Authority
	// Query is a free-text search over artifact name and metadata values.
	Query string
}

// Page is a keyset-paginated result window.
type Page struct {
	Items      []*ConfigObject
	NextCursor string // empty when there are no further results
}

// ListParams controls pagination and ordering.
type ListParams struct {
	Filter Filter
	Limit  int    // capped by the repository to a sane maximum
	Cursor string // opaque; empty for the first page
}

// ConfigObjectRepository is the persistence contract for ConfigObject.
// Implementations must be safe for concurrent use.
type ConfigObjectRepository interface {
	// Create inserts a new object. Returns ErrConflict on (artifact,version) clash.
	Create(ctx context.Context, obj *ConfigObject) (*ConfigObject, error)
	// Get returns an object by cfg_id, or ErrNotFound.
	Get(ctx context.Context, cfgID string) (*ConfigObject, error)
	// List returns a keyset-paginated page of objects matching params.
	List(ctx context.Context, params ListParams) (*Page, error)
	// Update applies a partial update. expectedRowVersion enforces optimistic
	// locking; a mismatch returns ErrVersionMismatch.
	Update(ctx context.Context, cfgID string, expectedRowVersion int64, patch *Patch) (*ConfigObject, error)
	// There is deliberately no Delete here. Destroying a Configuration Object is
	// a §8 constitutional operation owned by the Governance Engine, which
	// enforces role protection, working-material-only, the dependents check and
	// the atomic audit append, and reaches storage through
	// GovernanceStore.RemoveObject. A destructive method on the persistence
	// contract is a second, unguarded door to that effect — it was wired to
	// DELETE /v1/artifacts/{id} and destroyed a ratified object with no audit
	// event (ADR-GOV-002). Do not add one back.

	// BulkCreate inserts many objects in a single transaction.
	BulkCreate(ctx context.Context, objs []*ConfigObject) ([]*ConfigObject, error)
}

// Patch carries partial-update fields. Nil pointers are left unchanged.
//
// It carries NO constitutional dimension. Lifecycle, Retention and Authority
// were removed by ADR-CP5: the registry owns persistence, governance owns
// constitutional mutation (§8: "No dimension changes except as an operation
// specifies"), and Authority is computed rather than asserted (§6).
//
// A dimension reaches storage through exactly one path —
// governance.Store.ApplyDimensions, inside the engine's own transaction and
// atomically with its audit event.
type Patch struct {
	RatifiedBy      *string
	ReviewCycle     *string
	RetentionPolicy *string
	Metadata        map[string]string // nil = unchanged; non-nil replaces
}

package grouping

import (
	"context"
	"log/slog"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// Config parameterises the Reconciler.
type Config struct {
	// Interval between reconciliation passes (default 60s — grouping is
	// eventually-consistent, not instant, so this can run less often than
	// alerting.Evaluator's own 30s: E4.2's payoff is noise REDUCTION on an
	// already-open incident, not a page an operator is waiting on).
	Interval time.Duration
	// MaxDepth bounds how many hops of an asset's dependency closure
	// deepestDownRoot considers, mirroring
	// postgres.DefaultDependencySuppressionMaxDepth's identical defensive
	// perf bound (default DefaultMaxDepth).
	MaxDepth int
}

func (c *Config) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 60 * time.Second
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = DefaultMaxDepth
	}
}

// Reconciler is the leader-gated topology-aware incident-grouping
// reconciliation pass (E4.2, ADR-ALERTING-004) — see the package doc comment
// for why this is a periodic sweep and not a hook inside
// alerting.Evaluator.correlate.
type Reconciler struct {
	store   Store
	graph   Graph
	metrics Metrics
	log     *slog.Logger
	now     func() time.Time
	cfg     Config
}

// NewReconciler builds the Reconciler over store (privileged pool) and graph
// (MUST be built over the TENANT-SCOPED pool — see Graph's own doc comment
// for why RLS must stay in force for the dependency walk even though store's
// own reads/writes are privileged and cross-tenant).
func NewReconciler(store Store, graph Graph, metrics Metrics, log *slog.Logger, cfg Config) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = NopMetrics{}
	}
	cfg.withDefaults()
	return &Reconciler{store: store, graph: graph, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg}
}

// Run reconciles on Interval until ctx is cancelled — the same shape
// alerting.Evaluator.Run uses.
func (r *Reconciler) Run(ctx context.Context) error {
	r.log.Info("incident grouping reconciler started", "interval", r.cfg.Interval.String())
	r.RunOnce(ctx)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.log.Info("incident grouping reconciler stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce reconciles every tenant that currently has at least one open,
// alert-sourced incident. Each tenant is independent — a topology fact in
// one tenant never influences another's grouping decision (tenant isolation
// is structural here: the down-set and the graph walk are both built from
// exactly one tenant's own OpenAlertIncidents/graph calls per iteration,
// never a shared cross-tenant map).
func (r *Reconciler) RunOnce(ctx context.Context) {
	tenants, err := r.store.TenantsWithOpenAlertIncidents(ctx)
	if err != nil {
		r.log.Error("incident grouping reconciler: list tenants", "err", err)
		r.metrics.IncErrors()
		return
	}
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			return
		}
		r.reconcileTenant(ctx, tenantID)
	}
}

// reconcileTenant computes and persists root_incident_id for every open,
// alert-sourced incident belonging to tenantID.
//
// The heuristic (ADR-ALERTING-004 §2, CTO-locked): for each open incident on
// asset X, walk X's transitive depends_on/runs_on closure
// (Graph.RecursiveDependenciesOfTypes — the FORWARD direction, "the things X
// depends on", never the reverse "things that depend on X" — see
// deepestDownRoot's own doc comment for why getting this backwards is the
// serious bug class this feature must not reproduce, the same
// direction-correctness ADR-ALERTING-003 §1 already proves for E3.3b's down-
// check). Among the down ones (an asset with its own open alert-incident,
// read from THIS tenant's own incident set, never a global one), the DEEPEST
// wins. No down dependency ⇒ root_incident_id = NULL (X is a root or
// standalone).
//
// Self-healing + idempotent: this recomputes candidate roots from the
// CURRENT open-incident set every call, so a resolved root's former children
// are re-evaluated on the very next pass (re-rooted to a still-down deeper
// dependency, or cleared to NULL), and a call against unchanged state writes
// nothing (SetRootIncidentID is only invoked where the computed value
// differs from what is already stored).
func (r *Reconciler) reconcileTenant(ctx context.Context, tenantID string) {
	incidents, err := r.store.OpenAlertIncidents(ctx, tenantID)
	if err != nil {
		r.log.Error("incident grouping reconciler: list open alert incidents", "tenant_id", tenantID, "err", err)
		r.metrics.IncErrors()
		return
	}
	if len(incidents) == 0 {
		r.metrics.IncTenantsProcessed()
		return
	}

	// down: assetID -> the id of ITS OWN open alert-incident. This is the
	// "is Y down" signal (ADR-ALERTING-004 §5's Honest Bound: a silently-dead
	// root with NO open incident of its own is not recognised as a root —
	// the same limitation class E3.3b's own down-check accepts).
	down := make(map[string]string, len(incidents))
	for _, inc := range incidents {
		down[inc.AssetID] = inc.IncidentID
	}

	// The dependency walk is RLS-scoped: bind the connection it acquires to
	// THIS tenant — never a request caller's, since there is none; this
	// background pass reasons about one tenant's own topology per iteration
	// — the identical non-decision postgres.DependencySuppressionStore.
	// Suppress's own doc comment already requires of E3.3b's walk
	// (ADR-TENANCY-001).
	scopedCtx := domain.WithTenant(ctx, &domain.Tenant{TenantID: tenantID})

	candidate := make(map[string]*string, len(incidents))
	for _, inc := range incidents {
		// Direction guarantee (coverage note): this package's own
		// TestReconciler_DirectionCorrectness uses a FAKE graph, so it pins the
		// reconciler LOGIC but cannot catch a reversal of the real SQL. The
		// real forward-direction of RecursiveDependenciesOfTypes is guarded by
		// the shared store test TestAssetGraph_ServiceMapFiltersToCompositionEdges
		// (flipping DirectionDependencies→Dependents in asset_graph_repo.go fails
		// it) — do not orphan that shared guarantee in a future refactor.
		nodes, err := r.graph.RecursiveDependenciesOfTypes(scopedCtx, inc.AssetID, GroupingEdgeTypes)
		if err != nil {
			r.log.Error("incident grouping reconciler: walk dependency closure",
				"tenant_id", tenantID, "asset_id", inc.AssetID, "err", err)
			r.metrics.IncErrors()
			continue
		}
		candidate[inc.IncidentID] = r.applyMaxDepth(nodes, down, inc.AssetID)
	}

	final := resolveFinalRoots(candidate)

	for _, inc := range incidents {
		want, computed := final[inc.IncidentID]
		if !computed {
			// The walk for this incident errored above; leave its stored
			// value untouched rather than guessing — the next pass retries.
			continue
		}
		if strPtrEqual(inc.RootIncidentID, want) {
			continue
		}
		if err := r.store.SetRootIncidentID(ctx, tenantID, inc.IncidentID, want); err != nil {
			r.log.Error("incident grouping reconciler: set root_incident_id",
				"tenant_id", tenantID, "incident_id", inc.IncidentID, "err", err)
			r.metrics.IncErrors()
			continue
		}
		r.metrics.IncIncidentsChanged()
	}
	r.metrics.IncTenantsProcessed()
}

// applyMaxDepth post-filters nodes to cfg.MaxDepth before handing them to
// deepestDownRoot — the same "post-filter on an already-ordered result, not
// a second query" shape postgres.DependencySuppressionStore.Suppress's own
// candidates slice uses for its identical defensive bound.
func (r *Reconciler) applyMaxDepth(nodes []domain.TraversalNode, down map[string]string, ownAssetID string) *string {
	bounded := make([]domain.TraversalNode, 0, len(nodes))
	for _, n := range nodes {
		if n.Depth > r.cfg.MaxDepth {
			continue
		}
		bounded = append(bounded, n)
	}
	return deepestDownRoot(bounded, down, ownAssetID)
}

// strPtrEqual reports whether two optional strings carry the same value —
// both nil, or both non-nil with equal contents.
func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Package httpapi is the Configuration Registry HTTP transport: routing,
// middleware, handlers, and the published OpenAPI contract.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/diag"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/graph"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/policy"
	"github.com/rpsg/oneops/pkg/version"
)

// idempotencyStore persists idempotent responses. *postgres.IdempotencyStore
// satisfies it; tests supply a fake.
type idempotencyStore interface {
	Lookup(ctx context.Context, key string) (status int, body []byte, found bool, err error)
	Save(ctx context.Context, key, method, path string, status int, body []byte) error
}

// Server wires the registry dependencies into an HTTP handler.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	repo     domain.ConfigObjectRepository
	idem     idempotencyStore
	verifier *auth.Verifier
	metrics  *observability.Metrics
	health   *Health
	// invariant reports why the platform must not serve tenant data, or nil when
	// it may. Nil (unset) leaves the gate open, which is what unit tests and any
	// deployment without a sentinel get (ADR-SECURITY-002).
	invariant  func() error
	graph      *graph.Service        // M2.3 graph transport; nil until SetGraph
	graphRepo  domain.GraphTraversal // direct (one-hop) lookups
	diag       http.Handler          // operational diagnostics; nil until SetDiagnostics
	governance governanceExecutor    // constitutional operations; nil until SetGovernance

	// Read-only governance/audit query dependencies; nil until SetGovernanceQuery.
	auditRead     auditReadPort
	auditVerifier audit.ChainVerifier
	schedStatus   func() SchedulerView

	// Administration APIs; nil until SetAdmin. adminSnapshot reuses diagnostics;
	// adminRunIntegrity reuses the verification scheduler.
	adminSnapshot     func(context.Context) diag.Snapshot
	adminRunIntegrity func(context.Context) AdminIntegrityRun

	// Event delivery administration; nil until SetWebhooks.
	webhooks      webhookRegistry
	webhookTester func(context.Context, events.Webhook) (events.DeliveryStatus, error)

	// Event consumption/replay administration; nil until SetWebhookConsume.
	deliveryOps events.DeliveryOps
	replayJobs  events.ReplayJobStore

	// Policy automation administration; nil until SetPolicies.
	policies     policyRegistry
	policyTester func(context.Context, policy.Policy) (policy.ExecutionStatus, error)

	// Read-only execution timeline; nil until SetTimeline.
	timeline timelineService

	// Read-only compliance & evidence engine; nil until SetCompliance.
	compliance       complianceService
	complianceExport func()

	// User registry; nil until SetUsers. app_user is global (ADR-IDENTITY-002
	// §3), so nothing here is tenant-scoped.
	users       domain.UserRepository
	adminAudit  domain.AdminAuditReader
	memberships domain.MembershipRepository

	// Team registry; nil until SetTeams. team is TENANT-OWNED, like membership
	// above, so nothing here needs an exemption from row-level security.
	teams domain.TeamRepository

	// CMDB Asset registry; nil until SetAssets. asset/asset_relationship are
	// TENANT-OWNED, like team above. assetGraph is a second graph.Service —
	// distinct from the M2.3 one above, which walks configuration_object's
	// dependency_edge — traversing asset_relationship instead, over the same
	// domain.GraphTraversal interface (ADR-ASSET-001 §4).
	assets         domain.AssetRepository
	assetGraph     *graph.Service
	assetGraphRepo domain.GraphTraversal

	// Notification registry (read-only); nil until SetNotifications.
	// notification is TENANT-OWNED, like team above, so nothing here needs an
	// exemption from row-level security.
	notifications notificationRegistry

	// Setting registry; nil until SetSettings. setting is TENANT-OWNED, like
	// team above, so nothing here needs an exemption from row-level security.
	settings domain.SettingRepository

	// Multi-approver quorum read side (ADR-GOV-005); nil until SetApprovals.
	// approval_record is TENANT-OWNED, like setting above. The write side is
	// wired into the Governance Engine directly (governance.ApprovalRecorder),
	// not here — recording an approval is a constitutional mutation, listing
	// who has approved so far is a read.
	approvals domain.ApprovalRepository

	// Organisation registry; nil until SetOrganizations. organization is global
	// for the same reason (ADR-IDENTITY-002 §3.1): tenant_id is discovered BY
	// reading this mapping, so the mapping cannot be scoped by it.
	organizations domain.OrganizationRepository

	// Tenant registry; nil until SetTenants. While nil the platform resolves
	// every request to the system tenant, which is the pre-tenancy behaviour.
	tenants domain.TenantRepository

	// Per-tenant rate limiter (ADR-SECURITY-004); nil when
	// ONEOPS_RATE_LIMIT_ENABLED=false, which s.rateLimit treats as pass-through.
	limiter *rateLimiter
}

// SetGovernance wires the Governance Engine behind the constitutional-operation
// endpoints. It is additive: the registry operates without it. The engine remains
// the sole owner of all business logic; the API is transport only.
func (s *Server) SetGovernance(e governanceExecutor) {
	s.governance = e
}

// SetGraph wires the dependency-graph traversal layer (M2.3). It is additive:
// the registry endpoints operate without it. The GraphService is constructed
// here from the traversal repository, unchanged from M2.2.
func (s *Server) SetGraph(gt domain.GraphTraversal) {
	s.graphRepo = gt
	s.graph = graph.NewService(gt)
}

// SetAssetGraph wires the CMDB relationship-graph traversal layer. It is
// additive: the registry endpoints operate without it. Mirrors SetGraph
// exactly, over asset_relationship instead of dependency_edge.
func (s *Server) SetAssetGraph(gt domain.GraphTraversal) {
	s.assetGraphRepo = gt
	s.assetGraph = graph.NewService(gt)
}

// SetDiagnostics wires the read-only operational diagnostics handler. It is
// additive: the registry operates without it. When set, it is served
// (authenticated, read permission) at /internal/diagnostics.
func (s *Server) SetDiagnostics(h http.Handler) {
	s.diag = h
}

// NewServer constructs a Server. ready checks downstream readiness (DB ping).
func NewServer(
	cfg *config.Config,
	log *slog.Logger,
	repo domain.ConfigObjectRepository,
	idem idempotencyStore,
	verifier *auth.Verifier,
	metrics *observability.Metrics,
	ready func(context.Context) error,
) *Server {
	s := &Server{
		cfg:      cfg,
		log:      log,
		repo:     repo,
		idem:     idem,
		verifier: verifier,
		metrics:  metrics,
		health:   newHealth(ready),
	}
	if cfg.RateLimitEnabled {
		s.limiter = newRateLimiter(float64(cfg.RateLimitRPS), cfg.RateLimitBurst, rateLimiterIdleTTL)
	}
	return s
}

// SetInvariantGate installs the check that decides whether the tenant-data
// surface may serve. Additive, like the other Set* wiring: a server without it
// serves unconditionally (ADR-SECURITY-002).
func (s *Server) SetInvariantGate(invariant func() error) { s.invariant = invariant }

// Router builds the fully-wired HTTP handler.
func (s *Server) Router() http.Handler {
	return otelhttp.NewHandler(s.routes(), "oneops-controlplane")
}

// routes builds the mux Router serves. It is separate from Router only so the
// route table stays reachable as a chi.Routes: the OpenAPI contract guard walks
// it to derive its subject set, and the alternative — restating the routes in a
// test — is a second list that drifts from this one silently.
func (s *Server) routes() *chi.Mux {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.requestID, s.logging, s.metrics.Middleware)

	r.Get("/healthz", s.health.Live)
	r.Get("/readyz", s.health.Ready)
	r.Get("/auth/config", s.serveAuthConfig)
	// /metrics rides the public listener only when no separate observability
	// listener is configured. It discloses audit-integrity state, per-route
	// request volumes and dependency health — no tenant data, but enough to
	// tell an attacker when audit verification is failing. Production config
	// validation refuses an empty ONEOPS_METRICS_ADDR.
	if s.cfg.MetricsAddr == "" {
		r.Handle("/metrics", s.metrics.Handler())
	}
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/docs", serveDocs)

	// The console, when built, is served from this origin so the browser and the
	// API share it — no CORS layer required. Falls back to the JSON service
	// descriptor when the console has not been built.
	if root, ok := webFS(); ok {
		r.Get("/", serveConsoleIndex(root))
		r.Handle("/assets/*", serveConsoleAssets(root))
	} else {
		r.Get("/", s.serveRoot)
	}

	// Operational diagnostics: read-only, authenticated, no secrets. Additive.
	if s.diag != nil {
		r.With(s.authenticate, s.requirePermission(auth.PermRead)).
			Method(http.MethodGet, "/internal/diagnostics", s.diag)
	}

	// Runtime profiling: mounted only when explicitly enabled (disabled default).
	s.mountPProf(r)

	r.Route("/v1", func(rt chi.Router) {
		// The invariant gate precedes authentication: when the boundary that
		// makes tenant identity mean anything is gone, no request on the
		// tenant-data surface may proceed — including one that would otherwise
		// authenticate perfectly (ADR-SECURITY-002). /healthz, /readyz and
		// /metrics stay outside it so a breached instance can still be
		// diagnosed.
		rt.Use(s.invariantGate)
		rt.Use(s.authenticate)
		// Per-tenant rate limiting runs after authentication so the tenant is
		// always resolved (ADR-SECURITY-004): a runaway or abusive tenant is
		// throttled without being able to starve any other tenant's share of
		// this instance's capacity.
		rt.Use(s.rateLimit)
		rt.With(s.requirePermission(auth.PermRead)).Get("/artifacts", s.listArtifacts)
		rt.With(s.requirePermission(auth.PermRead)).Get("/artifacts/{id}", s.getArtifact)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/artifacts", s.createArtifact)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/artifacts/bulk", s.bulkCreateArtifacts)
		rt.With(s.requirePermission(auth.PermWrite)).Patch("/artifacts/{id}", s.patchArtifact)
		rt.With(s.requirePermission(auth.PermDelete)).Delete("/artifacts/{id}", s.deleteArtifact)

		// M2.3 Dependency Graph — read-only, same authorization as config reads.
		rt.With(s.requirePermission(auth.PermRead)).Get("/configurations/{cfgId}/dependencies", s.getDependencies)
		rt.With(s.requirePermission(auth.PermRead)).Get("/configurations/{cfgId}/dependents", s.getDependents)
		rt.With(s.requirePermission(auth.PermRead)).Get("/configurations/{cfgId}/cycles", s.getCycles)

		// Governance — constitutional operations (§8). State-changing operations
		// require write; deletion requires delete. All logic lives in the engine.
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/ratify", s.ratifyGovernance)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/approve", s.approveGovernance)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/extend", s.extendGovernance)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/replace", s.replaceGovernance)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/suspend", s.suspendGovernance)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/deprecate", s.deprecateGovernance)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/withdraw", s.withdrawGovernance)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/governance/{id}/archive", s.archiveGovernance)
		rt.With(s.requirePermission(auth.PermDelete)).Delete("/governance/{id}", s.deleteGovernance)

		// Governance — read-only query APIs (state, history, audit chain/events,
		// verification). All read; same authorization as configuration reads.
		rt.With(s.requirePermission(auth.PermRead)).Get("/governance/{id}", s.getGovernanceState)
		rt.With(s.requirePermission(auth.PermRead)).Get("/governance/{id}/history", s.getGovernanceHistory)
		rt.With(s.requirePermission(auth.PermRead)).Get("/governance/{id}/audit", s.getAuditChain)
		rt.With(s.requirePermission(auth.PermRead)).Get("/governance/{id}/audit/events", s.getAuditEvents)
		rt.With(s.requirePermission(auth.PermRead)).Get("/governance/{id}/verification", s.getVerification)
		// Multi-approver quorum (ADR-GOV-005) — who has approved so far.
		rt.With(s.requirePermission(auth.PermRead)).Get("/governance/{id}/approvals", s.getGovernanceApprovals)

		// Administration & Operations — read-only except the explicit integrity
		// run (which reuses the existing scheduler). All require admin permission.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/status", s.adminStatus)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/integrity", s.adminIntegrity)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/integrity/run", s.adminIntegrityRun)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/metrics", s.adminMetrics)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/config", s.adminConfig)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/report", s.adminReport)

		// Tenant registry — the isolation boundary every other row belongs to.
		//
		// These are platform operations, not tenant operations, and they are the
		// one part of /admin that row-level security does not confine: the
		// registry is exempt by necessity, since resolving a token to a tenant
		// must precede binding one (ADR-TENANCY-001 §4).
		//
		// They previously required PermAdmin, the same permission that
		// administers webhooks inside a tenant. Any tenant administrator could
		// therefore enumerate every customer, register tenants binding external
		// identifiers of its choosing, and suspend a different customer — locking
		// them out of the platform entirely. Verified against the running
		// service; see ADR-AUTHZ-001.
		rt.With(s.requirePlatformAdmin).Get("/admin/tenants", s.listTenants)
		rt.With(s.requirePlatformAdmin).Post("/admin/tenants", s.createTenant)
		rt.With(s.requirePlatformAdmin).Patch("/admin/tenants/{id}", s.patchTenant)

		// The user registry is a platform operation like the tenant registry:
		// app_user is global, so administering it is not an act inside any one
		// tenant's boundary (ADR-AUTHZ-001, ADR-IDENTITY-002 §3.1).
		rt.With(s.requirePlatformAdmin).Get("/admin/users", s.listUsers)
		rt.With(s.requirePlatformAdmin).Post("/admin/users", s.createUser)
		rt.With(s.requirePlatformAdmin).Get("/admin/users/{id}", s.getUser)
		rt.With(s.requirePlatformAdmin).Patch("/admin/users/{id}", s.patchUser)

		// The organisation registry is the most privileged of the three.
		// Creating an organisation creates the Tenant realising it, and
		// suspending one cascades to that tenant (ADR-IDENTITY-001 §7.1, §8.3)
		// — so a caller who reached these routes could mint isolation
		// boundaries and lock any customer out of the platform. organization is
		// global and outside row-level security, which makes this middleware
		// the only control in front of them (ADR-IDENTITY-002 §3.1).
		rt.With(s.requirePlatformAdmin).Get("/admin/organizations", s.listOrganizations)
		rt.With(s.requirePlatformAdmin).Post("/admin/organizations", s.createOrganization)
		rt.With(s.requirePlatformAdmin).Get("/admin/organizations/{id}", s.getOrganization)
		rt.With(s.requirePlatformAdmin).Patch("/admin/organizations/{id}", s.patchOrganization)

		// Administrative audit — the constitutional read boundary
		// (ADR-AUDIT-007 §6.5). Platform administrators only: the table is
		// outside row-level security by §6.4, and the store behind this route
		// is built from the privileged pool so the request-path database role
		// holds no read access of its own. There is exactly one read path.
		rt.With(s.requirePlatformAdmin).Get("/admin/audit/events", s.listAdminAudit)

		// Membership administration. Unlike users and organizations, membership
		// is TENANT-OWNED: it is in TenantOwnedTables and carries row-level
		// security, so the isolation is the database policy and the permission
		// is the tenant-administration tier, as it is for webhooks and policies.
		// requirePlatformAdmin would be wrong twice over — it is not a platform
		// registry, and that middleware requires the system tenant, whose
		// connection the policy would confine to system rows.
		//
		// Mounted here rather than under /admin/organizations deliberately: that
		// prefix is a global registry, and every route beneath it must be
		// platform-admin.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/memberships", s.listMemberships)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/memberships", s.grantMembership)
		rt.With(s.requirePermission(auth.PermAdmin)).Delete("/admin/memberships/{id}", s.revokeMembership)

		// Team administration. Team is TENANT-OWNED, exactly like membership
		// above and for the same reason: it is in TenantOwnedTables and carries
		// row-level security, so the permission tier is tenant administration,
		// not requirePlatformAdmin.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/teams", s.listTeams)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/teams", s.createTeam)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/teams/{id}", s.getTeam)
		rt.With(s.requirePermission(auth.PermAdmin)).Patch("/admin/teams/{id}", s.patchTeam)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/teams/{id}/members", s.listTeamMembers)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/teams/{id}/members", s.addTeamMember)
		rt.With(s.requirePermission(auth.PermAdmin)).Delete("/admin/teams/{id}/members/{userId}", s.removeTeamMember)

		// CMDB Asset administration. asset/asset_relationship are TENANT-OWNED,
		// exactly like team above, so the permission tier is tenant
		// administration, not requirePlatformAdmin. The relationship routes are
		// a static collection (/admin/assets/relationships) mounted alongside
		// the per-asset routes, the same shape webhooks' deadletters collection
		// uses next to /admin/webhooks/{id}.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets", s.listAssets)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/assets", s.createAsset)
		// Bulk import/export/duplicate-detection (E1.4), static collections
		// mounted alongside /admin/assets/relationships for the same reason.
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/assets/import", s.importAssets)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/export", s.exportAssets)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/duplicates", s.duplicateAssets)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/assets/relationships", s.createAssetRelationship)
		rt.With(s.requirePermission(auth.PermAdmin)).Delete("/admin/assets/relationships/{id}", s.deleteAssetRelationship)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/{id}", s.getAsset)
		rt.With(s.requirePermission(auth.PermAdmin)).Patch("/admin/assets/{id}", s.patchAsset)
		rt.With(s.requirePermission(auth.PermAdmin)).Delete("/admin/assets/{id}", s.deleteAsset)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/{id}/relationships", s.listAssetRelationships)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/{id}/dependencies", s.getAssetDependencies)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/{id}/dependents", s.getAssetDependents)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/{id}/service-map", s.getAssetServiceMap)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/assets/{id}/history", s.getAssetHistory)

		// Notification administration (read-only). notification is TENANT-OWNED,
		// exactly like team above, so the permission tier is tenant
		// administration, not requirePlatformAdmin. There is no create/patch
		// route: notifications are produced only by the policy Notifier (and any
		// future producer), never directly over HTTP.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/notifications", s.listNotifications)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/notifications/{id}", s.getNotification)

		// Setting administration. setting is TENANT-OWNED, exactly like team
		// and notification above, so the permission tier is tenant
		// administration, not requirePlatformAdmin. There is one PUT per key
		// rather than a bulk PATCH: each key carries its own optimistic-lock
		// row_version, and a bulk write could consume several versions while
		// reporting only one outcome — the same reason patchTeam refuses to
		// change name and status in one call.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/settings", s.listSettings)
		rt.With(s.requirePermission(auth.PermAdmin)).Put("/admin/settings/{key}", s.putSetting)

		// Event delivery — webhook administration (admin permission).
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/webhooks", s.listWebhooks)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/webhooks", s.createWebhook)
		rt.With(s.requirePermission(auth.PermAdmin)).Patch("/admin/webhooks/{id}", s.patchWebhook)
		rt.With(s.requirePermission(auth.PermAdmin)).Delete("/admin/webhooks/{id}", s.deleteWebhook)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/webhooks/{id}/test", s.testWebhook)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/webhooks/{id}/deliveries", s.listWebhookDeliveries)

		// Event consumption & replay (admin). Static collection routes are
		// registered before the {id} routes so chi resolves them unambiguously.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/webhooks/deadletters", s.listDeadLetters)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/webhooks/deadletters/retry", s.retryDeadLetters)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/webhooks/replay/jobs", s.listReplayJobs)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/webhooks/replay/jobs/{jobID}", s.getReplayJob)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/webhooks/{id}/replay", s.replayWebhook)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/webhooks/{id}/deliveries/{deliveryID}", s.getDelivery)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/webhooks/{id}/deliveries/{deliveryID}/retry", s.retryDelivery)

		// Policy automation (admin). Consumes committed events only; isolated from
		// governance/audit/delivery.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/policies", s.listPolicies)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/policies", s.createPolicy)
		rt.With(s.requirePermission(auth.PermAdmin)).Patch("/admin/policies/{id}", s.patchPolicy)
		rt.With(s.requirePermission(auth.PermAdmin)).Delete("/admin/policies/{id}", s.deletePolicy)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/policies/{id}/executions", s.listPolicyExecutions)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/policies/{id}/test", s.testPolicy)
		// Composed run inspection (ADR-POLICY-001 W4): a run's progress is a pure
		// projection over its Executions, never a stored entity.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/policies/{id}/runs/{runId}", s.getPolicyRun)

		// Execution timeline — read-only operational read model over existing data.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/timeline/{eventID}", s.getEventTimeline)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/governance/{id}/timeline", s.getGovernanceTimeline)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/replay/{jobID}/timeline", s.getReplayTimeline)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/policies/{id}/timeline", s.getPolicyTimeline)

		// Compliance & evidence — read-only. Static /reports before {governanceID}.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/compliance/reports", s.getComplianceReports)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/compliance/{governanceID}", s.getComplianceSummary)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/compliance/{governanceID}/evidence", s.getComplianceEvidence)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/compliance/{governanceID}/checks", s.getComplianceChecks)
	})

	return r
}

// MetricsServer builds the observability listener, or nil when /metrics is
// served on the public listener instead.
//
// It carries no authentication and must not be exposed by the public ingress:
// the isolation is the bind address. Kept deliberately separate from Router so
// nothing tenant-facing can be added to it by accident.
func (s *Server) MetricsServer() *http.Server {
	if s.cfg.MetricsAddr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.metrics.Handler())
	return &http.Server{
		Addr:              s.cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// HTTPServer builds an *http.Server with production timeouts.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.cfg.HTTPAddr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func (s *Server) serveRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such resource")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "oneops-controlplane",
		"version": version.Version,
		"openapi": "/openapi.yaml",
		"docs":    "/docs",
	})
}

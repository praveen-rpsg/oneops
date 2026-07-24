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
	cfg        *config.Config
	log        *slog.Logger
	repo       domain.ConfigObjectRepository
	idem       idempotencyStore
	verifier   *auth.Verifier
	metrics    *observability.Metrics
	health     *Health
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

	// Tenant registry; nil until SetTenants. While nil the platform resolves
	// every request to the system tenant, which is the pre-tenancy behaviour.
	tenants domain.TenantRepository
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
	return &Server{
		cfg:      cfg,
		log:      log,
		repo:     repo,
		idem:     idem,
		verifier: verifier,
		metrics:  metrics,
		health:   newHealth(ready),
	}
}

// Router builds the fully-wired HTTP handler.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.requestID, s.logging, s.metrics.Middleware)

	r.Get("/healthz", s.health.Live)
	r.Get("/readyz", s.health.Ready)
	r.Get("/auth/config", s.serveAuthConfig)
	r.Handle("/metrics", s.metrics.Handler())
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
		rt.Use(s.authenticate)
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

		// Administration & Operations — read-only except the explicit integrity
		// run (which reuses the existing scheduler). All require admin permission.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/status", s.adminStatus)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/integrity", s.adminIntegrity)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/integrity/run", s.adminIntegrityRun)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/metrics", s.adminMetrics)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/config", s.adminConfig)
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/report", s.adminReport)

		// Tenant registry — the isolation boundary every other row belongs to.
		// Administering tenants is strictly more privileged than administering
		// data inside one, so these require admin like the rest of /admin.
		rt.With(s.requirePermission(auth.PermAdmin)).Get("/admin/tenants", s.listTenants)
		rt.With(s.requirePermission(auth.PermAdmin)).Post("/admin/tenants", s.createTenant)
		rt.With(s.requirePermission(auth.PermAdmin)).Patch("/admin/tenants/{id}", s.patchTenant)

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

	return otelhttp.NewHandler(r, "oneops-controlplane")
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

// Command controlplane is the OneOps Configuration Registry HTTP service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/alerting"
	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/authority"
	"github.com/rpsg/oneops/internal/collector"
	"github.com/rpsg/oneops/internal/compliance"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/diag"
	"github.com/rpsg/oneops/internal/escalation"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/governance"
	"github.com/rpsg/oneops/internal/grouping"
	"github.com/rpsg/oneops/internal/httpapi"
	"github.com/rpsg/oneops/internal/notification"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/ops"
	"github.com/rpsg/oneops/internal/policy"
	"github.com/rpsg/oneops/internal/safehttp"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
	"github.com/rpsg/oneops/internal/timeline"
	"github.com/rpsg/oneops/pkg/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	startedAt := time.Now()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	rootCtx := context.Background()

	// Background workers are singletons by design — the relay's cursor is a
	// read-modify-write and ClaimDue is not an atomic claim — but the deployment
	// runs multiple replicas. Every worker's Run is collected here and started
	// only on the instance that wins leadership (advisory lock), so exactly one
	// process runs them. Two replicas without this double-deliver every webhook
	// and double-execute every policy action (verified live).
	var workers []func(context.Context) error

	shutdownTracing, err := observability.SetupTracing(rootCtx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}

	pool, err := postgres.NewPool(rootCtx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.WaitForDB(rootCtx, pool, 30*time.Second); err != nil {
		return err
	}

	if cfg.AutoMigrate {
		migCtx, cancel := context.WithTimeout(rootCtx, 30*time.Second)
		err := migrate.Up(migCtx, pool)
		cancel()
		if err != nil {
			return err
		}
	}

	// Request-scoped work runs on a second pool over the same database. Its
	// connections assume oneops_app and carry the tenant resolved at the
	// authentication boundary, so PostgreSQL row-level security confines every
	// query to that tenant (ADR-TENANCY-001 §3).
	//
	// `pool` above keeps the connecting role and its RLS bypass. Migrations
	// need it, and so do the background workers below: they process every
	// tenant from one process and would otherwise read nothing — the audit
	// integrity sweeper in particular would report healthy precisely because it
	// could no longer see anything to check.
	//
	// The split is by pool, not by credential: the same DSN is used for both,
	// so there is no second secret to provision or rotate.
	appPool, err := postgres.NewTenantScopedPool(rootCtx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer appPool.Close()

	repo := postgres.NewConfigObjectRepo(appPool)
	idem := postgres.NewIdempotencyStore(appPool)

	// Enterprise SSO: the default IdP (ONEOPS_JWT_ISSUER/...) plus any
	// tenant-brought IdPs from ONEOPS_ADDITIONAL_IDPS, each verified with its
	// own issuer, audience and keys. config.Load already validated this set
	// (no duplicate issuers, every entry has a key source), so a construction
	// error here would be a bug in that validation, not a runtime condition.
	idpConfigs := make([]auth.IDPConfig, 0, 1+len(cfg.AdditionalIDPs))
	idpConfigs = append(idpConfigs, auth.IDPConfig{
		Issuer: cfg.JWTIssuer, Audience: cfg.JWTAudience, HMACKey: cfg.JWTHMACKey, JWKSURL: cfg.JWKSURL,
	})
	for _, extra := range cfg.AdditionalIDPs {
		idpConfigs = append(idpConfigs, auth.IDPConfig{
			Issuer: extra.Issuer, Audience: extra.Audience, HMACKey: extra.HMACKey, JWKSURL: extra.JWKSURL,
		})
	}
	verifier, err := auth.NewMultiVerifier(idpConfigs)
	if err != nil {
		return fmt.Errorf("build auth verifier: %w", err)
	}
	metrics := observability.NewMetrics()
	execMetrics := ops.NewExecutiveMetrics(metrics.Registry())

	// The one set of platform invariants. Startup refuses to boot on any of them
	// and the sentinel re-verifies all of them for the life of the process, from
	// this single definition — so an invariant cannot be enforced at one point and
	// not the other (ADR-SECURITY-003).
	//
	// Order matters and is preserved by ops.CheckAll: the ownership check queries
	// columns the schema check proves exist, so a broken schema must short-circuit
	// before it produces a database error instead of a finding.
	platformInvariants := []ops.Invariant{
		{
			Name:  "ownership-model schema invariants",
			Check: func(c context.Context) ([]string, error) { return postgres.NewSchemaValidator(pool).Validate(c) },
		},
		{
			Name:  "ownership graph consistency",
			Check: func(c context.Context) ([]string, error) { return postgres.NewOwnershipValidator(pool).Validate(c) },
		},
		{
			// Recorded as "future hardening" by ADR-CONCURRENCY-004 and never
			// built: a cursor restored ahead of its log silently skips every
			// event in between (ADR-TENANCY-011). Registering it here gives it
			// both the boot gate and the sentinel at once — the point of the
			// registry (ADR-SECURITY-003).
			Name:  "relay cursors within the audit log",
			Check: func(c context.Context) ([]string, error) { return postgres.NewCursorValidator(pool).Validate(c) },
		},
	}
	invariantSentinel := ops.NewSentinel(
		"platform invariants",
		func(c context.Context) ([]string, error) { return ops.CheckAll(c, platformInvariants) },
		time.Duration(cfg.SchemaSentinelIntervalSeconds)*time.Second,
		logger, execMetrics,
	)

	// Readiness fails on a breach as well as on dependency loss: an instance that
	// is refusing to serve tenant data must not stay in the load balancer.
	ready := func(c context.Context) error {
		if err := pool.Ping(c); err != nil {
			return err
		}
		return invariantSentinel.Err()
	}
	srv := httpapi.NewServer(cfg, logger, repo, idem, verifier, metrics, ready)
	srv.SetInvariantGate(invariantSentinel.Err)
	srv.SetGraph(postgres.NewGraphRepo(appPool)) // M2.3 dependency-graph endpoints

	// Governance API: the engine owns the single atomic constitutional mutation
	// (ADR-AUDIT-005); its audit append is instrumented via MeteredAuditor. The
	// HTTP layer is transport only.
	// Two audit stores over the same table, for the two directions of access.
	//
	// auditStore keeps the owning pool: the integrity verifier, the event relay
	// and the policy consumer all sweep every tenant's chains from one process
	// and would read nothing if confined.
	//
	// The engine's own store and appender are tenant-scoped, because a
	// constitutional operation is tenant data. On the owning pool they were not:
	// a second tenant could POST /v1/governance/{id}/suspend against another
	// tenant's ratified artifact and receive HTTP 200 — the lifecycle changed,
	// the row version advanced, and an entry was written into the victim's
	// append-only chain attributed to the attacker's operation id. The read
	// endpoints refused the same id, because they resolve the configuration
	// object through the scoped repository first; the write path went straight
	// to the engine and never did. Verified against the running service.
	//
	// Both engine dependencies must share one pool: ADR-AUDIT-005 requires the
	// state change and its audit append to commit in a single transaction, and
	// a transaction cannot span two pools.
	auditStore := postgres.NewAuditStore(pool)

	// Startup invariant gate (ADR-TENANCY-006/007, ADR-SECURITY-003).
	//
	// Ownership resolution and isolation are only as strong as the schema
	// underneath them, and a restored database may be internally inconsistent —
	// split-brain history, or data owned by a tenant a partial registry restore
	// dropped. The platform proves both before it accepts traffic and refuses to
	// boot otherwise, naming an example so a human can repair it.
	//
	// This evaluates exactly the set the sentinel re-verifies. Previously the two
	// were written out separately here and only the schema check was sentinelled,
	// so the identical broken-ownership database refused to *start* while a
	// process already running served on it happily (proven live).
	if problems, verr := ops.CheckAll(rootCtx, platformInvariants); verr != nil {
		return fmt.Errorf("startup invariant validation failed: %w", verr)
	} else if len(problems) > 0 {
		for _, p := range problems {
			logger.Error("startup invariant validation", "problem", p)
		}
		return fmt.Errorf("refusing to start: a platform invariant does not hold "+
			"(%d problem(s)); repair before starting (see ADR-SECURITY-003)", len(problems))
	}

	auditVerifier := audit.NewVerifier(auditStore)
	scopedAuditStore := postgres.NewAuditStore(appPool)
	meteredAuditor := ops.NewMeteredAuditor(postgres.NewAuditAppender(appPool, scopedAuditStore), execMetrics)
	engine, err := governance.NewEngine(postgres.NewGovernanceStore(appPool), governance.AllowAllAuthorizer{}, meteredAuditor)
	if err != nil {
		return err
	}

	// M4 WP-1 — first runtime construction of the Authority Resolution Engine
	// (M3.1–M3.5). Until now it existed only under test: nothing in the running
	// system instantiated it. One AuthorityStore satisfies all four graph ports
	// and backs one Resolver, which the four evaluators share; ReplacementTest
	// composes them into §8 Replacement's precondition.
	//
	// Wiring is additive: it changes no existing operation. Without it the engine
	// refuses Replacement outright (ErrReplacementTesterUnavailable) rather than
	// performing it untested.
	// Tenant-scoped: the Replacement Test reads the dependency graph, artifact
	// citations and responsibility ownership of the objects being replaced, all
	// of which are tenant data, and it runs inside a request.
	authorityStore := postgres.NewAuthorityStore(appPool)
	authorityResolver := authority.NewResolver(authorityStore)
	replacementTest, err := authority.NewReplacementTest(
		authority.NewActiveDependencyEvaluator(authorityResolver),
		authority.NewResponsibilityEvaluator(authorityStore),
		authority.NewArtifactCitationEvaluator(authorityResolver, authorityStore),
		authority.NewGapEvaluator(authorityResolver, authorityStore),
	)
	if err != nil {
		return err
	}
	engine.SetReplacementTester(replacementTest)

	// Multi-approver approval quorum (ADR-GOV-005). Wiring is additive: it
	// changes no existing operation for any tenant whose
	// governance_required_approvals stays at its default of 1. Without it the
	// engine refuses Approval outright (ErrApprovalRecorderUnavailable) rather
	// than approving on an unenforced quorum.
	approvalStore := postgres.NewApprovalStore(appPool)
	engine.SetApprovalRecorder(approvalStore)

	srv.SetGovernance(engine)
	srv.SetApprovals(approvalStore)

	httpServer := srv.HTTPServer()

	// Observability listener. Bound separately so /metrics is reachable by the
	// scraper without being reachable through the public ingress — exposure
	// becomes a deployment decision rather than an authentication check
	// Prometheus would have to be taught to satisfy.
	metricsServer := srv.MetricsServer()
	if metricsServer != nil {
		go func() {
			logger.Info("observability listener started", "addr", cfg.MetricsAddr)
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("observability listener failed", "err", err)
			}
		}()
	}

	logger.Info("starting oneops control plane",
		"version", version.Version, "commit", version.Commit,
		"addr", cfg.HTTPAddr, "env", cfg.Env)

	ctx, stop := signal.NotifyContext(rootCtx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Audit-integrity verification scheduler (operational; ADR-AUDIT-005 frozen
	// verifier is delegated to unchanged). It stops when ctx is cancelled.
	var scheduler *ops.Scheduler
	if cfg.VerifyIntervalSeconds > 0 {
		scheduler, err = ops.New(
			auditVerifier,
			auditStore,
			ops.NewPromMetrics(metrics.Registry()),
			logger,
			ops.Config{
				Interval:      time.Duration(cfg.VerifyIntervalSeconds) * time.Second,
				RunTimeout:    time.Duration(cfg.VerifyTimeoutSeconds) * time.Second,
				RetryAttempts: cfg.VerifyRetryAttempts,
				RetryBackoff:  time.Second,
			},
		)
		if err != nil {
			return err
		}
		workers = append(workers, scheduler.Run)
	}

	// Read-only governance/audit query APIs: reuse the audit store's read methods,
	// the frozen verifier, and the scheduler's operational status.
	srv.SetGovernanceQuery(auditStore, auditVerifier, func() httpapi.SchedulerView {
		if scheduler == nil {
			return httpapi.SchedulerView{Enabled: false}
		}
		st := scheduler.Status()
		return httpapi.SchedulerView{
			Enabled:     true,
			HasRun:      st.HasRun,
			Running:     st.Running,
			LastRunAt:   st.LastRunAt,
			LastHealthy: st.LastHealthy,
			Stalled:     st.Stalled,
			ChainsTotal: st.ChainsTotal,
			ChainsOK:    st.ChainsOK,
			Failures:    st.Failures,
			Errors:      st.Errors,
		}
	})

	// Enterprise event delivery: a relay tails the committed audit log and a
	// dispatcher delivers signed webhooks. Both are decoupled from the
	// constitutional path (ADR-AUDIT-005) — they only read committed audit events.
	// Two instances over the same tables, deliberately.
	//
	// The relay and dispatcher below must see every tenant's subscriptions from
	// one process, so they take the owning pool and its RLS bypass. The
	// administration API takes the tenant-scoped pool, so one tenant cannot
	// read, re-secret, disable or delete another tenant's webhooks.
	//
	// A single shared instance previously served both, which meant the admin
	// API inherited the workers' bypass: any authenticated tenant could list
	// every tenant's endpoint URLs, rotate their HMAC secrets (returned in the
	// response, enabling forged signed deliveries) and disable their delivery
	// entirely. Verified against the running service before this change.
	webhookStore := postgres.NewWebhookStore(pool)
	webhookAdminStore := postgres.NewWebhookStore(appPool)
	eventMetrics := events.NewPromMetrics(metrics.Registry())
	relay := events.NewRelay(auditStore, webhookStore, webhookStore, webhookStore, eventMetrics, logger, events.RelayConfig{})
	// auditStore resolves event ownership authoritatively for the dispatcher.
	// It reads audit_event.tenant_id — written inside the governance transaction
	// and never rewritten — rather than trusting the owner label on the queued
	// delivery, which is forgeable by anyone with database write access
	// (ADR-TENANCY-003). It is on the privileged pool by design: the dispatcher
	// serves every tenant, and this read is the security source, not tenant
	// data returned to a caller.
	// Outbound delivery uses the SSRF-safe client: it refuses to dial loopback,
	// link-local (cloud metadata), or private addresses unless explicitly allowed,
	// so a tenant-supplied webhook URL cannot make the platform a confused deputy
	// against internal services (ADR-SECURITY-001).
	deliveryClient := safehttp.Client(15*time.Second, cfg.WebhookAllowPrivateTargets)
	dispatcher := events.NewDispatcher(webhookStore, webhookStore, auditStore, deliveryClient, eventMetrics, logger, events.DispatcherConfig{})
	workers = append(workers, relay.Run, dispatcher.Run)
	srv.SetWebhooks(webhookAdminStore, func(ctx context.Context, wh events.Webhook) (events.DeliveryStatus, error) {
		return dispatcher.Deliver(ctx, events.Delivery{
			ID:        "test_" + wh.ID,
			WebhookID: wh.ID,
			Event:     events.Event{Operation: "test", ChainID: wh.ID, CfgID: wh.ID, OccurredAt: time.Now().UTC()},
			Status:    events.StatusPending,
		})
	})

	// Event consumption & replay (PRS-019): replay + retention workers over the
	// committed audit log and existing deliveries. Both reuse existing components.
	consumeMetrics := events.NewPromConsumeMetrics(metrics.Registry())
	replayWorker := events.NewReplayWorker(webhookStore, auditStore, webhookStore, webhookStore, webhookStore, consumeMetrics, logger, events.ReplayConfig{})
	retentionWorker := events.NewRetentionWorker(webhookStore, consumeMetrics, logger, events.RetentionConfig{
		MaxAge: time.Duration(cfg.WebhookRetentionHours) * time.Hour,
	})
	workers = append(workers, replayWorker.Run, retentionWorker.Run)
	srv.SetWebhookConsume(webhookAdminStore, webhookAdminStore)

	// Policy automation (PRS-020): an isolated event consumer. It tails the
	// committed audit log with its own cursor, evaluates policies, and runs
	// pluggable actions asynchronously. Failures never affect Governance, Audit,
	// Replay, or Event Delivery.
	// Split for the same reason as the webhook stores above: the consumer and
	// executor process every tenant, while the administration API must be
	// confined to the caller's tenant.
	policyStore := postgres.NewPolicyStore(pool)
	policyAdminStore := postgres.NewPolicyStore(appPool)
	policyMetrics := policy.NewPromMetrics(metrics.Registry())
	// Policy HTTP actions POST to operator-supplied URLs too, so they share the
	// SSRF-safe client (ADR-SECURITY-001).
	policyActionClient := safehttp.Client(15*time.Second, cfg.WebhookAllowPrivateTargets)

	// Notification Service: persisted, tracked delivery of the platform's own
	// outbound notifications, replacing the fire-and-forget policy.Notifier call
	// with a real record (status, attempts, last error). Split for the same
	// reason as the webhook and policy stores above: the delivery worker
	// processes every tenant, while the read-only admin API must be confined to
	// the caller's tenant.
	notificationStore := postgres.NewNotificationStore(pool)
	notificationAdminStore := postgres.NewNotificationStore(appPool)
	notificationMetrics := notification.NewPromMetrics(metrics.Registry())
	notificationSvc := notification.NewService(notificationStore)
	// The webhook channel POSTs to operator-supplied URLs too, so it shares the
	// SSRF-safe client (ADR-SECURITY-001). No EmailSender is wired yet anywhere
	// in this service (policy.DefaultRegistry's own email action is also nil
	// below), so the email channel fails its attempts until one is configured.
	notificationHTTPClient := safehttp.Client(15*time.Second, cfg.WebhookAllowPrivateTargets)
	notificationWorker := notification.NewWorker(notificationStore, nil, notificationHTTPClient, notificationMetrics, logger, notification.WorkerConfig{})
	workers = append(workers, notificationWorker.Run)
	srv.SetNotifications(notificationAdminStore)
	// policy.Notifier is backed by the Notification Service: a policy's
	// "notification" action now produces a tracked Notification row instead of
	// the previous fire-and-forget log line.
	policyNotifier := notification.NewPolicyNotifier(notificationSvc)
	policyRegistry := policy.DefaultRegistry(policyActionClient, nil, nil, policyNotifier, logger)
	policyConsumer := policy.NewConsumer(auditStore, policyStore, policyStore, policyStore, policyMetrics, logger, policy.ConsumerConfig{})
	// auditStore is the authoritative event-owner resolver, exactly as for the
	// webhook dispatcher: the executor re-derives the triggering event's owner
	// from audit_event and refuses to run a policy against another tenant's
	// event, rather than trusting the event snapshot in the queued execution
	// row (ADR-TENANCY-003).
	policyExecutor := policy.NewExecutor(policyStore, policyStore, auditStore, policyRegistry, policyMetrics, logger, policy.ExecutorConfig{})
	// Reconciliation sweep (closes the W2 executor's permanent-stall gap,
	// ADR-POLICY-001 §3): advanceRun enqueues a composed run's next step
	// AFTER its predecessor's success already committed, so a worker that
	// crashes in that window leaves the run wedged forever — ClaimDue never
	// reselects a succeeded row. The Reconciler periodically re-derives any
	// run in that state through the same advanceRunSteps path advanceRun
	// itself uses, so a webhook-of-actions run cannot wedge permanently on a
	// mid-flight crash. Same pool as the consumer/executor above (every
	// tenant's runs), same leader gate below (exactly one sweeper).
	policyReconciler := policy.NewReconciler(policyStore, policyStore, logger, policy.ReconcilerConfig{})
	// Gate evaluation (W3, ADR-POLICY-002): the same sweep resolves a run's
	// paused "approval" gates before re-deriving stalled runs, so a gate this
	// pass flips to passed is picked up by the very same pass's stalled-run
	// scan. Built over the admin pool (every tenant, no RLS binding), the same
	// posture policyStore itself has here — tenant scoping is explicit in
	// every query, never implicit (ADR-TENANCY-003).
	policyReconciler.SetApprovalGate(postgres.NewApprovalGateStore(pool))
	workers = append(workers, policyConsumer.Run, policyExecutor.Run, policyReconciler.Run)
	srv.SetPolicies(policyAdminStore, func(ctx context.Context, p policy.Policy) (policy.ExecutionStatus, error) {
		ex := policy.Execution{
			ID: "poltest_" + p.ID, PolicyID: p.ID, Status: policy.ExecPending,
			Event: policy.Event{Operation: "test", CfgID: p.ID, EventID: "test"},
		}
		return policyExecutor.Attempt(ctx, ex), nil
	})

	// Execution timeline (PRS-021): a read-only operational read model that
	// composes existing persisted data (audit, deliveries, replay jobs, policy
	// executions). It participates in no execution and modifies no subsystem.
	// Tenant-scoped: the timeline is a projection over governance, audit,
	// delivery and policy rows, so it reproduces their contents and must
	// reproduce their isolation. On the owning pool it exposed another tenant's
	// governance history — actor, operation, timestamps and correlation ids —
	// to any authenticated caller who knew a configuration id, which the
	// governance endpoints themselves correctly refused. Verified against the
	// running service. No worker consumes the timeline; it is read-only and
	// request-path only.
	timelineSvc := timeline.NewService(postgres.NewTimelineStore(appPool), timeline.NewPromMetrics(metrics.Registry()))
	srv.SetTimeline(timelineSvc)

	// Compliance & evidence engine (PRS-022): a read-only capability composing
	// deterministic evidence from existing persisted data (governance state, audit
	// verification, execution timeline). It participates in no execution.
	complianceMetrics := compliance.NewPromMetrics(metrics.Registry())
	complianceSvc := compliance.NewService(repo, auditVerifier, timelineSvc, complianceMetrics)
	srv.SetCompliance(complianceSvc, complianceMetrics.IncExport)

	// Tenant registry: the isolation boundary a bearer token is resolved
	// against. Wiring it makes the authentication boundary reject tokens
	// asserting an unregistered or suspended tenant; while unwired every
	// request resolves to the system tenant, which is the pre-tenancy
	// behaviour and keeps single-tenant deployments working unchanged.
	// Deliberately on the privileged pool. Resolving a bearer token to a tenant
	// necessarily happens before any tenant is known, so binding the registry
	// to a tenant-scoped connection would be circular. `tenant` is excluded
	// from row-level security for the same reason (ADR-TENANCY-001 §4).
	srv.SetTenants(postgres.NewTenantStore(pool))

	// Identity registries. Both are global (ADR-IDENTITY-002 §3.1) and outside
	// row-level security, so — unlike the tenant registry above — they need no
	// exemption from the tenant-scoped pool: no policy applies to app_user or
	// organization, and oneops_app holds the grants for both. Binding them to
	// the scoped pool keeps the privileged pool's surface as small as it was.
	//
	// The organisation registry writes the `tenant` row too, inside one
	// transaction (ADR-IDENTITY-001 §7.1); `tenant` is likewise outside RLS and
	// grantable, so the scoped role can complete that write.
	// Captured in a named variable, not only handed to SetUsers, because
	// internal/escalation's paging Worker also reads it (resolving an
	// on-call user's email) — see that wiring below for why reusing this
	// exact tenant-scoped instance, rather than a new privileged-pool
	// re-implementation, is correct (app_user is a GLOBAL table with no
	// row-level security, ADR-IDENTITY-002 §3.1).
	userStore := postgres.NewUserStore(appPool)
	srv.SetUsers(userStore)
	srv.SetOrganizations(postgres.NewOrganizationStore(appPool))
	// The administrative audit reader is built from the PRIVILEGED pool, and
	// that is the least-privilege choice rather than a shortcut: the tenant
	// pool's role holds no read access to administrative history, and granting
	// it any would put every customer's trail one statement away from the
	// request path on a table that has no row-level security by design
	// (ADR-AUDIT-007 §6.4, §6.5).
	srv.SetAdminAudit(postgres.NewAdminAuditQueryStore(pool))
	// Tenant-owned and under row-level security, so the tenant-scoped pool is
	// correct and no exemption is needed. Captured in a named variable for
	// the same reason userStore is above: internal/escalation's Worker also
	// reads it, for the page-time active-membership re-check.
	membershipStore := postgres.NewMembershipStore(appPool)
	srv.SetMemberships(membershipStore)
	// E-ACT.0's tenant-scoped user directory reuses the SAME appPool-scoped
	// instance rather than a second one: unlike E5.2b-2's escalation_state
	// (privileged Seeder/Worker + a distinct tenant-scoped read instance),
	// MembershipStore has no privileged role anywhere in this codebase — it
	// is appPool-scoped by construction, so there is nothing to dual-role.
	srv.SetTenantUserDirectory(membershipStore)
	// Team is tenant-owned and under row-level security, exactly like
	// membership above, so no exemption is needed here either.
	srv.SetTeams(postgres.NewTeamStore(appPool))
	// Setting is tenant-owned and under row-level security, exactly like team
	// above, so no exemption is needed here either.
	srv.SetSettings(postgres.NewSettingStore(appPool))
	// CMDB Asset/relationship graph: tenant-owned and under row-level
	// security, exactly like team above. SetAssetGraph wires the same
	// dependency-graph engine SetGraph wires for configuration_object, over
	// asset_relationship instead (ADR-ASSET-001 §4).
	srv.SetAssets(postgres.NewAssetStore(appPool))
	srv.SetAssetGraph(postgres.NewAssetGraphRepo(appPool))
	// Telemetry ingestion/query (E2.1): tenant-owned and under row-level
	// security, exactly like asset above — a TimescaleDB hypertable is a
	// regular table to this wiring rule (ADR-TELEMETRY-001).
	srv.SetTelemetry(postgres.NewTelemetryStore(appPool))
	// Telemetry retention + downsampling (E2.1b, ADR-TELEMETRY-002): both
	// workers process every tenant's chunks/buckets from one process, the
	// same category of background worker the webhook dispatcher and policy
	// consumer already are, so they are built from the PRIVILEGED pool, not
	// appPool — there is no single tenant's connection a whole-hypertable
	// drop_chunks or a cross-tenant aggregation could run on.
	telemetryRollupWorker := postgres.NewTelemetryRollupWorker(pool, logger, postgres.TelemetryRollupConfig{})
	telemetryRetentionWorker := postgres.NewTelemetryRetentionWorker(pool, logger, postgres.TelemetryRetentionConfig{})
	workers = append(workers, telemetryRollupWorker.Run, telemetryRetentionWorker.Run)

	// Collector check administration + the agentless collector framework
	// (E2.2a): collector_check is tenant-owned and under row-level security,
	// exactly like asset above, so the admin CRUD API is built from the
	// tenant-scoped pool. The scheduler, like the telemetry workers above, is
	// a cross-tenant background worker: it due-scans every tenant's checks
	// from one process, so it is built from the PRIVILEGED pool instead —
	// one CollectorCheckStore type, two roles, exactly mirroring
	// NotificationStore/notificationStore+notificationAdminStore above.
	collectorCheckAdminStore := postgres.NewCollectorCheckStore(appPool)
	collectorCheckStore := postgres.NewCollectorCheckStore(pool)
	srv.SetCollectorChecks(collectorCheckAdminStore)
	// A check target is customer-supplied — a URL to poll — exactly the same
	// SSRF vector a webhook delivery target is, so the executor shares the
	// SSRF-safe client (ADR-SECURITY-001): it refuses to dial loopback,
	// link-local (cloud metadata), or private addresses unless explicitly
	// allowed.
	collectorHTTPClient := safehttp.Client(15*time.Second, cfg.WebhookAllowPrivateTargets)
	collectorMetrics := collector.NewPromMetrics(metrics.Registry())
	collectorScheduler := collector.NewScheduler(
		collectorCheckStore, postgres.NewTelemetryStore(pool), collectorHTTPClient, collectorMetrics, logger, collector.Config{})
	workers = append(workers, collectorScheduler.Run)

	// Incident administration (E5.1, the ITSM core): incident/incident_event
	// are tenant-owned and under row-level security, exactly like asset
	// above, so the admin CRUD + lifecycle + timeline API is built from the
	// tenant-scoped pool. Captured in a named variable for the same reason
	// userStore is above: internal/escalation's Worker also reads it, to
	// re-check an incident is still open before paging it.
	incidentStore := postgres.NewIncidentStore(appPool)
	srv.SetIncidents(incidentStore)

	// Alert rule administration + the leader-gated evaluator (E3.1, E4.1):
	// alert_rule is tenant-owned and under row-level security, exactly like
	// collector_check above, so the admin CRUD API is built from the
	// tenant-scoped pool. The evaluator, like the telemetry and collector
	// workers above, is a cross-tenant background worker: it re-evaluates
	// every tenant's enabled rules from one process, so it is built from the
	// PRIVILEGED pool instead — one AlertRuleStore type, two roles, exactly
	// mirroring CollectorCheckStore above. Its telemetry read goes through
	// QueryRangeForTenant, not QueryRange: an explicit tenant_id predicate
	// rather than a bound-connection assumption, because this connection has
	// no single tenant bound to it (domain.TelemetryRepository's doc
	// comment). A firing is DERIVED and delivered through notificationSvc
	// (built above) — there is no separate alert-delivery path.
	//
	// E4.1 wires the same firing into Incident creation/linking: the
	// correlator is *postgres.IncidentStore built over the PRIVILEGED pool
	// too — the identical dual-role split AlertRuleStore already has above
	// (the tenant-scoped instance backs SetIncidents just above; this one
	// backs alerting.IncidentCorrelator instead) — every correlation
	// read/write it performs carries an explicit tenant_id predicate
	// (ADR-TENANCY-012), never assumed from this connection's (absent) RLS.
	srv.SetAlertRules(postgres.NewAlertRuleStore(appPool))
	alertRuleStore := postgres.NewAlertRuleStore(pool)
	incidentCorrelator := postgres.NewIncidentStore(pool)

	// Maintenance window administration + the evaluator's suppression check
	// (E3.3a, ADR-ALERTING-002): maintenance_window is tenant-owned and under
	// row-level security, exactly like alert_rule/incident above, so the
	// admin CRUD API is built from the tenant-scoped pool. The evaluator's
	// active-window check is the identical dual-role split AlertRuleStore
	// already has — one MaintenanceWindowStore type, two roles depending on
	// which pool constructs it. It must never be nil (see
	// alerting.NewEvaluator's doc comment): planned maintenance must not page
	// regardless of whether a deployment remembers to wire it.
	srv.SetMaintenanceWindows(postgres.NewMaintenanceWindowStore(appPool))
	maintenanceChecker := postgres.NewMaintenanceWindowStore(pool)

	// On-call schedule administration (E5.2a): on_call_schedule/
	// on_call_participant are tenant-owned and under row-level security,
	// exactly like maintenance_window above, so the admin CRUD + roster +
	// "who's on call" API is built from the tenant-scoped pool. Unlike
	// alert_rule/maintenance_window there is no privileged-pool counterpart —
	// no background worker in E5.2a consults this store directly (E5.2b-2's
	// escalation Worker below reads it, but as this exact tenant-scoped
	// instance bound per-row via domain.WithTenant, not a privileged one).
	// Captured in a named variable for the same reason userStore is above.
	onCallScheduleStore := postgres.NewOnCallScheduleStore(appPool)
	srv.SetOnCallSchedules(onCallScheduleStore)

	// Escalation policy + tier administration (E5.2b-1): escalation_policy/
	// escalation_tier are tenant-owned and under row-level security, exactly
	// like on_call_schedule above, so the admin CRUD + tier ladder API is
	// built from the tenant-scoped pool. Captured in a named variable for
	// the same reason userStore is above: E5.2b-2's escalation Worker below
	// walks a policy's tier ladder through this exact instance.
	escalationPolicyStore := postgres.NewEscalationPolicyStore(appPool)
	srv.SetEscalationPolicies(escalationPolicyStore)

	// Dependency-aware suppression (E3.3b, ADR-ALERTING-003): the down-check
	// and the suppression record are privileged and cross-tenant (built over
	// pool, same as maintenanceChecker above), but the dependency WALK itself
	// must stay RLS-scoped — see DependencySuppressionStore's own doc comment
	// for why bypassing RLS on asset_relationship would let a firing on one
	// tenant's asset be "explained away" by another tenant's edge. It is
	// therefore handed its own AssetGraphRepo built over appPool (the
	// tenant-scoped pool), not the instance srv.SetAssetGraph above uses —
	// same stateless adapter, wired a second time for a different caller. It
	// must never be nil either, for the identical "must not depend on
	// whether this was wired" reason maintenanceChecker's comment states
	// (alerting.NewEvaluator's doc comment).
	dependencyGraph := postgres.NewAssetGraphRepo(appPool)
	dependencyChecker := postgres.NewDependencySuppressionStore(pool, dependencyGraph, 0)

	alertingMetrics := alerting.NewPromMetrics(metrics.Registry())
	alertEvaluator := alerting.NewEvaluator(
		alertRuleStore, postgres.NewTelemetryStore(pool), notificationSvc, incidentCorrelator,
		maintenanceChecker, dependencyChecker, alertingMetrics, logger, alerting.Config{})
	workers = append(workers, alertEvaluator.Run)

	// Topology-aware incident grouping (E4.2, ADR-ALERTING-004): a SEPARATE
	// leader-gated reconciliation pass, not a hook inside the evaluator's own
	// correlate() — see internal/grouping's package doc comment for why. It
	// reads/writes incident.root_incident_id, so it is the identical
	// dual-role split incidentCorrelator above already draws: the store here
	// is *postgres.IncidentGroupingStore built over the PRIVILEGED pool,
	// while the admin CRUD API (srv.SetIncidents above) stays on the
	// tenant-scoped one. Its dependency walk must stay RLS-scoped — the same
	// reason dependencyChecker above is handed dependencyGraph (built over
	// appPool) rather than a privileged-pool traversal — so it reuses that
	// identical AssetGraphRepo construction over appPool, wired a third time
	// for a different caller (see dependencyGraph's own comment).
	groupingStore := postgres.NewIncidentGroupingStore(pool)
	groupingMetrics := grouping.NewPromMetrics(metrics.Registry())
	groupingReconciler := grouping.NewReconciler(groupingStore, dependencyGraph, groupingMetrics, logger, grouping.Config{})
	workers = append(workers, groupingReconciler.Run)

	// Escalation engine (E5.2b-2, ADR-ONCALL-003): pages an incident's
	// on-call chain and escalates through its policy's tiers while it stays
	// open and unacknowledged, stopping the moment it is acked or resolved.
	// TWO SEPARATE leader-gated passes, mirroring notificationWorker/
	// groupingReconciler above rather than a third shape: a Seeder that
	// enrols newly-open, unacked, alert-sourced incidents (decoupled from
	// alertEvaluator's own hot path — it never touches it), and a Worker
	// that claims due escalation_state rows and pages/escalates them.
	// escalation_state is the work-queue: privileged and cross-tenant, the
	// identical dual-role split notificationStore/groupingStore above
	// already draw (built over pool). Every OTHER read the Worker performs —
	// the incident, the policy's tiers, who is on call, active membership,
	// the on-call user's email — reuses the EXISTING tenant-scoped stores
	// captured above (incidentStore, escalationPolicyStore,
	// onCallScheduleStore, membershipStore, userStore) UNCHANGED, bound to
	// each claimed row's own tenant per call (domain.WithTenant) rather than
	// a privileged re-implementation of any of them — see
	// internal/escalation's own package doc comment for why re-deriving the
	// tenant this way is safe (ADR-TENANCY-003). Paging reuses notificationSvc
	// (built over pool above) to enqueue an email Notification — no change to
	// internal/notification at all.
	escalationStateStore := postgres.NewEscalationStateStore(pool)
	escalationMetrics := escalation.NewPromMetrics(metrics.Registry())
	escalationSeeder := escalation.NewSeeder(escalationStateStore, escalationMetrics, logger, escalation.SeederConfig{})
	escalationWorker := escalation.NewWorker(
		escalationStateStore, incidentStore, escalationPolicyStore, onCallScheduleStore,
		membershipStore, userStore, notificationSvc, escalationMetrics, logger, escalation.WorkerConfig{})
	workers = append(workers, escalationSeeder.Run, escalationWorker.Run)

	// NOC operational-overview projection (E7.1): a read-only, computed
	// aggregate over incident/alert-rule/asset/on-call/escalation-state — not
	// a reified Dashboard/Report entity (docs/PLATFORM-BUILD-PLAN.md §4). Its
	// incident/alert-rule/asset/on-call reads reuse the EXISTING tenant-scoped
	// instances srv.SetIncidents/SetAlertRules/SetAssets/SetOnCallSchedules
	// already wired above, unchanged. escalation_state's ONLY existing
	// instance (escalationStateStore just above) is privileged, so this is a
	// SECOND instance built over appPool instead — the identical dual-role
	// split every other tenant-owned table's admin-CRUD/privileged-worker
	// split already draws, applied here between a read-only projection and
	// the privileged Seeder/Worker.
	srv.SetNOCEscalations(postgres.NewEscalationStateStore(appPool))

	// Operational diagnostics + administration APIs: both reuse one diagnostics
	// builder; administration also reuses the verification scheduler.
	diagBuilder := buildDiagnostics(cfg, startedAt, pool, scheduler)
	srv.SetDiagnostics(diagBuilder.Handler())
	srv.SetAdmin(diagBuilder.Snapshot, func(ctx context.Context) httpapi.AdminIntegrityRun {
		if scheduler == nil {
			return httpapi.AdminIntegrityRun{Healthy: true}
		}
		rep := scheduler.RunOnce(ctx)
		return httpapi.AdminIntegrityRun{
			StartedAt:   rep.StartedAt,
			DurationMS:  rep.Duration.Milliseconds(),
			ChainsTotal: rep.ChainsTotal,
			ChainsOK:    rep.ChainsOK,
			Failures:    len(rep.Failures),
			Errors:      len(rep.Errors),
			Healthy:     rep.Healthy(),
		}
	})

	// Start the background workers on exactly one instance. Every replica serves
	// the HTTP API — those handlers are request-scoped and safe concurrently —
	// but only the leader runs the singleton workers, so a webhook is delivered
	// once and a policy action executes once regardless of replica count. A
	// standby promotes itself if the leader's advisory-lock connection drops.
	// Workers run under the leadership context, not the process context: when this
	// instance loses the lock it is demoted and lctx is cancelled, so its workers
	// stop rather than keep producing and dispatching alongside the new leader.
	// Re-verify the ownership-model schema invariants for the life of the
	// process, not just at boot (ADR-SECURITY-002). Started only after startup
	// validation passed, so its first verdict confirms a known-good boundary.
	go invariantSentinel.Run(ctx)

	// Start the background workers on exactly one instance. Every replica serves
	// the HTTP API — those handlers are request-scoped and safe concurrently —
	// but only the leader runs the singleton workers, so a webhook is delivered
	// once and a policy action executes once regardless of replica count. A
	// standby promotes itself if the leader's advisory-lock connection drops.
	// Workers run under the leadership context, not the process context: when this
	// instance loses the lock it is demoted and lctx is cancelled, so its workers
	// stop rather than keep producing and dispatching alongside the new leader.
	//
	// The workers are additionally gated on the schema sentinel: they read and
	// write tenant-owned rows through the same privileged path the API does, so
	// a breached boundary must stop them too — refusing HTTP while a relay keeps
	// fanning out across a broken isolation boundary would be a gate with a hole
	// in it.
	ops.RunAsLeader(ctx, cfg.DatabaseURL, ops.WorkerLeaderKey, logger, func(lctx context.Context) {
		for _, run := range workers {
			run := run
			go func() { _ = ops.RunWhileHealthy(lctx, invariantSentinel, logger, run) }()
		}
	})

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			execMetrics.IncStartupFailure()
			stop()
		}
	}()

	execMetrics.SetStartupDuration(time.Since(startedAt))
	execMetrics.SetDependencyUp("postgres", true)

	<-ctx.Done()
	logger.Info("shutdown signal received, draining")

	shutdownStart := time.Now()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownGrace)*time.Second)
	defer cancel()

	if metricsServer != nil {
		_ = metricsServer.Shutdown(shutdownCtx)
	}
	err = httpServer.Shutdown(shutdownCtx)
	execMetrics.SetShutdownDuration(time.Since(shutdownStart))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			execMetrics.IncShutdownTimeout()
		}
		return err
	}
	if err := shutdownTracing(shutdownCtx); err != nil {
		logger.Warn("tracing shutdown", "err", err)
	}
	logger.Info("shutdown complete")
	return nil
}

// buildDiagnostics assembles the read-only operational diagnostics handler from
// static metadata and live providers. It adapts ops.SchedulerStatus into the
// diag package's decoupled shape and reuses pool.Ping as the DB health check.
func buildDiagnostics(cfg *config.Config, startedAt time.Time, pool *pgxpool.Pool, scheduler *ops.Scheduler) *diag.Builder {
	migrationVersion, _ := migrate.Latest()

	schedStatus := func() diag.SchedulerStatus {
		if scheduler == nil {
			return diag.SchedulerStatus{Enabled: false}
		}
		st := scheduler.Status()
		return diag.SchedulerStatus{
			Enabled:             true,
			Running:             st.Running,
			HasRun:              st.HasRun,
			IntervalSeconds:     st.IntervalSeconds,
			LastRunAt:           st.LastRunAt,
			SinceLastRunSeconds: st.SinceLastRunSeconds,
			Stalled:             st.Stalled,
			LastHealthy:         st.LastHealthy,
			ChainsTotal:         st.ChainsTotal,
			ChainsOK:            st.ChainsOK,
			Failures:            st.Failures,
			Errors:              st.Errors,
		}
	}

	return diag.NewBuilder(diag.Options{
		Service:          cfg.ServiceName,
		Version:          version.Version,
		Commit:           version.Commit,
		MigrationVersion: migrationVersion,
		StartedAt:        startedAt,
		Config: diag.ConfigSummary{
			Env:                   cfg.Env,
			HTTPAddr:              cfg.HTTPAddr,
			ServiceName:           cfg.ServiceName,
			AuthEnabled:           cfg.AuthEnabled,
			AutoMigrate:           cfg.AutoMigrate,
			DBMaxConns:            cfg.DBMaxConns,
			TracingEnabled:        cfg.OTLPEndpoint != "",
			PProfEnabled:          cfg.PProfEnabled,
			VerifyIntervalSeconds: cfg.VerifyIntervalSeconds,
		},
		Modules: map[string]bool{
			"governance":             true,
			"audit":                  true,
			"dependency_graph":       true,
			"auth":                   cfg.AuthEnabled,
			"tracing":                cfg.OTLPEndpoint != "",
			"verification_scheduler": cfg.VerifyIntervalSeconds > 0,
			"pprof":                  cfg.PProfEnabled,
		},
		SchedulerStatus: schedStatus,
		DBName:          "postgres",
		DBCheck:         pool.Ping,
	})
}

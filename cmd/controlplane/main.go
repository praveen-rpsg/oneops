// Command controlplane is the OneOps Configuration Registry HTTP service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/authority"
	"github.com/rpsg/oneops/internal/compliance"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/diag"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/governance"
	"github.com/rpsg/oneops/internal/httpapi"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/ops"
	"github.com/rpsg/oneops/internal/policy"
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
	verifier := auth.NewVerifier(cfg.JWTIssuer, cfg.JWTAudience, cfg.JWTHMACKey, cfg.JWKSURL)
	metrics := observability.NewMetrics()
	execMetrics := ops.NewExecutiveMetrics(metrics.Registry())

	srv := httpapi.NewServer(cfg, logger, repo, idem, verifier, metrics, pool.Ping)
	srv.SetGraph(postgres.NewGraphRepo(appPool)) // M2.3 dependency-graph endpoints

	// Governance API: the engine owns the single atomic constitutional mutation
	// (ADR-AUDIT-005); its audit append is instrumented via MeteredAuditor. The
	// HTTP layer is transport only.
	auditStore := postgres.NewAuditStore(pool)
	auditVerifier := audit.NewVerifier(auditStore)
	meteredAuditor := ops.NewMeteredAuditor(postgres.NewAuditAppender(pool, auditStore), execMetrics)
	engine, err := governance.NewEngine(postgres.NewGovernanceStore(pool), governance.AllowAllAuthorizer{}, meteredAuditor)
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
	authorityStore := postgres.NewAuthorityStore(pool)
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

	srv.SetGovernance(engine)

	httpServer := srv.HTTPServer()

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
		go func() { _ = scheduler.Run(ctx) }()
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
	webhookStore := postgres.NewWebhookStore(pool)
	eventMetrics := events.NewPromMetrics(metrics.Registry())
	relay := events.NewRelay(auditStore, webhookStore, webhookStore, webhookStore, eventMetrics, logger, events.RelayConfig{})
	dispatcher := events.NewDispatcher(webhookStore, webhookStore, &http.Client{Timeout: 15 * time.Second}, eventMetrics, logger, events.DispatcherConfig{})
	go func() { _ = relay.Run(ctx) }()
	go func() { _ = dispatcher.Run(ctx) }()
	srv.SetWebhooks(webhookStore, func(ctx context.Context, wh events.Webhook) (events.DeliveryStatus, error) {
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
	go func() { _ = replayWorker.Run(ctx) }()
	go func() { _ = retentionWorker.Run(ctx) }()
	srv.SetWebhookConsume(webhookStore, webhookStore)

	// Policy automation (PRS-020): an isolated event consumer. It tails the
	// committed audit log with its own cursor, evaluates policies, and runs
	// pluggable actions asynchronously. Failures never affect Governance, Audit,
	// Replay, or Event Delivery.
	policyStore := postgres.NewPolicyStore(pool)
	policyMetrics := policy.NewPromMetrics(metrics.Registry())
	policyRegistry := policy.DefaultRegistry(&http.Client{Timeout: 15 * time.Second}, nil, nil, nil, logger)
	policyConsumer := policy.NewConsumer(auditStore, policyStore, policyStore, policyStore, policyMetrics, logger, policy.ConsumerConfig{})
	policyExecutor := policy.NewExecutor(policyStore, policyStore, policyRegistry, policyMetrics, logger, policy.ExecutorConfig{})
	go func() { _ = policyConsumer.Run(ctx) }()
	go func() { _ = policyExecutor.Run(ctx) }()
	srv.SetPolicies(policyStore, func(ctx context.Context, p policy.Policy) (policy.ExecutionStatus, error) {
		ex := policy.Execution{
			ID: "poltest_" + p.ID, PolicyID: p.ID, Status: policy.ExecPending,
			Event: policy.Event{Operation: "test", CfgID: p.ID, EventID: "test"},
		}
		return policyExecutor.Attempt(ctx, ex), nil
	})

	// Execution timeline (PRS-021): a read-only operational read model that
	// composes existing persisted data (audit, deliveries, replay jobs, policy
	// executions). It participates in no execution and modifies no subsystem.
	timelineSvc := timeline.NewService(postgres.NewTimelineStore(pool), timeline.NewPromMetrics(metrics.Registry()))
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

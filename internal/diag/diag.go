// Package diag builds a read-only operational snapshot of the platform and serves
// it as machine-readable JSON. It observes only: it holds no business logic,
// mutates nothing, and depends on no internal subsystem — callers inject the
// dynamic values (scheduler status, dependency check) so this package stays
// decoupled and cycle-free.
package diag

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ConfigSummary is a redacted view of runtime configuration. It deliberately
// contains no secrets (no JWT keys, no database credentials/DSN) — only
// operationally useful, non-sensitive settings.
type ConfigSummary struct {
	Env                   string `json:"env"`
	HTTPAddr              string `json:"http_addr"`
	ServiceName           string `json:"service_name"`
	AuthEnabled           bool   `json:"auth_enabled"`
	AutoMigrate           bool   `json:"auto_migrate"`
	DBMaxConns            int    `json:"db_max_conns"`
	TracingEnabled        bool   `json:"tracing_enabled"`
	PProfEnabled          bool   `json:"pprof_enabled"`
	VerifyIntervalSeconds int    `json:"verify_interval_seconds"`
}

// SchedulerStatus mirrors the scheduler's observable state without importing the
// ops package (the wiring adapts ops.SchedulerStatus into this).
type SchedulerStatus struct {
	Enabled             bool      `json:"enabled"`
	Running             bool      `json:"running"`
	HasRun              bool      `json:"has_run"`
	IntervalSeconds     float64   `json:"interval_seconds"`
	LastRunAt           time.Time `json:"last_run_at"`
	SinceLastRunSeconds float64   `json:"since_last_run_seconds"`
	Stalled             bool      `json:"stalled"`
	LastHealthy         bool      `json:"last_healthy"`
	ChainsTotal         int       `json:"chains_total"`
	ChainsOK            int       `json:"chains_ok"`
	Failures            int       `json:"failures"`
	Errors              int       `json:"errors"`
}

// DependencyStatus reports one downstream dependency's health.
type DependencyStatus struct {
	Name  string `json:"name"`
	Up    bool   `json:"up"`
	Error string `json:"error,omitempty"`
}

// Snapshot is the complete operational report. It is safe to log, expose to an
// operator, or serialize; it carries no secrets.
type Snapshot struct {
	Service          string             `json:"service"`
	Version          string             `json:"version"`
	Commit           string             `json:"commit"`
	Env              string             `json:"env"`
	MigrationVersion string             `json:"migration_version"`
	StartedAt        time.Time          `json:"started_at"`
	GeneratedAt      time.Time          `json:"generated_at"`
	UptimeSeconds    float64            `json:"uptime_seconds"`
	Healthy          bool               `json:"healthy"`
	Modules          map[string]bool    `json:"modules"`
	Config           ConfigSummary      `json:"config"`
	Scheduler        SchedulerStatus    `json:"scheduler"`
	Dependencies     []DependencyStatus `json:"dependencies"`
}

// Builder composes snapshots from static metadata plus injected live providers.
type Builder struct {
	service          string
	version          string
	commit           string
	migrationVersion string
	started          time.Time
	cfg              ConfigSummary
	modules          map[string]bool
	schedulerStatus  func() SchedulerStatus
	dbName           string
	dbCheck          func(ctx context.Context) error
	now              func() time.Time
}

// Options configures a Builder. Providers may be nil; a nil scheduler provider is
// reported as disabled, and a nil db check is reported as unknown-up.
type Options struct {
	Service          string
	Version          string
	Commit           string
	MigrationVersion string
	StartedAt        time.Time
	Config           ConfigSummary
	Modules          map[string]bool
	SchedulerStatus  func() SchedulerStatus
	DBName           string
	DBCheck          func(ctx context.Context) error
}

// NewBuilder constructs a Builder from static metadata and live providers.
func NewBuilder(o Options) *Builder {
	if o.Modules == nil {
		o.Modules = map[string]bool{}
	}
	if o.DBName == "" {
		o.DBName = "postgres"
	}
	return &Builder{
		service:          o.Service,
		version:          o.Version,
		commit:           o.Commit,
		migrationVersion: o.MigrationVersion,
		started:          o.StartedAt,
		cfg:              o.Config,
		modules:          o.Modules,
		schedulerStatus:  o.SchedulerStatus,
		dbName:           o.DBName,
		dbCheck:          o.DBCheck,
		now:              time.Now,
	}
}

// Snapshot builds the current operational snapshot, running the dependency check
// under ctx. Healthy is true when every dependency is up and the scheduler (if
// enabled and it has run) is neither stalled nor unhealthy.
func (b *Builder) Snapshot(ctx context.Context) Snapshot {
	now := b.now()
	s := Snapshot{
		Service:          b.service,
		Version:          b.version,
		Commit:           b.commit,
		Env:              b.cfg.Env,
		MigrationVersion: b.migrationVersion,
		StartedAt:        b.started,
		GeneratedAt:      now,
		Modules:          b.modules,
		Config:           b.cfg,
	}
	if !b.started.IsZero() {
		s.UptimeSeconds = now.Sub(b.started).Seconds()
	}

	db := DependencyStatus{Name: b.dbName, Up: true}
	if b.dbCheck != nil {
		if err := b.dbCheck(ctx); err != nil {
			db.Up = false
			db.Error = err.Error()
		}
	}
	s.Dependencies = []DependencyStatus{db}

	if b.schedulerStatus != nil {
		s.Scheduler = b.schedulerStatus()
	}

	// The scheduler degrades health only once it is enabled and has actually run,
	// and then only if it is stalled or its last sweep was unhealthy.
	schedulerDegraded := s.Scheduler.Enabled && s.Scheduler.HasRun &&
		(s.Scheduler.Stalled || !s.Scheduler.LastHealthy)
	s.Healthy = db.Up && !schedulerDegraded
	return s
}

// Handler returns an http.Handler that serves the JSON snapshot. It is read-only
// and side-effect free (a dependency ping aside). Non-GET methods get 405.
func (b *Builder) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap := b.Snapshot(r.Context())
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusOK
		if !snap.Healthy {
			status = http.StatusServiceUnavailable
		}
		w.WriteHeader(status)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snap)
	})
}

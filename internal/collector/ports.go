// Package collector is the agentless collector framework (E2.2a): a
// leader-gated scheduler that polls configured collector_check targets on
// their configured interval and writes each result as telemetry samples tied
// to the checked asset (domain.Sample, E2.1), mirroring internal/notification
// and internal/events' leader-gated delivery shape.
//
// It ships two executors — an HTTP uptime/synthetic check (E2.2a) and an
// SNMP v2c reachability check (Phase A.1, ADR-NET-001) — behind a Type
// dispatch that cloud/API pollers extend later without changing the
// scheduler itself.
package collector

import (
	"context"
	"net/http"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// HTTPDoer is the minimal HTTP client an "http" check needs (satisfied by
// *http.Client). The scheduler never constructs its own client: the caller
// injects one built by safehttp.Client, so a request to a customer-supplied
// check target is SSRF-guarded exactly like webhook delivery and the
// notification webhook channel (ADR-SECURITY-001) — this package does not,
// and must not, reimplement that guard.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Store is the persistence port the scheduler depends on: which checks are
// due, and recording that one ran. *postgres.CollectorCheckStore satisfies it
// when built over the PRIVILEGED pool, exactly as NotificationStore
// implements notification.Store over that same pool for its delivery
// Worker — the scheduler serves every tenant's due checks from one process,
// so it needs the connection that bypasses row-level security, while the
// admin CRUD API is built from the tenant-scoped pool (domain.
// CollectorCheckRepository) so one tenant cannot see or schedule another's
// checks.
type Store interface {
	// DueChecks returns up to limit enabled checks whose interval has
	// elapsed since LastRunAt (or that have never run), across every tenant,
	// oldest-due first. It is a plain due-scan, not an atomic claim: this
	// scheduler runs on exactly one instance at a time (RunAsLeader), so
	// nothing else can select the same row concurrently within one process,
	// and the brief window around a leadership handover is safe because a
	// re-sent (tenant, asset, metric, ts) sample is idempotent
	// (domain.TelemetryRepository.WriteSamples' ON CONFLICT DO NOTHING) —
	// a double-run in that window writes a duplicate sample at ~the same
	// timestamp, not a correctness bug.
	DueChecks(ctx context.Context, now time.Time, limit int) ([]*domain.CollectorCheck, error)
	// RecordRun sets a check's LastRunAt/LastStatus. This does NOT touch
	// RowVersion or UpdatedAt — see collector_check's migration header
	// comment for why a background confirmation must not consume the same
	// optimistic-lock version an operator's PATCH depends on.
	RecordRun(ctx context.Context, checkID string, runAt time.Time, status domain.CollectorCheckStatus) error
}

// Metrics receives scheduler observability signals. NopMetrics discards them.
type Metrics interface {
	IncChecked(status domain.CollectorCheckStatus)
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

// IncChecked implements Metrics.
func (NopMetrics) IncChecked(domain.CollectorCheckStatus) {}

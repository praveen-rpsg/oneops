package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultSentinelInterval is how often a continuously-held invariant is
// re-verified. It is short enough that a weakened boundary is caught in the same
// operational minute it is introduced, and cheap enough to run forever: the
// checks are catalogue reads, not table scans.
const DefaultSentinelInterval = 30 * time.Second

// SentinelMetrics observes invariant health. Nil is allowed.
type SentinelMetrics interface {
	SetInvariantBreached(breached bool)
	IncInvariantCheckFailure()
}

// Sentinel continuously re-verifies an invariant that the platform proved once
// at startup.
//
// The schema validator (ADR-TENANCY-007) establishes that row-level security is
// enabled and forced, that ownership columns are mandatory, and that the audit
// log's append-only guards exist — and the process then serves traffic for days.
// Every one of those is a *runtime-mutable* fact: one ALTER, one careless
// migration, one restore from an older dump, and the boundary is gone while the
// boot-time verdict still says it holds. Proven live: with row-level security
// disabled after startup, a tenant read another tenant's rows through the
// tenant-scoped pool while the process reported ready and logged nothing.
//
// A boundary that is only checked at boot is not enforced; it is assumed. The
// sentinel turns the check from an event into a property (ADR-SECURITY-002).
type Sentinel struct {
	name     string
	check    func(context.Context) ([]string, error)
	interval time.Duration
	log      *slog.Logger
	metrics  SentinelMetrics

	mu       sync.RWMutex
	breach   []string
	lastRun  time.Time
	lastErr  error
	verified bool
}

// NewSentinel builds a sentinel over a check that returns a list of problems.
// An empty list means the invariant holds. The sentinel starts in the
// unverified state: it has no opinion until its first run completes, so callers
// must treat "not yet verified" as distinct from "verified healthy".
func NewSentinel(name string, check func(context.Context) ([]string, error), interval time.Duration, log *slog.Logger, metrics SentinelMetrics) *Sentinel {
	if interval <= 0 {
		interval = DefaultSentinelInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Sentinel{name: name, check: check, interval: interval, log: log, metrics: metrics}
}

// Run re-verifies on the interval until ctx is done. It runs once immediately so
// a sentinel started after a passing boot check confirms the invariant is still
// held before the first interval elapses.
func (s *Sentinel) Run(ctx context.Context) {
	s.runOnce(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Sentinel) runOnce(ctx context.Context) {
	problems, err := s.check(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRun = time.Now().UTC()

	if err != nil {
		// The check itself failed (the database is unreachable mid-restart, say).
		// That is not evidence the invariant is broken, and treating it as a
		// breach would take the fleet down on every blip. Readiness already
		// covers dependency loss; record and carry the previous verdict.
		s.lastErr = err
		if s.metrics != nil {
			s.metrics.IncInvariantCheckFailure()
		}
		s.log.Warn("sentinel: invariant check failed to run; carrying previous verdict",
			"invariant", s.name, "err", err, "previously_breached", len(s.breach) > 0)
		return
	}
	s.lastErr = nil

	was := len(s.breach) > 0
	now := len(problems) > 0
	s.breach = problems
	s.verified = true

	if s.metrics != nil {
		s.metrics.SetInvariantBreached(now)
	}
	switch {
	case now && !was:
		// The transition that matters. Loud, once, with every problem named.
		s.log.Error("SENTINEL BREACH: an invariant proven at startup no longer holds; "+
			"refusing to serve until it is repaired",
			"invariant", s.name, "problems", len(problems), "detail", strings.Join(problems, "; "))
	case now && was:
		s.log.Warn("sentinel: invariant still breached", "invariant", s.name, "problems", len(problems))
	case !now && was:
		s.log.Warn("sentinel: invariant repaired; serving resumes", "invariant", s.name)
	}
}

// Breach returns the current problems, empty when the invariant holds.
func (s *Sentinel) Breach() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.breach...)
}

// Err reports why the platform must not serve, or nil when it may.
//
// An unverified sentinel reports an error: the process has not yet confirmed the
// invariant in this run, and the fail-closed reading of "unknown" is "do not
// serve". In practice startup validation has already passed by the time traffic
// arrives, so this is a very short window.
func (s *Sentinel) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.verified {
		return fmt.Errorf("invariant %q not yet verified", s.name)
	}
	if len(s.breach) == 0 {
		return nil
	}
	return fmt.Errorf("invariant %q breached: %s", s.name, strings.Join(s.breach, "; "))
}

// Healthy reports whether the invariant is verified and held.
func (s *Sentinel) Healthy() bool { return s.Err() == nil }

// LastRun reports when the invariant was last re-verified, for operators asking
// how stale the verdict is.
func (s *Sentinel) LastRun() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRun
}

// InvariantGuard is the part of a Sentinel that RunWhileHealthy needs.
type InvariantGuard interface {
	Healthy() bool
	Err() error
}

// runWhileHealthyPoll is how often a gated worker re-checks the guard. It is
// well below the sentinel interval so a breach stops work promptly.
const runWhileHealthyPoll = 2 * time.Second

// RunWhileHealthy runs a background worker only while an invariant holds, and
// stops it the moment the invariant is breached (ADR-SECURITY-002).
//
// The HTTP gate alone would be a hole: the relay, dispatcher and policy executor
// read and write tenant-owned rows through the same privileged path, so a
// boundary that no longer isolates tenants must stop them too. The worker is run
// under a context cancelled on breach — the same mechanism demotion already uses
// (ADR-CONCURRENCY-003), so a worker stopped this way releases its claims and
// records its outcomes exactly as it does on a leadership change
// (ADR-CONCURRENCY-006).
//
// It returns when ctx is done. While breached it waits, and it restarts the
// worker if the invariant is repaired, so an operator's fix restores service
// without a redeploy.
func RunWhileHealthy(ctx context.Context, guard InvariantGuard, log *slog.Logger, run func(context.Context) error) error {
	if log == nil {
		log = slog.Default()
	}
	for ctx.Err() == nil {
		if !guard.Healthy() {
			if !waitOrDone(ctx, runWhileHealthyPoll) {
				return ctx.Err()
			}
			continue
		}

		wctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- run(wctx) }()

		finished, breach := watchWhileHealthy(ctx, guard, done)
		cancel()
		if !finished {
			<-done // the worker observes the cancellation and returns
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if breach != nil {
			log.Error("worker stopped: platform invariant breached; not processing "+
				"tenant-owned work until it is repaired", "err", breach.Error())
		}
	}
	return ctx.Err()
}

// watchWhileHealthy blocks until the guard is breached, the worker returns, or
// ctx ends. It reports whether the worker already finished — the caller must not
// wait on `done` a second time if it did — and the breach (nil if none).
func watchWhileHealthy(ctx context.Context, guard InvariantGuard, done <-chan error) (finished bool, breach error) {
	t := time.NewTicker(runWhileHealthyPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-done:
			return true, nil
		case <-t.C:
			if err := guard.Err(); err != nil {
				return false, err
			}
		}
	}
}

// Invariant is one platform property that must hold both at startup and for the
// life of the process.
//
// It exists so those two enforcement points cannot diverge. ADR-SECURITY-002
// established that a boundary verified only at boot is not enforced but assumed,
// and put the schema check under a sentinel — but the ownership check was left
// behind, so the platform refused to *start* on a broken ownership graph while
// happily *serving* on one (proven live: readyz=200 and /v1=200 at runtime; the
// same database refused to boot). Fail-closed at boot and fail-open at runtime
// is not a policy; it is an accident of where the check happened to be called.
//
// Registering an invariant here gives it both enforcement points at once, which
// is what makes the omission structurally impossible rather than merely fixed
// (ADR-SECURITY-003).
type Invariant struct {
	// Name identifies the invariant in logs and in the breach report.
	Name string
	// Check returns the problems found, empty when the invariant holds.
	Check func(context.Context) ([]string, error)
}

// CheckAll evaluates invariants in order and stops at the first that reports
// problems or cannot run.
//
// Order is load-bearing and short-circuiting is deliberate: later invariants may
// depend on earlier ones holding. The ownership check queries columns the schema
// check proves exist, so running it against a schema already known to be broken
// produces a database error rather than a finding, and would bury the real
// problem under a spurious one.
//
// Problems are prefixed with the invariant's name so a breach report says which
// boundary failed.
func CheckAll(ctx context.Context, invariants []Invariant) ([]string, error) {
	for _, inv := range invariants {
		problems, err := inv.Check(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", inv.Name, err)
		}
		if len(problems) > 0 {
			out := make([]string, len(problems))
			for i, p := range problems {
				out[i] = inv.Name + ": " + p
			}
			return out, nil
		}
	}
	return nil, nil
}

// Package ops provides operational tooling for running OneOps in production. It
// adds no governance or audit semantics: it only observes, schedules, and reports
// over the existing, frozen subsystems. In particular it never verifies, hashes,
// canonicalizes, or mutates — audit-chain verification is delegated unchanged to
// the audit subsystem's Verifier (ADR-AUDIT-003/004/005 are untouched).
package ops

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

// ChainLister enumerates the audit chains to verify. *postgres.AuditStore
// satisfies it via a read-only query; declaring the port here keeps the scheduler
// dependency-inverted and unit-testable without a database.
type ChainLister interface {
	ListChainIDs(ctx context.Context) ([]string, error)
}

// Recorder receives operational signals from a verification run so they can be
// exported (e.g. Prometheus). It is behaviour-free: implementations only observe.
type Recorder interface {
	// ChainVerified is called once per chain that verified without a transport
	// error; res.OK distinguishes a healthy chain from a detected integrity break.
	ChainVerified(chainID string, res domain.VerifyResult, dur time.Duration)
	// ChainError is called when verification could not complete (transport/timeout).
	ChainError(chainID string, dur time.Duration)
	// RunCompleted is called once per sweep with the aggregate report.
	RunCompleted(report IntegrityReport)
}

// NopRecorder is the default Recorder: it discards every signal.
type NopRecorder struct{}

// ChainVerified implements Recorder.
func (NopRecorder) ChainVerified(string, domain.VerifyResult, time.Duration) {}

// ChainError implements Recorder.
func (NopRecorder) ChainError(string, time.Duration) {}

// RunCompleted implements Recorder.
func (NopRecorder) RunCompleted(IntegrityReport) {}

// ChainFailure is one chain whose verification detected an integrity break.
type ChainFailure struct {
	ChainID       string
	HeadSeq       int64
	FirstBreakSeq *int64
	BreakReason   string
	Checked       int64
}

// ChainError is one chain (or the listing itself, ChainID empty) that could not
// be verified due to a transport/timeout error rather than an integrity break.
type ChainError struct {
	ChainID string
	Err     string
}

// IntegrityReport is the outcome of one verification sweep. It is a diagnostic
// value — safe to log, expose to an operator, or assert on in tests.
type IntegrityReport struct {
	StartedAt   time.Time
	Duration    time.Duration
	ChainsTotal int
	ChainsOK    int
	Failures    []ChainFailure
	Errors      []ChainError
}

// Healthy reports whether the sweep found neither an integrity break nor an error.
func (r IntegrityReport) Healthy() bool {
	return len(r.Failures) == 0 && len(r.Errors) == 0
}

// Config parameterises the scheduler. Zero values are replaced with safe defaults.
type Config struct {
	// Interval between sweeps (default 5m).
	Interval time.Duration
	// RunTimeout bounds a single chain verification (default 30s).
	RunTimeout time.Duration
	// RetryAttempts is the number of RETRIES (not tries) for a transport error
	// during a single chain verification (default 0). Integrity breaks are never
	// retried — a detected break must surface immediately.
	RetryAttempts int
	// RetryBackoff waits between retries (default 0).
	RetryBackoff time.Duration
}

func (c *Config) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.RunTimeout <= 0 {
		c.RunTimeout = 30 * time.Second
	}
	if c.RetryAttempts < 0 {
		c.RetryAttempts = 0
	}
	if c.RetryBackoff < 0 {
		c.RetryBackoff = 0
	}
}

// Scheduler periodically verifies every audit chain and reports integrity via
// logs and a Recorder. It owns no audit logic; VerifyChain is the frozen audit
// Verifier. It is safe for a single goroutine (Run) plus ad-hoc RunOnce calls.
type Scheduler struct {
	verifier audit.ChainVerifier
	lister   ChainLister
	recorder Recorder
	log      *slog.Logger
	cfg      Config

	mu        sync.Mutex // guards the observable status below
	running   bool
	hasRun    bool
	lastRunAt time.Time
	lastRep   IntegrityReport
}

// New builds a Scheduler. verifier and lister are required; recorder defaults to
// NopRecorder and log to slog.Default(). Config zero-values get safe defaults.
func New(verifier audit.ChainVerifier, lister ChainLister, recorder Recorder, log *slog.Logger, cfg Config) (*Scheduler, error) {
	if verifier == nil {
		return nil, errNilVerifier
	}
	if lister == nil {
		return nil, errNilLister
	}
	if recorder == nil {
		recorder = NopRecorder{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Scheduler{verifier: verifier, lister: lister, recorder: recorder, log: log, cfg: cfg}, nil
}

// Run performs an immediate sweep and then one sweep per Interval until ctx is
// cancelled, at which point it returns ctx.Err() — supporting graceful shutdown.
func (s *Scheduler) Run(ctx context.Context) error {
	s.log.Info("audit integrity scheduler started",
		"interval", s.cfg.Interval.String(), "run_timeout", s.cfg.RunTimeout.String())

	s.setRunning(true)
	defer s.setRunning(false)

	s.RunOnce(ctx)

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("audit integrity scheduler stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-ticker.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce verifies every chain once and returns the diagnostic report. It never
// returns an error: listing/verification problems are captured in the report so a
// single bad chain never aborts the sweep or crashes the scheduler.
func (s *Scheduler) RunOnce(ctx context.Context) IntegrityReport {
	start := time.Now()
	report := IntegrityReport{StartedAt: start}

	chains, err := s.lister.ListChainIDs(ctx)
	if err != nil {
		report.Errors = append(report.Errors, ChainError{Err: err.Error()})
		report.Duration = time.Since(start)
		s.storeReport(report)
		s.log.Error("audit integrity: could not list chains", "err", err)
		s.recorder.RunCompleted(report)
		return report
	}
	report.ChainsTotal = len(chains)

	for _, chainID := range chains {
		if ctx.Err() != nil {
			report.Errors = append(report.Errors, ChainError{ChainID: chainID, Err: ctx.Err().Error()})
			break
		}
		res, dur, verr := s.verifyWithRetry(ctx, chainID)
		if verr != nil {
			report.Errors = append(report.Errors, ChainError{ChainID: chainID, Err: verr.Error()})
			s.recorder.ChainError(chainID, dur)
			s.log.Error("audit integrity: verification error", "chain_id", chainID, "err", verr)
			continue
		}
		s.recorder.ChainVerified(chainID, res, dur)
		if res.OK {
			report.ChainsOK++
			continue
		}
		report.Failures = append(report.Failures, ChainFailure{
			ChainID:       chainID,
			HeadSeq:       res.HeadSeq,
			FirstBreakSeq: res.FirstBreakSeq,
			BreakReason:   res.BreakReason,
			Checked:       res.Checked,
		})
		s.log.Error("audit integrity: CHAIN BREAK DETECTED",
			"chain_id", chainID,
			"first_break_seq", derefSeq(res.FirstBreakSeq),
			"reason", res.BreakReason,
			"head_seq", res.HeadSeq,
			"checked", res.Checked)
	}

	report.Duration = time.Since(start)
	s.storeReport(report)
	s.recorder.RunCompleted(report)
	if report.Healthy() {
		s.log.Info("audit integrity: sweep healthy",
			"chains", report.ChainsTotal, "ok", report.ChainsOK,
			"duration_ms", report.Duration.Milliseconds())
	} else {
		s.log.Error("audit integrity: sweep found problems",
			"chains", report.ChainsTotal, "ok", report.ChainsOK,
			"failures", len(report.Failures), "errors", len(report.Errors),
			"duration_ms", report.Duration.Milliseconds())
	}
	return report
}

// verifyWithRetry verifies one chain under a per-run timeout, retrying ONLY on a
// transport error (never on a detected integrity break). It returns the result,
// the elapsed time, and the final error if all attempts failed.
func (s *Scheduler) verifyWithRetry(ctx context.Context, chainID string) (domain.VerifyResult, time.Duration, error) {
	start := time.Now()
	var lastErr error
	for attempt := 0; attempt <= s.cfg.RetryAttempts; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, s.cfg.RunTimeout)
		res, err := s.verifier.VerifyChain(cctx, chainID)
		cancel()
		if err == nil {
			return res, time.Since(start), nil
		}
		lastErr = err
		if attempt == s.cfg.RetryAttempts {
			break
		}
		timer := time.NewTimer(s.cfg.RetryBackoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return domain.VerifyResult{}, time.Since(start), ctx.Err()
		}
	}
	return domain.VerifyResult{}, time.Since(start), lastErr
}

func derefSeq(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

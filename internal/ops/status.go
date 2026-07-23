package ops

import "time"

// SchedulerStatus is a read-only snapshot of the scheduler's observable state for
// operational diagnostics. It exposes no audit content beyond aggregate counts.
type SchedulerStatus struct {
	// Running is true while Run is active.
	Running bool
	// HasRun is true once at least one sweep has completed.
	HasRun bool
	// IntervalSeconds is the configured sweep interval.
	IntervalSeconds float64
	// LastRunAt is when the most recent sweep completed (zero if none).
	LastRunAt time.Time
	// SinceLastRunSeconds is the age of the last sweep (0 if none).
	SinceLastRunSeconds float64
	// Stalled is true when a sweep has run but the last one is older than twice
	// the interval — an alerting signal that the scheduler is not making progress.
	Stalled bool
	// LastHealthy is true when the most recent sweep found no breaks or errors.
	LastHealthy bool
	// ChainsTotal / ChainsOK / Failures / Errors summarize the last sweep.
	ChainsTotal int
	ChainsOK    int
	Failures    int
	Errors      int
}

func (s *Scheduler) setRunning(v bool) {
	s.mu.Lock()
	s.running = v
	s.mu.Unlock()
}

func (s *Scheduler) storeReport(r IntegrityReport) {
	s.mu.Lock()
	s.hasRun = true
	s.lastRunAt = time.Now()
	s.lastRep = r
	s.mu.Unlock()
}

// Status returns the current scheduler status. It is safe for concurrent use with
// a running scheduler and never blocks a sweep.
func (s *Scheduler) Status() SchedulerStatus {
	s.mu.Lock()
	running, hasRun, lastRunAt, rep := s.running, s.hasRun, s.lastRunAt, s.lastRep
	s.mu.Unlock()

	st := SchedulerStatus{
		Running:         running,
		HasRun:          hasRun,
		IntervalSeconds: s.cfg.Interval.Seconds(),
		LastRunAt:       lastRunAt,
		LastHealthy:     hasRun && rep.Healthy(),
		ChainsTotal:     rep.ChainsTotal,
		ChainsOK:        rep.ChainsOK,
		Failures:        len(rep.Failures),
		Errors:          len(rep.Errors),
	}
	if hasRun {
		since := time.Since(lastRunAt)
		st.SinceLastRunSeconds = since.Seconds()
		st.Stalled = since > 2*s.cfg.Interval
	}
	return st
}

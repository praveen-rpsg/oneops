package ops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// checker is a programmable invariant check.
type checker struct {
	mu       sync.Mutex
	problems []string
	err      error
	calls    int32
}

func (c *checker) check(context.Context) ([]string, error) {
	atomic.AddInt32(&c.calls, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.problems...), c.err
}

func (c *checker) set(problems []string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.problems, c.err = problems, err
}

// A sentinel has no opinion until it has actually run. Reporting "healthy"
// before the first check would let traffic through on an unverified boundary,
// which is the fail-open reading of "unknown".
func TestSentinel_UnverifiedIsNotHealthy(t *testing.T) {
	s := NewSentinel("test", (&checker{}).check, time.Second, quietLogger(), nil)
	if s.Healthy() {
		t.Error("a sentinel that has never run reports healthy — unknown must fail closed")
	}
	if s.Err() == nil {
		t.Error("Err() is nil before the first verification")
	}
}

// The whole point: a breach introduced after startup is detected.
func TestSentinel_DetectsBreachAfterStartup(t *testing.T) {
	c := &checker{}
	s := NewSentinel("test", c.check, 10*time.Millisecond, quietLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	waitFor(t, time.Second, s.Healthy, "sentinel never reported healthy on a clean check")

	// The runtime event.
	c.set([]string{"row-level security is off"}, nil)

	waitFor(t, time.Second, func() bool { return !s.Healthy() },
		"sentinel did not detect a breach introduced after it started")

	if got := s.Breach(); len(got) != 1 || got[0] != "row-level security is off" {
		t.Errorf("Breach() = %v, want the reported problem", got)
	}
	if err := s.Err(); err == nil || !strings.Contains(err.Error(), "row-level security is off") {
		t.Errorf("Err() = %v, want it to name the problem", err)
	}
}

// Repair restores service without a restart: an operator fixing the boundary is
// the intended recovery path.
func TestSentinel_RecoversAfterRepair(t *testing.T) {
	c := &checker{problems: []string{"broken"}}
	s := NewSentinel("test", c.check, 10*time.Millisecond, quietLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	waitFor(t, time.Second, func() bool { return !s.Healthy() }, "sentinel never saw the initial breach")
	c.set(nil, nil)
	waitFor(t, time.Second, s.Healthy, "sentinel did not recover after the invariant was repaired")
}

// A check that cannot run (database unreachable during a restart) is not
// evidence of a breach. Treating it as one would take every replica out of
// service on a transient blip — readiness already covers dependency loss.
func TestSentinel_CheckErrorCarriesPreviousVerdict(t *testing.T) {
	c := &checker{}
	s := NewSentinel("test", c.check, 10*time.Millisecond, quietLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	waitFor(t, time.Second, s.Healthy, "sentinel never reported healthy")

	c.set(nil, errors.New("connection refused"))
	time.Sleep(100 * time.Millisecond)

	if !s.Healthy() {
		t.Error("a failed check flipped the verdict to breached — a database blip would " +
			"take the whole fleet out of service")
	}
}

// The counterpart: a check error must not mask a breach that was already found.
func TestSentinel_CheckErrorDoesNotClearABreach(t *testing.T) {
	c := &checker{problems: []string{"broken"}}
	s := NewSentinel("test", c.check, 10*time.Millisecond, quietLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	waitFor(t, time.Second, func() bool { return !s.Healthy() }, "sentinel never saw the breach")

	c.set(nil, errors.New("connection refused"))
	time.Sleep(100 * time.Millisecond)

	if s.Healthy() {
		t.Error("an unreachable database cleared a known breach — a breach must be repaired, " +
			"not forgotten")
	}
}

// --- RunWhileHealthy ---------------------------------------------------------

type fakeGuard struct{ healthy atomic.Bool }

func (g *fakeGuard) Healthy() bool { return g.healthy.Load() }
func (g *fakeGuard) Err() error {
	if g.healthy.Load() {
		return nil
	}
	return errors.New("breached")
}

// A worker touching tenant-owned rows must stop when the boundary is breached —
// gating HTTP alone would leave the relay fanning out across a broken boundary.
func TestRunWhileHealthy_StopsTheWorkerOnBreach(t *testing.T) {
	g := &fakeGuard{}
	g.healthy.Store(true)

	var running atomic.Bool
	worker := func(ctx context.Context) error {
		running.Store(true)
		defer running.Store(false)
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = RunWhileHealthy(ctx, g, quietLogger(), worker) }()

	waitFor(t, 2*time.Second, running.Load, "worker never started while the invariant held")

	g.healthy.Store(false)
	waitFor(t, 5*time.Second, func() bool { return !running.Load() },
		"worker kept running after the invariant was breached")
}

// And it comes back when the operator repairs the boundary, without a redeploy.
func TestRunWhileHealthy_RestartsTheWorkerAfterRepair(t *testing.T) {
	g := &fakeGuard{}
	g.healthy.Store(false)

	var starts atomic.Int32
	worker := func(ctx context.Context) error {
		starts.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = RunWhileHealthy(ctx, g, quietLogger(), worker) }()

	time.Sleep(100 * time.Millisecond)
	if starts.Load() != 0 {
		t.Fatalf("worker started %d time(s) while the invariant was breached", starts.Load())
	}

	g.healthy.Store(true)
	waitFor(t, 5*time.Second, func() bool { return starts.Load() >= 1 },
		"worker did not restart after the invariant was repaired")
}

// RunWhileHealthy must return when the parent context ends, breached or not, so
// shutdown is never blocked by it.
func TestRunWhileHealthy_ReturnsOnContextCancel(t *testing.T) {
	g := &fakeGuard{} // permanently breached
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = RunWhileHealthy(ctx, g, quietLogger(), func(context.Context) error { return nil })
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunWhileHealthy did not return when its context was cancelled — shutdown would hang")
	}
}

// --- helpers -----------------------------------------------------------------

func waitFor(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

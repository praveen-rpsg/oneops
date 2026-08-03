package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/safehttp"
)

// TestScheduler_SSRFGuardRefusesLoopbackTarget is the mutation-critical proof
// for E2.2a's #1 security concern: a check target is customer-supplied, so a
// target pointed at loopback (or, identically, a private RFC1918 address or
// the 169.254.169.254 cloud metadata address — see safehttp.IsPublicIP) must
// never actually be dialed. This wires the SAME safehttp.Client the
// scheduler is wired with in production (cmd/controlplane/main.go), against
// a real local server, and proves the request never reaches it: up=0, and
// (mutation check) the test server's handler is never invoked at all — a
// regression that let the guard through would instead see the handler run
// and Up=true.
func TestScheduler_SSRFGuardRefusesLoopbackTarget(t *testing.T) {
	handlerCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// srv.URL is http://127.0.0.1:<port> — loopback, exactly the class
	// safehttp.Client refuses when allowPrivate is false (the secure default
	// this platform ships, ADR-SECURITY-001).
	guardedClient := safehttp.Client(2*time.Second, false)

	check := newHTTPCheck("asset-ssrf", srv.URL)
	store := newFakeStore(check)
	telemetry := &fakeTelemetry{}
	sched := NewScheduler(store, telemetry, guardedClient, nil, quiet(), Config{})

	sched.RunOnce(context.Background())

	if handlerCalled {
		t.Fatal("the SSRF-guarded client reached the loopback target's handler — the guard did not bite")
	}
	samples := telemetry.samplesFor("asset-ssrf")
	byMetric := map[string]domain.Sample{}
	for _, s := range samples {
		byMetric[s.Metric] = s
	}
	if s, ok := byMetric["chk_up"]; !ok || s.Value != 0 {
		t.Errorf("chk_up = %+v (present=%v), want value 0 — a loopback target must be refused, not reached", s, ok)
	}
	if _, ok := byMetric["chk_status_code"]; ok {
		t.Error("chk_status_code sample present, want absent — the request was refused before any response")
	}
	runs := store.runsFor(check.CheckID)
	if len(runs) != 1 || runs[0].Status != domain.CollectorCheckStatusDown {
		t.Fatalf("recorded runs = %+v, want exactly one with status down", runs)
	}
}

func TestScheduler_HTTPCheck_Success_WritesUpLatencyStatusSamples(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	check := newHTTPCheck("asset-1", srv.URL)
	store := newFakeStore(check)
	telemetry := &fakeTelemetry{}
	sched := NewScheduler(store, telemetry, http.DefaultClient, nil, quiet(), Config{})

	sched.RunOnce(context.Background())

	samples := telemetry.samplesFor("asset-1")
	if len(samples) != 3 {
		t.Fatalf("wrote %d samples, want 3 (up, latency_ms, status_code): %+v", len(samples), samples)
	}
	byMetric := map[string]domain.Sample{}
	for _, s := range samples {
		byMetric[s.Metric] = s
	}
	if s, ok := byMetric["chk_up"]; !ok || s.Value != 1 {
		t.Errorf("chk_up = %+v (present=%v), want value 1", s, ok)
	}
	if _, ok := byMetric["chk_latency_ms"]; !ok {
		t.Error("chk_latency_ms sample missing")
	}
	if s, ok := byMetric["chk_status_code"]; !ok || s.Value != 200 {
		t.Errorf("chk_status_code = %+v (present=%v), want value 200", s, ok)
	}

	runs := store.runsFor(check.CheckID)
	if len(runs) != 1 || runs[0].Status != domain.CollectorCheckStatusOK {
		t.Fatalf("recorded runs = %+v, want exactly one with status ok", runs)
	}
}

func TestScheduler_HTTPCheck_Down_RecordsUpZeroNoStatusCode(t *testing.T) {
	// Nothing is listening at this address: the request fails at the
	// transport level, exactly a "failed/timed-out check" — not an error
	// that drops the sample.
	check := newHTTPCheck("asset-2", "http://127.0.0.1:1")
	store := newFakeStore(check)
	telemetry := &fakeTelemetry{}
	sched := NewScheduler(store, telemetry, http.DefaultClient, nil, quiet(), Config{CheckTimeout: 2 * time.Second})

	sched.RunOnce(context.Background())

	samples := telemetry.samplesFor("asset-2")
	byMetric := map[string]domain.Sample{}
	for _, s := range samples {
		byMetric[s.Metric] = s
	}
	if s, ok := byMetric["chk_up"]; !ok || s.Value != 0 {
		t.Errorf("chk_up = %+v (present=%v), want value 0", s, ok)
	}
	if _, ok := byMetric["chk_latency_ms"]; !ok {
		t.Error("chk_latency_ms sample missing — a failed check must still record how long it took to fail")
	}
	if _, ok := byMetric["chk_status_code"]; ok {
		t.Error("chk_status_code sample present, want absent — no response was ever received")
	}

	runs := store.runsFor(check.CheckID)
	if len(runs) != 1 || runs[0].Status != domain.CollectorCheckStatusDown {
		t.Fatalf("recorded runs = %+v, want exactly one with status down", runs)
	}
}

func TestScheduler_HangingTarget_TimesOutAndDoesNotStallTheScheduler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	check := newHTTPCheck("asset-hang", srv.URL)
	store := newFakeStore(check)
	telemetry := &fakeTelemetry{}
	sched := NewScheduler(store, telemetry, http.DefaultClient, nil, quiet(), Config{CheckTimeout: 50 * time.Millisecond})

	start := time.Now()
	sched.RunOnce(context.Background())
	elapsed := time.Since(start)

	if elapsed > 300*time.Millisecond {
		t.Fatalf("RunOnce took %v against a target hanging for 2s with a 50ms check timeout — a hanging target stalled the scheduler", elapsed)
	}
	runs := store.runsFor(check.CheckID)
	if len(runs) != 1 || runs[0].Status != domain.CollectorCheckStatusDown {
		t.Fatalf("recorded runs = %+v, want exactly one with status down", runs)
	}
}

func TestScheduler_UnsupportedType_RecordsRunWritesNoSamples(t *testing.T) {
	check, err := domain.NewCollectorCheck("t-test", "asset-3", "snmp", "10.0.0.1:161/public", 30, "chk")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	store := newFakeStore(check)
	telemetry := &fakeTelemetry{}
	sched := NewScheduler(store, telemetry, http.DefaultClient, nil, quiet(), Config{})

	sched.RunOnce(context.Background())

	if samples := telemetry.samplesFor("asset-3"); len(samples) != 0 {
		t.Errorf("wrote %d samples for an unsupported check type, want 0: %+v", len(samples), samples)
	}
	runs := store.runsFor(check.CheckID)
	if len(runs) != 1 || runs[0].Status != domain.CollectorCheckStatusUnsupported {
		t.Fatalf("recorded runs = %+v, want exactly one with status unsupported", runs)
	}
}

// TestScheduler_BoundedConcurrency proves a pass with more due checks than
// Concurrency never runs more than Concurrency of them at once — the "bounded
// concurrency" requirement — by having every check's target block until
// released and counting the observed peak of simultaneously in-flight
// requests.
func TestScheduler_BoundedConcurrency(t *testing.T) {
	const (
		numChecks   = 6
		concurrency = 2
	)
	var (
		inFlight int32
		peak     int32
	)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var checks []*domain.CollectorCheck
	for i := 0; i < numChecks; i++ {
		checks = append(checks, newHTTPCheck("asset-conc", srv.URL))
	}
	store := newFakeStore(checks...)
	telemetry := &fakeTelemetry{}
	sched := NewScheduler(store, telemetry, http.DefaultClient, nil, quiet(), Config{Concurrency: concurrency, CheckTimeout: 5 * time.Second})

	done := make(chan struct{})
	go func() {
		sched.RunOnce(context.Background())
		close(done)
	}()

	// Give the pass time to saturate its concurrency cap, then release every
	// blocked request at once.
	time.Sleep(200 * time.Millisecond)
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not complete after release")
	}

	if got := atomic.LoadInt32(&peak); got > int32(concurrency) {
		t.Errorf("peak concurrent in-flight requests = %d, want <= %d", got, concurrency)
	}
}

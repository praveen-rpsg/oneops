//go:build loadtest

// Command loadtest is a repeatable load-generation harness for the /v1 API
// (T2-F). It mints its own bearer token, seeds a pool of artifacts, then drives
// a weighted read/write mix against a running control-plane instance and
// prints throughput, latency percentiles, and a status-code breakdown.
//
// It is deliberately excluded from the normal build (go build/test ./...) by
// this file's build tag: this is an operational measurement tool, not
// application code, and internal/arch/wiring_test.go's
// TestOperationalBinariesAreRegistered requires every cmd/ entry to be a
// registered privileged binary obtaining ownership through the shared
// security framework — a load generator does neither, so it lives outside
// cmd/ under a build tag instead of trying to satisfy a guard built for a
// different kind of program.
//
// Run via `make loadtest` or directly:
//
//	go run -tags loadtest ./loadtest -base-url http://localhost:8080
//
// See docs/observability/PERFORMANCE-BASELINE.md for methodology and results.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	cfg := parseFlags()

	token, err := mintToken(cfg.jwtIssuer, cfg.jwtAudience, cfg.jwtSecret, cfg.jwtRole)
	if err != nil {
		log.Fatalf("mint token: %v", err)
	}

	client := &http.Client{
		Timeout: cfg.requestTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.workers * 2,
			MaxIdleConnsPerHost: cfg.workers * 2,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	c := &apiClient{http: client, baseURL: cfg.baseURL, token: token}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("seeding %d artifacts against %s ...\n", cfg.seed, cfg.baseURL)
	pool, err := seedArtifacts(ctx, c, cfg.seed)
	if err != nil {
		log.Fatalf("seed: %v", err)
	}
	fmt.Printf("seeded %d artifacts\n", len(pool.ids))

	mix := defaultMix()

	var (
		results   []result
		resultsMu sync.Mutex
	)
	var completed int64

	runFor := cfg.duration
	targetReqs := cfg.requests
	deadline := time.Now().Add(runFor)

	fmt.Printf("running: workers=%d duration=%s requests=%d mix=%s\n",
		cfg.workers, runFor, targetReqs, mix.describe())

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < cfg.workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := newWorkerRand(workerID)
			local := make([]result, 0, 256)
			flush := func() {
				if len(local) == 0 {
					return
				}
				resultsMu.Lock()
				results = append(results, local...)
				resultsMu.Unlock()
				local = local[:0]
			}
			for {
				if targetReqs > 0 {
					if atomic.AddInt64(&completed, 1) > int64(targetReqs) {
						flush()
						return
					}
				} else if time.Now().After(deadline) {
					flush()
					return
				}
				select {
				case <-ctx.Done():
					flush()
					return
				default:
				}

				ep := mix.pick(rng)
				reqStart := time.Now()
				status, err := ep.do(ctx, c, pool, rng)
				lat := time.Since(reqStart)
				local = append(local, result{endpoint: ep.name, status: status, latency: lat, err: err})

				if len(local) >= 256 {
					flush()
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	report(results, elapsed)
}

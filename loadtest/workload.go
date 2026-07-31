//go:build loadtest

package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"time"
)

// endpoint is one member of the request mix. weight is relative, not a
// percentage — mix.pick normalises against the sum of all weights.
type endpoint struct {
	name   string
	weight int
	do     func(ctx context.Context, c *apiClient, pool *seedPool, rng *rand.Rand) (int, error)
}

// mix is a weighted set of endpoints with a cumulative-weight index for O(log
// n) selection.
type mix struct {
	endpoints []endpoint
	cum       []int
	total     int
}

func newMix(eps []endpoint) mix {
	m := mix{endpoints: eps, cum: make([]int, len(eps))}
	sum := 0
	for i, e := range eps {
		sum += e.weight
		m.cum[i] = sum
	}
	m.total = sum
	return m
}

func (m mix) pick(rng *rand.Rand) endpoint {
	r := rng.IntN(m.total)
	for i, c := range m.cum {
		if r < c {
			return m.endpoints[i]
		}
	}
	return m.endpoints[len(m.endpoints)-1]
}

func (m mix) describe() string {
	s := ""
	for _, e := range m.endpoints {
		pct := float64(e.weight) / float64(m.total) * 100
		s += fmt.Sprintf("%s=%.0f%% ", e.name, pct)
	}
	return s
}

// writeCounter guarantees a unique (artifact, version) pair per write across
// all workers for the life of the process — Create enforces uniqueness on
// that pair, and a collision would surface as a 409 that measures the
// harness's naming scheme rather than the server.
var writeCounter int64

// defaultMix is the representative ops read/write ratio the story calls for:
// ~90% reads spread across the read-heavy list/get/governance/dependency
// routes, ~10% writes. Endpoints are the ones actually routed in
// internal/httpapi/server.go — verified against it, not guessed.
func defaultMix() mix {
	runStamp := time.Now().UnixNano()
	return newMix([]endpoint{
		{
			name: "GET /v1/artifacts", weight: 35,
			do: func(ctx context.Context, c *apiClient, _ *seedPool, _ *rand.Rand) (int, error) {
				return c.do(ctx, "GET", "/v1/artifacts?limit=50", nil)
			},
		},
		{
			name: "GET /v1/artifacts/{id}", weight: 25,
			do: func(ctx context.Context, c *apiClient, pool *seedPool, rng *rand.Rand) (int, error) {
				id := pool.randomID(rng.Uint64())
				return c.do(ctx, "GET", "/v1/artifacts/"+id, nil)
			},
		},
		{
			name: "GET /v1/governance/{id}", weight: 15,
			do: func(ctx context.Context, c *apiClient, pool *seedPool, rng *rand.Rand) (int, error) {
				id := pool.randomID(rng.Uint64())
				return c.do(ctx, "GET", "/v1/governance/"+id, nil)
			},
		},
		{
			name: "GET /v1/configurations/{cfgId}/dependencies", weight: 15,
			do: func(ctx context.Context, c *apiClient, pool *seedPool, rng *rand.Rand) (int, error) {
				id := pool.randomID(rng.Uint64())
				return c.do(ctx, "GET", "/v1/configurations/"+id+"/dependencies", nil)
			},
		},
		{
			name: "POST /v1/artifacts", weight: 10,
			do: func(ctx context.Context, c *apiClient, _ *seedPool, _ *rand.Rand) (int, error) {
				n := atomic.AddInt64(&writeCounter, 1)
				name := fmt.Sprintf("loadtest-write-%d-%d", runStamp, n)
				body := createArtifactBody{
					Artifact: name, Version: "1.0.0",
					Role: "governance", Lifecycle: "draft", RetentionClass: "working_material",
				}
				return c.do(ctx, "POST", "/v1/artifacts", body)
			},
		},
	})
}

// newWorkerRand gives each worker its own PCG source seeded from its id and
// the current time, so goroutines never contend on a shared generator (the
// default top-level math/rand/v2 functions do, via an internal lock).
func newWorkerRand(workerID int) *rand.Rand {
	seed := uint64(time.Now().UnixNano()) ^ (uint64(workerID) << 32) //nolint:gosec // load-generation mix selection, not a security context
	return rand.New(rand.NewPCG(seed, uint64(workerID)+1))
}

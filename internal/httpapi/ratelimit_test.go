package httpapi

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// The bucket must allow exactly burst requests immediately, then refuse until
// tokens refill — the whole point of a token bucket over a fixed window.
func TestRateLimiter_AllowsBurstThenBlocksThenRecovers(t *testing.T) {
	rl := newRateLimiter(1 /* rps */, 2 /* burst */, time.Minute)
	now := time.Now()

	for i := 0; i < 2; i++ {
		ok, _ := rl.allow("tenant-a", now)
		if !ok {
			t.Fatalf("request %d within burst was refused", i)
		}
	}

	ok, retryAfter := rl.allow("tenant-a", now)
	if ok {
		t.Fatal("request beyond burst was allowed")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want > 0 so the caller can back off", retryAfter)
	}

	// After the bucket has refilled by at least one token (1 rps => 1s), a
	// further request must succeed.
	later := now.Add(1100 * time.Millisecond)
	if ok, _ := rl.allow("tenant-a", later); !ok {
		t.Error("request after refill window was still refused")
	}
}

// A rejected request must not consume bucket capacity, or a burst of
// rejections would slow down the tenant's real recovery once it stops
// hammering the endpoint.
func TestRateLimiter_RejectionDoesNotConsumeCapacity(t *testing.T) {
	rl := newRateLimiter(1, 1, time.Minute)
	now := time.Now()

	if ok, _ := rl.allow("tenant-a", now); !ok {
		t.Fatal("first request was refused")
	}
	// Several rejected attempts at the same instant.
	for i := 0; i < 5; i++ {
		if ok, _ := rl.allow("tenant-a", now); ok {
			t.Fatalf("rejected-window request %d was unexpectedly allowed", i)
		}
	}
	// One second later, exactly one token should be available — not starved by
	// the rejected attempts above.
	later := now.Add(1100 * time.Millisecond)
	if ok, _ := rl.allow("tenant-a", later); !ok {
		t.Error("refill was starved by earlier rejections")
	}
	if ok, _ := rl.allow("tenant-a", later); ok {
		t.Error("a second token appeared out of nowhere")
	}
}

// Tenants are isolated: exhausting one tenant's bucket must not affect
// another's.
func TestRateLimiter_TenantsAreIsolated(t *testing.T) {
	rl := newRateLimiter(1, 1, time.Minute)
	now := time.Now()

	if ok, _ := rl.allow("tenant-a", now); !ok {
		t.Fatal("tenant-a's first request was refused")
	}
	if ok, _ := rl.allow("tenant-a", now); ok {
		t.Fatal("tenant-a exceeded its burst but was allowed")
	}
	if ok, _ := rl.allow("tenant-b", now); !ok {
		t.Error("tenant-b was throttled by tenant-a's exhausted bucket")
	}
}

// Idle tenants must be evicted, or the map grows without bound as new tenants
// appear over the platform's lifetime.
func TestRateLimiter_EvictsIdleTenants(t *testing.T) {
	ttl := time.Minute
	rl := newRateLimiter(1, 1, ttl)
	now := time.Now()

	for i := 0; i < 50; i++ {
		rl.allow(fmt.Sprintf("tenant-%d", i), now)
	}
	rl.mu.Lock()
	before := len(rl.buckets)
	rl.mu.Unlock()
	if before != 50 {
		t.Fatalf("buckets = %d, want 50", before)
	}

	// Advance well past the ttl and touch one different tenant — the sweep
	// runs on that call and must clear every idle bucket.
	later := now.Add(ttl + time.Second)
	rl.allow("tenant-fresh", later)

	rl.mu.Lock()
	after := len(rl.buckets)
	_, freshSurvived := rl.buckets["tenant-fresh"]
	rl.mu.Unlock()
	if after != 1 || !freshSurvived {
		t.Errorf("buckets after sweep = %d (fresh survived=%v), want 1 (tenant-fresh only)",
			after, freshSurvived)
	}
}

// The bucket map is shared across every request goroutine; concurrent access
// must be race-free (go test -race) and must not lose or corrupt entries.
func TestRateLimiter_ConcurrentAccessIsRaceFree(t *testing.T) {
	rl := newRateLimiter(1000, 1000, time.Minute)
	now := time.Now()

	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			tenant := fmt.Sprintf("tenant-%d", g%5)
			for i := 0; i < 200; i++ {
				rl.allow(tenant, time.Now())
			}
			_ = now
		}(g)
	}
	wg.Wait()

	rl.mu.Lock()
	n := len(rl.buckets)
	rl.mu.Unlock()
	if n != 5 {
		t.Errorf("buckets = %d, want 5 (one per distinct tenant)", n)
	}
}

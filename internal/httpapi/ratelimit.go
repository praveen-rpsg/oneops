package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/rpsg/oneops/internal/domain"
)

// rateLimiterIdleTTL bounds how long an idle tenant's bucket is kept. Without
// this, every distinct tenant that has ever made one request would occupy
// memory forever — the map is sized by tenants active within this window, not
// by every tenant that has ever existed (ADR-SECURITY-004).
const rateLimiterIdleTTL = 10 * time.Minute

// tenantBucket is one tenant's token bucket plus the last time it was touched,
// so sweep can tell an idle bucket from an active one.
type tenantBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// rateLimiter is an in-memory, per-instance, per-tenant token bucket
// (ADR-SECURITY-004). It deliberately holds no shared state across replicas:
// with N running instances the effective global limit for a tenant is
// approximately N times rps/burst, a known and accepted trade-off documented
// in the ADR.
//
// The map is guarded by a single mutex. Lookups and inserts are O(1); the
// occasional eviction sweep is O(tenants) but runs at most once per ttl, so
// the amortized per-request cost stays O(1).
type rateLimiter struct {
	mu    sync.Mutex
	rps   rate.Limit
	burst int
	ttl   time.Duration

	buckets   map[string]*tenantBucket
	lastSweep time.Time
}

// newRateLimiter builds a limiter that grants rps tokens per second, up to
// burst, per tenant. Buckets idle longer than ttl are evicted.
func newRateLimiter(rps float64, burst int, ttl time.Duration) *rateLimiter {
	return &rateLimiter{
		rps:     rate.Limit(rps),
		burst:   burst,
		ttl:     ttl,
		buckets: make(map[string]*tenantBucket),
	}
}

// allow reports whether tenantID may proceed at time now, and — when it may
// not — how long the caller should wait before retrying. now is threaded
// through explicitly (rather than read from time.Now() here) so tests can
// exercise refill and eviction without sleeping.
func (rl *rateLimiter) allow(tenantID string, now time.Time) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.evictLocked(now)

	b, ok := rl.buckets[tenantID]
	if !ok {
		b = &tenantBucket{limiter: rate.NewLimiter(rl.rps, rl.burst)}
		rl.buckets[tenantID] = b
	}
	b.lastSeen = now

	res := b.limiter.ReserveN(now, 1)
	if !res.OK() {
		// Only possible if burst is misconfigured to 0; fail closed rather than
		// let a single misconfiguration serve unlimited traffic.
		return false, time.Second
	}
	if delay := res.DelayFrom(now); delay > 0 {
		// Give the token back: a rejected request must not also consume bucket
		// capacity, or a burst of rejections would slow the tenant's genuine
		// recovery once it backs off.
		res.CancelAt(now)
		return false, delay
	}
	return true, 0
}

// evictLocked removes buckets idle longer than ttl. Called with mu held. It
// runs at most once per ttl so it does not turn every request into an
// O(tenants) scan.
func (rl *rateLimiter) evictLocked(now time.Time) {
	if rl.ttl <= 0 || now.Sub(rl.lastSweep) < rl.ttl {
		return
	}
	rl.lastSweep = now
	for id, b := range rl.buckets {
		if now.Sub(b.lastSeen) > rl.ttl {
			delete(rl.buckets, id)
		}
	}
}

// rateLimit enforces the per-tenant token bucket on the /v1 surface. It runs
// after s.authenticate so domain.TenantIDFrom always resolves to a real
// (or the system) tenant — never an empty key that would let every
// unauthenticated caller share one bucket.
//
// A nil limiter (ONEOPS_RATE_LIMIT_ENABLED=false) is a deliberate pass-through
// for a clean rollback, not an error.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		tenantID := domain.TenantIDFrom(r.Context())
		ok, retryAfter := s.limiter.allow(tenantID, time.Now())
		if !ok {
			if s.metrics != nil {
				s.metrics.IncRateLimited()
			}
			seconds := int(math.Ceil(retryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeProblem(w, r, http.StatusTooManyRequests, "too many requests",
				"rate limit exceeded for this tenant")
			return
		}
		next.ServeHTTP(w, r)
	})
}

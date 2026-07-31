package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

func rateLimitTestServer(rps float64, burst int) *Server {
	return &Server{
		log:     quietLog(),
		metrics: observability.NewMetrics(),
		limiter: newRateLimiter(rps, burst, rateLimiterIdleTTL),
	}
}

func requestFor(tenantID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	ctx := domain.WithTenant(r.Context(), &domain.Tenant{
		TenantID: tenantID, Slug: tenantID, Name: tenantID, Status: domain.TenantActive,
	})
	return r.WithContext(ctx)
}

// A tenant that exceeds its bucket gets 429 with Retry-After and the same
// RFC 7807 problem body every other error path uses, while a different tenant
// on the same instance is completely unaffected (ADR-SECURITY-004).
func TestRateLimit_TenantOverLimitGets429_OtherTenantUnaffected(t *testing.T) {
	s := rateLimitTestServer(1, 1)

	var reached int
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	})
	h := s.rateLimit(next)

	// tenant-a: first request consumes the single burst token.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, requestFor("tenant-a"))
	if rec1.Code != http.StatusOK {
		t.Fatalf("tenant-a first request: status = %d, want 200", rec1.Code)
	}

	// tenant-a: second request, same instant, must be throttled.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, requestFor("tenant-a"))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("tenant-a second request: status = %d, want 429", rec2.Code)
	}
	if got := rec2.Header().Get("Retry-After"); got == "" {
		t.Error("429 response has no Retry-After header; client cannot back off")
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var problem Problem
	if err := json.Unmarshal(rec2.Body.Bytes(), &problem); err != nil {
		t.Fatalf("429 body did not decode as a Problem: %v", err)
	}
	if problem.Status != http.StatusTooManyRequests {
		t.Errorf("problem.Status = %d, want 429", problem.Status)
	}
	if problem.Detail == "" {
		t.Error("problem.Detail is empty")
	}

	// tenant-b: an independent bucket, unaffected by tenant-a's exhaustion.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, requestFor("tenant-b"))
	if rec3.Code != http.StatusOK {
		t.Fatalf("tenant-b request: status = %d, want 200 (isolation broken)", rec3.Code)
	}

	if reached != 2 {
		t.Errorf("handler reached %d times, want 2 (tenant-a's first + tenant-b's)", reached)
	}
}

// ONEOPS_RATE_LIMIT_ENABLED=false must fully disable enforcement: a nil
// limiter is a pass-through, not a smaller limit.
func TestRateLimit_DisabledIsPassThrough(t *testing.T) {
	s := &Server{log: quietLog(), metrics: observability.NewMetrics()}

	var reached int
	h := s.rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 100; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, requestFor("tenant-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 with rate limiting disabled", i, rec.Code)
		}
	}
	if reached != 100 {
		t.Errorf("handler reached %d times, want 100", reached)
	}
}

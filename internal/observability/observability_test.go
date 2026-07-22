package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestSetupTracing(t *testing.T) {
	ctx := context.Background()
	shNoop, err := SetupTracing(ctx, "svc", "")
	if err != nil {
		t.Fatalf("noop tracing: %v", err)
	}
	if err := shNoop(ctx); err != nil {
		t.Errorf("noop shutdown: %v", err)
	}

	sh, err := SetupTracing(ctx, "svc", "localhost:4317")
	if err != nil {
		t.Fatalf("tracing with endpoint: %v", err)
	}
	_ = sh(ctx)
}

func TestMetricsMiddlewareAndHandler(t *testing.T) {
	m := NewMetrics()
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "http_requests_total") {
		t.Errorf("metrics output missing counter:\n%s", rec.Body.String())
	}
}

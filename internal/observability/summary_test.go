package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestSummary_DerivesFromExistingRegistry(t *testing.T) {
	m := NewMetrics()

	// Drive one request through the middleware (in a chi router so the route
	// pattern resolves) so http_requests_total increments.
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	sum, err := m.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	v, ok := sum["http_requests_total"]
	if !ok || v < 1 {
		t.Fatalf("http_requests_total = %v (present=%v), want >= 1", v, ok)
	}
	// Only whitelisted names appear.
	for name := range sum {
		if !summaryMetrics[name] {
			t.Errorf("unexpected metric in summary: %q", name)
		}
	}
}

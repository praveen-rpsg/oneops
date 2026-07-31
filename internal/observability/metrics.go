// Package observability wires Prometheus metrics and OpenTelemetry tracing.
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns the Prometheus registry and HTTP instrumentation.
type Metrics struct {
	reg         *prometheus.Registry
	reqTotal    *prometheus.CounterVec
	reqDur      *prometheus.HistogramVec
	inflight    prometheus.Gauge
	rateLimited prometheus.Counter
}

// NewMetrics builds and registers the metric collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())

	m := &Metrics{
		reg: reg,
		reqTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests.",
		}, []string{"method", "route", "status"}),
		reqDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "In-flight HTTP requests.",
		}),
		// No tenant label: cardinality is bounded by tenant count, which is
		// unbounded over the platform's lifetime (ADR-SECURITY-004).
		rateLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_rate_limited_total",
			Help: "Total requests rejected by the per-tenant rate limiter.",
		}),
	}
	reg.MustRegister(m.reqTotal, m.reqDur, m.inflight, m.rateLimited)
	return m
}

// IncRateLimited records a request rejected by the per-tenant rate limiter.
func (m *Metrics) IncRateLimited() { m.rateLimited.Inc() }

// Handler serves the Prometheus exposition endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Registry returns the shared Prometheus registry so operational subsystems
// (e.g. the audit-integrity scheduler) can register their own collectors into
// the single exposition served by Handler.
func (m *Metrics) Registry() prometheus.Registerer { return m.reg }

// Middleware records request counts and latencies keyed by chi route pattern.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inflight.Inc()
		defer m.inflight.Dec()

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		m.reqTotal.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		m.reqDur.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

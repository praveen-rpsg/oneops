package events

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ConsumeMetrics receives replay/recovery observability signals.
type ConsumeMetrics interface {
	IncReplayJob()
	AddReplayEvents(n int)
	ObserveReplayDuration(d time.Duration)
	SetDeadLetters(n int)
}

// NopConsumeMetrics discards all consumption signals.
type NopConsumeMetrics struct{}

// IncReplayJob implements ConsumeMetrics.
func (NopConsumeMetrics) IncReplayJob() {}

// AddReplayEvents implements ConsumeMetrics.
func (NopConsumeMetrics) AddReplayEvents(int) {}

// ObserveReplayDuration implements ConsumeMetrics.
func (NopConsumeMetrics) ObserveReplayDuration(time.Duration) {}

// SetDeadLetters implements ConsumeMetrics.
func (NopConsumeMetrics) SetDeadLetters(int) {}

// PromConsumeMetrics is the Prometheus ConsumeMetrics. It adds only the four new
// consumption collectors to the shared registry (no duplication of PRS-018).
type PromConsumeMetrics struct {
	jobs        prometheus.Counter
	events      prometheus.Counter
	duration    prometheus.Histogram
	deadLetters prometheus.Gauge
}

var _ ConsumeMetrics = (*PromConsumeMetrics)(nil)

// NewPromConsumeMetrics builds and registers the consumption collectors.
func NewPromConsumeMetrics(reg prometheus.Registerer) *PromConsumeMetrics {
	m := &PromConsumeMetrics{
		jobs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_webhook_replay_jobs_total", Help: "Total replay jobs executed.",
		}),
		events: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_webhook_replay_events_total", Help: "Total events re-enqueued by replay.",
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "oneops_webhook_replay_duration_seconds", Help: "Replay job execution duration.",
			Buckets: prometheus.DefBuckets,
		}),
		deadLetters: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_webhook_deadletters", Help: "Current dead-letter deliveries.",
		}),
	}
	reg.MustRegister(m.jobs, m.events, m.duration, m.deadLetters)
	return m
}

// IncReplayJob implements ConsumeMetrics.
func (m *PromConsumeMetrics) IncReplayJob() { m.jobs.Inc() }

// AddReplayEvents implements ConsumeMetrics.
func (m *PromConsumeMetrics) AddReplayEvents(n int) { m.events.Add(float64(n)) }

// ObserveReplayDuration implements ConsumeMetrics.
func (m *PromConsumeMetrics) ObserveReplayDuration(d time.Duration) { m.duration.Observe(d.Seconds()) }

// SetDeadLetters implements ConsumeMetrics.
func (m *PromConsumeMetrics) SetDeadLetters(n int) { m.deadLetters.Set(float64(n)) }

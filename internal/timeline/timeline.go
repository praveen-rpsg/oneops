// Package timeline is a READ-ONLY operational read model. It reconstructs the
// chronological lifecycle of a governance operation — governance commit, audit
// append, webhook deliveries, replay activity, and policy executions — by
// composing EXISTING persisted data. It introduces no new execution state,
// participates in no execution, and modifies no runtime subsystem: every method
// is a projection over rows the other subsystems already wrote.
package timeline

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// Row types — the neutral records the Source reads from existing tables. They
// carry no behavior; the Service projects them into timeline Entries.

// AuditRow is one committed audit event (governance+audit atomic commit).
type AuditRow struct {
	ChainID     string
	Seq         int64
	EventID     string
	OperationID string
	Operation   string
	Actor       string
	OccurredAt  time.Time
}

// DeliveryRow is one webhook delivery record.
type DeliveryRow struct {
	ID          string
	WebhookID   string
	EventID     string
	Status      string
	StatusCode  int
	LastAttempt time.Time
	CreatedAt   time.Time
}

// ReplayRow is one replay job record.
type ReplayRow struct {
	ID             string
	WebhookID      string
	Status         string
	EventsReplayed int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PolicyRow is one policy execution record.
type PolicyRow struct {
	ID         string
	PolicyID   string
	EventID    string
	Status     string
	RetryCount int
	StartedAt  time.Time
	EndedAt    time.Time
	CreatedAt  time.Time
}

// Source is the read-only data access the timeline composes. It is satisfied by
// *postgres.TimelineStore, which runs SELECT-only queries over the existing
// audit_event, webhook_delivery, webhook_replay_job, and policy_execution tables.
type Source interface {
	AuditByEventID(ctx context.Context, eventID string) ([]AuditRow, error)
	AuditByChain(ctx context.Context, chainID string, limit int) ([]AuditRow, error)
	DeliveriesByEventIDs(ctx context.Context, eventIDs []string) ([]DeliveryRow, error)
	PolicyExecutionsByEventIDs(ctx context.Context, eventIDs []string) ([]PolicyRow, error)
	ReplayJob(ctx context.Context, jobID string) (ReplayRow, bool, error)
	PolicyExecution(ctx context.Context, execID string) (PolicyRow, bool, error)
}

// Components of the timeline (used for filtering and deterministic tie-breaks).
const (
	CompGovernance = "governance"
	CompAudit      = "audit"
	CompWebhook    = "webhook"
	CompReplay     = "replay"
	CompPolicy     = "policy"
)

var componentOrder = map[string]int{
	CompGovernance: 0, CompAudit: 1, CompWebhook: 2, CompReplay: 3, CompPolicy: 4,
}

// Entry is one point on the execution timeline.
type Entry struct {
	Timestamp   time.Time         `json:"timestamp"`
	Component   string            `json:"component"`
	Action      string            `json:"action"`
	Status      string            `json:"status"`
	DurationMS  int64             `json:"duration_ms"`
	Correlation map[string]string `json:"correlation"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Filter narrows and paginates a timeline.
type Filter struct {
	From      time.Time
	To        time.Time
	Component string
	Status    string
	Offset    int
	Limit     int
}

func (f Filter) limit() int {
	if f.Limit <= 0 {
		return 100
	}
	return f.Limit
}

// Page is a filtered, ordered, paginated timeline.
type Page struct {
	Entries    []Entry `json:"entries"`
	NextOffset string  `json:"next_offset,omitempty"`
}

// Metrics receives timeline query observability signals.
type Metrics interface {
	IncQuery()
	ObserveDuration(d time.Duration)
}

// NopMetrics discards signals.
type NopMetrics struct{}

// IncQuery implements Metrics.
func (NopMetrics) IncQuery() {}

// ObserveDuration implements Metrics.
func (NopMetrics) ObserveDuration(time.Duration) {}

// Service builds timelines from the Source. It is read-only and stateless.
type Service struct {
	src     Source
	metrics Metrics
	now     func() time.Time
}

// NewService builds a Service.
func NewService(src Source, metrics Metrics) *Service {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	return &Service{src: src, metrics: metrics, now: func() time.Time { return time.Now().UTC() }}
}

// ByEvent composes the timeline correlated by a single audit EventID.
func (s *Service) ByEvent(ctx context.Context, eventID string, f Filter) (Page, error) {
	return s.query(f, func() ([]Entry, error) {
		audits, err := s.src.AuditByEventID(ctx, eventID)
		if err != nil {
			return nil, err
		}
		return s.composeForEvents(ctx, audits, []string{eventID})
	})
}

// ByGovernance composes the timeline for a configuration object (governance id =
// its chain), across every committed operation on it.
func (s *Service) ByGovernance(ctx context.Context, cfgID string, f Filter) (Page, error) {
	return s.query(f, func() ([]Entry, error) {
		audits, err := s.src.AuditByChain(ctx, cfgID, 1000)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(audits))
		for _, a := range audits {
			ids = append(ids, a.EventID)
		}
		return s.composeForEvents(ctx, audits, ids)
	})
}

// ByReplay composes the timeline for a replay job.
func (s *Service) ByReplay(ctx context.Context, jobID string, f Filter) (Page, error) {
	return s.query(f, func() ([]Entry, error) {
		job, ok, err := s.src.ReplayJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNotFound
		}
		return replayEntries(job), nil
	})
}

// ByPolicyExecution composes the timeline for one policy execution, including the
// committed event it reacted to.
func (s *Service) ByPolicyExecution(ctx context.Context, execID string, f Filter) (Page, error) {
	return s.query(f, func() ([]Entry, error) {
		pe, ok, err := s.src.PolicyExecution(ctx, execID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNotFound
		}
		var entries []Entry
		if pe.EventID != "" {
			audits, err := s.src.AuditByEventID(ctx, pe.EventID)
			if err != nil {
				return nil, err
			}
			for _, a := range audits {
				entries = append(entries, auditEntries(a)...)
			}
		}
		entries = append(entries, policyEntry(pe))
		return entries, nil
	})
}

// composeForEvents projects audit rows plus their correlated deliveries and
// policy executions into a single entry set.
func (s *Service) composeForEvents(ctx context.Context, audits []AuditRow, eventIDs []string) ([]Entry, error) {
	var entries []Entry
	for _, a := range audits {
		entries = append(entries, auditEntries(a)...)
	}
	dels, err := s.src.DeliveriesByEventIDs(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	for _, d := range dels {
		entries = append(entries, deliveryEntry(d))
	}
	pes, err := s.src.PolicyExecutionsByEventIDs(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	for _, pe := range pes {
		entries = append(entries, policyEntry(pe))
	}
	return entries, nil
}

// query runs a gather func, records metrics, then orders/filters/paginates.
func (s *Service) query(f Filter, gather func() ([]Entry, error)) (Page, error) {
	s.metrics.IncQuery()
	start := s.now()
	defer func() { s.metrics.ObserveDuration(s.now().Sub(start)) }()

	entries, err := gather()
	if err != nil {
		return Page{}, err
	}
	entries = filter(entries, f)
	sortEntries(entries)
	return paginate(entries, f), nil
}

func filter(in []Entry, f Filter) []Entry {
	out := in[:0:0]
	for _, e := range in {
		if !f.From.IsZero() && e.Timestamp.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && e.Timestamp.After(f.To) {
			continue
		}
		if f.Component != "" && e.Component != f.Component {
			continue
		}
		if f.Status != "" && e.Status != f.Status {
			continue
		}
		out = append(out, e)
	}
	return out
}

// sortEntries orders deterministically: timestamp, then component order, then a
// stable correlation key, so equal timestamps never reorder between calls.
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if !es[i].Timestamp.Equal(es[j].Timestamp) {
			return es[i].Timestamp.Before(es[j].Timestamp)
		}
		if componentOrder[es[i].Component] != componentOrder[es[j].Component] {
			return componentOrder[es[i].Component] < componentOrder[es[j].Component]
		}
		return corrKey(es[i]) < corrKey(es[j])
	})
}

func corrKey(e Entry) string {
	return e.Action + "|" + e.Correlation["event_id"] + "|" + e.Correlation["delivery_id"] +
		"|" + e.Correlation["policy_execution_id"] + "|" + e.Correlation["replay_job_id"]
}

func paginate(es []Entry, f Filter) Page {
	off := f.Offset
	if off < 0 {
		off = 0
	}
	if off >= len(es) {
		return Page{Entries: []Entry{}}
	}
	end := off + f.limit()
	if end >= len(es) {
		return Page{Entries: es[off:]}
	}
	return Page{Entries: es[off:end], NextOffset: strconv.Itoa(end)}
}

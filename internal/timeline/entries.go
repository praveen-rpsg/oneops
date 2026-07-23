package timeline

import (
	"errors"
	"strconv"
	"time"
)

// ErrNotFound is returned when a correlation root (replay job, policy execution)
// does not exist.
var ErrNotFound = errors.New("timeline: not found")

// auditEntries projects one committed audit row into a governance-commit entry
// and an audit-append entry (the two are atomic per ADR-AUDIT-005).
func auditEntries(a AuditRow) []Entry {
	corr := map[string]string{
		"event_id": a.EventID, "governance_id": a.ChainID, "operation_id": a.OperationID, "cfg_id": a.ChainID,
	}
	meta := map[string]string{"operation": a.Operation, "actor": a.Actor, "seq": strconv.FormatInt(a.Seq, 10)}
	cp := func(m map[string]string) map[string]string {
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return []Entry{
		{Timestamp: a.OccurredAt, Component: CompGovernance, Action: "operation_committed", Status: "committed", Correlation: cp(corr), Metadata: cp(meta)},
		{Timestamp: a.OccurredAt, Component: CompAudit, Action: "event_appended", Status: "sealed", Correlation: cp(corr), Metadata: cp(meta)},
	}
}

func deliveryEntry(d DeliveryRow) Entry {
	ts := d.LastAttempt
	if ts.IsZero() {
		ts = d.CreatedAt
	}
	meta := map[string]string{}
	if d.StatusCode != 0 {
		meta["status_code"] = strconv.Itoa(d.StatusCode)
	}
	return Entry{
		Timestamp: ts, Component: CompWebhook, Action: "delivery_" + d.Status, Status: d.Status,
		DurationMS:  durationMS(d.CreatedAt, d.LastAttempt),
		Correlation: map[string]string{"delivery_id": d.ID, "event_id": d.EventID, "webhook_id": d.WebhookID},
		Metadata:    meta,
	}
}

func replayEntries(r ReplayRow) []Entry {
	corr := map[string]string{"replay_job_id": r.ID, "webhook_id": r.WebhookID}
	entries := []Entry{{
		Timestamp: r.CreatedAt, Component: CompReplay, Action: "job_created", Status: "pending",
		Correlation: cloneMap(corr),
	}}
	if r.Status == "completed" || r.Status == "failed" {
		entries = append(entries, Entry{
			Timestamp: r.UpdatedAt, Component: CompReplay, Action: "job_" + r.Status, Status: r.Status,
			DurationMS:  durationMS(r.CreatedAt, r.UpdatedAt),
			Correlation: cloneMap(corr),
			Metadata:    map[string]string{"events_replayed": strconv.Itoa(r.EventsReplayed)},
		})
	}
	return entries
}

func policyEntry(p PolicyRow) Entry {
	ts := p.EndedAt
	if ts.IsZero() {
		ts = p.CreatedAt
	}
	return Entry{
		Timestamp: ts, Component: CompPolicy, Action: "policy_" + p.Status, Status: p.Status,
		DurationMS:  durationMS(p.StartedAt, p.EndedAt),
		Correlation: map[string]string{"policy_execution_id": p.ID, "policy_id": p.PolicyID, "event_id": p.EventID},
		Metadata:    map[string]string{"retry_count": strconv.Itoa(p.RetryCount)},
	}
}

func durationMS(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Package policy implements enterprise policy automation. It is an event CONSUMER
// only: it never participates in a governance or audit transaction and never
// influences a governance decision. It tails the committed audit-event log (the
// same read-only source the webhook relay uses) with its own cursor, evaluates
// operator-defined policies against each published event, and runs pluggable
// actions asynchronously with retries and independent persistence. A policy
// failure can never affect Governance, Audit, Replay, or Event Delivery.
package policy

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ErrStaleClaim is returned by MarkResult when the execution row was reclaimed by
// another worker before this one recorded its result: the fencing token no longer
// matches, so nothing was written. Not a failure — the signal that this worker
// was evicted mid-flight and its result must be discarded (ADR-CONCURRENCY-005).
var ErrStaleClaim = errors.New("policy: execution result fenced — row reclaimed by another worker")

// Event is a committed governance event projected from an audit event and
// evaluated by policy conditions. Conditions match ONLY this published event.
type Event struct {
	// TenantID owns the event. Read from the audit row, never inferred: the
	// consumer tails every tenant's chains on a privileged connection.
	TenantID    string
	EventID     string
	OperationID string
	Operation   string
	Actor       string
	CfgID       string // resource identifier (per-object chain)
	Seq         int64
	OccurredAt  time.Time
	// Metadata holds event attributes available for matching (payload-derived
	// dimensions such as new_lifecycle, plus resource_type/labels when present).
	Metadata map[string]string
}

// eventFrom projects a committed audit event, decoding its payload descriptor
// into matchable metadata. It reads only committed data; it reconstructs nothing.
func eventFrom(a domain.AuditEvent) Event {
	ev := Event{
		TenantID: a.TenantID,
		EventID:  a.EventID, OperationID: a.OperationID, Operation: string(a.Operation),
		Actor: a.Actor, CfgID: a.ChainID, Seq: a.Seq, OccurredAt: a.OccurredAt,
		Metadata: map[string]string{},
	}
	var pv map[string]any
	if len(a.PayloadCanonical) > 0 && json.Unmarshal(a.PayloadCanonical, &pv) == nil {
		for k, v := range pv {
			ev.Metadata[k] = stringify(v)
		}
	}
	return ev
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case json.Number:
		return t.String()
	case float64:
		return trimFloat(t)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func trimFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// Condition selects which events a policy reacts to. Empty slices mean "any";
// all populated criteria must match (AND). Metadata entries must each equal the
// event's corresponding metadata value.
type Condition struct {
	Operations []string          `json:"operations,omitempty"`
	Resources  []string          `json:"resources,omitempty"`
	Actors     []string          `json:"actors,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Matches reports whether the event satisfies the condition.
func (c Condition) Matches(ev Event) bool {
	if len(c.Operations) > 0 && !contains(c.Operations, ev.Operation) {
		return false
	}
	if len(c.Resources) > 0 && !contains(c.Resources, ev.CfgID) {
		return false
	}
	if len(c.Actors) > 0 && !contains(c.Actors, ev.Actor) {
		return false
	}
	for k, v := range c.Metadata {
		if ev.Metadata[k] != v {
			return false
		}
	}
	return true
}

// ActionSpec names an action type and its opaque configuration (validated by
// the action implementation, not here).
type ActionSpec struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Policy is an operator-defined automation: when a committed event matches
// Condition, Action is executed asynchronously.
type Policy struct {
	ID string
	// TenantID owns the policy. Compared against the event's owner by
	// domain.FanOut before the condition is evaluated.
	TenantID  string
	Name      string
	Enabled   bool
	Condition Condition
	Action    ActionSpec
	// Steps, when non-empty, is this Policy's composed response: an ordered,
	// gated Sequence of Actions run one at a time instead of the single
	// Action above (ADR-POLICY-001). Empty Steps means "use Action", exactly
	// as every Policy behaved before this field existed.
	Steps      Sequence
	MaxRetries int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// OwnerTenantID implements domain.Owned.
func (p Policy) OwnerTenantID() string { return p.TenantID }

// OwnerTenantID implements domain.Owned.
func (e Event) OwnerTenantID() string { return e.TenantID }

// ExecutionStatus is the lifecycle state of a policy execution.
type ExecutionStatus string

// Execution states.
const (
	ExecPending    ExecutionStatus = "pending"
	ExecRunning    ExecutionStatus = "running"
	ExecSucceeded  ExecutionStatus = "succeeded"
	ExecFailed     ExecutionStatus = "failed"
	ExecDeadLetter ExecutionStatus = "dead_letter"
)

// Execution is one asynchronous, independently-persisted run of a policy
// against a committed event.
type Execution struct {
	ID            string
	PolicyID      string
	Event         Event
	Status        ExecutionStatus
	RetryCount    int
	Error         string
	StartedAt     time.Time
	EndedAt       time.Time
	NextAttemptAt time.Time
	CreatedAt     time.Time
	// ClaimedAt is the fencing token (see events.Delivery.ClaimedAt): the moment
	// this execution row was claimed by the worker now holding it. A worker whose
	// lease expired and whose row was reclaimed holds a stale token and is fenced
	// out of MarkResult, so its late completion cannot corrupt the reclaimer's
	// state (ADR-CONCURRENCY-005). Zero on rows never claimed (the admin test path).
	ClaimedAt time.Time
	// RunID groups the Executions belonging to one composed Sequence run, one
	// Execution per step index (ADR-POLICY-001). Empty for a single-action
	// policy's execution, exactly as before this field existed — RunProgress
	// is meaningful only for Executions that share one.
	RunID string
	// StepIndex is this Execution's position in its run's Sequence. Zero and
	// unused when RunID is empty.
	StepIndex int
	// Gate is this step's Decision gate resolution, set only when the
	// Sequence step at StepIndex names a Gate. GateNone otherwise.
	Gate GateState
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

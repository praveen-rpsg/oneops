package sdk

import (
	"encoding/json"
	"time"
)

// Retention classes accepted by Archive (mirrors the OpenAPI enum).
const (
	RetentionHistoricalRecord   = "historical_record"
	RetentionHistoricalEvidence = "historical_evidence"
	RetentionAuditRecord        = "audit_record"
	RetentionSupersededPlan     = "superseded_plan"
)

// GovernanceState is the resulting dimensional state of a write operation.
type GovernanceState struct {
	Lifecycle      string `json:"lifecycle"`
	RetentionClass string `json:"retention_class"`
	Authority      string `json:"authority"`
}

// AuditMeta is the read-only audit metadata returned with a write result.
type AuditMeta struct {
	OperationID string `json:"operation_id"`
	ChainID     string `json:"chain_id"`
	Recorded    bool   `json:"recorded"`
}

// GovernanceResult is the outcome of a constitutional write operation.
type GovernanceResult struct {
	Operation  string           `json:"operation"`
	CfgID      string           `json:"cfg_id"`
	Actor      string           `json:"actor"`
	OccurredAt time.Time        `json:"occurred_at"`
	Removed    bool             `json:"removed"`
	RowVersion int64            `json:"row_version"`
	State      *GovernanceState `json:"state,omitempty"`
	Audit      AuditMeta        `json:"audit"`
}

// ObjectState is the current governance state of a configuration object.
type ObjectState struct {
	CfgID           string    `json:"cfg_id"`
	Lifecycle       string    `json:"lifecycle"`
	RetentionClass  string    `json:"retention_class"`
	RetentionPolicy string    `json:"retention_policy"`
	Authority       string    `json:"authority"`
	RowVersion      int64     `json:"row_version"`
	RatifiedBy      string    `json:"ratified_by"`
	ReviewCycle     string    `json:"review_cycle"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// HistoryItem is one governance operation in an object's history.
type HistoryItem struct {
	Seq            int64            `json:"seq"`
	Operation      string           `json:"operation"`
	Actor          string           `json:"actor"`
	OccurredAt     time.Time        `json:"occurred_at"`
	OperationID    string           `json:"operation_id"`
	Removed        bool             `json:"removed"`
	ResultingState *GovernanceState `json:"resulting_state,omitempty"`
}

// HistoryPage is a page of history plus the next cursor (empty when exhausted).
type HistoryPage struct {
	Items      []HistoryItem `json:"items"`
	NextCursor string        `json:"next_cursor"`
}

// AuditChain summarizes an object's audit chain and its verification status.
type AuditChain struct {
	ChainID       string `json:"chain_id"`
	Verified      bool   `json:"verified"`
	Checked       int64  `json:"checked"`
	HeadSeq       int64  `json:"head_seq"`
	HeadHash      string `json:"head_hash"`
	FirstBreakSeq *int64 `json:"first_break_seq,omitempty"`
	BreakReason   string `json:"break_reason"`
}

// AuditEvent is read-only audit event metadata.
type AuditEvent struct {
	Seq         int64     `json:"seq"`
	EventID     string    `json:"event_id"`
	OperationID string    `json:"operation_id"`
	Operation   string    `json:"operation"`
	Actor       string    `json:"actor"`
	OccurredAt  time.Time `json:"occurred_at"`
	PrevHash    string    `json:"prev_hash"`
	ThisHash    string    `json:"this_hash"`
}

// EventsPage is a page of audit events with the applied order and next cursor.
type EventsPage struct {
	Items      []AuditEvent `json:"items"`
	Order      string       `json:"order"`
	NextCursor string       `json:"next_cursor"`
}

// SchedulerStatus is the verification scheduler summary.
type SchedulerStatus struct {
	Enabled     bool      `json:"enabled"`
	HasRun      bool      `json:"has_run"`
	Running     bool      `json:"running"`
	LastRunAt   time.Time `json:"last_run_at"`
	LastHealthy bool      `json:"last_healthy"`
	Stalled     bool      `json:"stalled"`
	Failures    int       `json:"failures"`
	Errors      int       `json:"errors"`
}

// Verification is per-object integrity plus the latest scheduler result.
type Verification struct {
	ChainID       string           `json:"chain_id"`
	IntegrityOK   bool             `json:"integrity_ok"`
	Checked       int64            `json:"checked"`
	FirstBreakSeq *int64           `json:"first_break_seq,omitempty"`
	BreakReason   string           `json:"break_reason"`
	Scheduler     *SchedulerStatus `json:"scheduler,omitempty"`
}

// Dependency is a downstream dependency's health.
type Dependency struct {
	Name  string `json:"name"`
	Up    bool   `json:"up"`
	Error string `json:"error,omitempty"`
}

// AdminStatus is the platform status document.
type AdminStatus struct {
	Service          string          `json:"service"`
	Version          string          `json:"version"`
	Commit           string          `json:"commit"`
	BuildTime        string          `json:"build_time"`
	Env              string          `json:"env"`
	UptimeSeconds    float64         `json:"uptime_seconds"`
	MigrationVersion string          `json:"migration_version"`
	Scheduler        json.RawMessage `json:"scheduler"`
	VerifierHealthy  bool            `json:"verifier_healthy"`
	Dependencies     []Dependency    `json:"dependencies"`
	Healthy          bool            `json:"healthy"`
}

// AdminIntegrity is the integrity/scheduler summary.
type AdminIntegrity struct {
	Enabled     bool      `json:"enabled"`
	Running     bool      `json:"running"`
	HasRun      bool      `json:"has_run"`
	LastRunAt   time.Time `json:"last_run_at"`
	LastHealthy bool      `json:"last_healthy"`
	Stalled     bool      `json:"stalled"`
	ChainsTotal int       `json:"chains_total"`
	ChainsOK    int       `json:"chains_ok"`
	Failures    int       `json:"failures"`
	Errors      int       `json:"errors"`
}

// IntegrityRun is the result of an on-demand verification sweep.
type IntegrityRun struct {
	StartedAt   time.Time `json:"started_at"`
	DurationMS  int64     `json:"duration_ms"`
	ChainsTotal int       `json:"chains_total"`
	ChainsOK    int       `json:"chains_ok"`
	Failures    int       `json:"failures"`
	Errors      int       `json:"errors"`
	Healthy     bool      `json:"healthy"`
}

// MetricsSummary is the whitelisted operational counter summary.
type MetricsSummary struct {
	Counters map[string]float64 `json:"counters"`
}

// AdminConfig is the redacted runtime configuration and enabled modules.
type AdminConfig struct {
	Modules map[string]bool `json:"modules"`
	Config  json.RawMessage `json:"config"`
}

// Report is the combined operational report.
type Report struct {
	Diagnostics json.RawMessage    `json:"diagnostics"`
	Metrics     map[string]float64 `json:"metrics"`
}

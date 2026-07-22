package domain

import (
	"encoding/json"
	"time"
)

// ConfigurationOperation is one of the twelve governed configuration operations
// (Configuration State Model §8). It names the operation an audit event records.
type ConfigurationOperation string

// The twelve configuration operations (Configuration State Model §8).
const (
	OpRatification           ConfigurationOperation = "ratification"
	OpApproval               ConfigurationOperation = "approval"
	OpExtension              ConfigurationOperation = "extension"
	OpReplacement            ConfigurationOperation = "replacement" // supersession
	OpSuspension             ConfigurationOperation = "suspension"
	OpDeprecation            ConfigurationOperation = "deprecation"
	OpWithdrawal             ConfigurationOperation = "withdrawal"
	OpArchiving              ConfigurationOperation = "archiving"
	OpHistoricalPreservation ConfigurationOperation = "historical_preservation"
	OpDeletion               ConfigurationOperation = "deletion"
	OpBaselineFreeze         ConfigurationOperation = "baseline_freeze"
	OpAmendment              ConfigurationOperation = "amendment"
)

// Valid reports whether op is a known configuration operation (State Model §8).
func (op ConfigurationOperation) Valid() bool {
	switch op {
	case OpRatification, OpApproval, OpExtension, OpReplacement, OpSuspension,
		OpDeprecation, OpWithdrawal, OpArchiving, OpHistoricalPreservation,
		OpDeletion, OpBaselineFreeze, OpAmendment:
		return true
	default:
		return false
	}
}

// EventInput is the request to append one audit event. Actor and occurred_at are
// deliberately NOT inputs: the Actor is taken from the authenticated identity
// context and occurred_at is stamped server-authoritatively at append time, so a
// caller can neither forge the author nor backdate the record (ECR-04). Payload
// is raw JSON; canonicalization and hashing are performed in the audit package,
// never here (ECR-01). (EDS v1.1.0 §6.4.)
type EventInput struct {
	// ChainID is the governed-stream / tenant partition key of the audit chain.
	ChainID string
	// OperationID is the idempotent operation identifier; it drives a deterministic
	// event id so a retried operation appends at most once (ECR-05).
	OperationID string
	// Operation is the configuration operation being recorded (State Model §8).
	Operation ConfigurationOperation
	// Payload is the raw, well-formed JSON descriptor of the operation.
	Payload json.RawMessage
}

// Validate enforces EventInput invariants, returning a *ValidationError on the
// first violation or nil when the input is well-formed.
func (e *EventInput) Validate() error {
	if e.ChainID == "" {
		return newValidation("chain_id", "must not be empty")
	}
	if e.OperationID == "" {
		return newValidation("operation_id", "must not be empty")
	}
	if !e.Operation.Valid() {
		return newValidation("operation", "unknown configuration operation: "+string(e.Operation))
	}
	if len(e.Payload) == 0 || !json.Valid(e.Payload) {
		return newValidation("payload", "must be well-formed JSON")
	}
	return nil
}

// AuditEvent is one sealed, chained record of a governed operation — an
// append-only, provenance-bearing, tamper-evident value (Vol IV §4.6). It is a
// pure domain value; the audit package computes its hashes and identifiers.
type AuditEvent struct {
	// ChainID is the audit chain (partition) this event belongs to.
	ChainID string
	// Seq is the contiguous, strictly increasing position within the chain.
	Seq int64
	// EventID is the deterministic id derived from OperationID (set at append).
	EventID string
	// OperationID is the idempotent operation identifier.
	OperationID string
	// Operation is the recorded configuration operation (State Model §8).
	Operation ConfigurationOperation
	// Actor is the authenticated Actor id that performed the operation (ECR-04).
	Actor string
	// PayloadCanonical is the exact byte sequence that was hashed (ECR-01).
	PayloadCanonical []byte
	// PrevHash is the previous event's ThisHash (32 bytes; zero for genesis).
	PrevHash []byte
	// ThisHash is this event's chain hash (32 bytes).
	ThisHash []byte
	// OccurredAt is the server-authoritative time the event was recorded (ECR-04).
	OccurredAt time.Time
}

// VerifyResult reports the outcome of verifying an audit chain over a range.
type VerifyResult struct {
	// ChainID is the verified chain.
	ChainID string
	// OK is true when the range verified with no break.
	OK bool
	// Checked is the number of events verified.
	Checked int64
	// HeadSeq is the sequence of the last successfully verified event (the verified
	// chain head), or 0 for an empty chain. Informational output.
	HeadSeq int64
	// HeadHash is the this_hash of the last successfully verified event (the
	// verified chain head), or nil for an empty chain. Informational output.
	HeadHash []byte
	// FirstBreakSeq is the sequence of the first detected break, if any.
	FirstBreakSeq *int64
	// BreakReason categorizes the first detected break (empty when OK). It is
	// informational only — set by the verifier for diagnostics, with values
	// defined in the audit package; it drives no behavior, API, schema, or
	// persistence.
	BreakReason string
	// AnchorsMatched is the number of external anchors confirmed in the range.
	AnchorsMatched int
}

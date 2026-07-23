package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func validEventInput() domain.EventInput {
	return domain.EventInput{
		ChainID:     "ONEOPS-CFG-0007",
		OperationID: "op-ratify-0007-v3",
		Operation:   domain.OpRatification,
		Payload:     json.RawMessage(`{"lifecycle":"ratified","row_version":3}`),
	}
}

func TestResolve_ProducesCompleteAppendInput(t *testing.T) {
	in := validEventInput()
	actor := "user:alice"
	at := time.Date(2026, 7, 22, 10, 30, 0, 0, time.UTC)

	got, err := Resolve(in, actor, at)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Copied-through fields.
	if got.ChainID != in.ChainID {
		t.Errorf("ChainID = %q, want %q", got.ChainID, in.ChainID)
	}
	if got.OperationID != in.OperationID {
		t.Errorf("OperationID = %q, want %q", got.OperationID, in.OperationID)
	}
	if got.Operation != in.Operation {
		t.Errorf("Operation = %q, want %q", got.Operation, in.Operation)
	}
	if got.Actor != actor {
		t.Errorf("Actor = %q, want %q", got.Actor, actor)
	}
	if !got.OccurredAt.Equal(at) {
		t.Errorf("OccurredAt = %v, want %v", got.OccurredAt, at)
	}

	// Audit-derived fields must equal their authoritative producers exactly.
	wantEventID, err := DeriveEventID(in.OperationID)
	if err != nil {
		t.Fatalf("DeriveEventID: %v", err)
	}
	if got.EventID != wantEventID {
		t.Errorf("EventID = %q, want %q", got.EventID, wantEventID)
	}
	wantCanonical, err := Canonicalize(in.Payload)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if !bytes.Equal(got.PayloadCanonical, wantCanonical) {
		t.Errorf("PayloadCanonical = %q, want %q", got.PayloadCanonical, wantCanonical)
	}

	// No field left unpopulated (proves the input is appendable as-is).
	if got.ChainID == "" || got.OperationID == "" || got.EventID == "" ||
		got.Operation == "" || got.Actor == "" || got.OccurredAt.IsZero() ||
		len(got.PayloadCanonical) == 0 {
		t.Fatalf("Resolve left an AppendInput field empty: %+v", got)
	}
}

func TestResolve_Deterministic(t *testing.T) {
	in := validEventInput()
	at := time.Date(2026, 7, 22, 10, 30, 0, 0, time.UTC)

	first, err := Resolve(in, "user:alice", at)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := Resolve(in, "user:alice", at)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.EventID != second.EventID {
		t.Errorf("EventID not deterministic: %q vs %q", first.EventID, second.EventID)
	}
	if !bytes.Equal(first.PayloadCanonical, second.PayloadCanonical) {
		t.Errorf("PayloadCanonical not deterministic: %q vs %q", first.PayloadCanonical, second.PayloadCanonical)
	}
}

func TestResolve_KeyOrderDoesNotChangeCanonicalPayload(t *testing.T) {
	at := time.Date(2026, 7, 22, 10, 30, 0, 0, time.UTC)

	a := validEventInput()
	a.Payload = json.RawMessage(`{"lifecycle":"ratified","row_version":3}`)
	b := validEventInput()
	b.Payload = json.RawMessage(`{"row_version":3,"lifecycle":"ratified"}`)

	ra, err := Resolve(a, "user:alice", at)
	if err != nil {
		t.Fatalf("Resolve a: %v", err)
	}
	rb, err := Resolve(b, "user:alice", at)
	if err != nil {
		t.Fatalf("Resolve b: %v", err)
	}
	if !bytes.Equal(ra.PayloadCanonical, rb.PayloadCanonical) {
		t.Fatalf("semantically equal payloads canonicalized differently: %q vs %q",
			ra.PayloadCanonical, rb.PayloadCanonical)
	}
}

func TestResolve_ChangedPayloadChangesCanonical(t *testing.T) {
	at := time.Date(2026, 7, 22, 10, 30, 0, 0, time.UTC)

	a := validEventInput()
	b := validEventInput()
	b.Payload = json.RawMessage(`{"lifecycle":"ratified","row_version":4}`)

	ra, err := Resolve(a, "user:alice", at)
	if err != nil {
		t.Fatalf("Resolve a: %v", err)
	}
	rb, err := Resolve(b, "user:alice", at)
	if err != nil {
		t.Fatalf("Resolve b: %v", err)
	}
	if bytes.Equal(ra.PayloadCanonical, rb.PayloadCanonical) {
		t.Fatalf("distinct payloads produced identical canonical bytes: %q", ra.PayloadCanonical)
	}
}

func TestResolve_ValidationErrors(t *testing.T) {
	at := time.Date(2026, 7, 22, 10, 30, 0, 0, time.UTC)

	t.Run("empty actor", func(t *testing.T) {
		if _, err := Resolve(validEventInput(), "", at); !errors.Is(err, ErrEmptyActor) {
			t.Fatalf("error = %v, want %v", err, ErrEmptyActor)
		}
	})
	t.Run("zero occurred_at", func(t *testing.T) {
		if _, err := Resolve(validEventInput(), "user:alice", time.Time{}); !errors.Is(err, ErrMissingOccurredAt) {
			t.Fatalf("error = %v, want %v", err, ErrMissingOccurredAt)
		}
	})
	t.Run("invalid event input propagates", func(t *testing.T) {
		in := validEventInput()
		in.ChainID = "" // domain.EventInput.Validate rejects this.
		_, err := Resolve(in, "user:alice", at)
		if err == nil {
			t.Fatal("expected a validation error for empty chain id")
		}
		var ve *domain.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("error = %v, want *domain.ValidationError", err)
		}
	})
	t.Run("malformed payload", func(t *testing.T) {
		in := validEventInput()
		in.Payload = json.RawMessage(`{"a":`) // invalid JSON; fails input validation.
		if _, err := Resolve(in, "user:alice", at); err == nil {
			t.Fatal("expected an error for malformed payload")
		}
	})
}

// TestResolve_GovernanceCanBuildWithoutFabrication is the contract proof: using
// ONLY values a successful governance operation already holds — the governed
// object id, the operation's idempotency key, and the committed Operation/Actor/
// OccurredAt — plus a payload governance marshals from its own committed result,
// a complete AppendInput is obtained. Governance supplies no EventID and no
// canonical payload: those are produced inside the audit subsystem.
func TestResolve_GovernanceCanBuildWithoutFabrication(t *testing.T) {
	// Values that mirror a governance.Result + its Command, without importing
	// governance (this package must not depend on it).
	cfgID := "ONEOPS-CFG-0014"
	operationID := "idem-key-from-request-boundary"
	operation := domain.OpDeprecation
	actor := "user:carol"
	occurredAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	// Governance marshals a descriptor of its own committed outcome. Marshalling
	// is not canonicalization; the canonical form is produced by Resolve.
	payload, err := json.Marshal(struct {
		Operation  string `json:"operation"`
		CfgID      string `json:"cfg_id"`
		Lifecycle  string `json:"lifecycle"`
		RowVersion int64  `json:"row_version"`
	}{
		Operation:  string(operation),
		CfgID:      cfgID,
		Lifecycle:  "deprecated",
		RowVersion: 9,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	in := domain.EventInput{
		ChainID:     domain.AuditChainID(cfgID), // single owned mapping
		OperationID: operationID,
		Operation:   operation,
		Payload:     payload,
	}

	got, err := Resolve(in, actor, occurredAt)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got.ChainID != cfgID {
		t.Errorf("ChainID = %q, want %q", got.ChainID, cfgID)
	}
	if got.EventID == "" || len(got.PayloadCanonical) == 0 {
		t.Fatalf("audit-derived fields not populated: %+v", got)
	}
	// The whole point: every field is set, none invented by governance.
	if got.OperationID != operationID || got.Operation != operation ||
		got.Actor != actor || !got.OccurredAt.Equal(occurredAt) {
		t.Fatalf("copied-through fields incorrect: %+v", got)
	}
}

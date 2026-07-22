package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func validEventInput() EventInput {
	return EventInput{
		ChainID:     "tenant-a",
		OperationID: "op-123",
		Operation:   OpReplacement,
		Payload:     json.RawMessage(`{"old":"A","new":"B"}`),
	}
}

func TestEventInputValidateOK(t *testing.T) {
	e := validEventInput()
	if err := e.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestEventInputValidateErrors(t *testing.T) {
	cases := []struct {
		field  string
		mutate func(*EventInput)
	}{
		{"chain_id", func(e *EventInput) { e.ChainID = "" }},
		{"operation_id", func(e *EventInput) { e.OperationID = "" }},
		{"operation", func(e *EventInput) { e.Operation = ConfigurationOperation("bogus") }},
		{"payload", func(e *EventInput) { e.Payload = nil }},
		{"payload", func(e *EventInput) { e.Payload = json.RawMessage("[") }}, // malformed JSON
	}
	for _, c := range cases {
		e := validEventInput()
		c.mutate(&e)
		err := e.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Errorf("%s: expected ValidationError, got %T", c.field, err)
			continue
		}
		if ve.Field != c.field {
			t.Errorf("expected field %q, got %q", c.field, ve.Field)
		}
	}
}

func TestConfigurationOperationValid(t *testing.T) {
	all := []ConfigurationOperation{
		OpRatification, OpApproval, OpExtension, OpReplacement, OpSuspension,
		OpDeprecation, OpWithdrawal, OpArchiving, OpHistoricalPreservation,
		OpDeletion, OpBaselineFreeze, OpAmendment,
	}
	if len(all) != 12 {
		t.Fatalf("expected 12 configuration operations, got %d", len(all))
	}
	for _, op := range all {
		if !op.Valid() {
			t.Errorf("%q should be valid", op)
		}
	}
	if ConfigurationOperation("nope").Valid() {
		t.Error("unknown operation should be invalid")
	}
}

// TestEventInputHasNoActorOrTime guards ECR-04: actor and occurred_at must not be
// caller-supplied fields on EventInput (they are set server-side at append).
func TestEventInputHasNoActorOrTime(t *testing.T) {
	tp := reflect.TypeOf(EventInput{})
	for _, forbidden := range []string{"Actor", "OccurredAt"} {
		if _, ok := tp.FieldByName(forbidden); ok {
			t.Errorf("EventInput must not have field %q (ECR-04)", forbidden)
		}
	}
}

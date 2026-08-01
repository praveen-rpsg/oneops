package policy

import (
	"encoding/json"
	"errors"
	"fmt"
)

// This file delivers the reduced Workflow concept — Vol II §5.3: "composition
// of Actions & Decisions over Time" — as data over the existing Action
// primitive. There is no Workflow package, type, or entity here: a Sequence
// is an ordered list of ActionStep, each an existing ActionSpec plus an
// optional Decision Gate, and a run's progress is a projection computed over
// the existing Execution rows a run produces — never a reified run entity.
// See ADR-POLICY-001.

// GateSpec names the Decision that must resolve before a composed Sequence
// advances past the step it guards. Type selects the gate's evaluator (e.g.
// "approval", consulting the governance multi-approver quorum,
// ADR-GOV-005) exactly as ActionSpec.Type selects an action implementation;
// Config is opaque to this package, validated by the evaluator, not here.
type GateSpec struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Validate reports whether the gate names an evaluator.
func (g GateSpec) Validate() error {
	if g.Type == "" {
		return errors.New("policy: gate type is required")
	}
	return nil
}

// ActionStep is one step of a composed Sequence: the Action to run, and an
// optional Decision Gate that must resolve before the NEXT step may run. A
// step with no Gate advances as soon as its Action's Execution succeeds.
type ActionStep struct {
	Action ActionSpec `json:"action"`
	Gate   *GateSpec  `json:"gate,omitempty"`
}

// Validate reports whether the step names an action and, if present, a
// well-formed gate.
func (s ActionStep) Validate() error {
	if s.Action.Type == "" {
		return errors.New("policy: step action type is required")
	}
	if s.Gate != nil {
		if err := s.Gate.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Sequence is an ordered, gated composition of Actions — a Policy's response
// when it is more than one action. An empty Sequence means the Policy's
// single Action field is its whole response, exactly as before this type
// existed: Sequence is additive, not a replacement (backward compatible by
// construction).
type Sequence []ActionStep

// Validate reports whether every step is well-formed. An empty Sequence is
// always valid — it simply is not in use.
func (seq Sequence) Validate() error {
	for i, s := range seq {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("policy: sequence step %d: %w", i, err)
		}
	}
	return nil
}

// GateState is a step's Decision gate resolution, recorded on the Execution
// that ran the step it guards. GateNone is the value for a step with no
// Gate, or for an Execution that has not reached its gate check yet.
type GateState string

// Gate resolution states.
const (
	GateNone    GateState = ""
	GatePending GateState = "pending"
	GatePassed  GateState = "passed"
)

// RunProgress is the run-progress view over one composed Sequence run. It is
// NOT persisted independently — there is no run-progress table and no run
// entity. It is computed by pairing a Policy's Sequence with the Executions
// that share one RunID (one Execution per step index), so a run's progress
// can never disagree with the Execution rows it is derived from.
type RunProgress struct {
	RunID string
	Seq   Sequence
	// Steps holds this run's Executions so far, at most one per step index.
	// A step not yet enqueued has no entry.
	Steps []Execution
}

// NextStepIndex is the run's cursor: the index of the first step not yet
// resolved, or len(Seq) once the run is complete. It is derived fresh from
// Steps every call, never cached or stored, which is what makes it a projection
// rather than a second source of truth for progress.
//
// CAUTION for the executor: a returned index is NOT always "attempt this
// action". When step i's action has already SUCCEEDED but its gate has not yet
// resolved to GatePassed, NextStepIndex returns i to mean "waiting on the gate"
// — the action must NOT be re-attempted. The caller must check the gate state
// at the returned index before enqueuing work (see ADR-POLICY-001 §3).
func (p RunProgress) NextStepIndex() int {
	for i := range p.Seq {
		ex, ok := executionAtStep(p.Steps, i)
		if !ok || ex.Status != ExecSucceeded {
			// Never attempted, or still pending/running/failed: this is where
			// the run resumes — ClaimDue's existing retry accounting decides
			// whether that is a first attempt or a retry.
			return i
		}
		if p.Seq[i].Gate != nil && ex.Gate != GatePassed {
			// The step itself succeeded, but its Decision gate has not resolved:
			// the sequence is paused here, not advanced.
			return i
		}
	}
	return len(p.Seq)
}

// Complete reports whether every step has a succeeded Execution and every
// Gate it names has passed.
func (p RunProgress) Complete() bool { return len(p.Seq) > 0 && p.NextStepIndex() >= len(p.Seq) }

func executionAtStep(steps []Execution, index int) (Execution, bool) {
	for _, ex := range steps {
		if ex.StepIndex == index {
			return ex, true
		}
	}
	return Execution{}, false
}

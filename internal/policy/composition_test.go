package policy

import (
	"strings"
	"testing"
)

// ---- GateSpec / ActionStep / Sequence Validate() ----------------------------

func TestGateSpec_Validate(t *testing.T) {
	if err := (GateSpec{}).Validate(); err == nil {
		t.Fatal("empty gate type must fail validation")
	}
	if err := (GateSpec{Type: "approval"}).Validate(); err != nil {
		t.Fatalf("named gate type must validate: %v", err)
	}
}

func TestActionStep_Validate(t *testing.T) {
	if err := (ActionStep{}).Validate(); err == nil {
		t.Fatal("a step with no action type must fail validation")
	}
	if err := (ActionStep{Action: ActionSpec{Type: "http"}}).Validate(); err != nil {
		t.Fatalf("action-only step must validate: %v", err)
	}
	valid := ActionStep{Action: ActionSpec{Type: "http"}, Gate: &GateSpec{Type: "approval"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("action+valid gate must validate: %v", err)
	}
	invalidGate := ActionStep{Action: ActionSpec{Type: "http"}, Gate: &GateSpec{}}
	if err := invalidGate.Validate(); err == nil {
		t.Fatal("a step with an unnamed gate must fail validation")
	}
}

func TestSequence_Validate(t *testing.T) {
	if err := Sequence(nil).Validate(); err != nil {
		t.Fatalf("an empty/absent sequence must always validate: %v", err)
	}
	good := Sequence{
		{Action: ActionSpec{Type: "notification"}},
		{Action: ActionSpec{Type: "http"}, Gate: &GateSpec{Type: "approval"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("well-formed sequence must validate: %v", err)
	}
	bad := Sequence{
		{Action: ActionSpec{Type: "notification"}},
		{Action: ActionSpec{}}, // step 1 has no action type
	}
	err := bad.Validate()
	if err == nil {
		t.Fatal("a sequence with one malformed step must fail validation")
	}
	if !strings.Contains(err.Error(), "step 1") {
		t.Errorf("error should identify the offending step index: %v", err)
	}
}

// ---- RunProgress: the run-progress projection -------------------------------

func seq(withGate bool) Sequence {
	s := Sequence{
		{Action: ActionSpec{Type: "http"}},
		{Action: ActionSpec{Type: "notification"}},
	}
	if withGate {
		s[0].Gate = &GateSpec{Type: "approval"}
	}
	return s
}

func TestRunProgress_NextStepIndex_NoAttemptsYet(t *testing.T) {
	p := RunProgress{RunID: "r1", Seq: seq(false)}
	if got := p.NextStepIndex(); got != 0 {
		t.Errorf("NextStepIndex with no Executions = %d, want 0", got)
	}
	if p.Complete() {
		t.Error("a run with no Executions must not report Complete")
	}
}

func TestRunProgress_NextStepIndex_FirstStepPendingOrFailed_DoesNotAdvance(t *testing.T) {
	p := RunProgress{RunID: "r1", Seq: seq(false), Steps: []Execution{
		{StepIndex: 0, Status: ExecFailed},
	}}
	if got := p.NextStepIndex(); got != 0 {
		t.Errorf("a failed/retryable step must not advance the cursor: got %d, want 0", got)
	}
}

func TestRunProgress_NextStepIndex_SucceededUngatedStep_Advances(t *testing.T) {
	p := RunProgress{RunID: "r1", Seq: seq(false), Steps: []Execution{
		{StepIndex: 0, Status: ExecSucceeded},
	}}
	if got := p.NextStepIndex(); got != 1 {
		t.Errorf("a succeeded, ungated step must advance the cursor: got %d, want 1", got)
	}
}

func TestRunProgress_NextStepIndex_GatePendingPausesTheRun(t *testing.T) {
	p := RunProgress{RunID: "r1", Seq: seq(true), Steps: []Execution{
		{StepIndex: 0, Status: ExecSucceeded, Gate: GatePending},
	}}
	if got := p.NextStepIndex(); got != 0 {
		t.Errorf("a succeeded step whose gate has not passed must pause at its own index: got %d, want 0", got)
	}
	if p.Complete() {
		t.Error("a run paused on a gate must not report Complete")
	}
}

func TestRunProgress_NextStepIndex_GatePassedAdvances(t *testing.T) {
	p := RunProgress{RunID: "r1", Seq: seq(true), Steps: []Execution{
		{StepIndex: 0, Status: ExecSucceeded, Gate: GatePassed},
	}}
	if got := p.NextStepIndex(); got != 1 {
		t.Errorf("a resolved gate must let the run advance: got %d, want 1", got)
	}
}

func TestRunProgress_Complete_AllStepsSucceeded(t *testing.T) {
	p := RunProgress{RunID: "r1", Seq: seq(true), Steps: []Execution{
		{StepIndex: 0, Status: ExecSucceeded, Gate: GatePassed},
		{StepIndex: 1, Status: ExecSucceeded},
	}}
	if !p.Complete() {
		t.Error("a run whose every step succeeded and whose gates all passed must be Complete")
	}
	if got := p.NextStepIndex(); got != len(p.Seq) {
		t.Errorf("NextStepIndex on a complete run = %d, want len(Seq)=%d", got, len(p.Seq))
	}
}

func TestRunProgress_Complete_EmptySequenceIsNotComplete(t *testing.T) {
	// A vacuous sequence is not "complete work" — there is nothing to run, so
	// nothing was accomplished either. Complete is reserved for an actual
	// composed run.
	p := RunProgress{RunID: "r1"}
	if p.Complete() {
		t.Error("an empty Sequence must not report Complete")
	}
}

// A sequence step's own retries are the existing Execution mechanism (the same
// row's RetryCount/status cycle a single-action policy already uses) — a
// composed run introduces no second retry concept.
func TestRunProgress_RetryOfSameStep_DoesNotSkipAhead(t *testing.T) {
	p := RunProgress{RunID: "r1", Seq: seq(false), Steps: []Execution{
		{StepIndex: 0, Status: ExecFailed, RetryCount: 2},
	}}
	if got := p.NextStepIndex(); got != 0 {
		t.Errorf("a step still retrying must remain the cursor: got %d, want 0", got)
	}
}

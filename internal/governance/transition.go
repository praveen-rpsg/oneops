// Package governance implements the constitutional Configuration Operations
// engine (Configuration State Model §8): the single authoritative mutation path
// for a Configuration Object's dimensions. It owns business validation, state-
// transition rules, authorization hooks, and one transaction per operation. It
// does not hash, persist audit, verify, or publish anchors.
package governance

import (
	"fmt"

	"github.com/rpsg/oneops/internal/domain"
)

// plan is the computed dimensional effect of a §8 operation. Remove replaces the
// dimension changes with object removal (Deletion).
type plan struct {
	Lifecycle domain.Lifecycle
	Retention domain.RetentionClass
	Authority domain.Authority
	Remove    bool
}

// TransitionError reports an operation rejected because the object's current
// state does not satisfy the operation's precondition (State Model §8).
type TransitionError struct {
	Operation domain.ConfigurationOperation
	From      domain.Lifecycle
	Reason    string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("governance: %s not permitted from lifecycle %q: %s", e.Operation, e.From, e.Reason)
}

func invalid(op domain.ConfigurationOperation, obj *domain.ConfigObject, reason string) error {
	return &TransitionError{Operation: op, From: obj.Lifecycle, Reason: reason}
}

// isArchivalRetention reports whether rc is a retention class Archiving may set.
func isArchivalRetention(rc domain.RetentionClass) bool {
	switch rc {
	case domain.RetentionHistoricalRecord, domain.RetentionHistoricalEvidence,
		domain.RetentionAuditRecord, domain.RetentionSupersededPlan:
		return true
	default:
		return false
	}
}

// planTransition computes the constitutional dimensional outcome of op on obj
// (State Model §8), validating the operation's precondition. It is a pure
// function — no persistence, no side effects. Operations requiring a successor
// edge (Extension, Replacement) or schema not yet present (Amendment, Baseline
// Freeze, Historical Preservation) return ErrUnsupportedOperation.
func planTransition(op domain.ConfigurationOperation, obj *domain.ConfigObject, cmd Command) (plan, error) {
	switch op {
	case domain.OpRatification:
		// Draft/In Review → Ratified; Authority Active; Retention Current Baseline.
		if obj.Lifecycle != domain.LifecycleDraft && obj.Lifecycle != domain.LifecycleInReview {
			return plan{}, invalid(op, obj, "requires draft or in_review")
		}
		return plan{Lifecycle: domain.LifecycleRatified, Retention: domain.RetentionCurrentBaseline, Authority: domain.AuthorityActive}, nil

	case domain.OpApproval:
		// Review passed (non-constitutional) → Approved; Authority Active.
		if obj.Lifecycle != domain.LifecycleDraft && obj.Lifecycle != domain.LifecycleInReview {
			return plan{}, invalid(op, obj, "requires draft or in_review")
		}
		return plan{Lifecycle: domain.LifecycleApproved, Retention: obj.RetentionClass, Authority: domain.AuthorityActive}, nil

	case domain.OpSuspension:
		// Resumable work → Suspended; Authority unchanged.
		if obj.Lifecycle == domain.LifecycleWithdrawn || obj.Lifecycle == domain.LifecycleSuspended {
			return plan{}, invalid(op, obj, "already terminal or suspended")
		}
		return plan{Lifecycle: domain.LifecycleSuspended, Retention: obj.RetentionClass, Authority: obj.Authority}, nil

	case domain.OpDeprecation:
		// In-force artifact phased out → Deprecated; Authority stays Active.
		if obj.Authority != domain.AuthorityActive {
			return plan{}, invalid(op, obj, "only active artifacts may be deprecated")
		}
		return plan{Lifecycle: domain.LifecycleDeprecated, Retention: obj.RetentionClass, Authority: domain.AuthorityActive}, nil

	case domain.OpWithdrawal:
		// Retracted → Withdrawn; Authority Non-Normative, or Historical if it once
		// governed (currently Active).
		if obj.Lifecycle == domain.LifecycleWithdrawn {
			return plan{}, invalid(op, obj, "already withdrawn")
		}
		au := domain.AuthorityNonNormative
		if obj.Authority == domain.AuthorityActive {
			au = domain.AuthorityHistorical
		}
		return plan{Lifecycle: domain.LifecycleWithdrawn, Retention: obj.RetentionClass, Authority: au}, nil

	case domain.OpArchiving:
		// Retention set/confirmed to an archival class; Authority NEVER changed.
		if !isArchivalRetention(cmd.TargetRetention) {
			return plan{}, invalid(op, obj, "target_retention must be an archival class")
		}
		return plan{Lifecycle: obj.Lifecycle, Retention: cmd.TargetRetention, Authority: obj.Authority}, nil

	case domain.OpDeletion:
		// Only Working Material may be removed (dependents checked by the engine).
		if obj.RetentionClass != domain.RetentionWorkingMaterial {
			return plan{}, invalid(op, obj, "only working_material may be deleted")
		}
		return plan{Remove: true}, nil

	default:
		return plan{}, fmt.Errorf("%w: %s", ErrUnsupportedOperation, op)
	}
}

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

// edgeSpec is a dependency edge an operation must record as part of its own
// constitutional mutation. Extension's effect on the base is *relational*, not
// dimensional (State Model §8: "base Extended By += successor"), so the edge is
// written inside the operation's single transaction — an audit event asserting
// an extension that has no edge, or an edge with no audit event, would break the
// atomicity guarantee of ADR-AUDIT-005 just as surely as a dimension would.
type edgeSpec struct {
	From string
	To   string
	Kind domain.EdgeKind
}

// plan is the computed effect of a §8 operation. Remove replaces the dimension
// changes with object removal (Deletion); Edge, when non-nil, is a dependency
// edge the operation must record in the same transaction.
type plan struct {
	Lifecycle domain.Lifecycle
	Retention domain.RetentionClass
	Authority domain.Authority
	Remove    bool
	Edge      *edgeSpec
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

// planTransition computes the constitutional outcome of op on obj (State Model
// §8), validating the operation's precondition. It is a pure function — no
// persistence, no side effects. Replacement (which requires the M3 four-part
// Replacement Test) and the operations whose schema is not yet present
// (Amendment, Baseline Freeze, Historical Preservation) return
// ErrUnsupportedOperation.
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

	case domain.OpExtension:
		// §8 Extension: a successor depends on / inherits the base. The base's
		// dimensions are ALL UNCHANGED — most importantly Authority, which stays
		// exactly what it was (Active stays Active). That is precisely what makes
		// Extension structurally distinct from Replacement, and its absence is what
		// produced the CVP error: an extension must never demote its base to
		// Historical. The operation's constitutional effect is the `extends` edge.
		//
		// The §8 precondition "base responsibilities not re-owned" is NOT enforced
		// here: the evaluator that decides it is M3.3's ResponsibilityEvaluator, and
		// wiring it into governance is WP-1 (Replacement). Duplicating that logic
		// here would violate composition-over-duplication (Law 7). Until WP-1 lands,
		// this operation enforces the structural preconditions only — the same
		// incremental convergence the ARB accepted for M3.1's F1 finding.
		if cmd.SuccessorID == "" {
			return plan{}, invalid(op, obj, "successor_id is required")
		}
		if cmd.SuccessorID == cmd.CfgID {
			return plan{}, invalid(op, obj, "an object cannot extend itself")
		}
		return plan{
			Lifecycle: obj.Lifecycle,
			Retention: obj.RetentionClass,
			Authority: obj.Authority,
			Edge:      &edgeSpec{From: cmd.SuccessorID, To: cmd.CfgID, Kind: domain.EdgeKindExtends},
		}, nil

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

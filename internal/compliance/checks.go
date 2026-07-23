package compliance

import "github.com/rpsg/oneops/internal/timeline"

// evaluateChecks runs the read-only compliance rules over already-composed
// evidence facets. The rule set and its order are fixed, so the result is
// deterministic for a given persisted state.
func evaluateChecks(gov GovernanceSummary, integ IntegritySummary, entries []timeline.Entry) []Check {
	auditPresent := false
	failedPolicy := false
	hasApproval := false
	for _, e := range entries {
		switch e.Component {
		case timeline.CompAudit:
			auditPresent = true
		case timeline.CompPolicy:
			if e.Status == "failed" || e.Status == "dead_letter" {
				failedPolicy = true
			}
		case timeline.CompGovernance:
			if op := e.Metadata["operation"]; op == "ratification" || op == "approval" {
				hasApproval = true
			}
		}
	}

	lifecycleComplete := gov.Lifecycle != "" && gov.Lifecycle != "draft" && gov.Lifecycle != "in_review"
	approvalsPresent := gov.RatifiedBy != "" || hasApproval

	return []Check{
		{
			ID: "audit-chain-verified", Description: "The audit chain verifies with no break.",
			Passed: integ.Verified, Detail: reason(integ.Verified, "verified", integ.BreakReason),
		},
		{
			ID: "no-failed-integrity-verification", Description: "No integrity break was detected.",
			Passed: integ.FirstBreakSeq == nil,
		},
		{
			ID: "audit-events-present", Description: "The governance object has committed audit events.",
			Passed: auditPresent,
		},
		{
			ID: "governance-lifecycle-complete", Description: "Lifecycle progressed past draft/in_review.",
			Passed: lifecycleComplete, Detail: gov.Lifecycle,
		},
		{
			ID: "required-approvals-present", Description: "An approver or ratification is recorded.",
			Passed: approvalsPresent,
		},
		{
			ID: "policy-executions-completed", Description: "No failed or dead-letter policy executions.",
			Passed: !failedPolicy,
		},
	}
}

func reason(ok bool, okMsg, breakMsg string) string {
	if ok {
		return okMsg
	}
	if breakMsg != "" {
		return breakMsg
	}
	return "not verified"
}

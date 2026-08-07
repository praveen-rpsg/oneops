# ADR-SOC-010 — Security-response automation is the SAFE slice of SOAR: outbound-only actions, triggered off the incident lifecycle, never the audit chain

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Decider** | Acting CTO (founder deferred: "need best, leaving it on you CTO") |
| **Related** | ADR-SOC-003 (security incidents), ADR-POLICY-001 (the policy action-step engine reused for execution), Vol II §5.3 / PLATFORM-BUILD-PLAN §4 (Workflow reduced away), the ratified machine-action-attestation finding + V2 "price the autonomy" strategy, `internal/security/responder.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.5. **Completes the E8 SOC epic.** |

## Context

E8.5's plan line said "SOAR playbooks on the workflow engine." Scouting found
two hard facts: (1) there is NO workflow engine — it was reduced away; the
`policy` engine consumes the `audit_event` **governance hash-chain** (a closed
set of 12 constitutional operations), and injecting operational security events
there would corrupt the constitutional ledger; (2) programmable security
RESPONSE is automated machine action — the corpus's most safety-gated capability
(the uncontested #2 finding; the V2 "price the autonomy, not the observation"
strategy). Building destructive/autonomous response speculatively, on a
customer-less product, is exactly what the standing charter cautions against.

## Decision

Build the **SAFE slice** and draw the autonomy line explicitly.

- **`security_response_rule`** (tenant-owned config, NOT a reified Workflow/
  Playbook): a condition (`min_severity` threshold + optional `asset_id`) + ONE
  `ActionSpec`. `action_type` is restricted to a **SAFE allowlist — `http`
  (webhook) and `notification` (internal)** — the exact action types
  `policy.DefaultRegistry` already registers, reused for execution. The
  allowlist is enforced in BOTH `domain.Validate` AND a DB CHECK constraint
  (defense in depth); `command` (arbitrary execution) and any destructive/
  response action (isolate/block/disable/quarantine) are refused at both layers.
- **Triggered off the security-incident lifecycle, NOT the audit chain.** A
  leader-gated `security.Responder` (mirroring the Detector/IOCMatcher) scans
  security-sourced incidents in a trailing window per tenant, matches enabled
  rules, and runs the safe action via the SAME `policy` action `Registry`
  instance the policy executor uses. `domain.ConfigurationOperation` and the
  `audit_event` chain / `policy.Consumer` are **untouched** (verified: empty
  diff).
- **Exactly-once, record-first (at-most-once on crash).** A
  `security_response_dispatch` ledger `UNIQUE (tenant, incident, rule)`
  (append-only, immutability-triggered + REVOKEd like `incident_event`) is
  claimed under the incident's `FOR UPDATE` BEFORE the action runs. An outbound
  webhook is not naturally idempotent, so a crash between claim and run drops
  that one firing rather than risking a duplicate external call — the safer
  trade for a SAFE-action responder. Window-tiling bounds re-scan; 0 new
  privileged-read guard exemptions.

### The line we did NOT cross

Destructive/autonomous response actions are **deferred behind the machine-action
attestation model** — they are structurally unreachable today (no such
`policy.Action` exists; the allowlist blocks even `command`). Enabling them is a
strategy-level decision (design the autonomy/attestation model first), not an
incremental story.

## Consequences

**Guaranteed.** Operators can automate SAFE, outbound security response
(webhook to an external SOAR/ticketing tool; internal notification) on security
incidents, tenant-isolated, exactly-once, reduced-concept (rule + action, no
Workflow). The constitutional audit ledger is untouched. No destructive machine
action is reachable.

**Not built / deferred.** Destructive/containment actions (the autonomy
frontier); a timeline note on the triggering incident (kept the only side
effect the safe action itself); the fuller "SOAR" of orchestrated multi-system
containment. The platform still auto-responds via the E5 notify→page→escalate
loop independently of this engine.

## Enforcement

- `TestNewSecurityResponseRule_RejectsCommandAction` / `_RejectsEveryNonSafeActionType`
  + `TestSecurityResponseRule_DBLevelRejectsUnsafeActionType` — the allowlist is
  the safety gate, enforced at domain AND database.
- `TestSecurityResponderStore_ClaimDispatch_ExactlyOnce`,
  `TestSecurityResponder_EndToEnd_DispatchesSafeActionExactlyOnceAcrossRepeatedPasses`,
  `_TwoTenantsSharingAssetIDIsolated` — exactly-once + isolation.
- `TestEveryAuditAppendPath_SerialisesOnItsChainHead` (dispatch ledger swept in),
  `TestPrivilegedReads_AreScopedToATenant` (0 new exemptions) — must stay green.
- The audit chain / `ConfigurationOperation` must stay untouched; the SAFE
  allowlist must never gain a destructive type without superseding this ADR and
  the attestation-model decision.

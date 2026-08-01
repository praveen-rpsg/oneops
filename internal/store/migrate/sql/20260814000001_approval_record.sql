-- ADR-GOV-005 Multi-approver approval quorum — approval_record tracks each
-- DISTINCT approver's approval of a governance object (Configuration State
-- Model §8 Approval), the unit the tenant's governance_required_approvals
-- quorum threshold (Setting, internal/domain/setting.go) is counted over.
--
-- UNIQUE (tenant_id, governance_id, approver_user_id) is the whole
-- enforcement mechanism: it is what makes a second approval from the same
-- approver impossible to record, rather than merely discouraged by
-- application logic a bug (or a second code path) could bypass — the same
-- shape team_membership's uq_team_membership_team_user already uses.
--
-- approver_user_id is deliberately NOT a foreign key to app_user. The
-- identity recorded here is governance.Command.Actor, which
-- handlers_governance.go has passed straight through as the verified JWT
-- `sub` (internal/auth/jwt.go) since the engine's first version — this
-- platform has never resolved that identity against app_user anywhere (no
-- request path does the lookup, and dev-mode's synthetic actor is the
-- literal string "system", not a provisioned app_user row). An FK here would
-- make every Approval call using an unprovisioned identity fail with a
-- foreign-key violation instead of recording an approval, which is a
-- regression relative to today's single-actor approve. This is a real
-- identity-binding gap between the governance engine and the app_user/
-- membership model, not a decision this migration papers over; it is
-- recorded as a follow-up in ADR-GOV-005.
--
-- governance_id DOES reference configuration_object: it names a real
-- governance object or it names nothing, and ON DELETE CASCADE keeps a
-- deleted (working_material) object from leaving orphaned approval rows —
-- mirroring dependency_edge's own FK to configuration_object.
CREATE TABLE IF NOT EXISTS approval_record (
    approval_id      text        PRIMARY KEY,
    tenant_id        text        NOT NULL REFERENCES tenant (tenant_id),
    governance_id    text        NOT NULL REFERENCES configuration_object (cfg_id) ON DELETE CASCADE,
    approver_user_id text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_approval_tenant_governance_approver UNIQUE (tenant_id, governance_id, approver_user_id)
);

CREATE INDEX IF NOT EXISTS ix_approval_governance ON approval_record (governance_id, created_at);

COMMENT ON TABLE approval_record IS
    'One distinct approver''s approval of a governance object (State Model §8 Approval), counted toward the tenant''s governance_required_approvals quorum (ADR-GOV-005).';
COMMENT ON COLUMN approval_record.tenant_id IS
    'The RLS key. The write always supplies it explicitly (governance.Command.TenantID); the policy''s WITH CHECK is defence in depth, not the source of the value.';
COMMENT ON COLUMN approval_record.approver_user_id IS
    'The approving actor''s identity (governance.Command.Actor / the verified JWT sub) — NOT a foreign key to app_user; see ADR-GOV-005 for the identity-binding gap this leaves open.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001), the same shape as 20260812000001's
-- setting policy and 20260810000001's team policy.
--
-- current_setting('app.tenant_id', true) returns NULL when unset, and NULL
-- matches no row: fail-closed by construction.
ALTER TABLE approval_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_record FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON approval_record;
CREATE POLICY tenant_isolation ON approval_record
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

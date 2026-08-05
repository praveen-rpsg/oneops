-- Reverses 20260902000001_escalation_policy.

DROP POLICY IF EXISTS tenant_isolation ON escalation_tier;
ALTER TABLE IF EXISTS escalation_tier DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON escalation_policy;
ALTER TABLE IF EXISTS escalation_policy DISABLE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS ix_escalation_tier_on_call_schedule;
DROP INDEX IF EXISTS ix_escalation_tier_tenant;
DROP INDEX IF EXISTS ix_escalation_policy_tenant_policy;

DROP TABLE IF EXISTS escalation_tier;
DROP TABLE IF EXISTS escalation_policy;

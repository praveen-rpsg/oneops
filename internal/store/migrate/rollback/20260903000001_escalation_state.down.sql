-- Reverses 20260903000001_escalation_state.

DROP POLICY IF EXISTS tenant_isolation ON escalation_state;
ALTER TABLE IF EXISTS escalation_state DISABLE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS ix_escalation_state_policy;
DROP INDEX IF EXISTS ix_escalation_state_claim;

DROP TABLE IF EXISTS escalation_state;

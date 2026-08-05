-- Reverses 20260901000001_on_call_schedule.

DROP POLICY IF EXISTS tenant_isolation ON on_call_participant;
ALTER TABLE IF EXISTS on_call_participant DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON on_call_schedule;
ALTER TABLE IF EXISTS on_call_schedule DISABLE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS ix_on_call_participant_tenant;
DROP INDEX IF EXISTS ix_on_call_schedule_tenant_schedule;

DROP TABLE IF EXISTS on_call_participant;
DROP TABLE IF EXISTS on_call_schedule;

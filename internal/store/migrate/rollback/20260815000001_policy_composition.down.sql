-- Reverses 20260815000001_policy_composition.

DROP INDEX IF EXISTS ix_policy_execution_run;
DROP INDEX IF EXISTS uq_policy_execution_run_step;
ALTER TABLE policy_execution DROP COLUMN IF EXISTS gate;
ALTER TABLE policy_execution DROP COLUMN IF EXISTS step_index;
ALTER TABLE policy_execution DROP COLUMN IF EXISTS run_id;

ALTER TABLE policy DROP CONSTRAINT IF EXISTS ck_policy_has_response;
ALTER TABLE policy DROP COLUMN IF EXISTS steps;

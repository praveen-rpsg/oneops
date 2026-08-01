-- ADR-POLICY-001 Action-Decision composition — a Policy's response can be an
-- ordered, gated SEQUENCE of Actions instead of one Action (the reduced
-- Workflow concept, Vol II §5.3: "composition of Actions & Decisions over
-- Time"), delivered by extending the existing policy/policy_execution
-- tables. No workflow_* table exists here or anywhere in this schema.
--
-- steps carries the Sequence definition (policy.Sequence, JSON-encoded): an
-- ordered array of {action:{type,config}, gate?:{type,config}}. A policy
-- with an empty steps array (the default, and every policy created before
-- this column existed) is unchanged — it still runs its single
-- action_type/action_config exactly as before.
ALTER TABLE policy
    ADD COLUMN IF NOT EXISTS steps jsonb NOT NULL DEFAULT '[]';

-- A policy must describe a response somehow: a single action_type, or a
-- non-empty steps sequence (or, for forward compatibility, both — Steps
-- takes precedence when present; ADR-POLICY-001). This is enforcement, not
-- documentation: it is what makes an empty-response policy impossible to
-- insert, rather than merely discouraged by application code that could be
-- bypassed.
ALTER TABLE policy
    ADD CONSTRAINT ck_policy_has_response
        CHECK (action_type <> '' OR jsonb_array_length(steps) > 0);

-- run_id/step_index/gate turn a policy_execution row into one STEP of a
-- composed Sequence run. There is no separate run-progress table and no run
-- entity: policy.RunProgress is computed by grouping a run's own
-- policy_execution rows by run_id and ordering them by step_index — a
-- projection over Executions this table already produces, not a reified
-- Workflow run (ADR-POLICY-001).
--
-- run_id is nullable: NULL is "not part of a composed run", the state of
-- every execution enqueued before these columns existed and of every
-- execution a single-action policy still produces today. gate defaults to
-- '' (policy.GateNone) for the same reason action_type defaults to
-- non-empty text rather than NULL elsewhere in this table.
-- One ADD COLUMN per statement (not a multi-clause ALTER): the derived-schema
-- extractor (internal/kg/extract/schema) captures only the first ADD of a
-- multi-clause ALTER, which would leave step_index and gate invisible in the
-- knowledge graph. Every migration in this tree uses one ADD per statement.
ALTER TABLE policy_execution ADD COLUMN IF NOT EXISTS run_id     text NULL;
ALTER TABLE policy_execution ADD COLUMN IF NOT EXISTS step_index int  NOT NULL DEFAULT 0;
ALTER TABLE policy_execution ADD COLUMN IF NOT EXISTS gate       text NOT NULL DEFAULT '';

-- One execution row per step of a given run: the uniqueness a "sequence" is
-- allowed to assume (no two workers can independently mint two rows for the
-- same run's same step). Partial on run_id IS NOT NULL so unrelated
-- single-action executions (run_id NULL, step_index 0 by default) never
-- collide with each other or with this constraint.
--
-- Leads with tenant_id even though run_id (a platform-minted ULID, never
-- client-supplied) cannot itself collide across tenants: tenant_id in the
-- key is the schema-derivable route the tenant-key-scope sweep looks for
-- first (entry 1), and costs nothing since the column already exists here.
CREATE UNIQUE INDEX IF NOT EXISTS uq_policy_execution_run_step
    ON policy_execution (tenant_id, run_id, step_index) WHERE run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_policy_execution_run ON policy_execution (run_id) WHERE run_id IS NOT NULL;

COMMENT ON COLUMN policy.steps IS
    'Ordered [{action:{type,config}, gate?:{type,config}}] composition (policy.Sequence). Empty means: use action_type/action_config as the whole response, exactly as before this column existed (ADR-POLICY-001).';
COMMENT ON COLUMN policy_execution.run_id IS
    'Groups the Executions of one composed Sequence run, one row per step_index. NULL for a single-action policy execution (ADR-POLICY-001).';
COMMENT ON COLUMN policy_execution.step_index IS
    'This Execution''s position in its run''s Sequence. Meaningful only when run_id is set.';
COMMENT ON COLUMN policy_execution.gate IS
    'This step''s Decision gate resolution ("" none, "pending", "passed") — policy.GateState. Meaningful only when the Sequence step at step_index names a gate.';

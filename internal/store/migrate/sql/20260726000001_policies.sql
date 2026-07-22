-- PRS-020 Policy Automation — policy registry, execution history, consumer
-- cursor. Additive only: touches no governance, audit, graph, or webhook tables.
-- Policy automation is an event CONSUMER that reads the committed audit log out
-- of band; it never participates in a governance or audit transaction.

CREATE TABLE IF NOT EXISTS policy (
    id            text        PRIMARY KEY,
    name          text        NOT NULL,
    enabled       boolean     NOT NULL DEFAULT true,
    condition     jsonb       NOT NULL DEFAULT '{}',
    action_type   text        NOT NULL,
    action_config jsonb       NOT NULL DEFAULT '{}',
    max_retries   int         NOT NULL DEFAULT 5,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS policy_execution (
    id              text        PRIMARY KEY,
    policy_id       text        NOT NULL,
    event_id        text        NOT NULL,
    event           jsonb       NOT NULL,
    status          text        NOT NULL DEFAULT 'pending',
    retry_count     int         NOT NULL DEFAULT 0,
    error           text        NOT NULL DEFAULT '',
    started_at      timestamptz,
    ended_at        timestamptz,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_policy_execution_due
    ON policy_execution (next_attempt_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX IF NOT EXISTS ix_policy_execution_policy
    ON policy_execution (policy_id, created_at DESC);

CREATE TABLE IF NOT EXISTS policy_cursor (
    chain_id   text        PRIMARY KEY,
    last_seq   bigint      NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

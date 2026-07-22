-- PRS-019 Event Consumption & Replay — replay job tracking. Additive only:
-- touches no governance, audit, or graph tables, and does not alter the webhook
-- tables from PRS-018. Replay operates exclusively on the committed audit log and
-- existing delivery records; it never regenerates events.

CREATE TABLE IF NOT EXISTS webhook_replay_job (
    id              text        PRIMARY KEY,
    webhook_id      text        NOT NULL,
    from_ts         timestamptz,
    to_ts           timestamptz,
    delivery_ids    text[]      NOT NULL DEFAULT '{}',
    status          text        NOT NULL DEFAULT 'pending',
    events_replayed int         NOT NULL DEFAULT 0,
    error           text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_webhook_replay_job_pending
    ON webhook_replay_job (created_at)
    WHERE status = 'pending';

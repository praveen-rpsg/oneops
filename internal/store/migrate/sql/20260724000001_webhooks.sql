-- PRS-018 Event Delivery — webhook registry, delivery status, and relay cursor.
-- Additive only: touches no governance, audit, or graph tables. It adds no
-- constitutional behavior; it is operational infrastructure for external event
-- delivery, which reads the committed audit_event log out of band.

CREATE TABLE IF NOT EXISTS webhook (
    id          text        PRIMARY KEY,
    url         text        NOT NULL,
    secret      text        NOT NULL,
    enabled     boolean     NOT NULL DEFAULT true,
    operations  text[]      NOT NULL DEFAULT '{}',
    resources   text[]      NOT NULL DEFAULT '{}',
    max_retries int         NOT NULL DEFAULT 5,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webhook_delivery (
    id               text        PRIMARY KEY,
    webhook_id       text        NOT NULL,
    chain_id         text        NOT NULL,
    seq              bigint      NOT NULL,
    event_id         text        NOT NULL,
    operation_id     text        NOT NULL,
    operation        text        NOT NULL,
    actor            text        NOT NULL,
    cfg_id           text        NOT NULL,
    occurred_at      timestamptz NOT NULL,
    status           text        NOT NULL DEFAULT 'pending',
    retry_count      int         NOT NULL DEFAULT 0,
    last_status_code int         NOT NULL DEFAULT 0,
    last_attempt     timestamptz,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_webhook_delivery_due
    ON webhook_delivery (next_attempt_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX IF NOT EXISTS ix_webhook_delivery_wh
    ON webhook_delivery (webhook_id, seq);

-- webhook_cursor: the relay's per-chain progress through the committed audit log.
CREATE TABLE IF NOT EXISTS webhook_cursor (
    chain_id   text        PRIMARY KEY,
    last_seq   bigint      NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

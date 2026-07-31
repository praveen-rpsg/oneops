-- OPS-S<notification> Notification Service — persisted, tracked delivery of the
-- platform's own outbound notifications (today: the policy "notification"
-- action; any future producer tomorrow), replacing the fire-and-forget
-- Notifier call in internal/policy/actions.go.
--
-- notification is TENANT-SCOPED, exactly as webhook and webhook_delivery are
-- (20260724000001): tenant_id is denormalised onto the row and is the RLS key.
-- The delivery worker runs on the privileged, cross-tenant connection (it
-- serves every tenant from one process, like the webhook dispatcher), so
-- tenant_id is supplied by the producer at enqueue time and never derived from
-- a bound connection there.
--
-- NO ADMINISTRATIVE AUDIT. ADR-AUDIT-007 §6.2 scopes the administrative-audit
-- chokepoint to five named identity-governance tables. A Notification is
-- neither identity-governance nor a Configuration Object; it follows the same
-- pattern as webhook, policy and team: tenant-owned and under row-level
-- security, without the administrative chokepoint.
--
-- THREE STATUSES, ONE FENCING TOKEN. Unlike webhook_delivery there is no
-- separate "inflight" status: a claim is tracked by claimed_at alone (a lease,
-- mirroring ADR-CONCURRENCY-002), while status stays pending/failed until the
-- worker records a terminal outcome. A permanently exhausted retry budget is
-- expressed by next_attempt_at going to NULL rather than by a fourth status —
-- ClaimDue's `next_attempt_at IS NOT NULL AND next_attempt_at <= now()`
-- predicate then excludes it from every future claim, the same role
-- webhook_delivery's dead_letter status plays.
--
-- status defaults to 'pending', so this table is swept by
-- arch.TestEveryWorkQueue_HasAFencingToken (ADR-CONCURRENCY-007) and is
-- required to carry claimed_at, which it does.

CREATE TABLE IF NOT EXISTS notification (
    notification_id text        PRIMARY KEY,
    tenant_id        text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    channel          text        NOT NULL,
    recipient        text        NOT NULL,
    subject          text        NOT NULL DEFAULT '',
    body             text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'pending',
    attempts         integer     NOT NULL DEFAULT 0,
    last_error       text        NOT NULL DEFAULT '',
    next_attempt_at  timestamptz,
    claimed_at       timestamptz,
    row_version      bigint      NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_notification_channel  CHECK (channel IN ('email', 'webhook', 'inapp')),
    CONSTRAINT ck_notification_status   CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT ck_notification_recipient_length CHECK (char_length(recipient) <= 320),
    CONSTRAINT ck_notification_subject_length   CHECK (char_length(subject) <= 200),
    CONSTRAINT ck_notification_body_length      CHECK (char_length(body) <= 10000)
);

CREATE INDEX IF NOT EXISTS ix_notification_tenant ON notification (tenant_id);

-- ix_notification_claim is the claim query's index: ClaimDue orders due rows by
-- next_attempt_at under FOR UPDATE SKIP LOCKED, restricted to the two
-- claimable statuses — the same shape as webhook_delivery's claim path.
CREATE INDEX IF NOT EXISTS ix_notification_claim
    ON notification (next_attempt_at)
    WHERE status IN ('pending', 'failed');

COMMENT ON TABLE notification IS
    'A tracked, persisted outbound notification with delivery status and retry (ADR-CONCURRENCY-001/002/005/007 applied to the platform''s own notification channel).';
COMMENT ON COLUMN notification.tenant_id IS
    'Supplied by the producer at enqueue time, not derived from a bound connection: the delivery worker is cross-tenant, like the webhook dispatcher.';
COMMENT ON COLUMN notification.next_attempt_at IS
    'NULL means the retry budget is exhausted and this row will never be claimed again — the dead_letter role, without a fourth status value.';
COMMENT ON COLUMN notification.claimed_at IS
    'The fencing token a delivery worker is handed by ClaimDue and must present back to MarkSent/MarkFailed (ADR-CONCURRENCY-005). NULL means unclaimed.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring 20260810000001's team policy).
--
-- current_setting('app.tenant_id', true) returns NULL when unset, and NULL
-- matches no row: fail-closed by construction. USING governs which rows a read
-- may see; WITH CHECK governs which rows a write may create, so a tenant
-- cannot insert a notification labelled with another tenant's id.
ALTER TABLE notification ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON notification;
CREATE POLICY tenant_isolation ON notification
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

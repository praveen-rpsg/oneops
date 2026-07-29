-- The delivery record captures the destination it was actually sent to
-- (ADR-GOV-004).
--
-- webhook_delivery recorded webhook_id and nothing about where the request went.
-- The destination was only obtainable by joining to webhook.url — a mutable
-- field on a row an administrator may PATCH at any time. So the historical
-- record of where governed data was sent was retroactively rewritable: proven
-- live, a delivery that had already been POSTed to one URL read as having gone
-- to a different URL after a single PATCH, with no audit event anywhere.
--
-- A record of a past event must hold the facts as they were, not a pointer into
-- state that can change afterwards. delivered_to is written in the same fenced
-- UPDATE that records the outcome, so it is exactly as trustworthy as the
-- outcome itself.
--
-- NULL means "no attempt has been made yet" — a pending delivery has no
-- destination fact to record, and back-filling one from the current webhook URL
-- would manufacture the very claim this column exists to stop.

ALTER TABLE webhook_delivery ADD COLUMN IF NOT EXISTS delivered_to text;

COMMENT ON COLUMN webhook_delivery.delivered_to IS
    'Destination URL of the most recent attempt, captured at attempt time. NULL until first attempted. Never derived from webhook.url (ADR-GOV-004).';

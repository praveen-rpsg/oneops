-- Reverses 20260801000001_delivery_destination.
-- Dropping the column discards the recorded destinations; after this the
-- historical record is once again only obtainable by joining to the mutable
-- webhook.url (ADR-GOV-004).
ALTER TABLE webhook_delivery DROP COLUMN IF EXISTS delivered_to;

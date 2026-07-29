-- Reverses 20260802000001_delivery_signed_ts. Dropping the column returns the
-- delivery-detail view to minting a fresh timestamp at read time (AR-001).
ALTER TABLE webhook_delivery DROP COLUMN IF EXISTS signed_ts;

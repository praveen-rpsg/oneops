-- A delivery records the timestamp it actually signed (AR-001, instance 3).
--
-- The delivery-detail endpoint rendered a past delivery's headers with
-- DeliveryView(d, wh.Secret, time.Now()) — minting a fresh X-OneOps-Timestamp
-- and signing it with the subscription's *current* secret. The headers shown for
-- a historical delivery were therefore never the headers sent, so an operator
-- could not use them to establish what the receiver should have verified, and
-- after a secret rotation the displayed signature was unrelated to anything that
-- ever crossed the wire.
--
-- AR-001 adopted: snapshot the facts of an act, version the configuration that
-- authorises it. The signed timestamp is a fact of the act — non-secret, small,
-- meaningless anywhere but on this row — so it is snapshotted here. The secret
-- is configuration, and remains out of the record (its versioning is instance 2,
-- scheduled separately).
--
-- NULL means no attempt has been made: there is no signature fact to report, and
-- inventing one is precisely the defect this column closes.

ALTER TABLE webhook_delivery ADD COLUMN IF NOT EXISTS signed_ts bigint;

COMMENT ON COLUMN webhook_delivery.signed_ts IS
    'Unix timestamp actually signed on the most recent attempt, captured at attempt time. NULL until attempted. Never recomputed at read time (AR-001).';

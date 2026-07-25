package events

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// DeliveryID is the deterministic identity of "this event delivered to this
// webhook". It is a pure function of the subscription and the event's position
// in the append-only log, so the same logical delivery always has the same id
// no matter how many times, or by how many workers, it is produced.
//
// This is what makes production idempotent (ADR-CONCURRENCY-003). The relay mints
// a delivery row per (webhook, event); with a random id, a re-processed event —
// after a crash between enqueue and cursor advance, or during a leadership
// overlap when two relays run — became a second row with a new id, a duplicate
// the receiver could not deduplicate. With a content-derived id the second
// production collides on the primary key (ON CONFLICT DO NOTHING) and no second
// row appears; and if a delivery is nonetheless retried, the receiver sees the
// same id and can collapse it. Verified: a cursor reset produced two rows with
// two ids before, and one row after.
//
// chainID and seq identify the committed event uniquely (chain_id is the object,
// seq its position); webhookID identifies the subscriber. Together they are the
// logical delivery.
func DeliveryID(webhookID, chainID string, seq int64) string {
	h := sha256.Sum256([]byte(webhookID + "\x00" + chainID + "\x00" + strconv.FormatInt(seq, 10)))
	return "dlv_" + hex.EncodeToString(h[:16])
}

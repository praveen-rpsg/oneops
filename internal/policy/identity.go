package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// ExecutionID is the deterministic identity of "this policy triggered by this
// event". Like the delivery id (ADR-CONCURRENCY-003), it is a pure function of
// the policy and the event's position in the append-only log, so a re-processed
// event — after a crash between enqueue and cursor advance, or during a
// leadership overlap when two consumers run — collides on the primary key rather
// than becoming a second execution row with a new id, which would run the
// outbound action twice.
//
// cfgID is the chain id (per-object chain) and seq the event's position; policyID
// identifies the automation. Together they are the logical execution.
func ExecutionID(policyID, cfgID string, seq int64) string {
	h := sha256.Sum256([]byte(policyID + "\x00" + cfgID + "\x00" + strconv.FormatInt(seq, 10)))
	return "exec_" + hex.EncodeToString(h[:16])
}

package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// eventIDDomainTag namespaces the deterministic event-id derivation so an event
// id can never collide with any other hashed artifact (mirroring anchorDomainTag
// for anchors). It is versioned so the derivation can evolve without ambiguity.
const eventIDDomainTag = "oneops.audit.event.v1"

// eventIDPrefix labels a derived id for humans and log greppability; it is part
// of the stable id contract.
const eventIDPrefix = "evt_"

// ErrEmptyOperationID is returned when DeriveEventID is given an empty id.
var ErrEmptyOperationID = errors.New("audit: operation_id must not be empty")

// DeriveEventID deterministically derives an audit EventID from an OperationID:
//
//	event_id = "evt_" + hex(sha256( len(tag)‖tag ‖ len(operation_id)‖operation_id ))
//
// using the package's length-prefixed framing so distinct inputs cannot collide
// by field-boundary shifting. It is the single owner of event-id generation:
// per the design, EventID is derived from OperationID and is never supplied by a
// governance or business caller.
//
// The derivation is pure and total — the same OperationID always yields the same
// EventID, and distinct OperationIDs yield distinct EventIDs. Because the store
// enforces UNIQUE (chain_id, event_id), a retried operation that carries the
// same OperationID re-derives the same EventID and is therefore appended at most
// once (ECR-05). DeriveEventID does not hash chain state, allocate sequences, or
// touch persistence.
func DeriveEventID(operationID string) (string, error) {
	if operationID == "" {
		return "", ErrEmptyOperationID
	}
	h := sha256.New()
	writeLenPrefixed(h, []byte(eventIDDomainTag))
	writeLenPrefixed(h, []byte(operationID))
	return eventIDPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

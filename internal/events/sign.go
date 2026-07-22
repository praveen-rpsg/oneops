package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

// Delivery HTTP headers.
const (
	// HeaderSignature carries hex(HMAC-SHA256) over the signed message.
	HeaderSignature = "X-OneOps-Signature"
	// HeaderTimestamp carries the unix seconds used in the signature.
	HeaderTimestamp = "X-OneOps-Timestamp"
	// HeaderDelivery carries the unique delivery id.
	HeaderDelivery = "X-OneOps-Delivery"
	// HeaderEvent carries the event type, e.g. "governance.ratification".
	HeaderEvent = "X-OneOps-Event"
)

// Payload is the JSON body delivered to a webhook. It is self-describing and
// exposes no internal audit implementation (no hashes).
type Payload struct {
	DeliveryID  string    `json:"delivery_id"`
	Event       string    `json:"event"`
	Timestamp   int64     `json:"timestamp"`
	ChainID     string    `json:"chain_id"`
	Seq         int64     `json:"seq"`
	EventID     string    `json:"event_id"`
	OperationID string    `json:"operation_id"`
	Operation   string    `json:"operation"`
	Actor       string    `json:"actor"`
	CfgID       string    `json:"cfg_id"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// EventType returns the canonical event type for an operation.
func EventType(operation string) string { return "governance." + operation }

// BuildPayload constructs the delivery payload bytes and its timestamp.
func BuildPayload(d Delivery, at time.Time) ([]byte, int64, error) {
	ts := at.Unix()
	p := Payload{
		DeliveryID:  d.ID,
		Event:       EventType(d.Event.Operation),
		Timestamp:   ts,
		ChainID:     d.Event.ChainID,
		Seq:         d.Event.Seq,
		EventID:     d.Event.EventID,
		OperationID: d.Event.OperationID,
		Operation:   d.Event.Operation,
		Actor:       d.Event.Actor,
		CfgID:       d.Event.CfgID,
		OccurredAt:  d.Event.OccurredAt,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, 0, err
	}
	return b, ts, nil
}

// Sign computes the delivery signature: hex(HMAC-SHA256(secret,
// "<timestamp>.<deliveryID>." ‖ payload)). Binding the timestamp and delivery id
// into the MACed message prevents replay and cross-delivery reuse.
func Sign(secret string, timestamp int64, deliveryID string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write([]byte(deliveryID))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a delivery signature in constant time. It is exported so
// receivers (and tests) can validate incoming deliveries with the shared secret.
func Verify(secret string, timestamp int64, deliveryID string, payload []byte, signature string) bool {
	expected := Sign(secret, timestamp, deliveryID, payload)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// DeliveryView reconstructs a delivery's canonical payload bytes and the HTTP
// headers it is sent with, for operator inspection. The payload is rebuilt
// verbatim from the committed event fields (no event is reconstructed outside the
// audit log); the signature is computed over it with the webhook secret at the
// given display time.
func DeliveryView(d Delivery, secret string, at time.Time) ([]byte, map[string]string, error) {
	payload, ts, err := BuildPayload(d, at)
	if err != nil {
		return nil, nil, err
	}
	headers := map[string]string{
		"Content-Type":  "application/json",
		HeaderEvent:     EventType(d.Event.Operation),
		HeaderDelivery:  d.ID,
		HeaderTimestamp: strconv.FormatInt(ts, 10),
		HeaderSignature: Sign(secret, ts, d.ID, payload),
	}
	return payload, headers, nil
}

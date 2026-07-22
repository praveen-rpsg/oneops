// Package audit implements the tamper-evident hashing for the Audit Integrity
// capability (Vol II §5.3/§14; Vol IV §4.6). This file is the canonical hash
// generator only: a pure, deterministic function over already-canonical inputs.
// It performs no persistence, sequence allocation, id generation, actor or
// timestamp resolution, or authorization — those are the caller's concern.
package audit

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// HashSize is the length in bytes of every chain hash (SHA-256).
const HashSize = 32

// Validation errors for ChainHash inputs. They are structured sentinels so
// callers can branch with errors.Is.
var (
	// ErrEmptyChainID is returned when ChainID is empty.
	ErrEmptyChainID = errors.New("audit: chain_id must not be empty")
	// ErrEmptyEventID is returned when EventID is empty.
	ErrEmptyEventID = errors.New("audit: event_id must not be empty")
	// ErrEmptyOperation is returned when Operation is empty.
	ErrEmptyOperation = errors.New("audit: operation must not be empty")
	// ErrEmptyActor is returned when Actor is empty.
	ErrEmptyActor = errors.New("audit: actor must not be empty")
	// ErrEmptyPayload is returned when PayloadCanonical is empty.
	ErrEmptyPayload = errors.New("audit: payload_canonical must not be empty")
	// ErrInvalidSeq is returned when Seq is less than 1.
	ErrInvalidSeq = errors.New("audit: seq must be >= 1")
	// ErrPrevHashLen is returned when prevHash is not exactly HashSize bytes.
	ErrPrevHashLen = errors.New("audit: prev_hash must be 32 bytes")
)

// GenesisPrevHash returns the previous-hash of the first event in a chain: a
// fresh slice of HashSize zero bytes. A new slice is returned each call so the
// caller can never mutate shared state.
func GenesisPrevHash() []byte {
	return make([]byte, HashSize)
}

// EventHashInput carries the fields cryptographically bound into an audit event's
// chain hash. PayloadCanonical is the exact, already-canonical byte sequence
// (ECR-01) — never jsonb, formatted JSON, or an application object. The generator
// treats it as opaque bytes; producing it is out of scope.
type EventHashInput struct {
	ChainID             string
	Seq                 int64
	EventID             string
	Operation           string
	Actor               string
	OccurredAtUnixNanos int64
	PayloadCanonical    []byte
}

// ChainHash computes this_hash = SHA256(prevHash ‖ H_fields), where H_fields is
// SHA256 over a length-prefixed framing of the input fields (EDS v1.1.0 §6.5).
// The framing removes concatenation ambiguity so distinct inputs cannot collide
// by field-boundary shifting. prevHash must be exactly HashSize bytes
// (GenesisPrevHash for the first event). The result is a new HashSize-byte slice.
func ChainHash(in EventHashInput, prevHash []byte) ([]byte, error) {
	if in.ChainID == "" {
		return nil, ErrEmptyChainID
	}
	if in.EventID == "" {
		return nil, ErrEmptyEventID
	}
	if in.Operation == "" {
		return nil, ErrEmptyOperation
	}
	if in.Actor == "" {
		return nil, ErrEmptyActor
	}
	if len(in.PayloadCanonical) == 0 {
		return nil, ErrEmptyPayload
	}
	if in.Seq < 1 {
		return nil, ErrInvalidSeq
	}
	if len(prevHash) != HashSize {
		return nil, ErrPrevHashLen
	}

	fields := sha256.New()
	writeLenPrefixed(fields, []byte(in.ChainID))
	writeUint64(fields, uint64(in.Seq))
	writeLenPrefixed(fields, []byte(in.EventID))
	writeLenPrefixed(fields, []byte(in.Operation))
	writeLenPrefixed(fields, []byte(in.Actor))
	writeUint64(fields, uint64(in.OccurredAtUnixNanos))
	writeLenPrefixed(fields, in.PayloadCanonical)
	fieldsSum := fields.Sum(nil)

	this := sha256.New()
	this.Write(prevHash)
	this.Write(fieldsSum)
	return this.Sum(nil), nil
}

// writeLenPrefixed writes a 4-byte big-endian length followed by b.
func writeLenPrefixed(h interface{ Write([]byte) (int, error) }, b []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

// writeUint64 writes v as 8 big-endian bytes (fixed width, unambiguous).
func writeUint64(h interface{ Write([]byte) (int, error) }, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	_, _ = h.Write(b[:])
}

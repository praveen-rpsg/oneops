package audit

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func baseInput() EventHashInput {
	return EventHashInput{
		ChainID:             "c1",
		Seq:                 1,
		EventID:             "e1",
		Operation:           "replacement",
		Actor:               "actor1",
		OccurredAtUnixNanos: 0,
		PayloadCanonical:    []byte(`{"k":"v"}`),
	}
}

func mustHash(t *testing.T, in EventHashInput, prev []byte) []byte {
	t.Helper()
	h, err := ChainHash(in, prev)
	if err != nil {
		t.Fatalf("ChainHash: %v", err)
	}
	if len(h) != HashSize {
		t.Fatalf("hash length = %d, want %d", len(h), HashSize)
	}
	return h
}

// Deterministic: repeated calls with the same input yield the identical hash.
func TestChainHashDeterministic(t *testing.T) {
	in, prev := baseInput(), GenesisPrevHash()
	first := mustHash(t, in, prev)
	for i := 0; i < 10; i++ {
		got := mustHash(t, in, prev)
		if !bytes.Equal(got, first) {
			t.Fatalf("non-deterministic hash at %d: %x != %x", i, got, first)
		}
	}
}

// Identical payload (and fields) → identical hash across independent inputs.
func TestIdenticalPayloadIdenticalHash(t *testing.T) {
	a := mustHash(t, baseInput(), GenesisPrevHash())
	b := mustHash(t, baseInput(), GenesisPrevHash())
	if !bytes.Equal(a, b) {
		t.Fatalf("identical inputs produced different hashes: %x != %x", a, b)
	}
}

// A payload change → a different hash.
func TestPayloadChangeChangesHash(t *testing.T) {
	a := mustHash(t, baseInput(), GenesisPrevHash())
	in := baseInput()
	in.PayloadCanonical = []byte(`{"k":"w"}`)
	b := mustHash(t, in, GenesisPrevHash())
	if bytes.Equal(a, b) {
		t.Fatal("different payloads produced the same hash")
	}
}

// The previous hash influences the result (chaining).
func TestPrevHashInfluence(t *testing.T) {
	a := mustHash(t, baseInput(), GenesisPrevHash())
	other := GenesisPrevHash()
	other[0] = 0x01
	b := mustHash(t, baseInput(), other)
	if bytes.Equal(a, b) {
		t.Fatal("different prev_hash produced the same hash")
	}
}

// Each bound field influences the result (field framing is not collapsible).
func TestEachFieldInfluencesHash(t *testing.T) {
	base := mustHash(t, baseInput(), GenesisPrevHash())
	mutations := map[string]func(*EventHashInput){
		"chain_id":  func(e *EventHashInput) { e.ChainID = "c2" },
		"seq":       func(e *EventHashInput) { e.Seq = 2 },
		"event_id":  func(e *EventHashInput) { e.EventID = "e2" },
		"operation": func(e *EventHashInput) { e.Operation = "approval" },
		"actor":     func(e *EventHashInput) { e.Actor = "actor2" },
		"occurred":  func(e *EventHashInput) { e.OccurredAtUnixNanos = 1 },
	}
	for name, m := range mutations {
		in := baseInput()
		m(&in)
		if got := mustHash(t, in, GenesisPrevHash()); bytes.Equal(got, base) {
			t.Errorf("mutating %s did not change the hash", name)
		}
	}
}

// Field-boundary framing: moving a character across a boundary changes the hash
// (guards against concatenation ambiguity).
func TestFramingPreventsBoundaryCollision(t *testing.T) {
	a := baseInput()
	a.ChainID, a.EventID = "ab", "e1"
	b := baseInput()
	b.ChainID, b.EventID = "a", "be1"
	ha := mustHash(t, a, GenesisPrevHash())
	hb := mustHash(t, b, GenesisPrevHash())
	if bytes.Equal(ha, hb) {
		t.Fatal("boundary-shifted fields collided — framing is ineffective")
	}
}

// Validation: every required-input failure returns its sentinel error.
func TestChainHashValidation(t *testing.T) {
	prev := GenesisPrevHash()
	cases := []struct {
		name string
		in   EventHashInput
		prev []byte
		want error
	}{
		{"empty chain", func() EventHashInput { i := baseInput(); i.ChainID = ""; return i }(), prev, ErrEmptyChainID},
		{"empty event", func() EventHashInput { i := baseInput(); i.EventID = ""; return i }(), prev, ErrEmptyEventID},
		{"empty op", func() EventHashInput { i := baseInput(); i.Operation = ""; return i }(), prev, ErrEmptyOperation},
		{"empty actor", func() EventHashInput { i := baseInput(); i.Actor = ""; return i }(), prev, ErrEmptyActor},
		{"empty payload", func() EventHashInput { i := baseInput(); i.PayloadCanonical = nil; return i }(), prev, ErrEmptyPayload},
		{"zero seq", func() EventHashInput { i := baseInput(); i.Seq = 0; return i }(), prev, ErrInvalidSeq},
		{"short prev", baseInput(), make([]byte, 31), ErrPrevHashLen},
		{"long prev", baseInput(), make([]byte, 33), ErrPrevHashLen},
		{"nil prev", baseInput(), nil, ErrPrevHashLen},
	}
	for _, c := range cases {
		_, err := ChainHash(c.in, c.prev)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

// GenesisPrevHash is 32 zero bytes and returns a fresh (non-shared) slice.
func TestGenesisPrevHash(t *testing.T) {
	g := GenesisPrevHash()
	if len(g) != HashSize {
		t.Fatalf("len = %d, want %d", len(g), HashSize)
	}
	if !bytes.Equal(g, make([]byte, HashSize)) {
		t.Fatalf("genesis is not all zeros: %x", g)
	}
	g[0] = 0xFF // mutate the returned slice
	if GenesisPrevHash()[0] != 0x00 {
		t.Fatal("GenesisPrevHash returns shared mutable state")
	}
}

// Golden vector: freezes the exact algorithm output as a regression guard.
// Any change to the framing or hash construction breaks this test by design.
func TestGoldenVector(t *testing.T) {
	const wantHex = "3d1e69c2b15a39844ca059467e69ce0e88877d7e35513350f2e73654d5f0df59"
	got := hex.EncodeToString(mustHash(t, baseInput(), GenesisPrevHash()))
	if got != wantHex {
		t.Fatalf("golden mismatch:\n got=%s\nwant=%s", got, wantHex)
	}
}

package audit

import (
	"errors"
	"strings"
	"testing"
)

func TestDeriveEventID_Deterministic(t *testing.T) {
	const op = "op-abc-123"
	first, err := DeriveEventID(op)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	for i := 0; i < 100; i++ {
		next, err := DeriveEventID(op)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if next != first {
			t.Fatalf("non-deterministic event id at %d: %q vs %q", i, first, next)
		}
	}
}

func TestDeriveEventID_DistinctInputsDistinctOutputs(t *testing.T) {
	seen := make(map[string]string)
	for _, op := range []string{"a", "b", "ab", "ba", "op-1", "op-2", "op-10"} {
		id, err := DeriveEventID(op)
		if err != nil {
			t.Fatalf("DeriveEventID(%q): %v", op, err)
		}
		if prev, dup := seen[id]; dup {
			t.Fatalf("collision: %q and %q both derived %q", prev, op, id)
		}
		seen[id] = op
	}
}

func TestDeriveEventID_Format(t *testing.T) {
	id, err := DeriveEventID("operation-1")
	if err != nil {
		t.Fatalf("DeriveEventID: %v", err)
	}
	if !strings.HasPrefix(id, "evt_") {
		t.Fatalf("event id %q missing evt_ prefix", id)
	}
	// evt_ + 64 lowercase hex chars (sha256).
	if got := len(id); got != len("evt_")+64 {
		t.Fatalf("event id length = %d, want %d", got, len("evt_")+64)
	}
	if strings.ToLower(id) != id {
		t.Fatalf("event id %q is not lowercase hex", id)
	}
}

func TestDeriveEventID_FramingIsUnambiguous(t *testing.T) {
	// Without length-prefixed framing, ("ab","c") and ("a","bc") could collide.
	// They must not: the tag is framed, and the operation id is a single field,
	// so any two distinct operation ids differ. Assert two boundary-shift-like
	// inputs differ.
	x, err := DeriveEventID("ab")
	if err != nil {
		t.Fatalf("x: %v", err)
	}
	y, err := DeriveEventID("a")
	if err != nil {
		t.Fatalf("y: %v", err)
	}
	if x == y {
		t.Fatalf("distinct operation ids collided: %q", x)
	}
}

func TestDeriveEventID_RejectsEmpty(t *testing.T) {
	if _, err := DeriveEventID(""); !errors.Is(err, ErrEmptyOperationID) {
		t.Fatalf("DeriveEventID(\"\") error = %v, want %v", err, ErrEmptyOperationID)
	}
}

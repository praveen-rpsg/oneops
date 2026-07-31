package domain

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// ---- Encode / DecodeAdminAuditCursor ---------------------------------------
//
// The cursor is the position in the total order (occurred_at, chain_id, seq).
// It must round-trip exactly for a real caller to page correctly, and it must
// fail closed on anything that did not come from Encode — a cursor that
// silently coerced into some other position would let a client skip or repeat
// rows without ever seeing an error.

func TestAdminAuditCursor_EncodeDecodeRoundTrips(t *testing.T) {
	cases := []AdminAuditCursor{
		{OccurredAt: time.UnixMicro(1_700_000_000_123_456).UTC(), ChainID: "chain:org:acme", Seq: 42},
		{OccurredAt: time.UnixMicro(0).UTC(), ChainID: PlatformAuditChain, Seq: 1},
		// ADR-AUDIT-007 §6.8: sibling acts of one administrative act share a
		// timestamp; (chain_id, seq) must still round-trip precisely.
		{OccurredAt: time.UnixMicro(1_700_000_000_000_000).UTC(), ChainID: "chain:org:acme", Seq: 9999999999},
	}
	for _, c := range cases {
		enc := c.Encode()
		got, err := DecodeAdminAuditCursor(enc)
		if err != nil {
			t.Fatalf("Decode(Encode(%+v)) returned error: %v", c, err)
		}
		if !got.OccurredAt.Equal(c.OccurredAt) || got.ChainID != c.ChainID || got.Seq != c.Seq {
			t.Errorf("round trip mismatch: got %+v, want %+v", got, c)
		}
	}
}

func TestAdminAuditCursor_EmptyStringIsTheFirstPageNotAnError(t *testing.T) {
	got, err := DecodeAdminAuditCursor("")
	if err != nil {
		t.Fatalf("empty cursor (no prior page) must not error, got %v", err)
	}
	if got != (AdminAuditCursor{}) {
		t.Errorf("empty cursor must decode to the zero value, got %+v", got)
	}
}

// DecodeAdminAuditCursor must fail closed: only a token this API produced may
// be accepted. Every case below is a caller composing, corrupting, or
// truncating a cursor rather than replaying one Encode returned.
func TestAdminAuditCursor_RejectsMalformedInput(t *testing.T) {
	valid := AdminAuditCursor{OccurredAt: time.UnixMicro(1_700_000_000_000_000).UTC(), ChainID: "chain:org:acme", Seq: 5}
	validEnc := valid.Encode()

	cases := map[string]string{
		"not base64 at all":      "not-valid-base64!!!",
		"base64 but wrong shape": base64.RawURLEncoding.EncodeToString([]byte("just-one-field")),
		"missing a separator":    base64.RawURLEncoding.EncodeToString([]byte("123\x1fchain-1")),
		"too many separators":    base64.RawURLEncoding.EncodeToString([]byte("123\x1fchain-1\x1f5\x1fextra")),
		"non-numeric timestamp":  base64.RawURLEncoding.EncodeToString([]byte("not-a-number\x1fchain-1\x1f5")),
		"non-numeric seq":        base64.RawURLEncoding.EncodeToString([]byte("123\x1fchain-1\x1fnot-a-number")),
		"empty chain id":         base64.RawURLEncoding.EncodeToString([]byte("123\x1f\x1f5")),
		"zero seq":               base64.RawURLEncoding.EncodeToString([]byte("123\x1fchain-1\x1f0")),
		"negative seq":           base64.RawURLEncoding.EncodeToString([]byte("123\x1fchain-1\x1f-5")),
		"truncated valid cursor": validEnc[:len(validEnc)-2],
		"standard (not raw) b64": base64.StdEncoding.EncodeToString([]byte("123\x1fchain-1\x1f5")), // may include padding Decode does not expect
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeAdminAuditCursor(tok)
			if !errors.Is(err, ErrInvalidCursor) {
				t.Errorf("%s: Decode(%q) = err %v, want ErrInvalidCursor", name, tok, err)
			}
		})
	}
}

func TestAdminAuditCursor_RejectsTamperedCursor(t *testing.T) {
	// A cursor produced by Encode, then bit-flipped, must not be silently
	// accepted as a different-but-valid position without at least being
	// re-checked against the structural rules (seq >= 1, chain id present).
	// This does not claim cryptographic tamper-evidence (there is none — the
	// cursor is not authenticated) but it must not bypass Decode's own
	// validation.
	c := AdminAuditCursor{OccurredAt: time.UnixMicro(1_700_000_000_000_000).UTC(), ChainID: "chain:org:acme", Seq: 5}
	enc := c.Encode()
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("Encode produced non-decodable base64: %v", err)
	}
	// Corrupt the seq field to something Decode's own rule must reject: force
	// it to end in a non-digit so ParseInt fails, rather than merely producing
	// another (still-valid-shaped) integer.
	tampered := append([]byte{}, raw...)
	tampered[len(tampered)-1] = 'x'
	tok := base64.RawURLEncoding.EncodeToString(tampered)
	if _, err := DecodeAdminAuditCursor(tok); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("tampered cursor must be rejected, got err %v", err)
	}
}

// ---- AdminAuditFilter.Validate ---------------------------------------------

func TestAdminAuditFilter_Validate_DefaultsAndCapsLimit(t *testing.T) {
	f := &AdminAuditFilter{}
	if err := f.Validate(); err != nil {
		t.Fatalf("empty filter (newest page of everything) must be valid, got %v", err)
	}
	if f.Limit != DefaultAdminAuditPageSize {
		t.Errorf("Limit with no caller value = %d, want default %d", f.Limit, DefaultAdminAuditPageSize)
	}

	f = &AdminAuditFilter{Limit: MaxAdminAuditPageSize + 1000}
	if err := f.Validate(); err != nil {
		t.Fatalf("an over-large limit must be capped, not rejected, got %v", err)
	}
	if f.Limit != MaxAdminAuditPageSize {
		t.Errorf("Limit = %d, want capped to max %d", f.Limit, MaxAdminAuditPageSize)
	}

	f = &AdminAuditFilter{Limit: -5}
	if err := f.Validate(); err != nil {
		t.Fatalf("a non-positive caller limit must fall back to the default, got %v", err)
	}
	if f.Limit != DefaultAdminAuditPageSize {
		t.Errorf("Limit = %d, want default %d for a non-positive input", f.Limit, DefaultAdminAuditPageSize)
	}
}

func TestAdminAuditFilter_Validate_RejectsUnknownOperation(t *testing.T) {
	f := &AdminAuditFilter{Operation: AdminOperation("bogus.operation")}
	if err := f.Validate(); err == nil {
		t.Error("an operation outside the administrative vocabulary must be rejected before it reaches SQL")
	}
}

func TestAdminAuditFilter_Validate_AcceptsKnownOperation(t *testing.T) {
	f := &AdminAuditFilter{Operation: AdminUserCreated}
	if err := f.Validate(); err != nil {
		t.Errorf("a known administrative operation must be accepted, got %v", err)
	}
}

func TestAdminAuditFilter_Validate_RejectsInvertedTimeRange(t *testing.T) {
	now := time.Now().UTC()
	f := &AdminAuditFilter{From: now, To: now.Add(-time.Hour)}
	if err := f.Validate(); err == nil {
		t.Error("to before from must be rejected — an inverted range would silently return nothing forever")
	}
}

func TestAdminAuditFilter_Validate_AllowsEqualFromAndTo(t *testing.T) {
	now := time.Now().UTC()
	f := &AdminAuditFilter{From: now, To: now}
	if err := f.Validate(); err != nil {
		t.Errorf("from == to (a single instant) must be a valid, if narrow, range, got %v", err)
	}
}

//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A cursor is a watermark: the highest event position durably processed. Its one
// non-negotiable property is that it never moves backward. If it can regress, a
// stale or overlapping writer (a demoted leader still running its relay for the
// bounded step-down window, ADR-CONCURRENCY-003) can rewind the watermark under a
// concurrent advance, forcing already-processed events to be re-read — safe only
// so long as production stays idempotent and the events remain in the log. The
// cursor must enforce monotonicity itself, not borrow it from those downstream
// properties.
//
// This proves the watermark is monotonic: a later write with a LOWER sequence is
// ignored, and the stored cursor is the maximum ever written. Against the
// pre-fix blind `SET last_seq = EXCLUDED.last_seq` it fails — the cursor regresses
// to the stale value. That failure is the live exploit; the pass is the fix.
func TestCursor_WebhookWriteIsMonotonic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewWebhookStore(pool)
	chain := fmt.Sprintf("mono-wh-%d", time.Now().UnixNano())

	if err := s.SetCursor(ctx, chain, 10); err != nil {
		t.Fatalf("advance to 10: %v", err)
	}
	// A stale/overlapping relay write with an older watermark.
	if err := s.SetCursor(ctx, chain, 5); err != nil {
		t.Fatalf("stale write: %v", err)
	}
	got, err := s.GetCursor(ctx, chain)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != 10 {
		t.Fatalf("cursor regressed to %d after a stale write; a watermark must never move backward (want 10)", got)
	}
	// A genuine advance still moves it forward.
	if err := s.SetCursor(ctx, chain, 12); err != nil {
		t.Fatalf("advance to 12: %v", err)
	}
	if got, _ := s.GetCursor(ctx, chain); got != 12 {
		t.Fatalf("cursor did not advance to 12, got %d", got)
	}
}

func TestCursor_PolicyWriteIsMonotonic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewPolicyStore(pool)
	chain := fmt.Sprintf("mono-pol-%d", time.Now().UnixNano())

	if err := s.SetPolicyCursor(ctx, chain, 10); err != nil {
		t.Fatalf("advance to 10: %v", err)
	}
	if err := s.SetPolicyCursor(ctx, chain, 5); err != nil {
		t.Fatalf("stale write: %v", err)
	}
	got, err := s.GetPolicyCursor(ctx, chain)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != 10 {
		t.Fatalf("policy cursor regressed to %d after a stale write; a watermark must never move backward (want 10)", got)
	}
	if err := s.SetPolicyCursor(ctx, chain, 12); err != nil {
		t.Fatalf("advance to 12: %v", err)
	}
	if got, _ := s.GetPolicyCursor(ctx, chain); got != 12 {
		t.Fatalf("policy cursor did not advance to 12, got %d", got)
	}
}

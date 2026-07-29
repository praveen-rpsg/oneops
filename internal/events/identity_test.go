package events

import "testing"

// TestDeliveryID_DeterministicAndIdempotent is the unit-level guarantee behind
// ADR-CONCURRENCY-003: the delivery id is a pure function of (webhook, chain,
// seq), so re-processing an event — after a crash before the cursor advanced, or
// during a leadership overlap — mints the SAME id and collides on the primary
// key rather than becoming a duplicate row with a new id that no receiver could
// deduplicate.
func TestDeliveryID_DeterministicAndIdempotent(t *testing.T) {
	// Same logical delivery, produced twice (e.g. two relays during an overlap).
	a := DeliveryID("wh_1", "chain_9", 42)
	b := DeliveryID("wh_1", "chain_9", 42)
	if a != b {
		t.Fatalf("same (webhook,chain,seq) produced different ids: %q vs %q — production is not idempotent", a, b)
	}
	if a[:4] != "dlv_" {
		t.Fatalf("delivery id lacks dlv_ prefix: %q", a)
	}

	// Any coordinate change is a different logical delivery and must not collide.
	for _, d := range []struct {
		name   string
		wh, ch string
		seq    int64
	}{
		{"different webhook", "wh_2", "chain_9", 42},
		{"different chain", "wh_1", "chain_8", 42},
		{"different seq", "wh_1", "chain_9", 43},
	} {
		if got := DeliveryID(d.wh, d.ch, d.seq); got == a {
			t.Errorf("%s collided with the base id %q — distinct deliveries must be distinct", d.name, a)
		}
	}

	// The separator must be unambiguous: distinct field splits must not alias.
	if DeliveryID("a", "bc", 1) == DeliveryID("ab", "c", 1) {
		t.Error("field boundary is ambiguous: (a,bc) aliased (ab,c)")
	}
}

package arch

import (
	"strings"
	"testing"
)

// A record of a past event must hold the facts as they were, not a pointer into
// state that can change afterwards.
//
// `webhook_delivery` recorded `webhook_id` and nothing about where the request
// went, so "where was this event actually sent?" was answerable only by joining
// to `webhook.url` — a field an administrator may PATCH at any time. Proven live
// against the running service: a delivery already POSTed to
// `http://127.0.0.1:9911/approved` read as having gone to
// `http://127.0.0.1:9911/attacker-controlled` after a single PATCH returned 200,
// with no audit event anywhere. The destination of every historical delivery
// through a subscription was retroactively rewritable (ADR-GOV-004).
func TestDelivery_RecordsItsOwnDestination(t *testing.T) {
	src := stripComments(readFile(t, "../events/events.go"))
	if !strings.Contains(src, "DeliveredTo") {
		t.Fatal("events.Delivery carries no recorded destination — where a delivery was sent " +
			"would again be derivable only from the webhook's current URL (ADR-GOV-004)")
	}

	ports := stripComments(readFile(t, "../events/ports.go"))
	if !strings.Contains(ports, "facts AttemptFacts") {
		t.Error("DeliveryStore.MarkResult does not take the attempt's facts, so they cannot be " +
			"captured in the same write as the outcome (ADR-GOV-004, AR-001)")
	}
	if !strings.Contains(src, "SignedTS") {
		t.Error("events.Delivery does not record the timestamp it signed — the delivery view " +
			"would mint a fresh one and show headers that were never sent (AR-001)")
	}
}

// The destination must be written in the same fenced UPDATE that records the
// outcome. A second write would be a second failure point: an outcome could be
// recorded with the destination missing, or fenced out while the destination
// landed.
func TestDeliveryDestination_IsWrittenWithTheOutcome(t *testing.T) {
	body := methodBody(t, "../store/postgres/webhook_store.go", "MarkResult")

	if !strings.Contains(body, "delivered_to") {
		t.Fatal("WebhookStore.MarkResult does not write delivered_to — the destination would not " +
			"be recorded at all (ADR-GOV-004)")
	}
	// One statement: the destination is set by the same UPDATE as the outcome.
	if strings.Count(body, "UPDATE webhook_delivery") != 1 {
		t.Errorf("MarkResult issues %d UPDATE statements; the destination must be recorded in the "+
			"same fenced write as the outcome, not a second one (ADR-GOV-004)",
			strings.Count(body, "UPDATE webhook_delivery"))
	}
	// An outcome reached without an attempt must not erase or invent a destination.
	if !strings.Contains(body, "COALESCE($9, signed_ts)") {
		t.Error("MarkResult overwrites signed_ts unconditionally — an outcome reached without an " +
			"attempt would erase the timestamp a real attempt signed (AR-001)")
	}
	if !strings.Contains(body, "COALESCE($8, delivered_to)") {
		t.Error("MarkResult overwrites delivered_to unconditionally — an outcome reached without " +
			"an attempt (a refused delivery) would erase the record of where a previous attempt " +
			"actually went (ADR-GOV-004)")
	}
}

// The dispatcher must record the URL it actually used. Passing anything else —
// or nothing — on an attempted outcome puts a claim in the record that the
// platform did not observe.
func TestDispatcher_RecordsTheURLItPosted(t *testing.T) {
	body := stripComments(methodBody(t, "../events/dispatcher.go", "attempt"))

	if !strings.Contains(body, "Destination: wh.URL") {
		t.Error("the dispatcher does not record the URL it posted to, so the recorded destination " +
			"would not be the one used (ADR-GOV-004)")
	}
	if !strings.Contains(body, "SignedTS: ts") {
		t.Error("the dispatcher does not record the timestamp it signed, so historical headers " +
			"would again be minted at read time (AR-001)")
	}
	// The delivered outcome specifically must carry the facts: the successful
	// case is precisely the one an investigator needs.
	if !strings.Contains(body, "StatusDelivered, del.RetryCount, code, now, time.Time{}, AttemptFacts{Destination: wh.URL, SignedTS: ts}") {
		t.Error("a successful delivery does not record the facts of its attempt (ADR-GOV-004, AR-001)")
	}
}

// Reading the destination back by joining to the webhook re-opens the class: the
// answer would once again be the subscription's *current* URL rather than the
// one used.
func TestDeliveryReads_DoNotDeriveDestinationFromTheWebhook(t *testing.T) {
	for _, file := range []string{
		"../store/postgres/webhook_store.go",
		"../store/postgres/webhook_consume_store.go",
	} {
		src := stripComments(readFile(t, file))
		// A join from webhook_delivery to webhook is only legitimate for the
		// retry budget (ClaimDue). Selecting the URL through one is not.
		if strings.Contains(src, "w.url") || strings.Contains(src, "webhook.url") {
			t.Errorf("%s reads a webhook URL alongside deliveries — the destination of a past "+
				"delivery must come from the delivery row, not from the subscription's current "+
				"state (ADR-GOV-004)", file)
		}
	}
}

// The delivery view must render headers from the recorded timestamp. Taking a
// caller-supplied "now" is how the defect arose: every caller passed
// time.Now(), so the headers shown for a past delivery were freshly minted and
// had never been sent (AR-001).
func TestDeliveryView_DoesNotMintATimestamp(t *testing.T) {
	src := stripComments(readFile(t, "../events/sign.go"))
	if strings.Contains(src, "func DeliveryView(d Delivery, secret string, at time.Time)") {
		t.Error("DeliveryView still accepts a timestamp from its caller — it must render from the " +
			"timestamp the delivery recorded (AR-001)")
	}
	if !strings.Contains(src, "d.SignedTS") {
		t.Error("DeliveryView does not read the recorded signing timestamp (AR-001)")
	}
	if !strings.Contains(src, "ErrNotAttempted") {
		t.Error("DeliveryView does not distinguish an unattempted delivery — it would render " +
			"headers for a request that was never sent (AR-001)")
	}
}

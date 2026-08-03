package collector

import (
	"context"
	"io"
	"net/http"
	"time"
)

// httpCheckResult is the outcome of one HTTP uptime/synthetic check. Up
// reports whether a complete HTTP response was received within the check's
// bounded timeout — reachability, not a judgement of the response's status
// code. A 500 or a 404 still counts as "up": the target answered. StatusCode
// is meaningful only when HasStatusCode is true (a response was actually
// received); a transport failure, a timeout, or an SSRF-refused dial all
// leave it unset, deliberately: fabricating a sentinel code would claim a
// fact the check never observed, and treating a blocked dial differently
// from an ordinary network failure would leak, via the recorded telemetry,
// which private addresses the guard is refusing (defense in depth alongside
// safehttp's own opaque BlockedError).
type httpCheckResult struct {
	Up            bool
	LatencyMs     float64
	StatusCode    int
	HasStatusCode bool
}

// runHTTPCheck performs one bounded GET against target through doer — which
// callers MUST build via safehttp.Client so the request cannot reach a
// non-public address (ADR-SECURITY-001; this package has no client of its
// own, see HTTPDoer's doc comment). ctx carries the per-check timeout
// (context.WithTimeout, set by the caller): a hanging target still returns
// here once ctx's deadline fires, so it cannot stall the scheduler.
func runHTTPCheck(ctx context.Context, doer HTTPDoer, target string) httpCheckResult {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		// A malformed target should have been refused at check-creation time
		// (the transport layer's format validation) — this is a defensive
		// fallback, not the primary defense, and it reports exactly what
		// happened: no response was received.
		return httpCheckResult{Up: false, LatencyMs: elapsedMs(start)}
	}
	resp, err := doer.Do(req)
	if err != nil {
		// Connection refused, DNS failure, TLS error, context deadline
		// exceeded, or safehttp's own BlockedError (SSRF refusal) — every
		// one of these is "no response", recorded identically as up=0. This
		// is the "failed/timed-out check records up=0, not an error that
		// drops the sample" rule.
		return httpCheckResult{Up: false, LatencyMs: elapsedMs(start)}
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain so the underlying connection can be reused by the pooled
	// transport, the same courtesy notification.Worker.deliverWebhook pays.
	_, _ = io.Copy(io.Discard, resp.Body)
	return httpCheckResult{
		Up: true, LatencyMs: elapsedMs(start),
		StatusCode: resp.StatusCode, HasStatusCode: true,
	}
}

func elapsedMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}

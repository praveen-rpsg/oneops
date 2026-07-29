// Package arch — build-failing architecture tests.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// methodBody returns the source text of a method on a receiver, or "" if absent.
func methodBody(t *testing.T, file, name string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(raw)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var body string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Recv == nil {
			return true
		}
		body = src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset]
		return false
	})
	if body == "" {
		t.Fatalf("%s: method %s not found", file, name)
	}
	return body
}

// The retry budget must be enforced by the claim, not by the worker.
//
// A worker only consults the budget after an attempt it survived. A row whose
// attempt kills the worker — OOM, node loss, SIGKILL, or a demotion that cancels
// the outcome write — is reclaimed by the lease, and if the reclaim does not
// charge the attempt, the budget never depletes and the row is redelivered
// forever with no terminating state. Proven live: 6 crash cycles, retry_count 0,
// still inflight (ADR-CONCURRENCY-006).
//
// So ClaimDue must (a) advance retry_count as it hands the row out, and (b)
// dead-letter a row whose next attempt would exceed its budget, in the same
// statement. Moving that decision back into the worker reopens the class.
func TestClaimDue_ChargesTheAttemptAndBoundsIt(t *testing.T) {
	cases := []struct{ file, fn, budgetSrc string }{
		{"../store/postgres/webhook_store.go", "ClaimDue", "webhook"},
		{"../store/postgres/policy_store.go", "ClaimDue", "policy"},
	}
	for _, c := range cases {
		t.Run(c.budgetSrc, func(t *testing.T) {
			body := methodBody(t, c.file, c.fn)

			if !strings.Contains(body, "retry_count = c.attempt_no") {
				t.Errorf("%s.%s does not advance retry_count on the claim — an attempt whose worker "+
					"never reports back would consume no budget, and the row would be retried "+
					"forever (ADR-CONCURRENCY-006)", c.file, c.fn)
			}
			if !strings.Contains(body, "'dead_letter'") {
				t.Errorf("%s.%s has no dead-letter transition — the claim is the only place a row "+
					"whose worker keeps dying can be terminated (ADR-CONCURRENCY-006)", c.file, c.fn)
			}
			if !strings.Contains(body, "max_retries") {
				t.Errorf("%s.%s does not read max_retries — the claim must enforce the same budget "+
					"the worker does, or the two disagree (ADR-CONCURRENCY-006)", c.file, c.fn)
			}
			// A missing subscriber/policy means no budget: the row can never succeed
			// and must terminate rather than loop. COALESCE(..., 0) is that rule.
			if !strings.Contains(body, "COALESCE") {
				t.Errorf("%s.%s does not defend against a missing budget source — an orphaned row "+
					"(subscriber deleted) would get a NULL budget and never terminate "+
					"(ADR-CONCURRENCY-006)", c.file, c.fn)
			}
		})
	}
}

// An outcome the platform has already produced must not be written through a
// context that a demotion or shutdown cancels.
//
// ADR-CONCURRENCY-003 made demotion routine: losing the advisory lock cancels
// the leadership context. The workers wrote their outcome with that same
// context, so a delivery in flight across a demotion POSTed to the subscriber
// and then recorded nothing — leaving the row claimed for reclaim and re-send.
// Proven live. The outcome write must use a context detached from the worker's
// cancellation (ADR-CONCURRENCY-006).
func TestWorkerOutcomeWrites_AreNotCancelledByDemotion(t *testing.T) {
	cases := []struct{ file, fn string }{
		{"../events/dispatcher.go", "attempt"},
		{"../policy/executor.go", "attempt"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			body := methodBody(t, c.file, c.fn)

			if !strings.Contains(body, "outcomeContext(ctx)") {
				t.Fatalf("%s.%s does not derive a detached outcome context — an outcome produced in "+
					"the outside world would be lost when the worker is stopped "+
					"(ADR-CONCURRENCY-006)", c.file, c.fn)
			}
			// Every MarkResult in the attempt path must go through the detached
			// context. A single `MarkResult(ctx,` re-opens the hole.
			if strings.Contains(body, "MarkResult(ctx,") {
				t.Errorf("%s.%s records an outcome with the worker's cancellable context — "+
					"a demotion mid-attempt loses that outcome and the row is re-sent with no "+
					"budget depletion; use the detached outcome context (ADR-CONCURRENCY-006)",
					c.file, c.fn)
			}
		})
	}
}

// The detached outcome context must drop only cancellation, and must carry its
// own deadline: an unbounded write would let a stuck database hold a demotion or
// shutdown open forever, which is a different liveness bug.
func TestOutcomeContext_IsDetachedAndBounded(t *testing.T) {
	for _, file := range []string{"../events/dispatcher.go", "../policy/executor.go"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			src := string(raw)
			if !strings.Contains(src, "context.WithoutCancel(ctx)") {
				t.Errorf("%s: outcomeContext must be built with context.WithoutCancel(ctx) so it keeps "+
					"the worker's values but not its cancellation (ADR-CONCURRENCY-006)", file)
			}
			if !strings.Contains(src, "context.WithTimeout(context.WithoutCancel(ctx)") {
				t.Errorf("%s: the detached outcome context must carry its own deadline, or a stuck "+
					"database holds shutdown open indefinitely (ADR-CONCURRENCY-006)", file)
			}
		})
	}
}

// A worker stopped between claiming and attempting must give the attempt back.
//
// The claim charges the attempt up front, so a batch claimed and then abandoned
// on shutdown would burn budget that was never spent — a few restarts would
// dead-letter healthy rows that were never sent. Releasing the unused claim is
// the honest counterpart, and it must run on the detached context because the
// worker's is already cancelled.
func TestWorkers_ReleaseUnusedClaimsOnStop(t *testing.T) {
	cases := []struct{ file, fn string }{
		{"../events/dispatcher.go", "RunOnce"},
		{"../policy/executor.go", "RunOnce"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			body := methodBody(t, c.file, c.fn)
			if !strings.Contains(body, "ReleaseClaim") {
				t.Errorf("%s.%s does not release claims it will not attempt — the claim already "+
					"charged each row an attempt, so abandoning the batch burns budget that was "+
					"never spent (ADR-CONCURRENCY-006)", c.file, c.fn)
			}
			if !strings.Contains(body, "outcomeContext(ctx)") {
				t.Errorf("%s.%s releases claims on the worker's cancelled context, which cannot "+
					"succeed — use the detached context (ADR-CONCURRENCY-006)", c.file, c.fn)
			}
		})
	}
}

package schema

import (
	"context"
	"testing"
	"time"
)

// §III budgets E3 at under 200ms.
//
// Asserted, not assumed. The statement scanner's first draft consulted a
// compiled regex at every byte of the corpus to find dollar-quote delimiters
// and took 906ms — four and a half times the budget, with every correctness
// test passing.
func TestExtractionIsWithinBudget(t *testing.T) {
	// A wall-clock budget is not measured under the race detector: the
	// instrumented timing is a property of the instrumentation, not the
	// extractor, and asserting a 200ms budget there flakes red under CI's
	// `-race` load for no real signal — the same reasoning, and the same
	// raceEnabled gate, the graph performance gates use
	// (internal/store/postgres/graph_perf_test.go). BenchmarkExtract below
	// tracks real throughput; a non-race `go test` still enforces the budget.
	if raceEnabled {
		t.Skip("extraction budget is not measured under -race (instrumented timing is not the extractor's; see BenchmarkExtract)")
	}
	if _, _, err := (Extractor{}).Extract(context.Background(), repoRoot); err != nil {
		t.Fatalf("warm: %v", err)
	}
	// Best-of-N: take the FASTEST of a few runs. A single measurement is
	// hostage to a coincidental scheduler stall on a loaded machine; the
	// minimum reflects the extractor's actual cost, so a real regression still
	// fails every run while transient load does not.
	const runs = 5
	best := time.Duration(1<<63 - 1)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if _, _, err := (Extractor{}).Extract(context.Background(), repoRoot); err != nil {
			t.Fatalf("extract: %v", err)
		}
		if elapsed := time.Since(start); elapsed < best {
			best = elapsed
		}
	}
	if best > 200*time.Millisecond {
		t.Errorf("extraction took %v (best of %d), over §III's 200ms budget", best, runs)
	} else {
		t.Logf("extraction took %v (best of %d)", best, runs)
	}
}

func BenchmarkExtract(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, _, err := (Extractor{}).Extract(context.Background(), repoRoot); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBlankNonCode(b *testing.B) {
	var src []byte
	for i := 0; i < 2000; i++ {
		src = append(src, []byte("CREATE INDEX IF NOT EXISTS ix_a ON t (c); -- note\n")...)
	}
	s := string(src)
	b.SetBytes(int64(len(s)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		blankNonCode(s)
	}
}

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
	if _, _, err := (Extractor{}).Extract(context.Background(), repoRoot); err != nil {
		t.Fatalf("warm: %v", err)
	}
	start := time.Now()
	if _, _, err := (Extractor{}).Extract(context.Background(), repoRoot); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("extraction took %v, over §III's 200ms budget", elapsed)
	} else {
		t.Logf("extraction took %v", elapsed)
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

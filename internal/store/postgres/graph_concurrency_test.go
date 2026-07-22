//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// canon renders a traversal result as a stable string for equality checks.
func canon(nodes []domain.TraversalNode) string {
	var b strings.Builder
	for _, n := range nodes {
		fmt.Fprintf(&b, "%s:%d;", n.CfgID, n.Depth)
	}
	return b.String()
}

// TestGraphConcurrentReaders runs mixed recursive/non-recursive reads from many
// goroutines and verifies determinism, absence of errors/deadlocks, and that the
// connection pool returns to zero acquired connections (no leak). Run under the
// race detector via `go test -race`.
func TestGraphConcurrentReaders(t *testing.T) {
	pool := graphPool(t)
	const n = 10000
	genBlocks(t, pool, n)
	repo := NewGraphRepo(pool)

	// Establish the expected (deterministic) result for each block root.
	roots := make([]string, 0, n/10)
	want := map[string]string{}
	ctx := context.Background()
	for i := 1; i <= n; i += 10 {
		root := "n" + itoa(i)
		roots = append(roots, root)
		res, err := repo.RecursiveDependencies(ctx, root)
		if err != nil {
			t.Fatalf("seed expectation %s: %v", root, err)
		}
		want[root] = canon(res)
	}

	for _, readers := range []int{10, 50, 100} {
		t.Run(fmt.Sprintf("readers-%d", readers), func(t *testing.T) {
			var wg sync.WaitGroup
			errCh := make(chan error, readers)
			for w := 0; w < readers; w++ {
				wg.Add(1)
				go func(seed int) {
					defer wg.Done()
					for k := 0; k < 20; k++ {
						root := roots[(seed+k)%len(roots)]
						switch k % 3 {
						case 0: // recursive dependencies
							res, err := repo.RecursiveDependencies(ctx, root)
							if err != nil {
								errCh <- err
								return
							}
							if canon(res) != want[root] {
								errCh <- fmt.Errorf("non-deterministic result for %s", root)
								return
							}
						case 1: // recursive dependents
							if _, err := repo.RecursiveDependents(ctx, root); err != nil {
								errCh <- err
								return
							}
						default: // direct (non-recursive)
							if _, err := repo.Dependencies(ctx, root); err != nil {
								errCh <- err
								return
							}
						}
					}
				}(w)
			}
			wg.Wait()
			close(errCh)
			for err := range errCh {
				t.Fatalf("concurrent reader error: %v", err)
			}
		})
	}

	// No connection leak: after all readers finish, acquired connections drain.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Stat().AcquiredConns() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if acq := pool.Stat().AcquiredConns(); acq != 0 {
		t.Errorf("connection leak: %d connections still acquired", acq)
	}
}

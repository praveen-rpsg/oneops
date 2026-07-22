//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// benchTraversal seeds a shape once, then benchmarks RecursiveDependencies from
// the given root. ReportAllocs surfaces allocs/op and B/op under -benchmem.
func benchTraversal(b *testing.B, seed func(testing.TB, *pgxpool.Pool) (int, int), root string) {
	pool := graphPool(b)
	seed(b, pool)
	repo := NewGraphRepo(pool)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.RecursiveDependencies(ctx, root); err != nil {
			b.Fatalf("traverse: %v", err)
		}
	}
}

func BenchmarkGraphLinear(b *testing.B) {
	benchTraversal(b, func(tb testing.TB, p *pgxpool.Pool) (int, int) { return genLinear(tb, p, 800) }, "n1")
}

func BenchmarkGraphDeep(b *testing.B) {
	benchTraversal(b, func(tb testing.TB, p *pgxpool.Pool) (int, int) { return genLinear(tb, p, 1500) }, "n1")
}

func BenchmarkGraphWide(b *testing.B) {
	benchTraversal(b, func(tb testing.TB, p *pgxpool.Pool) (int, int) { return genWide(tb, p, 4000) }, "n1")
}

func BenchmarkGraphDiamond(b *testing.B) {
	benchTraversal(b, func(tb testing.TB, p *pgxpool.Pool) (int, int) { return genLattice(tb, p, 30, 3) }, "n1")
}

func BenchmarkGraphDense(b *testing.B) {
	benchTraversal(b, func(tb testing.TB, p *pgxpool.Pool) (int, int) { return genDenseDAG(tb, p, 14, 5) }, "n1")
}

func BenchmarkGraphDisconnected(b *testing.B) {
	benchTraversal(b, func(tb testing.TB, p *pgxpool.Pool) (int, int) { return genBlocks(tb, p, 10000) }, "n1")
}

func BenchmarkGraphScale10k(b *testing.B) {
	pool := graphPool(b)
	genBlocks(b, pool, 10000)
	repo := NewGraphRepo(pool)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root := "n" + itoa((i%1000)*10+1)
		if _, err := repo.RecursiveDependencies(ctx, root); err != nil {
			b.Fatalf("traverse: %v", err)
		}
	}
}

func BenchmarkGraphScale50k(b *testing.B) {
	pool := graphPool(b)
	genBlocks(b, pool, 50000)
	repo := NewGraphRepo(pool)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root := "n" + itoa((i%5000)*10+1)
		if _, err := repo.RecursiveDependencies(ctx, root); err != nil {
			b.Fatalf("traverse: %v", err)
		}
	}
}

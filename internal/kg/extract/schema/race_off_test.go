//go:build !race

package schema

// raceEnabled reports whether the race detector is compiled in. The
// extraction-budget test (schema_bench_test.go) reads it to skip its wall-clock
// assertion under `-race`, the same convention the graph performance gates use
// (internal/store/postgres/graph_perf_test.go + race_off_test.go): latency
// measured under race instrumentation is a property of the instrumentation, not
// of the extractor, so asserting a millisecond budget there turns a green tree
// red under CI's `-race` load for no signal.
const raceEnabled = false

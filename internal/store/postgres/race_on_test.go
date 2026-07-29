//go:build integration && race

package postgres

// raceEnabled reports whether the binary was built with the race detector.
//
// Latency measured under race instrumentation is not a measurement of the
// system: the instrumented build is several times slower and contends on its own
// shadow state. A performance acceptance gate that runs in that build reports a
// number it cannot justify — and it did, failing the whole suite twice for
// environmental reasons while passing standalone, which teaches everyone to
// ignore a red suite. The gate is skipped there rather than loosened.
const raceEnabled = true

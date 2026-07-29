//go:build integration && !race

package postgres

// raceEnabled reports whether the binary was built with the race detector.
// See race_on_test.go for why the performance gate consults it.
const raceEnabled = false

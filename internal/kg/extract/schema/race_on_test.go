//go:build race

package schema

// raceEnabled is true when the test binary is built with `-race`. See
// race_off_test.go for why the extraction-budget assertion skips in that case.
const raceEnabled = true

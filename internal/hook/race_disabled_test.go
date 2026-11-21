//go:build !race

package hook_test

// raceDetector reports whether this test binary runs under the race detector
// (race_enabled_test.go).
const raceDetector = false

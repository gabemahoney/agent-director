package api

import "time"

// SetPauseTestKnobs lets pause_test override the polling cadence and
// sleeper without exporting them broadly. Tests pair this with
// t.Cleanup to restore production defaults.
func SetPauseTestKnobs(interval time.Duration, sleeper func(time.Duration)) {
	pausePollInterval = interval
	pauseSleep = sleeper
}

// FindMissing exposes the unexported findMissingImpl for white-box unit tests
// in package api_test. External callers use (c *Client).FindMissing instead.
var FindMissing = findMissingImpl

// Resume exposes the unexported resumeImpl for white-box unit tests in
// package api_test. External callers use (c *Client).Resume instead.
var Resume = resumeImpl

// ExpandTildeForTest exposes expandTilde for the b.6k1 regression test.
var ExpandTildeForTest = expandTilde

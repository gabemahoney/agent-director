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

// RepairTranscript exposes the unexported repairTranscriptImpl for white-box
// unit tests in package api_test (b.v2c AC7). External callers use
// (c *Client).RepairTranscript instead.
var RepairTranscript = repairTranscriptImpl

// ExpandTildeForTest exposes expandTilde for the b.6k1 regression test.
var ExpandTildeForTest = expandTilde

// GuardErrorEval re-exports the guardError sentinel string so package api_test
// can assert the exact guard_evaluation value recorded when the relay guard's
// store read fails, without hardcoding the literal in the test.
const GuardErrorEval = guardError

// EvaluateRelayGuardForTest exposes the unexported evaluateRelayGuard so
// package api_test can assert the guard-evaluation outcome string (in
// particular guardError) that Client.SendKeys records on ad.send_keys.called —
// a value the pure exported SendKeys discards. It flattens the unexported
// sendKeysGuard result into (eval, refuse, err) so the test needs no access to
// the struct's unexported fields.
func EvaluateRelayGuardForTest(s SendKeysStore, effectiveWindow time.Duration, now time.Time, row Spawn, instanceID string) (eval string, refuse bool, err error) {
	g, err := evaluateRelayGuard(s, effectiveWindow, now, row, instanceID)
	return g.eval, g.refuse, err
}

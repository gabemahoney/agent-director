package hook

import "time"

// SetNowFunc overrides the wall-clock seam Poll uses for deadline
// math and returns a restorer the caller MUST defer. Tests use this
// alongside an advancingClock to drive Poll on virtual time so a 1s
// timeout case (and friends) costs zero real wall-clock.
func SetNowFunc(f func() time.Time) func() {
	prev := nowFunc
	nowFunc = f
	return func() { nowFunc = prev }
}

// ExtractSessionID exposes extractSessionID to the external test package.
var ExtractSessionID = extractSessionID

// SessionStartWaitCap exposes sessionStartWaitCap, the longest SessionStart
// waits for its launch's identity write (WD 2026-09-30c), to the external
// test package.
const SessionStartWaitCap = sessionStartWaitCap

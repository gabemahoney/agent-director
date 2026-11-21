package cursorhost

import "time"

// testWaitBudget gives b.dcj's test-side progress waits race-detector headroom.
// Test-side progress waits must reuse this helper.
// Product deadlines, asserted timing bounds and negative waits must not use it.
func testWaitBudget(d time.Duration) time.Duration {
	return d * testWaitScale
}

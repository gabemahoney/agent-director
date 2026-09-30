package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
)

// SpawnWithCollisionReader runs the spawn verb path on c (its store for the
// insert, its tmux client and its config) with collisions in place of c's
// store for the collision pre-check. It exists for the pre-check
// store-failure tests: a test passes a failing wrapper of
// spawn.CollisionChecker to show that a failed read returns ErrInternal, not
// ErrInstanceIdCollision, with no inline SQL and no store tricks (SR-9.3,
// SR-20.2). External callers use (c *Client).Spawn instead.
func SpawnWithCollisionReader(c *Client, collisions spawn.CollisionChecker, params SpawnParams) (SpawnResult, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnResult{}, err
	}
	return runSpawn(c.st, collisions, c.tmuxClient, c.procChecker, c.cfg, c.now, c.logger, params)
}

// SetClockForTest replaces c's clock (time.Now in production) for this Client only (Appendix F.5).
func SetClockForTest(c *Client, now func() time.Time) { c.now = now }

// SetProcCheckerForTest replaces c's start-time reader (SR-3.8) for this Client only.
func SetProcCheckerForTest(c *Client, pc ProcChecker) { c.procChecker = pc }

// SetPauseTestKnobs lets pause_test override the polling cadence and
// sleeper without exporting them broadly. Tests pair this with
// t.Cleanup to restore production defaults.
func SetPauseTestKnobs(interval time.Duration, sleeper func(time.Duration)) {
	pausePollInterval = interval
	pauseSleep = sleeper
}

// TmuxClientOf returns the tmux client c was built with, so package api_test
// can drive the production client api.New wired from the [tmux] config.
func TmuxClientOf(c *Client) TmuxClient { return c.tmuxClient }

// FindMissing exposes the unexported findMissingImpl for white-box unit tests
// in package api_test. External callers use (c *Client).FindMissing instead.
var FindMissing = findMissingImpl

// Resume exposes the unexported resumeImpl for white-box unit tests in
// package api_test. External callers use (c *Client).Resume instead.
var Resume = resumeImpl

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

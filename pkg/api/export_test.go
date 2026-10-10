package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
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

// ReuseStore is the reuse path's store surface (reuseStore), so package
// api_test can wrap the store to inject failures and interleavings.
type ReuseStore = reuseStore

// SpawnWithReuseStore runs the spawn verb path on c, as Client.Spawn does,
// with rs as the reuse path's store (its pre-check read, change, restore and
// identity write). External callers use (c *Client).Spawn instead.
func SpawnWithReuseStore(c *Client, rs ReuseStore, params SpawnParams) (SpawnResult, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnResult{}, err
	}
	return runSpawnWithReuseStore(c.st, c.st, rs, c.tmuxClient, c.procChecker, c.cfg, c.now, c.logger, params)
}

// SetClockForTest replaces c's clock (time.Now in production) for this Client only (Appendix F.5).
func SetClockForTest(c *Client, now func() time.Time) { c.now = now }

// SetProcCheckerForTest replaces c's start-time reader (SR-3.8) for this Client only.
func SetProcCheckerForTest(c *Client, pc ProcChecker) { c.procChecker = pc }

// SetSleepForTest replaces c's sleep (time.Sleep in production), the pause of Client.Kill's process wait (SR-6.1)
// and of Client.Decide's waits for its verdict's ack (b.146 rule 16) and for a pre-v7 request's relay hook (b.pzy),
// for this Client only.
func SetSleepForTest(c *Client, sleep func(time.Duration)) { c.sleep = sleep }

// SetSelfPIDNSForTest replaces c's reader of its own pid namespace (probe.SelfPIDNamespace in production), the
// namespace a recorded relay hook is judged in (b.146 rule 14), for this Client only.
func SetSelfPIDNSForTest(c *Client, read func() (string, bool)) { c.selfPIDNS = read }

// SetPaneSelfForTest replaces c's reader of its own identity, which a pane answer records as its sender (b.146
// rule 8, problem 2), for this Client only.
func SetPaneSelfForTest(c *Client, self func() ProcessIdentity) { c.paneSelf = self }

// DecideWithSleep is Decide with the sleep of its waits given (its verdict's
// ack, b.146 rule 16; a pre-v7 request's relay hook, b.pzy), so a test stands
// in for the hook on its own clock. External callers use Decide or
// (c *Client).Decide instead.
var DecideWithSleep = decide

// CreatedAtResolution is the storage resolution of a permission request's
// created_at (createdAtResolution), which relayGuardHold adds to both the send-keys guard's release and
// Decide's wait (b.pzy, b.z6g), so no test spells it.
const CreatedAtResolution = createdAtResolution

// KillPollInterval is kill's poll interval (killPollInterval, SR-6.1), so no test spells it.
const KillPollInterval = killPollInterval

// SetPauseTestKnobs lets pause_test override the polling cadence and
// sleeper without exporting them broadly. Tests pair this with
// t.Cleanup to restore production defaults.
func SetPauseTestKnobs(interval time.Duration, sleeper func(time.Duration)) {
	pausePollInterval = interval
	pauseSleep = sleeper
}

// PauseTestKnobs returns the pause wait's current poll interval and sleeper,
// so a SetPauseTestKnobs caller restores them without spelling the default.
func PauseTestKnobs() (time.Duration, func(time.Duration)) { return pausePollInterval, pauseSleep }

// KillFinished and DeleteRows expose the operator-only actions that only
// agent-director-admin reaches, through internal/adminapi (b.vqr).
var (
	KillFinished = killFinished
	DeleteRows   = deleteRows
)

// TmuxClientOf returns the tmux client c was built with, so package api_test
// can drive the production client api.New wired from the [tmux] config.
func TmuxClientOf(c *Client) TmuxClient { return c.tmuxClient }

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
// sendKeysGuard result into (eval, refusal, err) so the test needs no access
// to the struct's unexported fields; hold is the pane-answer intent hold.
func EvaluateRelayGuardForTest(s SendKeysStore, v RelayView, hold time.Duration, row Spawn, params SendKeysParams) (eval string, refusal error, err error) {
	g, err := evaluateRelayGuard(s, newRelayJudge(v), hold, row, params)
	return g.eval, g.refusal, err
}

// PaneSHA256 exposes paneSHA256, the hash read-pane returns and send-keys and record-pane-answer compare (b.146
// rule 7), so no test spells the algorithm.
var PaneSHA256 = paneSHA256

// StartingSessionLimits and StartingSessionRow expose the starting-session
// rule's inputs (starting_session.go) to package api_test.
type (
	StartingSessionLimits = startingSessionLimits
	StartingSessionRow    = startingSessionRow
)

// StartingSessionLimitsOf exposes startingSessionLimitsOf, the bound and window
// read through config.Tmux's accessors.
var StartingSessionLimitsOf = startingSessionLimitsOf

// StartingSessionCheck exposes a classified row's classification and its
// refusal builders to package api_test.
type StartingSessionCheck struct{ c startingSessionCheck }

// CheckStartingSession runs checkStartingSession.
func CheckStartingSession(lim StartingSessionLimits, now time.Time, row StartingSessionRow, session *tmux.Session) StartingSessionCheck {
	return StartingSessionCheck{checkStartingSession(lim, now, row, session)}
}

// Class returns the rule's classification.
func (s StartingSessionCheck) Class() tmux.StartingSessionClass { return s.c.class }

// Refusal returns the refusal for every outcome.
func (s StartingSessionCheck) Refusal() error { return s.c.refusal() }

// UnavailableError returns steps 1 and 2's error, nil past both.
func (s StartingSessionCheck) UnavailableError() error { return s.c.unavailableError() }

// OwnIDConflictError returns step 3's own-id conflict.
func (s StartingSessionCheck) OwnIDConflictError() error { return s.c.ownIDConflictError() }

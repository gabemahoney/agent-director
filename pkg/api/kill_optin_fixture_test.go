package api_test

// kill_optin_fixture_test.go extends the kill fixture (kill_fixture_test.go)
// for kill's operator-only finished-row opt-in (SR-6.5): Kill and Client.Kill
// with the opt-in set, at e.cfg's durations or at a bound and window passed
// straight to Kill. The finished row itself is the shared startingRow
// (starting_row_fixture_test.go). It holds no tests.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killOptIn runs api.Kill as e.kill does, with the finished-row opt-in set.
func (e *killEnv) killOptIn(id string) (api.KillResult, error) {
	return e.killOptInWith(id, e.cfg.EffectiveStartingSession(), e.cfg.EffectiveStoppingWindow())
}

// killOptInWith runs api.Kill with the opt-in set, passing bound and window
// to Kill as its starting-session bound and stopping window; the kill exit
// wait is e.cfg's.
func (e *killEnv) killOptInWith(id string, bound, window time.Duration) (api.KillResult, error) {
	return api.Kill(e.store, e.rec, e.pc, bound, window, e.cfg.EffectiveKillExitWait(), e.clock.Now, e.sleep,
		api.KillParams{ClaudeInstanceID: id, IncludeFinished: true})
}

// killOptInClient runs Client.Kill with the opt-in set on a Client from
// e.client(t, settings...), returning its captured log.
func (e *killEnv) killOptInClient(t *testing.T, id string, settings ...apitest.TmuxSetting) (res api.KillResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.Kill(api.KillParams{ClaudeInstanceID: id, IncludeFinished: true})
	return res, buf.String(), err
}

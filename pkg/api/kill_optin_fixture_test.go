package api_test

// kill_optin_fixture_test.go extends the kill fixture (kill_fixture_test.go)
// for kill's operator-only finished-row opt-in (SR-6.5), which only
// agent-director-admin's kill-finished runs (b.vqr): the unexported killFinished
// (through export_test.go), at e.cfg's durations or at a bound and window
// passed straight to it, and the Client path through internal/adminapi. The finished row itself is the shared startingRow
// (starting_row_fixture_test.go). It holds no tests.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killOptIn runs killFinished as e.kill runs Kill, at e.cfg's durations.
func (e *killEnv) killOptIn(id string) (api.KillResult, error) {
	return e.killOptInWith(id, e.cfg.EffectiveStartingSession(), e.cfg.EffectiveStoppingWindow())
}

// killOptInWith runs killFinished, passing bound and window as its
// starting-session bound and stopping window; the kill exit wait is e.cfg's.
func (e *killEnv) killOptInWith(id string, bound, window time.Duration) (api.KillResult, error) {
	return api.KillFinished(e.store, e.rec, e.pc, bound, window, e.cfg.EffectiveKillExitWait(), e.clock.Now, e.sleep, id)
}

// killOptInClient runs adminapi.KillFinished on a Client from
// e.client(t, settings...), returning its captured log.
func (e *killEnv) killOptInClient(t *testing.T, id string, settings ...apitest.TmuxSetting) (res api.KillResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	r, err := adminapi.KillFinished(c, id)
	return api.KillResult{KillSent: r.KillSent}, buf.String(), err
}

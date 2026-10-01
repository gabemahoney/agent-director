package api_test

// kill_optin_fixture_test.go extends the kill fixture (kill_fixture_test.go)
// for kill's operator-only finished-row opt-in (SR-6.5): Kill and Client.Kill
// with the opt-in set. It holds no tests.

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killOptIn runs api.Kill as e.kill does, with the finished-row opt-in set.
func (e *killEnv) killOptIn(id string) (api.KillResult, error) {
	return api.Kill(e.store, e.rec, e.pc, e.cfg.EffectiveStartingSession(), e.cfg.EffectiveStoppingWindow(),
		e.cfg.EffectiveKillExitWait(), e.clock.Now, e.sleep, api.KillParams{ClaudeInstanceID: id, IncludeFinished: true})
}

// killOptInClient runs Client.Kill with the opt-in set on a Client from
// e.client(t, settings...), returning its captured log.
func (e *killEnv) killOptInClient(t *testing.T, id string, settings ...apitest.TmuxSetting) (res api.KillResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.Kill(api.KillParams{ClaudeInstanceID: id, IncludeFinished: true})
	return res, buf.String(), err
}

package api_test

// kill_optin_config_test.go: the stopping window and the starting-session
// bound in kill's finished-row opt-in (AC-CFG-02, SR-4.2, SR-20.6), as
// configured through api.New and Client.Kill, and as passed straight to the
// exported api.Kill. Descriptions carry the effective values. The runner is
// kill_optin_table_test.go's.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kocProbe is one row probing bound and window: ended window-1 s ago with an
// old session (stopping), ended the window ago with a session bound-1 s old
// (starting) or the bound old (killed when created before ended_at, else
// never reported in).
type kocProbe struct {
	name string
	row  startingRow
	want kftWant
}

// kocProbes are bound's and window's probes on an ended row.
func kocProbes(bound, window time.Duration) []kocProbe {
	atBound := kftKilled
	if bound <= window {
		atBound = kftNeverReportedIn
	}
	return []kocProbe{
		{"ended window-1 s ago, old session",
			startingRow{state: store.StateEnded, endedAgo: window - time.Second, age: window + bound}, kftStopping},
		{"ended the window ago, session bound-1 s old",
			startingRow{state: store.StateEnded, endedAgo: window, age: bound - time.Second}, kftStarting},
		{"ended the window ago, session the bound old",
			startingRow{state: store.StateEnded, endedAgo: window, age: bound}, atBound},
	}
}

// TestKillIncludeFinishedClientSettings: through api.New, the window and the
// bound at their safe minimums decide at value-1 s and value, each leaving the other at its default.
func TestKillIncludeFinishedClientSettings(t *testing.T) {
	t.Parallel()
	minB, minW := int64(config.MinStartingSessionSeconds), int64(config.MinStoppingWindowSeconds)
	cases := []struct {
		name          string
		settings      []apitest.TmuxSetting
		bound, window time.Duration
	}{
		{"window at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)},
			defBound, secs(minW)},
		{"bound at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB)},
			secs(minB), defWindow},
		{"both at their safe minimums", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB),
			apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)}, secs(minB), secs(minW)},
	}
	for _, tc := range cases {
		for _, p := range kocProbes(tc.bound, tc.window) {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				kftOurs(t, p.row, p.want, tc.bound, tc.window, func(e *killEnv, r resumeRow) (api.KillResult, error) {
					res, logs, err := e.killOptInClient(t, r.ID, tc.settings...)
					if logs != "" {
						t.Errorf("Client log = %q; want none", logs)
					}
					return res, err
				})
			})
		}
	}
}

// TestKillIncludeFinishedPassedDurations: api.Kill applies the bound and
// window it is given, not the configured defaults, below and above them.
func TestKillIncludeFinishedPassedDurations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		bound, window time.Duration
	}{
		{"safe minimums", secs(config.MinStartingSessionSeconds), secs(config.MinStoppingWindowSeconds)},
		{"above the defaults", defWindow + defBound, defBound},
	}
	for _, tc := range cases {
		for _, p := range kocProbes(tc.bound, tc.window) {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				kftOurs(t, p.row, p.want, tc.bound, tc.window, func(e *killEnv, r resumeRow) (api.KillResult, error) {
					return e.killOptInWith(r.ID, tc.bound, tc.window)
				})
			})
		}
	}
}

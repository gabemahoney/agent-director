package api_test

// starting_session_test.go tests the shared starting-session refusal
// (starting_session.go) through its export_test seam: both settings take
// effect when loaded through internal/config (AC-CFG-02, AC-RES-05), each
// outcome's error and description, and that steps 1 and 2 are decided without
// building the own-id conflict. The rule's pure boundaries are tested in
// internal/tmux; the verbs' use of it in their own tests.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// startingClockStart is where the tests' virtual clock starts.
var startingClockStart = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// secs is n whole seconds.
func secs(n int64) time.Duration { return time.Duration(n) * time.Second }

// loadStartingLimits loads a config holding settings through internal/config
// and returns its limits; no settings writes a config with no [tmux] table.
func loadStartingLimits(t *testing.T, settings ...apitest.TmuxSetting) api.StartingSessionLimits {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if len(settings) == 0 {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	} else {
		apitest.WriteTmuxConfig(t, path, settings...)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return api.StartingSessionLimitsOf(cfg.Tmux)
}

// startingFacts is a row and its lookup as the rule sees them, relative to
// the clock: ended endedAgo before now (unless noEndedAt), recording a pid
// and a session id as set, with a session created age before now (unless
// noSession, Gone while the agent process runs).
type startingFacts struct {
	endedAgo  time.Duration
	noEndedAt bool
	pid, sid  bool
	noSession bool
	age       time.Duration
}

// checkFacts classifies f under lim at the virtual clock's instant, the row
// named name.
func checkFacts(lim api.StartingSessionLimits, name string, f startingFacts) api.StartingSessionCheck {
	now := tmuxfix.NewClock(startingClockStart).Now()
	row := api.StartingSessionRow{InstanceID: "agent-ss", Name: name, RecordsPID: f.pid, RecordsSessionID: f.sid}
	if !f.noEndedAt {
		ended := now.Add(-f.endedAgo)
		row.EndedAt = &ended
	}
	var session *tmux.Session
	if !f.noSession {
		session = &tmux.Session{ID: "$3", Name: name, Created: now.Add(-f.age).Unix()}
	}
	return api.CheckStartingSession(lim, now, row, session)
}

// escapedName is a name from the tmuxfix catalogue whose Go quoted form
// escapes a printable byte (a backslash), so the description's quoting shows.
func escapedName(t *testing.T) string {
	t.Helper()
	for _, n := range tmuxfix.StoredNames() {
		if strconv.CanBackquote(n.Raw) && strconv.Quote(n.Raw) != `"`+n.Raw+`"` {
			return n.Raw
		}
	}
	t.Fatal("tmuxfix.StoredNames has no name needing escapes")
	return ""
}

// TestStartingSessionSettings: the configured bound and window decide at
// value-1 and value, each independent of the other, and an absent or 0
// setting uses the default (AC-CFG-02, AC-RES-05).
func TestStartingSessionSettings(t *testing.T) {
	defB, defW := int64(config.DefaultStartingSessionSeconds), int64(config.DefaultStoppingWindowSeconds)
	minB, minW := int64(config.MinStartingSessionSeconds), int64(config.MinStoppingWindowSeconds)
	cases := []struct {
		name     string
		settings []apitest.TmuxSetting
		bound    int64
		window   int64
	}{
		{"no [tmux] table", nil, defB, defW},
		{"keys set to 0", []apitest.TmuxSetting{
			apitest.TmuxInt(config.TmuxStartingSessionSeconds, 0), apitest.TmuxInt(config.TmuxStoppingWindowSeconds, 0)}, defB, defW},
		{"bound at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB)}, minB, defW},
		{"window at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)}, defB, minW},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lim := loadStartingLimits(t, tc.settings...)
			bound, window := secs(tc.bound), secs(tc.window)
			// old outlives every bound tested, so only the window decides.
			old := secs(defB + 1)
			probes := []struct {
				name string
				f    startingFacts
				want tmux.StartingSessionOutcome
			}{
				{"window-1 s", startingFacts{endedAgo: window - time.Second, pid: true, age: old}, tmux.StillStopping},
				{"window", startingFacts{endedAgo: window, pid: true, age: old}, tmux.PastBoth},
				{"bound-1 s", startingFacts{noEndedAt: true, pid: true, age: bound - time.Second}, tmux.StillStarting},
				{"bound", startingFacts{noEndedAt: true, pid: true, age: bound}, tmux.PastBoth},
			}
			for _, p := range probes {
				t.Run(p.name, func(t *testing.T) {
					c := checkFacts(lim, "ss-name", p.f)
					if got := c.Class().Outcome; got != p.want {
						t.Fatalf("outcome = %v; want %v", got, p.want)
					}
					ss := apitest.StartingSession{InstanceID: "agent-ss", Name: "ss-name", Window: window, Bound: bound,
						WindowChecked: !p.f.noEndedAt}
					desc := map[tmux.StartingSessionOutcome]apitest.DescCase{
						tmux.StillStopping: apitest.DescStillStopping(ss),
						tmux.StillStarting: apitest.DescStillStarting(ss),
						tmux.PastBoth:      apitest.DescOwnOldSession(ss),
					}[p.want]
					apitest.AssertDescription(t, c.Refusal().Error(), desc)
				})
			}
		})
	}
}

// TestStartingSessionRefusals: each outcome's error matches exactly one
// sentinel and its description case; Refusal gives the step's own error, and
// past both UnavailableError is nil, so steps 1 and 2 never build the own-id
// conflict.
func TestStartingSessionRefusals(t *testing.T) {
	minB, minW := int64(config.MinStartingSessionSeconds), int64(config.MinStoppingWindowSeconds)
	lim := loadStartingLimits(t,
		apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB), apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW))
	bound, window := secs(minB), secs(minW)
	name := escapedName(t)
	young, old := bound-time.Second, bound
	inside, outside := window-time.Second, window
	ss := func(noSession, windowChecked, sid bool) apitest.StartingSession {
		return apitest.StartingSession{InstanceID: "agent-ss", Name: name, Window: window, Bound: bound,
			NoSession: noSession, WindowChecked: windowChecked, SessionID: sid}
	}
	cases := []struct {
		name string
		f    startingFacts
		want string
		desc apitest.DescCase
	}{
		{"stopping, Ours", startingFacts{endedAgo: inside, pid: true, sid: true, age: old},
			"ErrTmuxUnresponsive", apitest.DescStillStopping(ss(false, true, true))},
		{"stopping, Gone", startingFacts{endedAgo: inside, pid: true, noSession: true},
			"ErrTmuxUnresponsive", apitest.DescStillStopping(ss(true, true, false))},
		{"starting", startingFacts{endedAgo: outside, pid: true, sid: true, age: young},
			"ErrTmuxUnresponsive", apitest.DescStillStarting(ss(false, true, true))},
		{"own id, Ours, window checked, session id", startingFacts{endedAgo: outside, pid: true, sid: true, age: old},
			"ErrTmuxSessionConflict", apitest.DescOwnOldSession(ss(false, true, true))},
		{"own id, Ours, window checked, no session id", startingFacts{endedAgo: outside, pid: true, age: old},
			"ErrTmuxSessionConflict", apitest.DescOwnOldSession(ss(false, true, false))},
		{"own id, Ours, window skipped by NULL ended_at", startingFacts{noEndedAt: true, pid: true, sid: true, age: old},
			"ErrTmuxSessionConflict", apitest.DescOwnOldSession(ss(false, false, true))},
		// Ended just now: inside the window, which neither pid nor session id skips.
		{"own id, Ours, window skipped by neither pid nor session id", startingFacts{age: old},
			"ErrTmuxSessionConflict", apitest.DescOwnOldSession(ss(false, false, false))},
		{"own id, Gone", startingFacts{endedAgo: outside, pid: true, sid: true, noSession: true},
			"ErrTmuxSessionConflict", apitest.DescOwnOldSession(ss(true, true, true))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := checkFacts(lim, name, tc.f)
			err := c.Refusal()
			assertOneName(t, err, tc.want)
			apitest.AssertDescription(t, err.Error(), tc.desc)
			step, pastBoth := c.UnavailableError(), c.Class().Outcome == tmux.PastBoth
			switch {
			case pastBoth && step != nil:
				t.Errorf("past both: UnavailableError = %q; want nil", step)
			case pastBoth && err.Error() != c.OwnIDConflictError().Error():
				t.Errorf("Refusal = %q; want the own-id conflict", err)
			case !pastBoth && (step == nil || err.Error() != step.Error()):
				t.Errorf("Refusal = %q; want UnavailableError %v", err, step)
			}
		})
	}
}

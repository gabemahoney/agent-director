package api_test

// resume_starting_test.go tests the starting-session rule at resume's
// pre-launch check (SR-8.2, SR-4.2; AC-RES-03, AC-RES-04, AC-RES-17) on the
// shared finished row (starting_row_fixture_test.go), and that resume reads
// the configured bound and window. Its pure boundaries are internal/tmux's;
// the configured values' starting_session_test.go's (AC-RES-05).

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// assertStartingRefusal checks err, a resume refused by the rule with want,
// against p's description case, and that nothing was written since before.
func (e *killEnv) assertStartingRefusal(t *testing.T, before resumeSnapshot, err error, want tmux.StartingSessionOutcome, p apitest.StartingSession) {
	t.Helper()
	name, desc := "ErrTmuxUnresponsive", apitest.DescCase{}
	switch want {
	case tmux.StillStopping:
		desc = apitest.DescStillStopping(p)
	case tmux.StillStarting:
		desc = apitest.DescStillStarting(p)
	default:
		name, desc = "ErrTmuxSessionConflict", apitest.DescOwnOldSession(p)
	}
	assertOneName(t, err, name)
	apitest.AssertDescription(t, err.Error(), desc, before.r.Token, before.r.StoreID)
	e.assertResumeWroteNothing(t, before)
}

// TestResumeStartingSessionCases: inside the window the window decides,
// whatever the session's age (AC-RES-17); outside it, or with a NULL
// ended_at, the session's age against the bound, a session created in the
// future being young (AC-RES-04); a future ended_at, a row with no pid, and
// Gone while the agent runs (AC-RES-03).
func TestResumeStartingSessionCases(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	longAgo := defWindow + defBound
	cases := []struct {
		name string
		row  startingRow
		want tmux.StartingSessionOutcome
	}{
		{"inside the window, session bound-1 s old",
			startingRow{state: store.StateEnded, endedAgo: defWindow - time.Second, age: defBound - time.Second}, tmux.StillStopping},
		{"inside the window, session the bound old",
			startingRow{state: store.StateMissing, endedAgo: defWindow - time.Second, age: defBound}, tmux.StillStopping},
		{"outside the window, session bound-1 s old",
			startingRow{state: store.StateEnded, endedAgo: longAgo, age: defBound - time.Second}, tmux.StillStarting},
		{"outside the window, session the bound old",
			startingRow{state: store.StateEnded, endedAgo: longAgo, age: defBound}, tmux.PastBoth},
		{"outside the window, session created 1 s in the future",
			startingRow{state: store.StateEnded, endedAgo: longAgo, age: -time.Second}, tmux.StillStarting},
		{"ended_at in the future, old session",
			startingRow{state: store.StateEnded, endedAgo: -time.Second, age: longAgo}, tmux.StillStopping},
		{"no ended_at, session bound-1 s old",
			startingRow{state: store.StateMissing, noEndedAt: true, age: defBound - time.Second}, tmux.StillStarting},
		{"no ended_at, session the bound old",
			startingRow{state: store.StateMissing, noEndedAt: true, age: defBound}, tmux.PastBoth},
		{"session id but no pid, inside the window, old session",
			startingRow{state: store.StateEnded, endedAgo: defWindow - time.Second, agent: agentNotRecorded, age: longAgo},
			tmux.StillStopping},
		{"Gone, agent running, inside the window",
			startingRow{state: store.StateEnded, endedAgo: defWindow - time.Second, noSession: true}, tmux.StillStopping},
		{"Gone, agent running, outside the window",
			startingRow{state: store.StateMissing, endedAgo: defWindow, noSession: true}, tmux.PastBoth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedStarting(t, tc.row)
			cols := e.columns(t, r.ID)
			if tc.row.noEndedAt && cols.EndedAt != nil {
				t.Fatalf("precondition: ended_at = %v; want NULL", cols.EndedAt)
			}
			if tc.row.agent == agentNotRecorded && cols.PID != nil {
				t.Fatalf("precondition: pid = %v; want NULL", cols.PID)
			}
			before := e.snapshotResume(t, r)
			_, err := e.resume(r.ID)
			e.assertStartingRefusal(t, before, err, tc.want, tc.row.startingCase(r, defBound, defWindow))
		})
	}
}

// TestResumeStartingSettings: through Client.Resume each setting at its safe
// minimum decides a row the defaults decide otherwise, and 0 gives the
// defaults, never a 0 s window or bound (AC-RES-05, AC-CFG-02).
func TestResumeStartingSettings(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	bk, wk, longAgo := config.TmuxStartingSessionSeconds, config.TmuxStoppingWindowSeconds, defWindow+defBound
	minB, minW := secs(config.MinStartingSessionSeconds), secs(config.MinStoppingWindowSeconds)
	zero := []apitest.TmuxSetting{apitest.TmuxInt(bk, 0), apitest.TmuxInt(wk, 0)}
	cases := []struct {
		name          string
		settings      []apitest.TmuxSetting
		bound, window time.Duration
		endedAgo, age time.Duration
		want          tmux.StartingSessionOutcome
	}{
		{"window at its safe minimum, ended it ago, old session (defaults: still stopping)",
			[]apitest.TmuxSetting{apitest.TmuxInt(wk, config.MinStoppingWindowSeconds)}, defBound, minW, minW, longAgo, tmux.PastBoth},
		{"bound at its safe minimum, ended long ago, session it old (defaults: still starting)",
			[]apitest.TmuxSetting{apitest.TmuxInt(bk, config.MinStartingSessionSeconds)}, minB, defWindow, longAgo, minB, tmux.PastBoth},
		{"both 0, ended window-1 s ago, old session", zero, defBound, defWindow, defWindow - time.Second, longAgo, tmux.StillStopping},
		{"both 0, ended long ago, session bound-1 s old", zero, defBound, defWindow, longAgo, defBound - time.Second, tmux.StillStarting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			row := startingRow{state: store.StateEnded, endedAgo: tc.endedAgo, age: tc.age}
			r := e.seedStarting(t, row)
			before := e.snapshotResume(t, r)
			_, logs, err := e.resumeClient(t, r.ID, tc.settings...)
			e.assertStartingRefusal(t, before, err, tc.want, row.startingCase(r, tc.bound, tc.window))
			if logs != "" {
				t.Errorf("Client log = %q; want none (a refusal is reported by its error alone)", logs)
			}
		})
	}
}

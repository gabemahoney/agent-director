package api_test

// spawn_reuse_window_test.go tests the starting-session rule at reuse's
// old-row lookup (SR-10.2, SR-4.2, SR-5.5; AC-REUSE-14, AC-RES-17, AC-CFG-02)
// through api.New and Client.Spawn with the reuse opt-in: the stopping window
// before the bound, each configured on its own, the window's skip rules (NULL
// or unparseable ended_at, neither pid nor session id), Gone while the agent
// runs, and every refusal writing nothing. Rows: spawn_reuse_fixture_test.go;
// the refusal check: resume_starting_test.go.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reuseStartingRow is a reusable row as the rule sees it at ruleInstant: its
// seed spec (ended_at Age ago), its agent, and its own session's age
// (noSession: Gone while the agent runs).
type reuseStartingRow struct {
	spec      reuseRowSpec
	agent     agentState
	noSession bool
	age       time.Duration
}

// windowSkipped reports whether the rule skips the stopping window for s: no
// parseable ended_at, or neither a pid nor a session id.
func (s reuseStartingRow) windowSkipped() bool {
	return s.spec.EndedAt != endedAged || (s.agent == agentNotRecorded && s.spec.NoSessionID)
}

// seedReuseStarting seeds s's reusable row with its own session unless
// noSession; a raw row (GetSpawn refuses it) gets its current-labelled
// session through placeHolder.
func (e *killEnv) seedReuseStarting(t *testing.T, s reuseStartingRow) reuseRow {
	t.Helper()
	r := e.seedReusable(t, s.agent, s.spec)
	switch {
	case s.noSession:
	case s.spec.EndedAt == endedUnparseable:
		own := e.holderSessions(t, r.killRow, holderCurrent)
		own[0].Created = e.ruleInstant().Add(-s.age).Unix()
		e.placeHolder(t, r.killRow, holderCurrent, own)
	default:
		e.seedSession(t, &r.killRow, e.createdBefore(s.age))
	}
	return r
}

// reuseStarting reuses s's row (requesting its recorded name) through a Client
// configured with settings and checks the refusal is want's under bound and
// window, that nothing was written and that the Client logged nothing.
func reuseStarting(t *testing.T, e *killEnv, r reuseRow, s reuseStartingRow, want tmux.StartingSessionOutcome,
	bound, window time.Duration, settings ...apitest.TmuxSetting) {
	t.Helper()
	before := e.snapshotReuse(t, r)
	_, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{}), settings...)
	p := apitest.StartingSession{InstanceID: r.ID, Name: r.Name, Window: window, Bound: bound, NoSession: s.noSession,
		WindowChecked: !s.windowSkipped(), SessionID: !s.spec.NoSessionID}
	e.assertStartingRefusal(t, resumeSnapshot{writesSnapshot: before, r: r.resumeRow}, err, want, p)
	if logs != "" {
		t.Errorf("Client log = %q; want none (a refusal is reported by its error alone)", logs)
	}
}

// TestSpawnReuseStartingWindowAndBound: ended and missing rows at window-1 s and the window, with a session
// at bound-1 s and the bound, per configuration; the window decides first and neither setting moves the other.
func TestSpawnReuseStartingWindowAndBound(t *testing.T) {
	minB, minW := int64(config.MinStartingSessionSeconds), int64(config.MinStoppingWindowSeconds)
	configs := []struct {
		name          string
		settings      []apitest.TmuxSetting
		bound, window time.Duration
	}{
		{"keys absent", nil, defBound, defWindow},
		{"both set to 0", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, 0),
			apitest.TmuxInt(config.TmuxStoppingWindowSeconds, 0)}, defBound, defWindow},
		{"window at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)},
			defBound, secs(minW)},
		{"bound at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB)},
			secs(minB), defWindow},
	}
	for _, c := range configs {
		probes := []struct {
			name     string
			ago, age time.Duration
			want     tmux.StartingSessionOutcome
		}{
			{"ended window-1 s ago, session the bound old", c.window - time.Second, c.bound, tmux.StillStopping},
			{"ended window-1 s ago, session bound-1 s old", c.window - time.Second, c.bound - time.Second, tmux.StillStopping},
			{"ended the window ago, session bound-1 s old", c.window, c.bound - time.Second, tmux.StillStarting},
			{"ended the window ago, session the bound old", c.window, c.bound, tmux.PastBoth},
		}
		for _, state := range []string{store.StateEnded, store.StateMissing} {
			for _, p := range probes {
				t.Run(c.name+"/"+state+", "+p.name, func(t *testing.T) {
					e := newKillEnv(t)
					s := reuseStartingRow{spec: reuseRowSpec{State: state, Age: p.ago}, age: p.age}
					reuseStarting(t, e, e.seedReuseStarting(t, s), s, p.want, c.bound, c.window, c.settings...)
				})
			}
		}
	}
}

// TestSpawnReuseStartingCases: a future ended_at, the window's skip rules, the no-session-id texts and Gone
// while the agent runs, at the defaults (AC-REUSE-14; SR-5.5's unparseable ended_at reads as none).
func TestSpawnReuseStartingCases(t *testing.T) {
	longAgo := defWindow + defBound
	inside := defWindow - time.Second
	cases := []struct {
		name string
		row  reuseStartingRow
		want tmux.StartingSessionOutcome
	}{
		{"ended_at in the future, old session",
			reuseStartingRow{spec: reuseRowSpec{Age: -time.Second}, age: longAgo}, tmux.StillStopping},
		{"AC-REUSE-14: ended long ago, session younger than the bound",
			reuseStartingRow{spec: reuseRowSpec{Age: longAgo}, age: defBound - time.Second}, tmux.StillStarting},
		{"NULL ended_at, session bound-1 s old",
			reuseStartingRow{spec: reuseRowSpec{State: store.StateMissing, EndedAt: endedNull}, age: defBound - time.Second},
			tmux.StillStarting},
		{"NULL ended_at, session the bound old",
			reuseStartingRow{spec: reuseRowSpec{State: store.StateMissing, EndedAt: endedNull}, age: defBound}, tmux.PastBoth},
		{"unparseable ended_at, session bound-1 s old",
			reuseStartingRow{spec: reuseRowSpec{EndedAt: endedUnparseable}, age: defBound - time.Second}, tmux.StillStarting},
		{"unparseable ended_at, session the bound old",
			reuseStartingRow{spec: reuseRowSpec{EndedAt: endedUnparseable}, age: defBound}, tmux.PastBoth},
		{"session id but no pid, inside the window, old session",
			reuseStartingRow{spec: reuseRowSpec{Age: inside}, agent: agentNotRecorded, age: longAgo}, tmux.StillStopping},
		{"neither pid nor session id, inside the window, session bound-1 s old",
			reuseStartingRow{spec: reuseRowSpec{Age: inside, NoSessionID: true}, agent: agentNotRecorded,
				age: defBound - time.Second}, tmux.StillStarting},
		{"neither pid nor session id, inside the window, session the bound old",
			reuseStartingRow{spec: reuseRowSpec{Age: inside, NoSessionID: true}, agent: agentNotRecorded, age: defBound},
			tmux.PastBoth},
		{"no session id, outside the window, session the bound old",
			reuseStartingRow{spec: reuseRowSpec{Age: defWindow, NoSessionID: true}, age: defBound}, tmux.PastBoth},
		{"Gone, agent running, inside the window",
			reuseStartingRow{spec: reuseRowSpec{Age: inside}, noSession: true}, tmux.StillStopping},
		{"Gone, agent running, outside the window",
			reuseStartingRow{spec: reuseRowSpec{State: store.StateMissing, Age: defWindow}, noSession: true}, tmux.PastBoth},
		{"Gone, agent running, no session id, outside the window",
			reuseStartingRow{spec: reuseRowSpec{Age: longAgo, NoSessionID: true}, noSession: true}, tmux.PastBoth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReuseStarting(t, tc.row)
			cols := e.columns(t, r.ID)
			switch tc.row.spec.EndedAt {
			case endedNull:
				if cols.EndedAt != nil {
					t.Fatalf("precondition: ended_at = %v; want NULL", cols.EndedAt)
				}
			case endedUnparseable:
				if s, ok := cols.EndedAt.(string); !ok || s == "" {
					t.Fatalf("precondition: ended_at = %v; want unparseable text", cols.EndedAt)
				}
			}
			if tc.row.agent == agentNotRecorded && cols.PID != nil {
				t.Fatalf("precondition: pid = %v; want NULL", cols.PID)
			}
			if sid, _ := cols.ClaudeSessionID.(string); tc.row.spec.NoSessionID && sid != "" {
				t.Fatalf("precondition: claude_session_id = %v; want NULL", cols.ClaudeSessionID)
			}
			reuseStarting(t, e, r, tc.row, tc.want, defBound, defWindow)
		})
	}
}

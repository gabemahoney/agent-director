package tmux_test

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Tests of the session-age helper (SR-3.9) and the starting-session rule's
// classification (SR-4.2): window first, then age, both strictly less than.
// Refusal texts and the unreadable creation time are resume's (Task 2).

// secs is n whole seconds.
func secs(n int64) time.Duration { return time.Duration(n) * time.Second }

// The effective settings at their defaults and safe minimums.
var (
	defWindow = secs(config.DefaultStoppingWindowSeconds)
	defBound  = secs(config.DefaultStartingSessionSeconds)
	minWindow = secs(config.MinStoppingWindowSeconds)
	minBound  = secs(config.MinStartingSessionSeconds)
)

// longAgo is far outside every window and bound tested here.
const longAgo = 24 * time.Hour

// startClock is a virtual clock on a whole second.
func startClock() *tmuxfix.Clock { return tmuxfix.NewClock(time.Unix(1_700_000_000, 0)) }

// ago returns d for an optional field; a negative d is that far in the future.
func ago(d time.Duration) *time.Duration { return &d }

// ssRow is the examined row and lookup as offsets from the clock's instant.
// A nil endedAgo is a NULL ended_at; a nil age is no session (Gone while the
// agent process runs). Zero window and bound mean the defaults.
type ssRow struct {
	endedAgo     *time.Duration
	noPID, noSID bool
	age          *time.Duration
	window       time.Duration
	bound        time.Duration
}

// input builds the rule's input as of now.
func (r ssRow) input(now time.Time) tmux.StartingSessionInput {
	in := tmux.StartingSessionInput{
		RecordsPID:       !r.noPID,
		RecordsSessionID: !r.noSID,
		Now:              now,
		Window:           r.window,
		Bound:            r.bound,
	}
	if in.Window == 0 {
		in.Window = defWindow
	}
	if in.Bound == 0 {
		in.Bound = defBound
	}
	if r.endedAgo != nil {
		ended := now.Add(-*r.endedAgo)
		in.EndedAt = &ended
	}
	if r.age != nil {
		in.Session = &tmux.Session{ID: "$1", Name: "ad-starting", Created: now.Add(-*r.age).Unix()}
	}
	return in
}

// elapsedOf is the Elapsed of an instant d before now (negative: future).
func elapsedOf(d time.Duration) tmux.Elapsed {
	if d < 0 {
		return tmux.Elapsed{Future: true}
	}
	return tmux.Elapsed{Since: d}
}

func TestStartingSession(t *testing.T) {
	cases := []struct {
		name        string
		row         ssRow
		want        tmux.StartingSessionOutcome
		wantChecked bool
	}{
		// Stopping window at the default (minus one, exact), young and old session.
		{"window default: inside, young session -> stopping",
			ssRow{endedAgo: ago(defWindow - time.Second), age: ago(defBound - time.Second)}, tmux.StillStopping, true},
		{"window default: inside, old session -> stopping",
			ssRow{endedAgo: ago(defWindow - time.Second), age: ago(defBound)}, tmux.StillStopping, true},
		{"window default: at window, young session -> starting",
			ssRow{endedAgo: ago(defWindow), age: ago(defBound - time.Second)}, tmux.StillStarting, true},
		{"window default: at window, old session -> past both",
			ssRow{endedAgo: ago(defWindow), age: ago(defBound)}, tmux.PastBoth, true},

		// Bound at the default (minus one, exact), row ended outside the window.
		{"bound default: bound minus one -> starting",
			ssRow{endedAgo: ago(longAgo), age: ago(defBound - time.Second)}, tmux.StillStarting, true},
		{"bound default: at bound -> past both",
			ssRow{endedAgo: ago(longAgo), age: ago(defBound)}, tmux.PastBoth, true},

		// Bound at its safe minimum (minus one, exact); the window is unchanged.
		{"bound minimum: bound minus one -> starting",
			ssRow{endedAgo: ago(longAgo), age: ago(minBound - time.Second), bound: minBound}, tmux.StillStarting, true},
		{"bound minimum: at bound -> past both",
			ssRow{endedAgo: ago(longAgo), age: ago(minBound), bound: minBound}, tmux.PastBoth, true},
		{"bound minimum: default window unchanged -> stopping",
			ssRow{endedAgo: ago(defWindow - time.Second), age: ago(minBound), bound: minBound}, tmux.StillStopping, true},

		// Window at its safe minimum (minus one, exact); the bound is unchanged.
		{"window minimum: inside -> stopping",
			ssRow{endedAgo: ago(minWindow - time.Second), age: ago(defBound), window: minWindow}, tmux.StillStopping, true},
		{"window minimum: at window, old session -> past both",
			ssRow{endedAgo: ago(minWindow), age: ago(defBound), window: minWindow}, tmux.PastBoth, true},
		{"window minimum: default bound unchanged -> starting",
			ssRow{endedAgo: ago(minWindow), age: ago(defBound - time.Second), window: minWindow}, tmux.StillStarting, true},

		// ended_at in the future, and NULL.
		{"future ended_at, old session -> stopping",
			ssRow{endedAgo: ago(-time.Second), age: ago(defBound)}, tmux.StillStopping, true},
		{"NULL ended_at, young session -> window skipped, starting",
			ssRow{age: ago(defBound - time.Second)}, tmux.StillStarting, false},
		{"NULL ended_at, old session -> window skipped, past both",
			ssRow{age: ago(defBound)}, tmux.PastBoth, false},

		// Which recorded facts keep the window.
		{"neither pid nor session id, young session -> window skipped, starting",
			ssRow{endedAgo: ago(defWindow - time.Second), noPID: true, noSID: true, age: ago(defBound - time.Second)}, tmux.StillStarting, false},
		{"neither pid nor session id, old session -> window skipped, past both",
			ssRow{endedAgo: ago(defWindow - time.Second), noPID: true, noSID: true, age: ago(defBound)}, tmux.PastBoth, false},
		{"pid only keeps the window -> stopping",
			ssRow{endedAgo: ago(defWindow - time.Second), noSID: true, age: ago(defBound)}, tmux.StillStopping, true},
		{"session id only keeps the window -> stopping",
			ssRow{endedAgo: ago(defWindow - time.Second), noPID: true, age: ago(defBound)}, tmux.StillStopping, true},

		// No session (Gone while the agent process runs): never starting.
		{"no session, inside window -> stopping",
			ssRow{endedAgo: ago(defWindow - time.Second)}, tmux.StillStopping, true},
		{"no session, at window -> past both",
			ssRow{endedAgo: ago(defWindow)}, tmux.PastBoth, true},
		{"no session, NULL ended_at -> past both",
			ssRow{}, tmux.PastBoth, false},

		// Session age edges.
		{"session created this second (age 0) -> starting",
			ssRow{endedAgo: ago(longAgo), age: ago(0)}, tmux.StillStarting, true},
		{"session created in the future -> starting",
			ssRow{endedAgo: ago(longAgo), age: ago(-longAgo)}, tmux.StillStarting, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.row.input(startClock().Now())
			got := tmux.StartingSession(in)

			want := tmux.StartingSessionClass{
				Outcome:        tc.want,
				WindowChecked:  tc.wantChecked,
				SessionPresent: tc.row.age != nil,
				Window:         in.Window,
				Bound:          in.Bound,
			}
			if tc.wantChecked {
				want.SinceEnd = elapsedOf(*tc.row.endedAgo)
			}
			if tc.row.age != nil && tc.want != tmux.StillStopping {
				want.Age = elapsedOf(*tc.row.age)
			}
			if got != want {
				t.Errorf("StartingSession() = %+v, want %+v", got, want)
			}
		})
	}
}

// Stepping the virtual clock moves the answer across the window and then the
// bound without waiting; a backward step brings the young answer back.
func TestStartingSession_ClockStepCrossesLimits(t *testing.T) {
	clock := startClock()
	// Created bound minus window ago, the session turns bound minus one
	// window minus one seconds later.
	ended := clock.Now().Add(-(defWindow - time.Second))
	session := &tmux.Session{ID: "$1", Created: clock.Now().Add(-(defBound - defWindow)).Unix()}
	classify := func() tmux.StartingSessionOutcome {
		return tmux.StartingSession(tmux.StartingSessionInput{
			EndedAt: &ended, RecordsPID: true, RecordsSessionID: true,
			Session: session, Now: clock.Now(), Window: defWindow, Bound: defBound,
		}).Outcome
	}

	steps := []struct {
		name    string
		advance time.Duration
		want    tmux.StartingSessionOutcome
	}{
		{"window minus one", 0, tmux.StillStopping},
		{"at window", time.Second, tmux.StillStarting},
		{"bound minus one", defWindow - 2*time.Second, tmux.StillStarting},
		{"at bound", time.Second, tmux.PastBoth},
		{"backward step", -time.Second, tmux.StillStarting},
	}
	for _, s := range steps {
		clock.Advance(s.advance)
		if got := classify(); got != s.want {
			t.Errorf("%s: outcome = %v, want %v", s.name, got, s.want)
		}
	}
}

func TestSessionAge(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
	}{
		{"past", defBound},
		{"same second", 0},
		{"future", -time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := startClock().Now()
			if got, want := tmux.SessionAge(now, now.Add(-tc.age).Unix()), elapsedOf(tc.age); got != want {
				t.Errorf("SessionAge() = %+v, want %+v", got, want)
			}
		})
	}
}

// A future instant is decided outright, so it is under even a zero limit.
func TestStartingSession_FutureUnderZeroLimit(t *testing.T) {
	if !(tmux.Elapsed{Future: true}).Under(0) {
		t.Error("future Under(0) = false, want true")
	}
	if (tmux.Elapsed{}).Under(0) {
		t.Error("zero elapsed Under(0) = true, want false")
	}
}

package api_test

// one_name_kill_optin_test.go holds the SR-1.5 one-name rows of kill with the
// finished-row opt-in (SR-6.5; Epic 18): one per error it returns on its own
// paths (the live-row refusal and the finished-row table), driven through
// api.Kill on the kill fixture (kill_optin_fixture_test.go) and checked by
// assertOneName. They run under TestOneNameKillOptInReturnedErrors, so -run
// OneName selects them.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// TestOneNameKillOptInReturnedErrors: every error kill with the opt-in adds
// matches exactly one catalogued sentinel (as TestOneNameReturnedErrors).
func TestOneNameKillOptInReturnedErrors(t *testing.T) {
	t.Parallel()
	for _, row := range oneNameKillOptInRows() {
		t.Run(row.name, func(t *testing.T) { assertOneName(t, row.run(t), row.want) })
	}
}

// oneNameKillOptIn is a row that runs api.Kill with the opt-in on a row
// seeded with spec after setup (when set) prepares e and r.
func oneNameKillOptIn(name, want string, spec killRowSpec, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
	return oneNameRow{name: "kill with the opt-in/" + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedRow(t, spec)
		if setup != nil {
			setup(t, e, &r)
		}
		_, err := e.killOptIn(r.ID)
		return err
	}}
}

// oneNameKillOptInRows are the opt-in's returned errors: the live-row
// refusal, then the finished-row table's on an ended row (koEnded): both
// "never reported in" and conflicting labels, still stopping and still
// starting, the follow-up after a sent kill, a different server, and the
// agent outliving a sent kill; then ErrInternal for an unusable recorded name.
func oneNameKillOptInRows() []oneNameRow {
	reportedIn, startedLater := koEnded(killRowSpec{}, time.Second), koEnded(killRowSpec{}, -time.Hour)
	pastBoth := func(_ *testing.T, e *killEnv, _ *killRow) { koPastBoth(e) }
	return []oneNameRow{
		oneNameKillOptIn("live row", "ErrSpawnNotResumable", killRowSpec{State: store.StateWorking}, nil),
		oneNameKillOptIn("never reported in, ours", "ErrTmuxSessionConflict", startedLater, pastBoth),
		oneNameKillOptIn("never reported in, leftover", "ErrTmuxSessionConflict",
			koEnded(killRowSpec{NoSession: true}, time.Second), func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
			}),
		oneNameKillOptIn("conflicting labels", "ErrTmuxSessionConflict", reportedIn,
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
			}),
		oneNameKillOptIn("still stopping", "ErrTmuxUnresponsive", koEnded(killRowSpec{}, 0), nil),
		oneNameKillOptIn("still starting", "ErrTmuxUnresponsive", startedLater, nil),
		oneNameKillOptIn("follow-up unanswered after a kill", "ErrTmuxUnresponsive",
			koEnded(killRowSpec{Agent: agentUnreadable}, time.Second), func(t *testing.T, e *killEnv, r *killRow) {
				koPastBoth(e)
				scriptKill(tmux.CallLookup, tmuxfix.Script{Times: 1}, tmuxfix.Script{Failure: tmux.FailTimeout})(t, e, r)
			}),
		oneNameKillOptIn("different server", "ErrTmuxNotAvailable", reportedIn, func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedBystander(t, r.Socket)
		}),
		oneNameKillOptIn("agent outlives a sent kill", "ErrTmuxKillFailed", reportedIn, pastBoth),
		oneNameKillOptIn("unusable recorded name", "", koEnded(unusableNameSpec(), time.Second), nil),
	}
}

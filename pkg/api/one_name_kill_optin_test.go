package api_test

// one_name_kill_optin_test.go holds the SR-1.5 one-name rows of kill with the
// finished-row opt-in (SR-6.5; Epic 18) that neither its call-site table row
// (lookup_calltable_kill_optin_test.go) nor TestAdviceFollow_C9 and C10 (the
// live row, still stopping or starting, never reported in) reach: the
// finished-row table's kill sequence on an ended row (koEnded), driven
// through api.Kill on the kill fixture and checked by assertOneName.

import (
	"testing"
	"time"

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

// oneNameKillOptInRows are, on an ended row reported in and past both the
// stopping window and the starting-session bound, the follow-up unanswered
// after a sent kill, and the agent outliving a sent kill.
func oneNameKillOptInRows() []oneNameRow {
	optIn := func(name, want string, spec killRowSpec, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
		return oneNameRow{name: "kill with the opt-in/" + name, want: want, run: func(t *testing.T) error {
			e := newKillEnv(t)
			r := e.seedRow(t, spec)
			koPastBoth(e)
			if setup != nil {
				setup(t, e, &r)
			}
			_, err := e.killOptIn(r.ID)
			return err
		}}
	}
	return []oneNameRow{
		optIn("follow-up unanswered after a kill", "ErrTmuxUnresponsive", koEnded(killRowSpec{Agent: agentUnreadable}, time.Second),
			scriptKill(tmux.CallLookup, tmuxfix.Script{Times: 1}, tmuxfix.Script{Failure: tmux.FailTimeout})),
		optIn("agent outlives a sent kill", "ErrTmuxKillFailed", koEnded(killRowSpec{}, time.Second), nil),
	}
}

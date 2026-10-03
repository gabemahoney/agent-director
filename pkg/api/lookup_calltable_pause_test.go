package api_test

// lookup_calltable_pause_test.go is pause's rows of the call-site table
// (lookup_calltable_test.go; SR-7.2, SR-7.3, SR-20.6): the waiting row, whose
// Ours cell clears the input line (C-u, b.9o4), types /exit and Enter into
// the agent's pane by id and then waits for the row to end, plus a pending
// and an ended row, whose state guards
// answer before any tmux call in every column. Only the /exit call is failed
// in the action columns. The agent's pane, lost replies, adoption, the Enter
// failures and the other follow-up outcomes are pause's per-verb files'.

import (
	"maps"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pauseCallTableInvoke pauses r and reports whether a text call was made; any
// must type /exit into the pane of r.Session carrying its label's token, with
// Enter to that pane, and the wait polls only after a delivered /exit.
func pauseCallTableInvoke(t *testing.T, e *killEnv, r killRow) (bool, error) {
	reads := e.store.stateReads
	_, err := e.pause(pauseParams(r))
	polls := e.store.stateReads - reads
	typed := len(e.rec.SocketCallsOf(tmux.CallSendText)) > 0
	if typed {
		pane := labelledPane(t, r.Session, r.Session.Label.Token)
		e.assertExitTyped(t, r.Socket, pane)
		for _, c := range e.rec.SocketCallsOf(tmux.CallSendEnter) {
			if c.Socket != r.Socket || c.Target != pane {
				t.Errorf("Enter to %q on %s; want %q on %s", c.Target, c.Socket, pane, r.Socket)
			}
		}
	}
	if waited, want := polls > 0, typed && err == nil; waited != want {
		t.Errorf("wait polled %d times (err %v); want a wait %v", polls, err, want)
	}
	return typed, err
}

// runCallTablePause runs a delivered /exit cell with the row ended by its own
// agent's SessionEnd after Enter (the wait's success); any other cell runs
// as runCallTableRowCell, the row unchanged.
func runCallTablePause(t *testing.T, v callTableVerb, col callTableColumn, cell callTableCell) {
	if !cell.sent || cell.errName != "" {
		runCallTableRowCell(t, v, col, cell)
		return
	}
	e := newKillEnv(t)
	r := e.seedRow(t, col.spec)
	e.endAfterEnter(t, r)

	sent, err := v.invoke(t, e, r)

	if err != nil || !sent {
		t.Fatalf("sent %v, err %v; want /exit sent and success", sent, err)
	}
	e.assertExitDelivered(t, r.Socket, labelledPane(t, r.Session, r.Token))
	if c := e.columns(t, r.ID); c.State != store.StateEnded {
		t.Errorf("row state = %v; want ended", c.State)
	}
}

// callTablePause is pause's waiting-row row (SR-7.2, SR-7.3): Ours sends /exit
// and Enter to the agent's pane by id, then waits; Leftover is
// ErrTmuxSessionConflict and Gone ErrTmuxSendKeys after the lookup only; Can't
// tell and tmux unavailable are the shared lookup refusals; a failed /exit
// whose follow-up finds Ours is ErrTmuxUnresponsive, and a timed-out one says
// the keys may have been delivered, with no follow-up; no refusal waits.
func callTablePause() callTableVerb {
	lookup := []tmux.Call{tmux.CallLookup}
	gone := callTableCell{errName: "ErrTmuxSendKeys", calls: lookup, desc: func(_ *killEnv, r killRow) apitest.DescCase {
		return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PanePause, InstanceID: r.ID, Name: r.Name})
	}}
	cells := callTableLookupRefusals()
	maps.Copy(cells, map[callTableOutcome]callTableCell{
		ctOurs: {sent: true, calls: pauseSendCalls},
		ctLeftover: {errName: "ErrTmuxSessionConflict", calls: lookup, desc: func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PanePause, InstanceID: r.ID,
				Sessions: []apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}}})
		}},
		ctGoneNoLabel:              gone,
		ctGoneOtherStoreRowToken:   gone,
		ctGoneOtherStoreOtherToken: gone,
		ctGoneForeignLabel:         gone,
		ctGoneNameUnlabelled:       gone,
		ctGoneServerRestarted:      gone,
		ctGoneNoServer:             gone,
		ctGoneNoSocket:             gone,
		ctActionRecognised: {errName: "ErrTmuxUnresponsive", sent: true, calls: withFollowUp(pauseTextCalls),
			desc: func(*killEnv, killRow) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendText, "").AfterTextFailed()
			}},
		ctActionTimeout: {errName: "ErrTmuxUnresponsive", sent: true, calls: pauseTextCalls,
			desc: func(e *killEnv, _ killRow) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PanePause, tmux.CallSendText, e.cfg.EffectiveActionTimeout())
			}},
	})
	maps.Copy(cells, callTableUnusableRefused())
	return callTableVerb{
		name:        "pause",
		invoke:      pauseCallTableInvoke,
		firstAction: tmux.CallSendText,
		actions:     []tmux.Call{callClearLine, tmux.CallSendText, tmux.CallSendEnter},
		cells:       cells,
		run:         runCallTablePause,
	}
}

// callTablePauseGuarded is pause's row on a row seeded in state: every column
// gets cell (the state guard's answer), with no tmux call and no wait.
func callTablePauseGuarded(state string, cell callTableCell) callTableVerb {
	v := callTablePause()
	v.name = "pause, " + state + " row"
	v.cells = map[callTableOutcome]callTableCell{}
	for _, col := range callTableColumns() {
		v.cells[col.outcome] = cell
	}
	v.run = func(t *testing.T, v callTableVerb, col callTableColumn, cell callTableCell) {
		col.spec.State = state
		runCallTableRowCell(t, v, col, cell)
	}
	return v
}

// callTablePausePending is a pending row: ErrSpawnNotPausable, whose text
// carries no label value and no session-ending form.
func callTablePausePending() callTableVerb {
	return callTablePauseGuarded(store.StatePending, callTableCell{errName: "ErrSpawnNotPausable",
		desc: func(*killEnv, killRow) apitest.DescCase { return apitest.DescCase{Name: "pause, not pausable"} }})
}

// callTablePauseEnded is an ended row: a no-op success.
func callTablePauseEnded() callTableVerb {
	return callTablePauseGuarded(store.StateEnded, callTableCell{})
}

package api_test

// lookup_calltable_sendkeys_test.go is send-keys' two rows of the call-site
// table (lookup_calltable_test.go; SR-7.2, SR-7.3, SR-20.6): the live row,
// and a pending row sent to with allow_pending (SR-7.1, SR-22.8). Only the
// text call is failed in the action columns. The agent's pane, lost replies,
// the Enter-call failures and the other follow-up outcomes are
// sendkeys_action_test.go's; pending launch shapes are sendkeys_pending_test.go's.

import (
	"maps"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// callTableKeys is the text every send-keys cell sends.
const callTableKeys = "call-table keys"

// sendKeysCallTableInvoke sends callTableKeys to r (allow_pending as given)
// and reports whether a text call was made; any text or Enter call must go to
// the pane of r.Session carrying its label's token, on r's socket.
func sendKeysCallTableInvoke(allowPending bool) func(*testing.T, *killEnv, killRow) (bool, error) {
	return func(t *testing.T, e *killEnv, r killRow) (bool, error) {
		_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: callTableKeys, AllowPending: allowPending})
		typed := len(e.rec.SocketCallsOf(tmux.CallSendText)) > 0
		if typed {
			pane := labelledPane(t, r.Session, r.Session.Label.Token)
			e.assertTextSent(t, r.Socket, pane, callTableKeys)
			for _, c := range e.rec.SocketCallsOf(tmux.CallSendEnter) {
				if c.Socket != r.Socket || c.Target != pane {
					t.Errorf("Enter to %q on %s; want %q on %s", c.Target, c.Socket, pane, r.Socket)
				}
			}
		}
		return typed, err
	}
}

// callTableSendKeys is send-keys' live-row row (SR-7.2, SR-7.3): Ours types
// the text and Enter into the agent's pane by id; Leftover is
// ErrTmuxSessionConflict and Gone ErrTmuxSendKeys after the lookup only; Can't
// tell and tmux unavailable are the shared lookup refusals; a failed text call
// whose follow-up finds Ours is ErrTmuxUnresponsive, and a timed-out one says
// the keys may have been delivered, with no follow-up.
func callTableSendKeys() callTableVerb {
	gone := callTableCell{errName: "ErrTmuxSendKeys", calls: []tmux.Call{tmux.CallLookup},
		desc: func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneSendKeys, InstanceID: r.ID, Name: r.Name})
		}}
	cells := callTableLookupRefusals()
	maps.Copy(cells, map[callTableOutcome]callTableCell{
		ctOurs: {sent: true, calls: paneSendCalls},
		ctLeftover: {errName: "ErrTmuxSessionConflict", calls: []tmux.Call{tmux.CallLookup},
			desc: func(_ *killEnv, r killRow) apitest.DescCase {
				return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PaneSendKeys, InstanceID: r.ID,
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
		ctActionRecognised: {errName: "ErrTmuxUnresponsive", sent: true, calls: withFollowUp(paneTextCalls),
			desc: func(*killEnv, killRow) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendText, "").AfterTextFailed()
			}},
		ctActionTimeout: {errName: "ErrTmuxUnresponsive", sent: true, calls: paneTextCalls,
			desc: func(e *killEnv, _ killRow) apitest.DescCase {
				return apitest.DescKeysTimeout(tmux.CallSendText, e.cfg.EffectiveActionTimeout())
			}},
	})
	return callTableVerb{
		name:        "send-keys",
		invoke:      sendKeysCallTableInvoke(false),
		firstAction: tmux.CallSendText,
		actions:     []tmux.Call{tmux.CallSendText, tmux.CallSendEnter},
		cells:       cells,
	}
}

// callTableSendKeysPending is send-keys' row on a pending row with
// allow_pending, its launch start at the fixture clock: live's cells, but
// Leftover is ErrSpawnNotInteractive ("not this launch's session"); another
// store's session stays Gone.
func callTableSendKeysPending() callTableVerb {
	v := callTableSendKeys()
	v.name = "send-keys, pending with allow_pending"
	v.invoke = sendKeysCallTableInvoke(true)
	v.cells = maps.Clone(v.cells)
	v.cells[ctLeftover] = callTableCell{errName: "ErrSpawnNotInteractive", calls: []tmux.Call{tmux.CallLookup},
		desc: func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescSendKeysPendingLeftover(r.ID, []apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}})
		}}
	v.run = func(t *testing.T, v callTableVerb, col callTableColumn, cell callTableCell) {
		col.spec.State = store.StatePending
		col.spec.Opts = append(slices.Clone(col.spec.Opts), apitest.WithLaunchStartedAt(killClockStart.UnixMilli()))
		runCallTableRowCell(t, v, col, cell)
	}
	return v
}

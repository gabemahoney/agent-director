package api_test

// lookup_calltable_readpane_test.go is read-pane's row of the call-site table
// (lookup_calltable_test.go; SR-7.2, SR-7.3, SR-7.5, SR-20.6) and the
// action-failure column's follow-up variants: a capture failing other than by
// a timeout, then one follow-up lookup of each outcome. The agent's pane, lost
// replies and more leftovers are readpane_pane_test.go's.

import (
	"maps"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// readPaneCallTableInvoke reads r's pane through a Client (e.pc judges the
// server); any capture must be of r.Session's token pane, and a result its text.
func readPaneCallTableInvoke(t *testing.T, e *killEnv, r killRow) (bool, error) {
	e.setPaneTexts(r.Socket)
	res, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID})
	captured := len(e.rec.SocketCallsOf(tmux.CallCapture)) > 0
	if captured {
		e.assertCaptured(t, labelledPane(t, r.Session, r.Session.Label.Token), api.DefaultReadPaneLines, false)
	}
	switch {
	case err == nil && res.Pane != paneText(r.Socket, labelledPane(t, r.Session, r.Session.Label.Token)):
		t.Errorf("pane = %q; want %q", res.Pane, paneText(r.Socket, labelledPane(t, r.Session, r.Session.Label.Token)))
	case err != nil && res.Pane != "":
		t.Errorf("pane = %q with error %v; want nothing read", res.Pane, err)
	}
	return captured, err
}

// callTableReadPane is read-pane's row (SR-7.2, SR-7.3): Ours and a lone
// leftover are read by pane id; Gone is ErrTmuxCaptureFailed after the lookup
// only; Can't tell and tmux unavailable are the shared lookup refusals; a
// capture timeout makes no follow-up, and a failed capture whose follow-up
// finds Ours is ErrTmuxUnresponsive.
func callTableReadPane() callTableVerb {
	gone := callTableCell{errName: "ErrTmuxCaptureFailed", calls: []tmux.Call{tmux.CallLookup},
		desc: func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneReadPane, InstanceID: r.ID, Name: r.Name})
		}}
	read := callTableCell{sent: true, calls: paneReadCalls}
	cells := callTableLookupRefusals()
	maps.Copy(cells, map[callTableOutcome]callTableCell{
		ctOurs:                     read,
		ctLeftover:                 read,
		ctGoneNoLabel:              gone,
		ctGoneOtherStoreRowToken:   gone,
		ctGoneOtherStoreOtherToken: gone,
		ctGoneForeignLabel:         gone,
		ctGoneNameUnlabelled:       gone,
		ctGoneServerRestarted:      gone,
		ctGoneNoServer:             gone,
		ctGoneNoSocket:             gone,
		ctActionRecognised: {errName: "ErrTmuxUnresponsive", sent: true,
			calls: withFollowUp(paneReadCalls),
			desc: func(*killEnv, killRow) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallCapture, "")
			}},
		ctActionTimeout: {errName: "ErrTmuxUnresponsive", sent: true, calls: paneReadCalls,
			desc: func(e *killEnv, _ killRow) apitest.DescCase {
				return apitest.DescCallTimeout(tmux.CallCapture, e.cfg.EffectiveActionTimeout())
			}},
	})
	maps.Copy(cells, callTableUnusableRefused())
	return callTableVerb{
		name:        "read-pane",
		invoke:      readPaneCallTableInvoke,
		firstAction: tmux.CallCapture,
		actions:     []tmux.Call{tmux.CallCapture},
		cells:       cells,
	}
}

// readPaneAfterCapture runs fn once the capture returns, changing what the
// follow-up lookup finds.
func readPaneAfterCapture(fn func(t *testing.T, e *killEnv, r killRow)) func(*testing.T, *killEnv, killRow) {
	return func(t *testing.T, e *killEnv, r killRow) {
		e.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) { fn(t, e, r) })
	}
}

// readPaneFollowUpLookup scripts s on every lookup after the capture.
func readPaneFollowUpLookup(s tmuxfix.Script) func(*testing.T, *killEnv, killRow) {
	return readPaneAfterCapture(func(_ *testing.T, e *killEnv, r killRow) { e.rec.Script(r.Socket, s, tmux.CallLookup) })
}

// TestCallTableReadPaneFollowUp checks a failed capture's one follow-up lookup
// per outcome (SR-7.3): Gone or Leftover is ErrTmuxCaptureFailed, the rest
// ErrTmuxUnresponsive or ErrTmuxNotAvailable; the reply text never classifies.
func TestCallTableReadPaneFollowUp(t *testing.T) {
	gone := func(_ *killEnv, r killRow) apitest.DescCase {
		return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneReadPane, InstanceID: r.ID, Name: r.Name,
			FailedCall: tmux.CallCapture})
	}
	unresponsive := func(*killEnv, killRow) apitest.DescCase { return apitest.DescUnrecognisedReply(tmux.CallCapture, "") }
	noServer := tmuxfix.Script{Failure: tmux.FailNoServer}
	paneReply := tmuxfix.Find(tmuxfix.Replies(apitest.TestSocket), "reply/cant-find-pane").FirstLine
	cases := []struct {
		name    string
		capture tmuxfix.Script
		world   func(t *testing.T, e *killEnv, r killRow)
		errName string
		desc    func(e *killEnv, r killRow) apitest.DescCase
	}{
		{"gone, session ended", noServer, func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.RemoveSessionAfter(tmux.CallCapture, r.Socket, r.Session.ID)
		}, "ErrTmuxCaptureFailed", gone},
		{"gone, server exited", noServer, readPaneAfterCapture(func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.StopServer(r.Socket).SetNoServerFailure(r.Socket, tmux.FailNoServer)
			e.syncServers()
		}), "ErrTmuxCaptureFailed", gone},
		{"leftover", noServer, func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.ReplaceSessionAfter(tmux.CallCapture, r.Socket, r.Session.ID, r.old())
		}, "ErrTmuxCaptureFailed", gone},
		{"unreadable, timeout", noServer, readPaneFollowUpLookup(tmuxfix.Script{Failure: tmux.FailTimeout}),
			"ErrTmuxUnresponsive", unresponsive},
		{"unreadable, unrecognised reply", noServer, readPaneFollowUpLookup(tmuxfix.Script{Failure: tmux.FailUnrecognized,
			FirstLine: callTableFirstLine(), ExitStatus: 1}), "ErrTmuxUnresponsive", unresponsive},
		{"provenance conflict, scope value", noServer, readPaneAfterCapture(func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}), "ErrTmuxUnresponsive", unresponsive},
		{"provenance conflict, duplicate label", noServer, readPaneAfterCapture(func(t *testing.T, e *killEnv, r killRow) {
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
		}), "ErrTmuxUnresponsive", unresponsive},
		{"different server", noServer, readPaneAfterCapture(func(t *testing.T, e *killEnv, r killRow) {
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedBystander(t, r.Socket)
		}), "ErrTmuxNotAvailable", func(_ *killEnv, r killRow) apitest.DescCase { return apitest.DescDifferentServer(r.ID) }},
		{"tmux unavailable, missing binary", noServer, readPaneFollowUpLookup(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			"ErrTmuxNotAvailable", func(*killEnv, killRow) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"tmux unavailable, socket permission", noServer, readPaneFollowUpLookup(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			"ErrTmuxNotAvailable", func(_ *killEnv, r killRow) apitest.DescCase { return apitest.DescSocketPermission(r.Socket) }},
		{"ours after a pane-vanished reply", tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: paneReply, ExitStatus: 1},
			nil, "ErrTmuxUnresponsive", func(*killEnv, killRow) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallCapture, paneReply)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.seedBystander(t, r.Socket)
			e.rec.Script(r.Socket, tc.capture, tmux.CallCapture)
			if tc.world != nil {
				tc.world(t, e, r)
			}
			before := e.columns(t, r.ID)

			_, err := readPaneCallTableInvoke(t, e, r)

			assertOneName(t, err, tc.errName)
			apitest.AssertDescription(t, err.Error(), tc.desc(e, r), r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
			e.assertPaneCalls(t, withFollowUp(paneReadCalls)...)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

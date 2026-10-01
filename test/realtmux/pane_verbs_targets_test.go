package realtmux_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The pane verbs' targeting on real tmux (SRD SR-20.7, SR-3.7, SR-7.2;
// Appendix E.9 T2a): sessions named like a session id or a pane id are never
// captured or typed into, and with base-index and pane-base-index 1 the
// verbs still reach the agent's pane by its id. id_targets_test.go proves T2a
// for the tmux client; these tests prove it for the verbs.

// idShapedWorld is one server holding raw sessions named like the absent ids
// $7 and %9 (the catalogue's id-shaped names), a row whose recorded pane id
// is the never-issued %9, a row whose own session, named $7, has ended, and a
// barrier session.
type idShapedWorld struct {
	NotFound, Gone           killRow
	Dollar, Percent, Barrier rawSession
}

// newIDShapedWorld builds the idShapedWorld on f's socket and checks neither
// id exists.
func newIDShapedWorld(t *testing.T, f *killFix) idShapedWorld {
	t.Helper()
	dollar, percent := idtIDShapedName(t, '$'), idtIDShapedName(t, '%')
	w := idShapedWorld{NotFound: f.liveRow(t, killRowSpec{PaneID: percent.Raw}), Gone: f.liveRow(t, killRowSpec{Name: dollar.Raw})}
	f.must(t, "kill-session", "-t", w.Gone.Reply.SessionID)
	waitPidGone(t, w.Gone.Reply.PanePID)
	w.Dollar, w.Percent, w.Barrier = f.startSession(t, dollar.Raw), f.startSession(t, percent.Raw), f.startSession(t, "")
	world := idtWorld(t, f.realTmux)
	for id, p := range world {
		if p.SessionID == dollar.Raw || id == percent.Raw {
			t.Fatalf("precondition: a session or pane already has the id a name spells:%s", idtDump(world))
		}
	}
	if world[w.Dollar.PaneID].SessionName != dollar.Stored || world[w.Percent.PaneID].SessionName != percent.Stored {
		t.Fatalf("precondition: sessions are not listed under the catalogue's stored names:%s", idtDump(world))
	}
	return w
}

// TestPaneVerbIDShapedNamesNeverReached (E.9 T2a): with the agent's pane not
// found or its session gone, each pane verb refuses and never reaches the $7 or %9 session.
func TestPaneVerbIDShapedNamesNeverReached(t *testing.T) {
	f := newKillFix(t)
	w := newIDShapedWorld(t, f)
	rows := []struct {
		desc string
		row  killRow
		want func(verb apitest.PaneVerb, r killRow) (string, apitest.DescCase)
	}{
		{"pane not found", w.NotFound, func(verb apitest.PaneVerb, r killRow) (string, apitest.DescCase) {
			return "ErrTmuxSessionConflict", apitest.DescPaneNotFound(apitest.PaneNotFound{Verb: verb, InstanceID: r.InstanceID, Name: r.Name})
		}},
		{"session gone", w.Gone, func(verb apitest.PaneVerb, r killRow) (string, apitest.DescCase) {
			return paneGoneName(verb), apitest.DescPaneGone(apitest.PaneGone{Verb: verb, InstanceID: r.InstanceID, Name: r.Name})
		}},
	}
	for _, rc := range rows {
		for _, verb := range paneVerbs {
			t.Run(rc.desc+"/"+string(verb), func(t *testing.T) {
				before := idtWorld(t, f.realTmux)

				call := f.paneVerb(t, verb, rc.row.InstanceID, paneMarker(t))
				name, c := rc.want(verb, rc.row)
				assertVerbError(t, string(verb), call.Err, name, c, rc.row.Token, f.StoreID)
				idtSendAndSee(t, f.realTmux, f.Client, w.Barrier.PaneID)
				idtSame(t, "the refused "+string(verb), before, idtWorld(t, f.realTmux),
					w.Dollar.PaneID, w.Percent.PaneID, w.NotFound.Reply.PaneID)
				assertProcs(t, false, w.Dollar.PanePID, w.Percent.PanePID)
				f.assertRowUnchanged(t, rc.row.InstanceID, rc.row.Before)
			})
		}
	}
}

// TestPaneVerbBaseIndexOneReachesPaneByID: with base-index and pane-base-index
// 1, each verb reaches a lost reply's pane by @ad_pane; only send-keys and pause adopt it.
func TestPaneVerbBaseIndexOneReachesPaneByID(t *testing.T) {
	cases := []struct {
		verb  apitest.PaneVerb
		state string
	}{
		{apitest.PaneReadPane, "pending"},
		{apitest.PaneSendKeys, "pending"},
		{apitest.PanePause, "waiting"},
	}
	for _, tc := range cases {
		t.Run(string(tc.verb), func(t *testing.T) {
			f := newKillFix(t)
			r := f.baseIndexOneLostReply(t, tc.state)
			text := paneMarker(t)
			if tc.verb == apitest.PaneReadPane {
				text = idtSendAndSee(t, f.realTmux, f.Client, r.Reply.PaneID)
			}
			world := idtWorld(t, f.realTmux)

			call := f.paneVerb(t, tc.verb, r.InstanceID, text)
			if call.Err != nil {
				t.Fatalf("%s: %s; want success", tc.verb, describe(call.Err))
			}
			untouched := idtPaneIDs(world)
			switch tc.verb {
			case apitest.PaneReadPane:
				if !strings.Contains(call.Pane, text) {
					t.Errorf("read-pane = %q; want the agent's pane showing %q", redact(call.Pane), text)
				}
				f.assertRowUnchanged(t, r.InstanceID, r.Before)
			default:
				if tc.verb == apitest.PanePause {
					text = "/exit"
				}
				waitPaneShows(t, f.realTmux, r.Reply.PaneID, text)
				untouched = slices.DeleteFunc(untouched, func(id string) bool { return id == r.Reply.PaneID })
				f.assertAdopted(t, r, tc.state)
			}
			idtSame(t, string(tc.verb), world, idtWorld(t, f.realTmux), untouched...)
		})
	}
}

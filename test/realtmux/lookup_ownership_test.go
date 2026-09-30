package realtmux_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The Ours side of the shared lookup on real tmux (SRD SR-3.3, SR-3.4,
// SR-3.7, SR-3.8, SR-3.10; provenance-fresh F4, F6; AC-LKP-16/18): the match
// baseline, a renamed session, a grouped viewer and a linked window sharing
// the agent's pane, and a remain-on-exit session outliving its agent.

// oursWant is the Ours result for agent a with the given holder, class and
// server status; no leftovers and no disagree reason.
func oursWant(a agent, holder string, class tmux.LabelClass, server string) wantResult {
	return wantResult{Verdict: tmux.Ours, Token: "ours", Session: a.Reply.SessionID,
		Holder: holder, HolderClass: class, Server: server}
}

// panesOf lists the pane ids of every window of session target on rt.
func panesOf(t testing.TB, rt *realTmux, target string) []string {
	t.Helper()
	return strings.Fields(rt.must(t, "list-panes", "-s", "-t", target, "-F", "#{pane_id}"))
}

// TestLookupOursMatch: an agent the production create made reads Ours with
// its recorded server identity (match) and without one (unknown, adopted).
func TestLookupOursMatch(t *testing.T) {
	f := newLookupFix(t)
	a := f.agent(t, createSpec{})
	cases := []struct {
		name   string
		opts   []rowOption
		server string
		adopt  bool
	}{
		{"recorded server identity", nil, tmux.ServerMatch, false},
		{"no recorded server identity", []rowOption{withoutIdentity()}, tmux.ServerUnknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.lookup(a.row(tc.opts...), a.Name)
			assertResult(t, tc.name, got, oursWant(a, a.Reply.SessionID, tmux.ClassCurrent, tc.server))
			if got.Adopt != tc.adopt {
				t.Errorf("Adopt = %v, want %v", got.Adopt, tc.adopt)
			}
			if got.ServerPID != a.Reply.ServerPID || got.ServerStart != a.Reply.ServerStart {
				t.Errorf("answering server = (%d, %d), want the create reply's (%d, %d)",
					got.ServerPID, got.ServerStart, a.Reply.ServerPID, a.Reply.ServerStart)
			}
		})
	}
}

// TestLookupOursRenamed: after a raw rename-session the agent is still Ours by
// its session id; the new name's holder is that session, the old name is free.
func TestLookupOursRenamed(t *testing.T) {
	f := newLookupFix(t)
	a := f.agent(t, createSpec{})
	renamed := uniqueName()
	f.must(t, "rename-session", "-t", a.Reply.SessionID, renamed)
	cases := []struct {
		name, holderName, holder string
		class                    tmux.LabelClass
	}{
		{"new name held by the agent's session", renamed, a.Reply.SessionID, tmux.ClassCurrent},
		{"old name not held", a.Name, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.lookup(a.row(), tc.holderName)
			assertResult(t, tc.name, got, oursWant(a, tc.holder, tc.class, tmux.ServerMatch))
			if got.Session.Name != renamed {
				t.Errorf("Ours session name = %q, want the new name %q", got.Session.Name, renamed)
			}
		})
	}
}

// TestLookupOursGroupedAndLinked: a grouped viewer and a session linking the
// agent's window share its pane but carry no label; killing the agent's
// session reads Gone while the shared pane's agent stays alive.
func TestLookupOursGroupedAndLinked(t *testing.T) {
	f := newLookupFix(t)
	a := f.agent(t, createSpec{})
	viewerName := uniqueName()
	viewer := strings.TrimSpace(f.must(t, "new-session", "-d", "-t", a.Reply.SessionID, "-s", viewerName, "-P", "-F", "#{session_id}"))
	linked := f.startSession(t, "")
	linkedName := f.format(t, linked.ID, "#{session_name}")
	window := f.format(t, a.Reply.PaneID, "#{window_id}")
	f.must(t, "link-window", "-d", "-s", window, "-t", linked.ID+":9")

	for _, sid := range []string{a.Reply.SessionID, viewer, linked.ID} {
		if panes := panesOf(t, f.realTmux, sid); !slices.Contains(panes, a.Reply.PaneID) {
			t.Errorf("session %s panes %v do not include the agent's pane %s", sid, panes, a.Reply.PaneID)
		}
	}
	for _, sid := range []string{viewer, linked.ID} {
		if f.label(t, sid) != "" {
			t.Errorf("session %s carries its own @ad_owner value; want none", sid)
		}
	}

	holders := []struct {
		name, holderName, holder string
		class                    tmux.LabelClass
	}{
		{"agent's name", a.Name, a.Reply.SessionID, tmux.ClassCurrent},
		{"grouped viewer's name", viewerName, viewer, tmux.ClassNone},
		{"linked session's name", linkedName, linked.ID, tmux.ClassNone},
	}
	for _, h := range holders {
		t.Run(h.name, func(t *testing.T) {
			assertResult(t, h.name, f.lookup(a.row(), h.holderName), oursWant(a, h.holder, h.class, tmux.ServerMatch))
		})
	}

	f.must(t, "kill-session", "-t", a.Reply.SessionID)
	assertResult(t, "after killing the agent's session", f.lookup(a.row(), a.Name),
		wantResult{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch})
	if panes := panesOf(t, f.realTmux, viewer); !slices.Contains(panes, a.Reply.PaneID) {
		t.Errorf("after the session kill the viewer's panes %v lost the agent's pane %s", panes, a.Reply.PaneID)
	}
	if got := f.judge(a.pane()); got != tmux.ProcAlive {
		t.Errorf("agent's pane process after the session kill: judgement %d, want alive (%d)", got, tmux.ProcAlive)
	}
}

// TestLookupOursRemainOnExit: with remain-on-exit on, the agent's session
// stays Ours after its pane process ends (pane_dead 1), which judges gone.
func TestLookupOursRemainOnExit(t *testing.T) {
	f := newLookupFix(t)
	a := f.agent(t, createSpec{})
	f.must(t, "set-option", "-w", "-t", a.Reply.SessionID, "remain-on-exit", "on")
	if err := syscallKill(a.Reply.PanePID); err != nil {
		t.Fatalf("end the agent's pane process %d: %v", a.Reply.PanePID, err)
	}
	waitFor(t, "agent's pane reads pane_dead 1",
		func() bool { return f.format(t, a.Reply.PaneID, "#{pane_dead}") == "1" },
		func() string { return "pane_dead " + f.format(t, a.Reply.PaneID, "#{pane_dead}") })

	assertResult(t, "dead pane kept by remain-on-exit", f.lookup(a.row(), a.Name),
		oursWant(a, a.Reply.SessionID, tmux.ClassCurrent, tmux.ServerMatch))
	if got := f.judge(a.pane()); got != tmux.ProcGone {
		t.Errorf("agent's pane process after it ended: judgement %d, want gone (%d)", got, tmux.ProcGone)
	}
}

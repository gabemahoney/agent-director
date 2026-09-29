package realtmux_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The create's @ad_owner label on real tmux (SRD SR-20.7 provenance bullet,
// SR-3.5; RN-4 R2a/R2b; provenance-fresh F1, F3, F7; AC-LKP-16/17). Label
// values are compared, never printed: failure messages name session ids only.

// labelState is every session's label on a server: the raw @ad_owner value
// read by id, and the session as the production lookup reports it.
type labelState struct {
	raw    map[string]string
	listed map[string]tmux.Session
}

// labelState reads every session's raw label by id and the production
// lookup's answer for the bound socket.
func (r *realTmux) labelState(t testing.TB) labelState {
	t.Helper()
	st := labelState{raw: map[string]string{}, listed: map[string]tmux.Session{}}
	for _, id := range strings.Fields(r.must(t, "list-sessions", "-F", "#{session_id}")) {
		st.raw[id] = r.label(t, id)
	}
	ans, err := newClient().Lookup(r.Socket)
	if err != nil {
		t.Fatalf("production lookup on %s: %s", r.Socket, describe(err))
	}
	for _, s := range ans.Sessions {
		st.listed[s.ID] = s
	}
	if ans.ScopeValue {
		t.Errorf("lookup reports a scope-level @ad_owner value; no call may set one")
	}
	return st
}

// assertOnlyLabelChange: from before to after only session id changed, to raw
// label wantRaw ("" for none) classified as want; no other label moved.
func assertOnlyLabelChange(t testing.TB, before, after labelState, id, wantRaw string, want tmux.Label) {
	t.Helper()
	for sid, raw := range before.raw {
		if sid == id {
			continue
		}
		if got, ok := after.raw[sid]; !ok || got != raw {
			t.Errorf("session %s: raw label changed or session gone (present %v)", sid, ok)
		}
		if after.listed[sid].Label != before.listed[sid].Label {
			t.Errorf("session %s: lookup label class changed", sid)
		}
	}
	for sid := range after.raw {
		if _, ok := before.raw[sid]; !ok && sid != id {
			t.Errorf("unexpected new session %s", sid)
		}
	}
	if got, ok := after.raw[id]; !ok || got != wantRaw {
		t.Errorf("session %s: raw label read by id is not the expected value (present %v, set %v, want set %v)",
			id, ok, got != "", wantRaw != "")
	}
	s, ok := after.listed[id]
	if !ok {
		t.Fatalf("production lookup does not list session %s", id)
	}
	if s.Label != want {
		t.Errorf("session %s: lookup label = {Kind %d, token match %v, id match %v}, want Kind %d",
			id, s.Label.Kind, s.Label.Token == want.Token, s.Label.InstanceID == want.InstanceID, want.Kind)
	}
}

// validLabelShapes returns the catalogue's valid label shapes whose instance
// id stands alone on the line (plain, '#', spaces, UTF-8).
func validLabelShapes(t testing.TB) []tmuxfix.LabelShape {
	t.Helper()
	var out []tmuxfix.LabelShape
	for _, s := range tmuxfix.LabelShapes() {
		if s.Want.Kind == tmux.LabelValid && !s.ScopeValue {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Fatalf("the catalogue has no valid label shape")
	}
	return out
}

// enterPane sets TMUX and TMUX_PANE from c's pane process, then checks an
// untargeted call resolves to c's session (later clients run inside that pane).
func (r *realTmux) enterPane(t testing.TB, c created) {
	t.Helper()
	env := procEnviron(t, c.Reply.PanePID)
	tmuxVar, ok1 := envValue(env, "TMUX")
	paneVar, ok2 := envValue(env, "TMUX_PANE")
	if !ok1 || !ok2 {
		t.Fatalf("pane process %d lacks TMUX (%v) or TMUX_PANE (%v)", c.Reply.PanePID, ok1, ok2)
	}
	if paneVar != c.Reply.PaneID {
		t.Fatalf("pane process TMUX_PANE = %q, want the create reply's pane id %q", paneVar, c.Reply.PaneID)
	}
	t.Setenv("TMUX", tmuxVar)
	t.Setenv("TMUX_PANE", paneVar)
	if got := strings.TrimSuffix(r.must(t, "display-message", "-p", "#{session_id}"), "\n"); got != c.Reply.SessionID {
		t.Fatalf("untargeted call from inside the pane resolves to session %q, want the holder %q", got, c.Reply.SessionID)
	}
}

// TestLabelChainedCreate: a created session carries ad1 <token> <own $N> <id>, for
// every valid id shape ('#' included), detached or from inside a labelled pane.
func TestLabelChainedCreate(t *testing.T) {
	for _, shape := range validLabelShapes(t) {
		for _, inside := range []bool{false, true} {
			where := "detached"
			if inside {
				where = "inside-pane"
			}
			t.Run(shape.Name+"/"+where, func(t *testing.T) {
				rt := newRealTmux(t)
				holder := rt.mustCreate(t, createSpec{})
				rt.startSession(t, "") // an unlabelled bystander
				if inside {
					rt.enterPane(t, holder)
				}
				before := rt.labelState(t)
				if before.listed[holder.Reply.SessionID].Label != tmuxfix.Valid(holder.Token, holder.InstanceID) {
					t.Fatalf("holder %s is not validly labelled before the create", holder.Reply.SessionID)
				}

				c := rt.mustCreate(t, createSpec{InstanceID: newInstanceID(shape.Want.InstanceID)})
				after := rt.labelState(t)
				id := c.Reply.SessionID
				if _, clash := before.raw[id]; clash {
					t.Fatalf("create reply names existing session %s", id)
				}
				assertOnlyLabelChange(t, before, after, id,
					tmuxfix.LabelValue(c.Token, id, c.InstanceID), tmuxfix.Valid(c.Token, c.InstanceID))
				if got := after.listed[id].Name; got != c.Name {
					t.Errorf("session %s: lookup name = %q, want %q", id, got, c.Name)
				}
				if after.raw[holder.Reply.SessionID] != before.raw[holder.Reply.SessionID] {
					t.Errorf("holder %s: label read by id changed", holder.Reply.SessionID)
				}
			})
		}
	}
}

// labelByIDNames returns F3's names that must be labelled by id, and the one
// among them that is a session id's text ($7).
func labelByIDNames(t testing.TB) (names []tmuxfix.StoredName, collider tmuxfix.StoredName) {
	t.Helper()
	for _, n := range tmuxfix.StoredNames() {
		if !strings.Contains(n.Source, "F3") {
			continue
		}
		if !n.LabelByID || !tmux.NeedsLabelByID(n.Raw) {
			t.Fatalf("catalogue F3 name %q is not a label-by-id name", n.Raw)
		}
		names = append(names, n)
		if strings.HasPrefix(n.Raw, "$") && strings.Trim(n.Raw[1:], "0123456789") == "" {
			collider = n
		}
	}
	if len(names) == 0 || collider.Raw == "" {
		t.Fatalf("catalogue lacks F3's $N-text name")
	}
	return names, collider
}

// TestLabelDollarNameByID proves F3: a $-bearing name gets no chain, even when
// a session with that id exists, and is labelled only by SetLabel on its id.
func TestLabelDollarNameByID(t *testing.T) {
	names, collider := labelByIDNames(t)
	for _, name := range names {
		for _, shape := range validLabelShapes(t) {
			t.Run(name.Raw+"/"+shape.Name, func(t *testing.T) {
				rt := newRealTmux(t)
				// Labelled sessions $0, $1, ... up to the one whose id is
				// the collider's text, as in F3.
				for n := 0; ; n++ {
					if n == 32 {
						t.Fatalf("no session id %s after %d creates", collider.Raw, n)
					}
					if rt.mustCreate(t, createSpec{}).Reply.SessionID == collider.Raw {
						break
					}
				}
				before := rt.labelState(t)
				if before.listed[collider.Raw].Label.Kind != tmux.LabelValid {
					t.Fatalf("session %s is not validly labelled before the create", collider.Raw)
				}

				c := rt.mustCreate(t, createSpec{Name: name.Raw, InstanceID: newInstanceID(shape.Want.InstanceID)})
				id := c.Reply.SessionID
				if _, clash := before.raw[id]; clash {
					t.Fatalf("create reply names existing session %s", id)
				}
				created := rt.labelState(t)
				assertOnlyLabelChange(t, before, created, id, "", tmux.Label{})
				if got := created.listed[id].Name; got != name.Stored {
					t.Errorf("session %s: lookup name = %q, want the stored form %q", id, got, name.Stored)
				}

				if err := newClient().SetLabel(rt.Socket, id, c.Token, c.InstanceID); err != nil {
					t.Fatalf("SetLabel %s: %s", id, describe(err))
				}
				labelled := rt.labelState(t)
				assertOnlyLabelChange(t, created, labelled, id,
					tmuxfix.LabelValue(c.Token, id, c.InstanceID), tmuxfix.Valid(c.Token, c.InstanceID))
			})
		}
	}
}

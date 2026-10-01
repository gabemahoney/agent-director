package realtmux_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The README "Operator actions" item "A row whose recorded name cannot be
// used" on real tmux (SRD SR-18.17 steps 2-5, SR-20.7 last bullet, Appendix
// E.10 N6; PRD AC-DOC-18): tmux's rewriting of '.', ':' and invalid UTF-8 in
// a new session's name, and the procedure's by-id tmux steps followed by
// Client.Delete. Label values are compared, never printed.

// n6Names returns the catalogue's E.10 N6 names, which between them hold
// '.', ':' and a byte that is not valid UTF-8.
func n6Names(t testing.TB) []tmuxfix.StoredName {
	t.Helper()
	var out []tmuxfix.StoredName
	dot, colon, bad := false, false, false
	for _, n := range tmuxfix.StoredNames() {
		if n.Source != "E.10 N6" {
			continue
		}
		out = append(out, n)
		dot = dot || strings.Contains(n.Raw, ".")
		colon = colon || strings.Contains(n.Raw, ":")
		bad = bad || !utf8.ValidString(n.Raw)
	}
	if !dot || !colon || !bad {
		t.Fatalf("the catalogue's N6 names lack '.' (%v), ':' (%v) or invalid UTF-8 (%v)", dot, colon, bad)
	}
	return out
}

// dotX returns the catalogue's N6 entry dot.x, stored as dot_x.
func dotX(t testing.TB) tmuxfix.StoredName {
	t.Helper()
	for _, n := range n6Names(t) {
		if n.Raw == "dot.x" {
			return n
		}
	}
	t.Fatalf("the catalogue has no N6 entry dot.x")
	return tmuxfix.StoredName{}
}

// listedSession is one line of step 2's listing.
type listedSession struct{ ID, Name string }

// procedureListing runs step 2's list-sessions -F '#{session_id}
// #{session_created} #{session_name}' and returns its sessions in order.
// Later procedure tests list with it, never their own list-sessions.
func (r *realTmux) procedureListing(t testing.TB) []listedSession {
	t.Helper()
	var out []listedSession
	for _, line := range strings.Split(strings.TrimSuffix(r.must(t, "list-sessions", "-F", "#{session_id} #{session_created} #{session_name}"), "\n"), "\n") {
		f := strings.SplitN(line, " ", 3)
		if len(f) != 3 {
			t.Fatalf("listing line %q has %d fields, want 3", line, len(f))
		}
		if _, err := strconv.ParseInt(f[1], 10, 64); err != nil {
			t.Errorf("listing line %q: session_created is not a number", line)
		}
		out = append(out, listedSession{ID: f[0], Name: f[2]})
	}
	return out
}

// procCandidate is a session listed under the stored form: what its label
// and environment say about the row, and whether the procedure ended it.
type procCandidate struct {
	ID    string
	Class tmux.LabelClass // the label's class for the row, by the production lookup
	Env   string          // step 3's show-environment line, "" when unset
	Ended bool
}

// procedureRemove runs the README steps 2 to 5 for row (recorded name n) by
// session id only: it ends each candidate whose label, or with no label
// whose environment, names row's id, checks it left the listing, then
// deletes the row through Client.Delete and checks it left the store.
// Later README procedure tests run their steps with it, never their own copy.
func (f *killFix) procedureRemove(t *testing.T, row tmux.Launch, n tmuxfix.StoredName) []procCandidate {
	t.Helper()
	var cands []procCandidate
	for _, s := range f.procedureListing(t) {
		if s.Name == n.Stored {
			cands = append(cands, procCandidate{ID: s.ID})
		}
	}
	labels := f.labelState(t)
	ownEnv := probe.EnvKey + "=" + row.InstanceID
	for i := range cands {
		c := &cands[i]
		// Without -q, tmux 3.3a refuses an unset @ad_owner with a non-zero
		// exit; a set one prints the value the harness reads by id.
		read := f.run(t, "show-options", "-t", c.ID, "-v", "@ad_owner")
		if set := labels.raw[c.ID] != ""; set != (read.Exit == 0) || strings.TrimSuffix(read.Stdout, "\n") != labels.raw[c.ID] {
			t.Errorf("session %s: step 3's label read (exit %d) differs from the harness's read by id (set %v)", c.ID, read.Exit, set)
		}
		c.Class = row.ClassOf(labels.listed[c.ID].Label)
		if res := f.run(t, "show-environment", "-t", c.ID, probe.EnvKey); res.Exit == 0 {
			c.Env = strings.TrimSuffix(res.Stdout, "\n")
		}
		own := c.Class == tmux.ClassCurrent || c.Class == tmux.ClassOld || (c.Class == tmux.ClassNone && c.Env == ownEnv)
		if !own {
			continue
		}
		f.must(t, "kill-session", "-t", c.ID)
		c.Ended = true
		for _, s := range f.procedureListing(t) {
			if s.ID == c.ID {
				t.Errorf("session %s is still listed after its kill by id", c.ID)
			}
		}
	}

	cl := f.open(t, 0, "")
	defer cl.Close() //nolint:errcheck
	res, err := cl.Delete([]string{row.InstanceID})
	if err != nil || res.Results[row.InstanceID] != "ok" {
		t.Fatalf("Delete %s: results %v, error %s; want ok", row.InstanceID, res.Results, describe(err))
	}
	if _, err := apitest.ReadSpawnColumns(f.DBPath, row.InstanceID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("read row %s after Delete: %v; want ErrSpawnNotFound", row.InstanceID, err)
	}
	return cands
}

// TestUnusableNameProcedureRewritingPremise: tmux lists each N6 name under
// its stored form with its session id, and the name target =dot.x does not
// reach dot_x's session.
func TestUnusableNameProcedureRewritingPremise(t *testing.T) {
	dot := dotX(t)
	for _, n := range n6Names(t) {
		t.Run(strconv.Quote(n.Raw), func(t *testing.T) {
			rt := newRealTmux(t)
			s := rt.startSession(t, n.Raw)
			if got, want := rt.procedureListing(t), []listedSession{{ID: s.ID, Name: n.Stored}}; !slices.Equal(got, want) {
				t.Errorf("listing = %q, want %q", got, want)
			}
			if n != dot {
				return
			}
			if res := rt.run(t, "kill-session", "-t", "="+n.Raw); res.Exit == 0 {
				t.Errorf("kill-session by the name target =%s exited 0; want a non-zero exit", n.Raw)
			}
			rt.assertSession(t, s.ID, true)
			assertProcs(t, false, s.PanePID)
		})
	}
}

// TestUnusableNameProcedureOwnedSession: a dot.x row's own dot_x session,
// labelled or carrying only the id in its environment, is found, ended by
// id, and the row deleted.
func TestUnusableNameProcedureOwnedSession(t *testing.T) {
	cases := []struct {
		desc      string
		labelled  bool
		wantClass tmux.LabelClass
	}{
		{desc: "labelled by session id", labelled: true, wantClass: tmux.ClassCurrent},
		{desc: "no label, environment hint only", wantClass: tmux.ClassNone},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			f := newKillFix(t)
			n := dotX(t)
			bystander := f.startSession(t, "") // keeps the server up for step 4's second listing
			row := tmux.Launch{InstanceID: newInstanceID("agent"), Token: newToken(t), StoreID: f.StoreID, Socket: f.Socket}
			s := f.startSessionWithID(t, n.Raw, row.InstanceID)
			if tc.labelled {
				if err := f.Client.SetLabel(f.Socket, s.ID, s.PaneID, row.Token, row.InstanceID, row.StoreID); err != nil {
					t.Fatalf("label session %s by id: %s", s.ID, describe(err))
				}
				if f.label(t, s.ID) != tmuxfix.LabelValue(row.Token, s.ID, row.InstanceID, row.StoreID) {
					t.Fatalf("session %s: label read by id is not the label builder's value", s.ID)
				}
			}
			srv := f.serverOf(t, f.realTmux, s.ID)
			f.seedRow(t, row.InstanceID, n.Raw, "", store.LaunchIdentity{Token: row.Token, Socket: f.Socket,
				ServerPID: srv.PID, ServerStart: srv.Start, ServerStarttime: srv.Starttime,
				PaneID: s.PaneID, PanePID: s.PanePID, PaneStarttime: f.startOf(t, s.PanePID)})

			got := f.procedureRemove(t, row, n)
			want := []procCandidate{{ID: s.ID, Class: tc.wantClass, Env: probe.EnvKey + "=" + row.InstanceID, Ended: true}}
			if !slices.Equal(got, want) {
				t.Errorf("candidates = %+v, want %+v", got, want)
			}
			f.assertSession(t, s.ID, false)
			waitPidGone(t, s.PanePID)
			f.assertSession(t, bystander.ID, true)
		})
	}
}

// TestUnusableNameProcedureOtherRowsSession: another row's labelled dot_x session
// is left running and its row unchanged, even with the dot.x row's id in its environment.
func TestUnusableNameProcedureOtherRowsSession(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		desc := "environment names the other row"
		if inherited {
			desc = "environment inherited from the dot.x row"
		}
		t.Run(desc, func(t *testing.T) {
			f := newKillFix(t)
			n := dotX(t)
			bystander := f.startSession(t, "") // keeps the server up should the procedure end dot_x
			other := f.liveRow(t, killRowSpec{Name: n.Stored})
			row := tmux.Launch{InstanceID: newInstanceID("agent"), Token: newToken(t), StoreID: f.StoreID, Socket: f.Socket}
			f.seedRow(t, row.InstanceID, n.Raw, "", store.LaunchIdentity{Token: row.Token, Socket: f.Socket,
				ServerPID: other.Server.PID, ServerStart: other.Server.Start, ServerStarttime: other.Server.Starttime})
			env := probe.EnvKey + "=" + other.InstanceID
			if inherited {
				f.must(t, "set-environment", "-t", other.Reply.SessionID, probe.EnvKey, row.InstanceID)
				env = probe.EnvKey + "=" + row.InstanceID
			}

			got := f.procedureRemove(t, row, n)
			want := []procCandidate{{ID: other.Reply.SessionID, Class: tmux.ClassForeign, Env: env}}
			if !slices.Equal(got, want) {
				t.Errorf("candidates = %+v, want %+v", got, want)
			}
			f.assertSession(t, other.Reply.SessionID, true)
			f.assertSession(t, bystander.ID, true)
			assertProcs(t, false, other.Reply.PanePID)
			f.assertRowUnchanged(t, other.InstanceID, other.Before)
		})
	}
}

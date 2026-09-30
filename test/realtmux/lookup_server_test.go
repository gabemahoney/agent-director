package realtmux_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The lookup's server check against real tmux 3.3a (SRD SR-3.3, SR-3.4,
// SR-20.7; AC-LKP-19): kill-server, a restart on the same socket and a
// re-bound socket, judged by the production start-time reader.

// serverChange turns the socket of a's server into one case's state and
// returns the session on the socket's new server ("" and a zero identity
// when no server answers).
type serverChange func(t *testing.T, f *lookupFix, a agent) (string, serverIdentity)

// noServer ends a's server and starts none.
func noServer(t *testing.T, f *lookupFix, a agent) (string, serverIdentity) {
	f.endServer(t, f.realTmux, a.Server)
	return "", serverIdentity{}
}

// restartServer ends a's server, starts a new one on the same socket with an
// unlabelled session and, when relabel, sets a's current label on it by id.
func restartServer(relabel bool) serverChange {
	return func(t *testing.T, f *lookupFix, a agent) (string, serverIdentity) {
		f.endServer(t, f.realTmux, a.Server)
		s := f.startSession(t, "")
		if relabel {
			if err := f.Client.SetLabel(f.Socket, s.ID, s.PaneID, a.Token, a.InstanceID, tmuxfix.StoreID); err != nil {
				t.Fatalf("label the new server's session by id: %s", describe(err))
			}
		}
		srv := f.serverOf(t, f.realTmux, s.ID)
		if srv.PID == a.Server.PID && srv.Start == a.Server.Start {
			t.Fatalf("the new server on %s has the old server's identity (pid %d, start %d)", f.Socket, srv.PID, srv.Start)
		}
		return s.ID, srv
	}
}

// rebindServer keeps a's server running on a moved socket and starts a new
// server at a's socket path with an agent from spec(a); both end in cleanup.
func rebindServer(spec func(agent) createSpec) serverChange {
	return func(t *testing.T, f *lookupFix, a agent) (string, serverIdentity) {
		recorded := tmux.ProcIdentity{PID: a.Server.PID, Starttime: a.Server.Starttime}
		if got := f.judge(recorded); got != tmux.ProcAlive {
			t.Fatalf("the production reader judges the created server %d as %v, want alive", a.Server.PID, got)
		}
		if env := cleanADVars(procEnviron(t, a.Server.PID)); len(env) != 0 {
			t.Errorf("the created server %d has AGENT_DIRECTOR_* variables %v, want none", a.Server.PID, env)
		}
		aside, fresh := f.rebind(t, f.realTmux, spec(a))
		assertResult(t, "the old server on the moved socket", f.lookup(a.row(withSocket(aside.Socket)), ""),
			wantResult{Verdict: tmux.Ours, Token: "ours", Session: a.Reply.SessionID, Server: tmux.ServerMatch})
		return fresh.Reply.SessionID, fresh.Server
	}
}

// TestLookup_ServerCheck: a no-server reply, a restarted server and a
// re-bound socket read as SR-3.3 says, with and without a recorded identity.
func TestLookup_ServerCheck(t *testing.T) {
	otherAgent := func(agent) createSpec { return createSpec{} }
	sameLabel := func(a agent) createSpec { return createSpec{InstanceID: a.InstanceID, Token: a.Token} }
	tests := []struct {
		name     string
		change   serverChange
		labelled bool       // the new server's session carries the row's current label
		recorded wantResult // the row with its recorded server identity; Session filled in below
	}{
		{
			name:     "no server after kill-server",
			change:   noServer,
			recorded: wantResult{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerRestarted},
		},
		{
			name:   "restarted, unlabelled session",
			change: restartServer(false),
			recorded: wantResult{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerRestarted,
				Disagree: []string{tmux.ReasonServerRestarted}},
		},
		{
			name:     "restarted, session relabelled by id",
			change:   restartServer(true),
			labelled: true,
			recorded: wantResult{Verdict: tmux.Ours, Token: "ours", Server: tmux.ServerRestarted,
				Disagree: []string{tmux.ReasonServerRestarted}},
		},
		{
			name:   "re-bound socket, another agent",
			change: rebindServer(otherAgent),
			recorded: wantResult{Verdict: tmux.CantTell, CantTell: tmux.CantTellDifferentServer, Token: "different_server",
				Server: tmux.ServerDiffers, Disagree: []string{tmux.ReasonServerMismatch}},
		},
		{
			name:     "re-bound socket, the row's current label",
			change:   rebindServer(sameLabel),
			labelled: true,
			recorded: wantResult{Verdict: tmux.CantTell, CantTell: tmux.CantTellDifferentServer, Token: "different_server",
				Server: tmux.ServerDiffers, Disagree: []string{tmux.ReasonServerMismatch}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFix(t)
			a := f.agent(t, createSpec{})
			session, srv := tc.change(t, f, a)

			unrecorded := wantResult{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerUnknown}
			if tc.labelled {
				unrecorded.Verdict, unrecorded.Token, unrecorded.Session = tmux.Ours, "ours", session
			}
			if tc.recorded.Verdict == tmux.Ours {
				tc.recorded.Session = session
			}
			rows := []struct {
				what  string
				row   tmux.Launch
				want  wantResult
				adopt bool
			}{
				{"recorded identity", a.row(), tc.recorded, false},
				{"no recorded identity", a.row(withoutIdentity()), unrecorded, tc.labelled},
			}
			for _, r := range rows {
				got := f.lookup(r.row, "")
				t.Logf("%s: %s, server %q, disagree %v", r.what, got.Token(), got.Server, got.Disagree)
				assertResult(t, r.what, got, r.want)
				if got.ServerPID != srv.PID || got.ServerStart != srv.Start {
					t.Errorf("%s: answering server = pid %d start %d, want pid %d start %d",
						r.what, got.ServerPID, got.ServerStart, srv.PID, srv.Start)
				}
				if got.Adopt != r.adopt {
					t.Errorf("%s: Adopt = %v, want %v", r.what, got.Adopt, r.adopt)
				}
			}
		})
	}
}

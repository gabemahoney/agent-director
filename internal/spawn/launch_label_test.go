package spawn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Launch's recorded launch and label (SR-3.3, SR-3.5, SR-3.6, SR-22.2): the
// launch start, the token, the socket, the socket-directory refusal and the
// label by id of a $ or \ name. The launchEnv fixture is in launch_test.go.

var launchTokenRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// sessionNamed returns the Recorder's one session on socket stored under name.
func sessionNamed(t *testing.T, rec *tmuxfix.Recorder, socket, name string) tmuxfix.SeedSession {
	t.Helper()
	for _, s := range rec.Sessions(socket) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no session %q on %s; have %+v", name, socket, rec.Sessions(socket))
	return tmuxfix.SeedSession{}
}

// TestLaunchRecordsLaunchStartTokenAndSocket: each insert stores the clock's
// time in ms, a fresh 16-hex token that labels the session, and the create's socket.
func TestLaunchRecordsLaunchStartTokenAndSocket(t *testing.T) {
	e := newLaunchEnv(t)
	var tokens []string
	for i, id := range []string{"id-first", "id-second"} {
		e.r.ClaudeInstanceID, e.r.TmuxSessionName = id, "cd-"+id
		wantStart := e.clock.Now().UnixMilli()
		e.mustLaunch()
		row := e.row(id)
		c := e.rec.SocketCallsOf(tmux.CallCreate)[i]

		if row.LaunchStartedAtMillis != wantStart {
			t.Errorf("%s: launch_started_at = %d; want the clock's %d", id, row.LaunchStartedAtMillis, wantStart)
		}
		tok := row.Identity.Token
		if !launchTokenRE.MatchString(tok) {
			t.Errorf("%s: launch_token = %q; want 16 lowercase hex", id, tok)
		}
		if row.Identity.Socket != e.socket || c.Socket != e.socket {
			t.Errorf("%s: row socket %q, create socket %q; want both %q", id, row.Identity.Socket, c.Socket, e.socket)
		}
		sess := sessionNamed(t, e.rec, e.socket, "cd-"+id)
		if want := tmuxfix.Valid(tok, id, e.s.StoreID()); c.Token != tok || sess.Label != want {
			t.Errorf("%s: create token %q, session label %+v; want token %q and label %+v", id, c.Token, sess.Label, tok, want)
		}
		if sess.Panes[0].AdPane != tok {
			t.Errorf("%s: pane label token = %q; want %q", id, sess.Panes[0].AdPane, tok)
		}
		tokens = append(tokens, tok)
		e.clock.Advance(1500 * time.Millisecond)
	}
	if tokens[0] == tokens[1] {
		t.Errorf("two launches share token %q; want a fresh token per launch", tokens[0])
	}
}

// TestLaunchRefusesUnusableSocketDir: an unsafe per-user directory is
// ErrTmuxNotAvailable with no row, no tmux call and no pre-trust write. Every
// refusal is internal/tmux TestResolveSocket's; pkg/api
// TestSpawnRefusesUnusableSocketDir runs all three TMUX_TMPDIR refusal shapes
// end to end (SR-20.6, RN-5).
func TestLaunchRefusesUnusableSocketDir(t *testing.T) {
	e := newLaunchEnv(t)
	userDir := filepath.Join(os.Getenv("TMUX_TMPDIR"), fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := withStubClaudeJSON(t)
	seedFile(t, stub, `{"projects":{}}`)

	_, _, err := e.launch()
	if !errors.Is(err, tmux.ErrTmuxNotAvailable) || errors.Is(err, tmux.ErrTmuxSessionCreate) || errors.Is(err, tmux.ErrTmuxUnresponsive) {
		t.Fatalf("Launch err = %v; want ErrTmuxNotAvailable and no other tmux sentinel", err)
	}
	if _, gerr := e.s.GetSpawn(e.r.ClaudeInstanceID); !errors.Is(gerr, store.ErrSpawnNotFound) {
		t.Errorf("GetSpawn err = %v; want ErrSpawnNotFound (no row)", gerr)
	}
	if n, m := len(e.rec.Calls()), len(e.rec.SocketCalls()); n+m != 0 {
		t.Errorf("tmux calls = %d name-based, %d socket; want none", n, m)
	}
	if got := mustReadFile(t, stub); string(got) != `{"projects":{}}` {
		t.Errorf("claude.json = %s; want it untouched (no pre-trust write)", got)
	}
}

// TestLaunchLabelsDollarAndBackslashNamesByID: a $ or \ name is created with
// no chain, then labelled by id on the reply's session and pane, and its identity recorded.
func TestLaunchLabelsDollarAndBackslashNamesByID(t *testing.T) {
	for _, name := range []string{`a$b`, `a\b`} {
		t.Run(name, func(t *testing.T) {
			e := newLaunchEnv(t)
			e.r.TmuxSessionName = name
			const serverPID = 4242
			e.rec.StartServer(e.socket, tmuxfix.Server{PID: serverPID})
			e.pc.Set(serverPID, procfix.Alive(procstarttimefix.LinuxProcStarttime))
			var atCreate tmuxfix.SeedSession
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				atCreate = e.rec.Sessions(e.socket)[0]
				e.pc.Set(atCreate.Panes[0].PID, procfix.Alive(procstarttimefix.DarwinProcStarttime))
			})

			id := e.mustLaunch()
			row := e.row(id)
			tok, storeID := row.Identity.Token, e.s.StoreID()
			if atCreate.LabelSet || atCreate.Label.Kind != tmux.LabelNone || atCreate.Panes[0].AdPane != "" {
				t.Errorf("session after the create = %+v; want no chained label", atCreate)
			}
			calls := e.rec.SocketCalls()
			if len(calls) != 2 || calls[0].Call != tmux.CallCreate || calls[1].Call != tmux.CallSetLabel {
				t.Fatalf("tmux calls = %+v; want the create then one label by id", calls)
			}
			pane := atCreate.Panes[0]
			l := calls[1]
			if l.Socket != e.socket || l.Target != atCreate.ID || l.PaneID != pane.ID {
				t.Errorf("label by id targets {%q %q %q}; want {%q %q %q}", l.Socket, l.Target, l.PaneID, e.socket, atCreate.ID, pane.ID)
			}
			if l.Token != tok || l.InstanceID != id || l.StoreID != storeID {
				t.Errorf("label by id value {%q %q %q}; want {%q %q %q}", l.Token, l.InstanceID, l.StoreID, tok, id, storeID)
			}
			after := e.rec.Sessions(e.socket)[0]
			if after.Label != tmuxfix.Valid(tok, id, storeID) || after.Panes[0].AdPane != tok {
				t.Errorf("session after the label = %+v; want ad1 %s %s %s %s", after, tok, atCreate.ID, id, storeID)
			}
			srv, _ := e.rec.Server(e.socket)
			want := store.LaunchIdentity{Token: tok, Socket: e.socket, ServerPID: serverPID,
				ServerStart: srv.Start, ServerStarttime: procstarttimefix.LinuxProcStarttime,
				PaneID: pane.ID, PanePID: pane.PID, PaneStarttime: procstarttimefix.DarwinProcStarttime}
			if row.Identity != want || row.RowVersion != 1 {
				t.Errorf("row identity = %+v (row_version %d); want %+v (row_version 1)", row.Identity, row.RowVersion, want)
			}
		})
	}
}

// TestLaunchLostReplyRecordsNoIdentity: a create that exits 0 with an
// unparseable reply succeeds with no label by id and no identity write; the
// one start-time read is the launch owner's, this process's (b.kdf), which
// the fake reads as gone, so no owner is recorded and none released.
func TestLaunchLostReplyRecordsNoIdentity(t *testing.T) {
	for _, name := range []string{"cd-plain", `a$b`} {
		t.Run(name, func(t *testing.T) {
			e := newLaunchEnv(t)
			e.r.TmuxSessionName = name
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 0, Applied: true}, tmux.CallCreate)

			id := e.mustLaunch()
			e.onlyCreate()
			row := e.row(id)
			if row.State != store.StatePending || row.RowVersion != 0 || !launchTokenRE.MatchString(row.Identity.Token) || row.Identity.Socket != e.socket {
				t.Errorf("row = {state %q, row_version %d, token %q, socket %q}; want pending, 0, a token, %q",
					row.State, row.RowVersion, row.Identity.Token, row.Identity.Socket, e.socket)
			}
			if calls := e.pc.StartTimeCalls(); row.Identity.ServerPID != 0 || row.Identity.PaneID != "" || !slices.Equal(calls, []int{os.Getpid()}) {
				t.Errorf("identity = %+v, start-time reads %v; want none, and only the owner's read of pid %d", row.Identity, calls, os.Getpid())
			}
			if row.LaunchOwner != (store.LaunchOwner{}) {
				t.Errorf("launch owner = %+v; want none (this process reads as gone)", row.LaunchOwner)
			}
		})
	}
}

package realtmux_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Replies (SRD SR-20.7, SR-20.4, SR-2.3, SR-2.5; Appendix E.8, E.10 O1-O4,
// E.14 F9c-F9e): every recognised reply arrives on standard error with exit
// status 1 and is typed by the production client with its socket path;
// answers arrive on standard output; kills, sends and the label by id print
// nothing. Every observation is compared with the tmuxfix catalogue entry.

// createReplyFields are the formats of the create reply's five fields, in
// tmux.CreateReply order.
var createReplyFields = []string{"#{session_id}", "#{pid}", "#{start_time}", "#{pane_id}", "#{pane_pid}"}

// queryCall runs the lookup or the pane listing on socket.
func queryCall(cl *tmux.Client, call tmux.Call, socket string) error {
	if call == tmux.CallLookup {
		_, err := cl.Lookup(socket)
		return err
	}
	_, err := cl.ListPanes(socket)
	return err
}

// endedServer waits for a server started by a create to be gone (a zombie
// counts) and checks its socket file is still there (F9c, F9d).
func endedServer(t *testing.T, rt *realTmux, c created) {
	t.Helper()
	waitPidGone(t, c.Reply.ServerPID)
	info, err := os.Lstat(rt.Socket)
	if err != nil || info.Mode().Type() != fs.ModeSocket {
		t.Fatalf("socket %s after its server ended: %v, mode %v; want the socket file still present", rt.Socket, err, info)
	}
}

// TestRepliesNoServerOrSocket checks the lookup's and pane listing's typed
// failure and observed reply for each socket state against the catalogue.
func TestRepliesNoServerOrSocket(t *testing.T) {
	scenarios := []struct {
		name string
		// setup leaves the bound socket in the scenario's state and returns
		// the catalogue entry both calls must answer with.
		setup func(t *testing.T, rt *realTmux) tmuxfix.Entry
	}{
		{"no socket ever created", func(t *testing.T, rt *realTmux) tmuxfix.Entry {
			return tmuxfix.NoSocket(rt.Socket)
		}},
		// E.3 R2: connecting to a regular file is refused like a stale socket.
		{"regular file at the socket path", func(t *testing.T, rt *realTmux) tmuxfix.Entry {
			if err := os.WriteFile(rt.Socket, nil, 0o600); err != nil {
				t.Fatalf("write regular file: %v", err)
			}
			return tmuxfix.NoServer(rt.Socket)
		}},
		{"server ended by kill-server", func(t *testing.T, rt *realTmux) tmuxfix.Entry {
			c := rt.mustCreate(t, createSpec{})
			rt.must(t, "kill-server")
			endedServer(t, rt, c)
			return tmuxfix.NoServer(rt.Socket)
		}},
		{"server killed with SIGKILL", func(t *testing.T, rt *realTmux) tmuxfix.Entry {
			c := rt.mustCreate(t, createSpec{})
			if err := syscallKill(c.Reply.ServerPID); err != nil {
				t.Fatalf("SIGKILL server %d: %v", c.Reply.ServerPID, err)
			}
			endedServer(t, rt, c)
			return tmuxfix.NoServer(rt.Socket)
		}},
		// F9e: after a reboot the server and the socket's directory are gone.
		{"socket directory removed", func(t *testing.T, rt *realTmux) tmuxfix.Entry {
			dir := filepath.Join(rt.Dir, "gone")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("make socket directory: %v", err)
			}
			*rt = *rt.at(t, filepath.Join(dir, "default"))
			c := rt.mustCreate(t, createSpec{})
			rt.must(t, "kill-server")
			waitPidGone(t, c.Reply.ServerPID)
			if err := os.RemoveAll(dir); err != nil {
				t.Fatalf("remove socket directory: %v", err)
			}
			return tmuxfix.NoSocket(rt.Socket)
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			rt := newRealTmux(t)
			want := sc.setup(t, rt)
			for _, call := range []tmux.Call{tmux.CallLookup, tmux.CallListPanes} {
				cl, log := newRecordingClient(t)
				assertCallError(t, queryCall(cl, call, rt.Socket), call, want)
				assertTriple(t, log.last(t).rawResult, want)
			}
		})
	}
}

// TestRepliesDuplicateSession checks a second create with a held name is
// FailDuplicate with the catalogue's reply, and leaves the holder alone.
func TestRepliesDuplicateSession(t *testing.T) {
	rt := newRealTmux(t)
	holder := rt.mustCreate(t, createSpec{})
	cl, log := newRecordingClient(t)
	second, err := rt.create(t, createSpec{Name: holder.Name, Client: cl})

	ans, lerr := newClient().Lookup(rt.Socket)
	if lerr != nil {
		t.Fatalf("lookup after the duplicate create: %s", describe(lerr))
	}
	if len(ans.Sessions) != 1 || ans.Sessions[0].ID != holder.Reply.SessionID {
		t.Fatalf("sessions after the duplicate create = %v, want only the holder %s",
			sessionIDs(ans), holder.Reply.SessionID)
	}
	want := tmuxfix.Duplicate(ans.Sessions[0].Name)
	assertCallError(t, err, tmux.CallCreate, want)
	assertTriple(t, log.last(t).rawResult, want)
	if second.Reply != (tmux.CreateReply{}) {
		t.Errorf("duplicate create returned reply %+v, want none", second.Reply)
	}
	if rt.label(t, holder.Reply.SessionID) != tmuxfix.LabelValue(holder.Token, holder.Reply.SessionID, holder.InstanceID, holder.StoreID) {
		t.Errorf("holder %s's label changed after the duplicate create (the chained label ran)", holder.Reply.SessionID)
	}
	if ans.Sessions[0].Label != tmuxfix.Valid(holder.Token, holder.InstanceID, holder.StoreID) {
		t.Errorf("lookup classifies the holder's label as kind %d, want the holder's valid label", ans.Sessions[0].Label.Kind)
	}
}

// sessionIDs lists a lookup answer's session ids for failure messages.
func sessionIDs(ans tmux.LookupAnswer) []string {
	var ids []string
	for _, s := range ans.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

// TestRepliesCreateReplyFields checks the create reply's five fields equal
// what tmux reports for the new session and the pane pid is the stub.
func TestRepliesCreateReplyFields(t *testing.T) {
	rt := newRealTmux(t)
	cl, log := newRecordingClient(t)
	c := rt.mustCreate(t, createSpec{Client: cl})

	target := "=" + c.Name + ":" // the exact session, its active pane
	reported := tmux.CreateReply{
		SessionID:   rt.format(t, target, createReplyFields[0]),
		ServerPID:   rt.formatInt(t, target, createReplyFields[1]),
		ServerStart: int64(rt.formatInt(t, target, createReplyFields[2])),
		PaneID:      rt.format(t, target, createReplyFields[3]),
		PanePID:     rt.formatInt(t, target, createReplyFields[4]),
	}
	if c.Reply != reported {
		t.Errorf("create reply = %+v, tmux reports %+v", c.Reply, reported)
	}
	if got := procCmdline(t, c.Reply.PanePID); !reflect.DeepEqual(got, stubCommand()) {
		t.Errorf("pane pid %d runs %q, want the stub %q", c.Reply.PanePID, got, stubCommand())
	}
	want := tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply")
	want.Stdout, want.Create = tmuxfix.CreateReplyLine(reported), reported
	assertTriple(t, log.last(t).rawResult, want)
}

// TestRepliesSuccessfulCallStreams checks answers arrive on stdout alone
// with exit 0, and kills, sends and the label by id print nothing.
func TestRepliesSuccessfulCallStreams(t *testing.T) {
	rt := newRealTmux(t)
	anchor := rt.mustCreate(t, createSpec{})
	id, pane := anchor.Reply.SessionID, anchor.Reply.PaneID

	// AC-LKP-07: no @ad_owner at global, server or global-window scope gives
	// an empty scope part and ScopeValue false.
	t.Run("lookup answer with no scope value", func(t *testing.T) {
		cl, log := newRecordingClient(t)
		got, err := cl.Lookup(rt.Socket)
		want := tmuxfix.Answer("lookup/live", "this server", anchor.Reply.ServerPID, anchor.Reply.ServerStart,
			[]tmuxfix.Listed{{ID: id, Created: int64(rt.formatInt(t, id, "#{session_created}")), Name: anchor.Name,
				Label: tmuxfix.LabelValue(anchor.Token, id, anchor.InstanceID, anchor.StoreID), Want: tmuxfix.Valid(anchor.Token, anchor.InstanceID, anchor.StoreID)}})
		assertCallError(t, err, tmux.CallLookup, want)
		assertTriple(t, log.last(t).rawResult, want)
		if !reflect.DeepEqual(got, want.Lookup) {
			t.Errorf("lookup answer differs from the live session (ScopeValue %v, %d sessions), want ScopeValue false and 1 session",
				got.ScopeValue, len(got.Sessions))
		}
	})

	t.Run("pane listing", func(t *testing.T) {
		cl, log := newRecordingClient(t)
		got, err := cl.ListPanes(rt.Socket)
		p := tmux.Pane{SessionID: id, Window: rt.formatInt(t, pane, "#{window_index}"),
			Index: rt.formatInt(t, pane, "#{pane_index}"), ID: pane, PID: anchor.Reply.PanePID}
		want := tmuxfix.Find(tmuxfix.PaneListings(), "panes/P1")
		want.Stdout, want.Panes = tmuxfix.PaneLine(p.SessionID, p.Window, p.Index, p.ID, p.PID)+"\n", []tmux.Pane{p}
		assertCallError(t, err, tmux.CallListPanes, want)
		assertTriple(t, log.last(t).rawResult, want)
		if !reflect.DeepEqual(got, want.Panes) {
			t.Errorf("pane listing = %+v, want %+v", got, want.Panes)
		}
	})

	silent := []struct {
		name string
		// calls are the call kinds the action makes, in order.
		calls []tmux.Call
		// act runs the action on its own target session; check (optional)
		// confirms it took effect.
		act   func(t *testing.T, cl *tmux.Client, target *created) error
		check func(t *testing.T, target created)
	}{
		{"label by id", []tmux.Call{tmux.CallSetLabel},
			// A fresh token and id, so the check sees the new value, not the chained one.
			func(t *testing.T, cl *tmux.Client, c *created) error {
				c.Token, c.InstanceID = newToken(t), newInstanceID("agent")
				return cl.SetLabel(rt.Socket, c.Reply.SessionID, c.Token, c.InstanceID, c.StoreID)
			},
			func(t *testing.T, c created) {
				if rt.label(t, c.Reply.SessionID) != tmuxfix.LabelValue(c.Token, c.Reply.SessionID, c.InstanceID, c.StoreID) {
					t.Errorf("session %s does not carry the label set by id", c.Reply.SessionID)
				}
			}},
		{"text and Enter", []tmux.Call{tmux.CallSendText, tmux.CallSendEnter},
			func(t *testing.T, cl *tmux.Client, c *created) error {
				return cl.SendKeysPane(rt.Socket, c.Reply.PaneID, c.Name, true)
			}, nil},
		{"pane kill", []tmux.Call{tmux.CallKillPane},
			func(t *testing.T, cl *tmux.Client, c *created) error { return cl.KillPane(rt.Socket, c.Reply.PaneID) },
			func(t *testing.T, c created) { waitPidGone(t, c.Reply.PanePID) }},
		{"session kill", []tmux.Call{tmux.CallKillSession},
			func(t *testing.T, cl *tmux.Client, c *created) error {
				return cl.KillSessionID(rt.Socket, c.Reply.SessionID)
			},
			func(t *testing.T, c created) { waitPidGone(t, c.Reply.PanePID) }},
	}
	want := tmuxfix.Silent()
	for _, tc := range silent {
		t.Run(tc.name, func(t *testing.T) {
			target := rt.mustCreate(t, createSpec{})
			cl, log := newRecordingClient(t)
			err := tc.act(t, cl, &target)
			got := log.calls(t)
			if len(got) != len(tc.calls) {
				t.Fatalf("%s made %d tmux calls, want %d", tc.name, len(got), len(tc.calls))
			}
			for i, call := range tc.calls {
				assertCallError(t, err, call, want)
				assertTriple(t, got[i].rawResult, want)
			}
			if tc.check != nil {
				tc.check(t, target)
			}
		})
	}
	if pidGone(anchor.Reply.ServerPID) {
		t.Errorf("server %d ended during the successful calls", anchor.Reply.ServerPID)
	}
}

// TestRepliesCaptureStream checks a capture of typed text echoed by cat is
// the catalogue's E.10 O4 answer on stdout, with empty stderr and exit 0.
func TestRepliesCaptureStream(t *testing.T) {
	rt := newRealTmux(t)
	want := tmuxfix.Find(tmuxfix.Captures(), "capture/O4")
	text, _, _ := strings.Cut(want.Stdout, "\n")
	c := rt.mustCreate(t, createSpec{Command: []string{"cat", "-"}})
	pane := c.Reply.PaneID
	if err := newClient().SendKeysPane(rt.Socket, pane, text, true); err != nil {
		t.Fatalf("send to pane %s: %s", pane, describe(err))
	}
	const rows = 24
	var last string
	waitFor(t, "pane shows the typed line and cat's copy", func() bool {
		last, _ = newClient().CapturePaneID(rt.Socket, pane, rows, false)
		return last == want.Stdout
	}, func() string { return "capture " + strings.ReplaceAll(last, "\n", `\n`) })

	cl, log := newRecordingClient(t)
	got, err := cl.CapturePaneID(rt.Socket, pane, rows, false)
	assertCallError(t, err, tmux.CallCapture, want)
	assertTriple(t, log.last(t).rawResult, want)
	if got != want.Stdout {
		t.Errorf("capture = %q, want %q", got, want.Stdout)
	}
}

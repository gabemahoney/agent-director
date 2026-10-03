package realtmux_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Permission (SRD SR-20.7, SR-1.8, SR-2.5; Appendix E.9 S6; AC-CLS-02 real
// tmux half): a live server whose socket has mode 000 answers every call kind
// with tmux's permission reply, which the production client types as
// FailSocketDenied carrying the socket path.

// TestPermissionDeniedEveryCall sets a live server's socket to mode 000 and
// checks every call kind's typed error and observed reply against the catalogue.
func TestPermissionDeniedEveryCall(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root's access ignores the socket's mode 000, so tmux would still connect")
	}
	rt := newRealTmux(t)
	c := rt.mustCreate(t, createSpec{})
	pane, session := c.Reply.PaneID, c.Reply.SessionID
	want := tmuxfix.SocketDenied(rt.Socket)

	cases := map[tmux.Call]struct {
		// setup returns the client for the call; it leaves the socket denied
		// by the time the call under test runs.
		setup func(t *testing.T) (*tmux.Client, *callLog)
		call  func(t *testing.T, cl *tmux.Client) error
	}{
		tmux.CallLookup:      {nil, func(t *testing.T, cl *tmux.Client) error { _, err := cl.Lookup(rt.Socket); return err }},
		tmux.CallListPanes:   {nil, func(t *testing.T, cl *tmux.Client) error { _, err := cl.ListPanes(rt.Socket); return err }},
		tmux.CallKillPane:    {nil, func(t *testing.T, cl *tmux.Client) error { return cl.KillPane(rt.Socket, pane) }},
		tmux.CallKillSession: {nil, func(t *testing.T, cl *tmux.Client) error { return cl.KillSessionID(rt.Socket, session) }},
		tmux.CallSendText:    {nil, func(t *testing.T, cl *tmux.Client) error { return cl.SendKeysPane(rt.Socket, pane, "x", false) }},
		// The Enter send follows a successful text send, so the socket is
		// denied between the two calls.
		tmux.CallSendEnter: {func(t *testing.T) (*tmux.Client, *callLog) {
			chmodSocket(t, rt.Socket, 0o600)
			return newDenyAtEnterClient(t, rt.Socket)
		}, func(t *testing.T, cl *tmux.Client) error { return cl.SendKeysPane(rt.Socket, pane, "x", true) }},
		tmux.CallSendKey: {nil, func(t *testing.T, cl *tmux.Client) error { return cl.SendKeyPane(rt.Socket, pane, "C-u") }},
		tmux.CallCapture: {nil, func(t *testing.T, cl *tmux.Client) error {
			_, err := cl.CapturePaneID(rt.Socket, pane, 10, false)
			return err
		}},
		tmux.CallCreate: {nil, func(t *testing.T, cl *tmux.Client) error { _, err := rt.create(t, createSpec{Client: cl}); return err }},
		tmux.CallSetLabel: {nil, func(t *testing.T, cl *tmux.Client) error {
			return cl.SetLabel(rt.Socket, session, pane, newToken(t), newInstanceID("agent"), tmuxfix.StoreID)
		}},
	}

	for _, call := range want.Calls() {
		tc, ok := cases[call]
		if !ok {
			t.Fatalf("no case drives the %s call, which the catalogue's %s entry covers", call, want.Name)
		}
		t.Run(string(call), func(t *testing.T) {
			var cl *tmux.Client
			var log *callLog
			if tc.setup != nil {
				cl, log = tc.setup(t)
			} else {
				chmodSocket(t, rt.Socket, 0o000)
				cl, log = newRecordingClient(t)
			}
			err := tc.call(t, cl)
			assertCallError(t, err, call, want)
			assertTriple(t, log.last(t).rawResult, want)
		})
	}
	if pidGone(c.Reply.ServerPID) {
		t.Errorf("server %d on %s ended while its socket was denied", c.Reply.ServerPID, rt.Socket)
	}
}

// chmodSocket sets the socket's mode; the harness cleanup restores the
// owner's access before ending the server.
func chmodSocket(t testing.TB, socket string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(socket, mode); err != nil {
		t.Fatalf("chmod %o %s: %v", mode, socket, err)
	}
}

// newDenyAtEnterClient returns a recording production client whose binary
// sets socket to mode 000 just before an Enter send, then runs the harness's
// recording wrapper, so that call is the last one logged.
func newDenyAtEnterClient(t testing.TB, socket string) (*tmux.Client, *callLog) {
	t.Helper()
	_, log := newRecordingClient(t)
	script := `#!/bin/sh
last=
for a in "$@"; do last=$a; done
if [ "$last" = Enter ]; then chmod 000 '` + socket + `' || exit 97; fi
exec '` + filepath.Join(log.dir, "tmux-recorder") + `' "$@"
`
	gate := filepath.Join(t.TempDir(), "tmux-deny-at-enter")
	if err := os.WriteFile(gate, []byte(script), 0o755); err != nil {
		t.Fatalf("write deny-at-Enter wrapper: %v", err)
	}
	return newClientWith(gate, clientTimeouts), log
}

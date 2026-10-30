package tmux_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The 3.2a replay (wording identical on 3.3a): tmuxfix's reply entries fed
// through the production client, per call kind, asserting typed outcomes only.

// replySocket is the socket the replies name. Calls pass testSocket, so a
// matching CallError.Socket shows it came from the reply.
const replySocket = "/tmp/tmux-1000/replied"

// replySentinels are the package's catalogued sentinels; no CallError wraps one.
var replySentinels = []error{tmux.ErrTmuxNotAvailable, tmux.ErrTmuxSessionCreate, tmux.ErrTmuxKillFailed,
	tmux.ErrTmuxListPanesFailed, tmux.ErrTmuxSendKeys, tmux.ErrTmuxCaptureFailed}

// replyCreateNames returns the create's names for kind k: one chained and one
// labelled by id; other kinds take no name.
func replyCreateNames(k tmux.Call) []string {
	if k == tmux.CallCreate {
		return []string{"n1", "a$b"}
	}
	return []string{""}
}

// b.qcr: a failed/unknown paste stage must never replay input or advance to
// Enter. Private buffers keep simultaneous requests and cleanup independent.
func TestPasteKeysPaneOrderingAndFailures(t *testing.T) {
	t.Parallel()
	ok := exitZero("")
	ready := exitZero("0\t0\n")
	noServer := tmuxfix.NoServer(replySocket).Result()
	var buffers sync.Map
	for _, tc := range []struct {
		name           string
		script         []tmux.RunResult
		steps          []string
		call           tmux.Call
		failure        tmux.Failure
		cleanupFailure bool
	}{
		{"success", []tmux.RunResult{ready, ok, ok, ok}, []string{"display-message", "load-buffer", "paste-buffer", "send-keys"}, "", 0, false},
		{"second concurrent success", []tmux.RunResult{ready, ok, ok, ok}, []string{"display-message", "load-buffer", "paste-buffer", "send-keys"}, "", 0, false},
		{"copy mode", []tmux.RunResult{exitZero("1\t0\n")}, []string{"display-message"}, tmux.CallSendText, tmux.FailUnrecognized, false},
		{"input off", []tmux.RunResult{exitZero("0\t1\n")}, []string{"display-message"}, tmux.CallSendText, tmux.FailUnrecognized, false},
		{"unknown mode", []tmux.RunResult{exitZero("\t\n")}, []string{"display-message"}, tmux.CallSendText, tmux.FailUnrecognized, false},
		{"query failure", []tmux.RunResult{noServer}, []string{"display-message"}, tmux.CallSendText, tmux.FailNoServer, false},
		{"query timeout", []tmux.RunResult{timedOut()}, []string{"display-message"}, tmux.CallSendText, tmux.FailTimeout, false},
		{"load failure", []tmux.RunResult{ready, noServer, ok}, []string{"display-message", "load-buffer", "delete-buffer"}, tmux.CallSendText, tmux.FailNoServer, false},
		{"load timeout", []tmux.RunResult{ready, timedOut(), ok}, []string{"display-message", "load-buffer", "delete-buffer"}, tmux.CallSendText, tmux.FailTimeout, false},
		{"paste failure", []tmux.RunResult{ready, ok, noServer, ok}, []string{"display-message", "load-buffer", "paste-buffer", "delete-buffer"}, tmux.CallSendText, tmux.FailNoServer, false},
		{"paste timeout", []tmux.RunResult{ready, ok, timedOut(), ok}, []string{"display-message", "load-buffer", "paste-buffer", "delete-buffer"}, tmux.CallSendText, tmux.FailTimeout, false},
		{"cleanup failure", []tmux.RunResult{ready, ok, timedOut(), noServer}, []string{"display-message", "load-buffer", "paste-buffer", "delete-buffer"}, tmux.CallSendText, tmux.FailTimeout, true},
		{"Enter failure", []tmux.RunResult{ready, ok, ok, noServer}, []string{"display-message", "load-buffer", "paste-buffer", "send-keys"}, tmux.CallSendEnter, tmux.FailNoServer, false},
		{"Enter timeout", []tmux.RunResult{ready, ok, ok, timedOut()}, []string{"display-message", "load-buffer", "paste-buffer", "send-keys"}, tmux.CallSendEnter, tmux.FailTimeout, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, runner := newScripted(t, tc.script...)
			text := "-界é\n#{pane_id} " + strings.Repeat("literal;", 800) + ";"
			err := c.PasteKeysPane(testSocket, "%3", text)
			if tc.failure == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				wantCallError(t, err, tc.call, tc.failure)
				if tc.cleanupFailure {
					joined, ok := err.(interface{ Unwrap() []error })
					if !ok || len(joined.Unwrap()) != 2 {
						t.Fatalf("cleanup failure lost: %v", err)
					}
					wantCallError(t, joined.Unwrap()[1], tmux.CallSendText, tmux.FailNoServer)
				}
			}
			calls := runner.Calls()
			if len(calls) != len(tc.steps) {
				t.Fatalf("calls=%d; want %v", len(calls), tc.steps)
			}
			buffer := ""
			for i, inv := range calls {
				cmd := argvCommand(t, inv)
				if len(cmd) == 0 || cmd[0] != tc.steps[i] {
					t.Fatalf("call %d=%q; want %s", i, cmd, tc.steps[i])
				}
				var want []string
				switch cmd[0] {
				case "display-message":
					want = []string{"display-message", "-p", "-t", "%3", "#{pane_in_mode}\t#{pane_input_off}"}
				case "load-buffer":
					if len(cmd) != 4 {
						t.Fatalf("load argv=%q", cmd)
					}
					buffer = cmd[2]
					if buffer == "" {
						t.Fatal("shared default paste buffer")
					}
					if _, reused := buffers.LoadOrStore(buffer, true); reused {
						t.Fatalf("reused buffer %q", buffer)
					}
					want = []string{"load-buffer", "-b", buffer, "-"}
				case "paste-buffer":
					want = []string{"paste-buffer", "-p", "-r", "-d", "-b", buffer, "-t", "%3"}
				case "delete-buffer":
					want = []string{"delete-buffer", "-b", buffer}
				case "send-keys":
					want = []string{"send-keys", "-t", "%3", "Enter"}
				}
				if !slices.Equal(cmd, want) {
					t.Errorf("call %d=%q; want %q", i, cmd, want)
				}
				if cmd[0] == "load-buffer" {
					if string(inv.Stdin) != text {
						t.Error("load did not preserve literal UTF-8/LF/dash/semicolon stdin")
					}
				} else if inv.Stdin != nil {
					t.Errorf("unexpected stdin on %s", cmd[0])
				}
				if inv.Timeout != testTimeouts.Action || inv.WaitDelay != testTimeouts.WaitDelay {
					t.Errorf("unbounded %s: timeout=%v wait=%v", cmd[0], inv.Timeout, inv.WaitDelay)
				}
			}
		})
	}
}

// replyCase names a subtest after the entry, the call kind and the create name.
func replyCase(entry string, k tmux.Call, name string) string {
	if name == "" {
		return entry + "/" + string(k)
	}
	return entry + "/" + string(k) + "/" + name
}

// replyCall makes one call of kind k answered by res (an Enter send follows a
// silent text send) and returns the create's reply and the call's error.
func replyCall(t *testing.T, k tmux.Call, name string, res tmux.RunResult) (tmux.CreateReply, error) {
	t.Helper()
	script := []tmux.RunResult{res}
	if k == tmux.CallSendEnter {
		script = []tmux.RunResult{tmuxfix.Silent().Result(), res}
	}
	c, _ := newScripted(t, script...)
	var reply tmux.CreateReply
	var err error
	switch k {
	case tmux.CallLookup:
		_, err = c.Lookup(testSocket)
	case tmux.CallListPanes:
		_, err = c.ListPanes(testSocket)
	case tmux.CallKillPane:
		err = c.KillPane(testSocket, "%0")
	case tmux.CallKillSession:
		err = c.KillSessionID(testSocket, "$0")
	case tmux.CallSendText:
		err = c.SendKeysPane(testSocket, "%0", "hi", false)
	case tmux.CallSendEnter:
		err = c.SendKeysPane(testSocket, "%0", "hi", true)
	case tmux.CallSendKey:
		err = c.SendKeyPane(testSocket, "%0", "C-u")
	case tmux.CallCapture:
		_, err = c.CapturePaneID(testSocket, "%0", 10, false)
	case tmux.CallCreate:
		reply, err = c.NewSession(testSocket, name, "/tmp", nil, []string{"claude"}, tmuxfix.Token, "agent-x", tmuxfix.StoreID)
	case tmux.CallSetLabel:
		err = c.SetLabel(testSocket, "$0", "%0", tmuxfix.Token, "agent-x", tmuxfix.StoreID)
	default:
		t.Fatalf("unknown call kind %q", k)
	}
	return reply, err
}

// wantCallError checks err is nil for want 0, else a *CallError of kind want on
// call k that wraps no sentinel and carries no launch token or store id; it
// returns it.
func wantCallError(t *testing.T, err error, k tmux.Call, want tmux.Failure) *tmux.CallError {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("want success, got %v", err)
		}
		return nil
	}
	ce := callError(t, err)
	if ce.Call != k || ce.Failure != want {
		t.Fatalf("got Call %q Failure %v, want Call %q Failure %v", ce.Call, ce.Failure, k, want)
	}
	for _, s := range replySentinels {
		if errors.Is(err, s) {
			t.Errorf("CallError satisfies errors.Is(%v)", s)
		}
	}
	for _, secret := range []string{tmuxfix.Token, tmuxfix.StoreID} {
		if strings.Contains(err.Error(), secret) || strings.Contains(ce.FirstLine, secret) {
			t.Errorf("CallError carries the launch token or store id %q: %q", secret, err.Error())
		}
	}
	return ce
}

// TestReplayReplies replays every reply and create entry on each call kind it
// lists, checking the kind, the reply's socket and the capped first line; a
// create returns its reply on success and with FailLabel (a chained name's
// label step failed; by id, the same answer is a success), else none.
func TestReplayReplies(t *testing.T) {
	entries := append(tmuxfix.Replies(replySocket), tmuxfix.CreateReplies()...)
	for _, e := range entries {
		for _, k := range e.Calls() {
			for _, name := range replyCreateNames(k) {
				t.Run(replyCase(e.Name, k, name), func(t *testing.T) {
					want := e.Want[k]
					if e.ChainOnly && tmux.NeedsLabelByID(name) {
						want = 0
					}
					reply, err := replyCall(t, k, name, e.Result())
					ce := wantCallError(t, err, k, want)
					var wantReply tmux.CreateReply
					if want == 0 || want == tmux.FailLabel {
						wantReply = e.Create
					}
					if reply != wantReply {
						t.Errorf("reply = %+v, want %+v", reply, wantReply)
					}
					if ce == nil {
						return
					}
					if k == tmux.CallCreate && (ce.ExitStatus != e.Exit || ce.HadStdout != (e.Stdout != "")) {
						t.Errorf("ExitStatus %d, HadStdout %v; want %d, %v", ce.ExitStatus, ce.HadStdout, e.Exit, e.Stdout != "")
					}
					if ce.Socket != e.Socket {
						t.Errorf("Socket = %q, want %q", ce.Socket, e.Socket)
					}
					wantFirst := ""
					if want == tmux.FailUnrecognized {
						wantFirst = e.FirstLine
					}
					if ce.FirstLine != wantFirst {
						t.Errorf("FirstLine = %q, want %q", ce.FirstLine, wantFirst)
					}
					if len(ce.FirstLine) > 200 || !utf8.ValidString(ce.FirstLine) {
						t.Errorf("FirstLine is %d bytes or splits a character: %q", len(ce.FirstLine), ce.FirstLine)
					}
				})
			}
		}
	}
}

// TestReplayEveryCallKind pins the outcomes that hold on every call kind
// (tmuxfix.AllCalls): the permission reply (AC-CLS-02) and an exec failure of
// the binary.
func TestReplayEveryCallKind(t *testing.T) {
	cases := []struct {
		name   string
		res    tmux.RunResult
		want   tmux.Failure
		socket string
	}{
		{"permission-denied", tmuxfix.SocketDenied(replySocket).Result(), tmux.FailSocketDenied, replySocket},
		{"exec-failure", notStarted(), tmux.FailUnavailable, ""},
	}
	for _, tc := range cases {
		for _, k := range tmuxfix.AllCalls() {
			for _, name := range replyCreateNames(k) {
				t.Run(replyCase(tc.name, k, name), func(t *testing.T) {
					_, err := replyCall(t, k, name, tc.res)
					if ce := wantCallError(t, err, k, tc.want); ce.Socket != tc.socket {
						t.Errorf("Socket = %q, want %q", ce.Socket, tc.socket)
					}
				})
			}
		}
	}
}

// TestReplaySendKeysPaneFailedStep checks a failed text send makes no Enter
// call, and a failed Enter send is reported as the Enter send.
func TestReplaySendKeysPaneFailedStep(t *testing.T) {
	fails := []struct {
		name string
		res  tmux.RunResult
		want tmux.Failure
	}{
		{"no-server", tmuxfix.NoServer(replySocket).Result(), tmux.FailNoServer},
		{"timeout", timedOut(), tmux.FailTimeout},
	}
	steps := []struct {
		name  string
		call  tmux.Call
		calls int
	}{
		{"text", tmux.CallSendText, 1},
		{"enter", tmux.CallSendEnter, 2},
	}
	for _, f := range fails {
		for _, s := range steps {
			t.Run(f.name+"/"+s.name, func(t *testing.T) {
				script := []tmux.RunResult{f.res}
				if s.call == tmux.CallSendEnter {
					script = []tmux.RunResult{tmuxfix.Silent().Result(), f.res}
				}
				c, r := newScripted(t, script...)
				wantCallError(t, c.SendKeysPane(testSocket, "%0", "hi", true), s.call, f.want)
				if n := len(r.Calls()); n != s.calls {
					t.Errorf("made %d tmux calls, want %d", n, s.calls)
				}
			})
		}
	}
}

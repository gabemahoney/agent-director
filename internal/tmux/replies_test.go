package tmux_test

import (
	"errors"
	"strings"
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

// replyCase names a subtest after the entry, the call kind and the create name.
func replyCase(entry string, k tmux.Call, name string) string {
	if name == "" {
		return entry + "/" + string(k)
	}
	return entry + "/" + string(k) + "/" + name
}

// replyCall makes one call of kind k answered by res (an Enter send follows a
// silent text send) and returns the runner and the call's error.
func replyCall(t *testing.T, k tmux.Call, name string, res tmux.RunResult) (*scriptedRunner, error) {
	t.Helper()
	script := []tmux.RunResult{res}
	if k == tmux.CallSendEnter {
		script = []tmux.RunResult{tmuxfix.Silent().Result(), res}
	}
	c, r := newScripted(t, script...)
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
		_, err = c.NewSession(testSocket, name, "/tmp", nil, []string{"claude"}, tmuxfix.Token, "agent-x", tmuxfix.StoreID)
	case tmux.CallSetLabel:
		err = c.SetLabel(testSocket, "$0", "%0", tmuxfix.Token, "agent-x", tmuxfix.StoreID)
	default:
		t.Fatalf("unknown call kind %q", k)
	}
	return r, err
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
	var ce *tmux.CallError
	if !errors.As(err, &ce) {
		t.Fatalf("want *tmux.CallError (%v), got %T: %v", want, err, err)
	}
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
// lists, checking the kind, the reply's socket and the capped first line.
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
					_, err := replyCall(t, k, name, e.Result())
					ce := wantCallError(t, err, k, want)
					if ce == nil {
						return
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

// TestReplayLookupNeverLeaksLabel checks a lookup answer that fails to parse
// keeps its label values out of FirstLine and Error().
func TestReplayLookupNeverLeaksLabel(t *testing.T) {
	for _, e := range tmuxfix.LookupAnswers() {
		want := e.Want[tmux.CallLookup]
		if want == 0 || len(e.LabelValues) == 0 {
			continue
		}
		t.Run(e.Name, func(t *testing.T) {
			_, err := replyCall(t, tmux.CallLookup, "", e.Result())
			ce := wantCallError(t, err, tmux.CallLookup, want)
			for _, v := range e.LabelValues {
				if strings.Contains(ce.FirstLine, v) || strings.Contains(err.Error(), v) {
					t.Errorf("label value %q leaked into %q", v, err.Error())
				}
			}
		})
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
		{"cant-find-pane", tmuxfix.Find(tmuxfix.Replies(replySocket), "reply/cant-find-pane").Result(), tmux.FailUnrecognized},
		{"timeout", timedOut(), tmux.FailTimeout},
		{"exec-failure", notStarted(), tmux.FailUnavailable},
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

package api_test

// sendkeys_paced_typing_test.go (b.vr5): on macOS a Claude pane lost the
// start of typed text longer than about 1 KiB, so send-keys typed a Claude
// row's fallback text in paced pieces. Busy send now covers Claude, whose send
// types nothing, so what remains is that the macOS selection leaves every
// other agent's send as one text call. api.SetClaudePacedTypingForTest sets
// the selection for the whole test binary, so these tests are serial.

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// pacedSend is one send-keys call of these tests: the row's agent, pending
// (else waiting), the macOS selection, the text, no_enter, expect_pane_sha256,
// the refusal wanted (nil: success) and the input steps wanted (inputSteps).
type pacedSend struct {
	name    string
	agent   string
	pending bool
	paced   bool
	text    string
	noEnter bool
	hash    string
	refused error
	want    []string
}

// inputSteps is a send's input up to its first Enter: "capture" for the hash
// check's capture, "type <bytes>" or "paste <bytes>" per text call, "pause
// <d>" for the pauses between them (summed) and "Enter". texts are the
// nonempty text calls.
type inputSteps struct {
	texts []tmuxfix.SocketCall
	steps []string
	pause time.Duration
	enter bool
}

// add appends the pause taken since the last step, if any, then step, if any.
func (s *inputSteps) add(step string) {
	if s.pause > 0 {
		s.steps = append(s.steps, "pause "+s.pause.String())
		s.pause = 0
	}
	if step != "" {
		s.steps = append(s.steps, step)
	}
}

// recordInput records e's input steps through an after-call hook and e.env.Sleep.
func recordInput(t *testing.T, e *nativeSubmissionEnv) *inputSteps {
	t.Helper()
	s := &inputSteps{}
	e.rec.AfterCall(tmuxfix.AnyCall, func(c tmuxfix.SocketCall, err error) {
		if err != nil || s.enter {
			return
		}
		switch {
		case c.Call == tmux.CallCapture:
			s.add("capture")
		case c.Call == tmux.CallSendText:
			if c.Text == "" {
				return
			}
			kind := "type"
			if c.Paste {
				kind = "paste"
			}
			s.texts = append(s.texts, c)
			s.add(fmt.Sprintf("%s %d", kind, len(c.Text)))
		case len(s.texts) > 0 && (c.Call == tmux.CallSendEnter || c.Call == tmux.CallSendKey && c.Key == "Enter"):
			s.add("Enter")
			s.enter = true
		}
	})
	sleep := e.env.Sleep
	e.env.Sleep = func(d time.Duration) {
		if len(s.texts) > 0 && !s.enter {
			s.pause += d
		}
		sleep(d)
	}
	return s
}

// sendPaced makes tc's call on a row of an unsupported native launch, as every
// Claude launch on macOS is, with the macOS selection set as tc says. It fails
// unless the call succeeds, or is refused with tc.refused and types nothing,
// with tc.want's steps, every text call to the row's own pane, the text typed
// exactly once, and one Enter unless no_enter. A pending row reports
// SessionStart at its first terminal read and turn activity after Enter.
func sendPaced(t *testing.T, tc pacedSend) {
	t.Helper()
	t.Cleanup(api.SetClaudePacedTypingForTest(tc.paced))
	e := newNativeSubmissionEnv(t)
	state := store.StateWaiting
	if tc.pending {
		state = store.StatePending
	}
	r := e.seedRow(t, killRowSpec{State: state, Opts: agentRowOpts(tc.agent)})
	e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
	if tc.pending {
		e.sessionStartsWhenPolled(t, r, nil)
		e.s.read = func(row api.Spawn) (api.Spawn, error) { row.State = store.StateWorking; return row, nil }
	}
	in := recordInput(t, e)

	_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: tc.text, NoEnter: tc.noEnter, AllowPending: tc.pending,
		ExpectPaneSHA256: tc.hash})

	text, enters := tc.text, map[bool]int{false: 1, true: 0}[tc.noEnter]
	if tc.refused != nil {
		assertOneSentinel(t, err, tc.refused)
		text, enters = "", 0
	} else if err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	in.add("") // the pause after the last piece, which no_enter ends with
	if !slices.Equal(in.steps, tc.want) {
		t.Errorf("input steps = %q; want %q", in.steps, tc.want)
	}
	var typed string
	for _, c := range in.texts {
		if c.Socket != r.Socket || c.Target != r.Spawn.Identity.PaneID {
			t.Errorf("text call to %s on %s; want the row's pane %s on %s", c.Target, c.Socket, r.Spawn.Identity.PaneID, r.Socket)
		}
		typed += c.Text
	}
	if typed != text {
		t.Errorf("typed %d bytes; want %d: the text sent, or nothing when refused", len(typed), len(text))
	}
	if got := len(ordinaryEnters(e.killEnv)); got != enters {
		t.Errorf("Enter calls = %d; want %d", got, enters)
	}
}

// oneCall is the steps of n bytes in one call of kind (type or paste), with
// Enter after it when enter.
func oneCall(kind string, n int, enter bool) []string {
	steps := []string{fmt.Sprintf("%s %d", kind, n)}
	if enter {
		steps = append(steps, "Enter")
	}
	return steps
}

// TestSendKeysPacedTypingLeavesOtherSendsAlone (b.vr5): with the macOS
// selection on, Codex and Cursor rows keep one text call with no pause:
// Codex's paste, otherwise typed text, Enter after it unless no_enter. (Paced
// typing was Claude's; busy send now covers Claude, whose send types nothing.)
func TestSendKeysPacedTypingLeavesOtherSendsAlone(t *testing.T) {
	// Serial: it sets the process-wide paced-typing selection with api.SetClaudePacedTypingForTest.
	long := strings.Repeat("s", 1023)
	for _, tc := range []pacedSend{
		{name: "codex", agent: store.AgentCodex, paced: true, text: long, want: oneCall("paste", 1023, true)},
		{name: "codex no_enter", agent: store.AgentCodex, paced: true, text: long, noEnter: true, want: oneCall("type", 1023, false)},
		{name: "cursor", agent: store.AgentCursor, paced: true, text: long, want: oneCall("type", 1023, true)},
		{name: "cursor no_enter", agent: store.AgentCursor, paced: true, text: long, noEnter: true, want: oneCall("type", 1023, false)},
	} {
		t.Run(tc.name, func(t *testing.T) { sendPaced(t, tc) })
	}
}

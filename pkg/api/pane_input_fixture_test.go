package api_test

// pane_input_fixture_test.go models the agent's input box as send-keys' and
// pause's keys calls leave it, as Claude Code's prompt input behaves (b.fji,
// b.9o4). The pane's capture shows the box, so read-pane shows what is typed.
// It holds no tests.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// paneInput is the agent pane's input box as send-keys' and pause's keys
// calls leave it, as Claude Code's prompt input behaves (b.fji, b.9o4): a
// text call types its text, C-u deletes to the start of the line, Enter
// submits the box (an empty box submits nothing). box is what is typed and
// not submitted, submitted the submissions in order. A failed call reached
// the pane only when it timed out and timeoutsReach is set; onSubmit, when
// set, runs on each submission.
type paneInput struct {
	timeoutsReach bool
	box           string
	submitted     []string
	onSubmit      func(line string)
}

// watchPaneInput models the input box of pane on e's Recorder from every
// keys call to it, in call order, and shows the box as the pane's capture.
func watchPaneInput(e *killEnv, pane string, timeoutsReach bool) *paneInput {
	in := &paneInput{timeoutsReach: timeoutsReach}
	e.rec.AfterCall(tmuxfix.AnyCall, func(c tmuxfix.SocketCall, err error) {
		if c.Target != pane || !in.reached(err) {
			return
		}
		switch {
		case c.Key == paneClearKey:
			in.box = in.box[:strings.LastIndex(in.box, "\n")+1]
		case c.Call == tmux.CallSendText:
			in.box += c.Text
		case c.Call == tmux.CallSendEnter:
			in.submit()
		default:
			return
		}
		e.rec.SetCapture(c.Socket, pane, in.box)
	})
	return in
}

// reached reports whether a keys call that returned err reached the pane.
func (in *paneInput) reached(err error) bool {
	var ce *tmux.CallError
	return err == nil || (in.timeoutsReach && errors.As(err, &ce) && ce.Failure == tmux.FailTimeout)
}

// submit submits the box; Enter on an empty box submits nothing.
func (in *paneInput) submit() {
	line := in.box
	if line == "" {
		return
	}
	in.submitted, in.box = append(in.submitted, line), ""
	if in.onSubmit != nil {
		in.onSubmit(line)
	}
}

// assertSubmittedOnce fails unless the agent got exactly text, submitted once, and nothing is left typed.
func (in *paneInput) assertSubmittedOnce(t *testing.T, text string) {
	t.Helper()
	if !slices.Equal(in.submitted, []string{text}) || in.box != "" {
		t.Errorf("agent got submissions %q with %q left typed; want %q submitted once", in.submitted, in.box, text)
	}
}

// paneAgentExiting is watchPaneInput on r's agent pane for an agent that
// ends its row (SessionEnd) on its exitOn-th "/exit" submission (0: never).
func paneAgentExiting(t *testing.T, e *killEnv, r killRow, timeoutsReach bool, exitOn int) *paneInput {
	t.Helper()
	in := watchPaneInput(e, r.Spawn.Identity.PaneID, timeoutsReach)
	exits := 0
	in.onSubmit = func(line string) {
		if line != exitText {
			return
		}
		if exits++; exits == exitOn {
			if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", r.Spawn.ClaudeSessionID); !a.Applied {
				t.Errorf("SessionEnd after /exit not applied: %s", a.Reason)
			}
		}
	}
	return in
}

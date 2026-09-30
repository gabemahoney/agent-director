package realtmux_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Send keys (SRD SR-2.1 "Text" row, SR-20.7; WD 2026-09-29b): the production
// SendKeysPane ends tmux's options with "--", so a text that starts with "-"
// is typed literally and never read as a send-keys flag.

// sendKeysCapLines is how many pane lines the capture reads (the whole
// visible pane).
const sendKeysCapLines = 24

// TestSendKeysDashTextsArriveLiterally sends "-x", "--" and "-l", each with
// Enter, through the production SendKeysPane into a pane running cat -, and
// checks both calls succeed silently and the pane then shows exactly the text
// twice (the tty's echo and cat's copy) and nothing else.
func TestSendKeysDashTextsArriveLiterally(t *testing.T) {
	rt := newRealTmux(t)
	for _, text := range []string{"-x", "--", "-l"} {
		t.Run(text, func(t *testing.T) {
			assertSendKeysArrives(t, rt, text)
		})
	}
}

// TestSendKeysSemicolonTextsArriveLiterally sends texts holding a ";" the
// same way. tmux reads an argument that ends in ";" as a command separator
// even after "--", so SendKeysPane escapes a text-final ";" (`a;` is sent as
// `a\;`); these cases prove each text, with or without a final ";", reaches
// the pane exactly as the caller wrote it.
func TestSendKeysSemicolonTextsArriveLiterally(t *testing.T) {
	rt := newRealTmux(t)
	for _, text := range []string{";", ";;", "a;", "a ;", `a\;`, ";a", "a;b", "-x;"} {
		t.Run(text, func(t *testing.T) {
			assertSendKeysArrives(t, rt, text)
		})
	}
}

// assertSendKeysArrives creates a session whose pane runs cat -, sends text
// with Enter through the production SendKeysPane, and checks the text call's
// argv ends in `-l -- <text>` (a text-final ";" escaped as `\;`), both calls
// succeed silently and the pane then shows exactly the text twice (the tty's
// echo and cat's copy) and nothing else.
func assertSendKeysArrives(t *testing.T, rt *realTmux, text string) {
	t.Helper()
	c := rt.mustCreate(t, createSpec{Command: []string{"cat", "-"}})
	pane := c.Reply.PaneID

	cl, log := newRecordingClient(t)
	err := cl.SendKeysPane(rt.Socket, pane, text, true)
	calls := log.calls(t)
	if len(calls) != 2 {
		t.Fatalf("SendKeysPane(%q) made %d tmux calls, want 2 (text, Enter); err %s", text, len(calls), describe(err))
	}
	// A text-final ";" goes to tmux escaped as `\;`; any other text as is.
	sent := text
	if body, ok := strings.CutSuffix(text, ";"); ok {
		sent = body + `\;`
	}
	if args := calls[0].Args; len(args) < 3 || !reflect.DeepEqual(args[len(args)-3:], []string{"-l", "--", sent}) {
		t.Errorf("SendKeysPane(%q) text call argv = %q, want it to end in -l -- %q", text, args, sent)
	}
	want := tmuxfix.Silent()
	for i, call := range []tmux.Call{tmux.CallSendText, tmux.CallSendEnter} {
		assertCallError(t, err, call, want)
		assertTriple(t, calls[i].rawResult, want)
	}

	wantShown := text + "\n" + text
	var (
		last    string
		lastErr error
	)
	waitFor(t, fmt.Sprintf("pane %s shows %q typed and echoed by cat, and nothing else", pane, text), func() bool {
		last, lastErr = newClient().CapturePaneID(rt.Socket, pane, sendKeysCapLines, false)
		return lastErr == nil && strings.TrimRight(last, "\n") == wantShown
	}, func() string {
		return fmt.Sprintf("capture %q (error %s), want %q then blank rows", last, describe(lastErr), wantShown)
	})
}

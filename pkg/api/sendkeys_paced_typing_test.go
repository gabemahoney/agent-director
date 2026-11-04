package api_test

// sendkeys_paced_typing_test.go (b.vr5): on macOS a Claude pane loses the
// start of typed text longer than about 1 KiB, so send-keys types a Claude
// row's fallback text longer than 512 bytes in pieces of at most 512 bytes,
// 100 ms apart, with Enter 500 ms after the last pause. Every other send keeps
// one text call. api.SetClaudePacedTypingForTest sets the macOS selection for
// the whole test binary, so these tests are serial.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
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

// paced is the steps of text typed in pieces of sizes bytes, each followed by
// a 100 ms pause, then, with enter, 500 ms more and one Enter.
func paced(enter bool, sizes ...int) []string {
	var steps []string
	for i, n := range sizes {
		pause := 100 * time.Millisecond
		if enter && i == len(sizes)-1 {
			pause += 500 * time.Millisecond
		}
		steps = append(steps, fmt.Sprintf("type %d", n), "pause "+pause.String())
	}
	if enter {
		steps = append(steps, "Enter")
	}
	return steps
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

// TestSendKeysMacClaudeLongTextPaced (b.vr5): with the macOS selection on, a
// Claude row's fallback text longer than 512 bytes, live or pending, is typed
// in rune-aligned pieces of at most 512 bytes that make up the exact text,
// 100 ms apart, then Enter 500 ms after the last pause; no_enter returns after
// that pause with no Enter. Text that is not UTF-8 is still typed literally.
// With expect_pane_sha256 the pane's hash is checked before the first piece,
// and a changed pane types nothing.
func TestSendKeysMacClaudeLongTextPaced(t *testing.T) {
	// Serial: it sets the process-wide paced-typing selection with api.SetClaudePacedTypingForTest.
	s1023 := strings.Repeat("s", 1023)
	m1100x3 := strings.Repeat(strings.Repeat("m", 274)+"\n", 3) + strings.Repeat("m", 275)
	straddle := strings.Repeat("a", 511) + "界" + strings.Repeat("b", 600) // 界 is bytes 511-513
	claude := store.AgentClaude
	for _, tc := range []pacedSend{
		{name: "s1023", agent: claude, paced: true, text: s1023, want: paced(true, 512, 511)},
		{name: "s1023 no_enter", agent: claude, paced: true, text: s1023, noEnter: true, want: paced(false, 512, 511)},
		{name: "m1100x3", agent: claude, paced: true, text: m1100x3, want: paced(true, 512, 512, 76)},
		{name: "rune across 512 no_enter", agent: claude, paced: true, text: straddle, noEnter: true, want: paced(false, 511, 512, 91)},
		{name: "not UTF-8", agent: claude, paced: true, text: strings.Repeat("\x80", 600), want: paced(true, 512, 88)},
		{name: "pending s1023", agent: claude, pending: true, paced: true, text: s1023, want: paced(true, 512, 511)},
		{name: "s1023 expect_pane_sha256", agent: claude, paced: true, text: s1023, hash: api.PaneSHA256(""),
			want: append([]string{"capture"}, paced(true, 512, 511)...)},
		{name: "s1023 pane changed", agent: claude, paced: true, text: s1023, hash: api.PaneSHA256("changed"),
			refused: api.ErrPaneChanged, want: []string{"capture"}},
	} {
		t.Run(tc.name, func(t *testing.T) { sendPaced(t, tc) })
	}
}

// TestSendKeysPacedTypingLeavesOtherSendsAlone (b.vr5): every send with the
// macOS selection off (Linux), even over paced typing's 32 KiB limit, Claude
// text of 512 bytes, and Codex and Cursor rows keep one text call with no
// pause: Codex's paste, otherwise typed text, Enter after it unless no_enter.
func TestSendKeysPacedTypingLeavesOtherSendsAlone(t *testing.T) {
	// Serial: it sets the process-wide paced-typing selection with api.SetClaudePacedTypingForTest.
	long := strings.Repeat("s", 1023)
	for _, tc := range []pacedSend{
		{name: "Linux claude", agent: store.AgentClaude, text: long, want: oneCall("type", 1023, true)},
		{name: "Linux claude over 32 KiB", agent: store.AgentClaude, text: strings.Repeat("s", 32<<10+1),
			want: oneCall("type", 32<<10+1, true)},
		{name: "Linux claude no_enter", agent: store.AgentClaude, text: long, noEnter: true, want: oneCall("type", 1023, false)},
		{name: "claude 512 bytes", agent: store.AgentClaude, paced: true, text: strings.Repeat("s", 512), want: oneCall("type", 512, true)},
		{name: "codex", agent: store.AgentCodex, paced: true, text: long, want: oneCall("paste", 1023, true)},
		{name: "codex no_enter", agent: store.AgentCodex, paced: true, text: long, noEnter: true, want: oneCall("type", 1023, false)},
		{name: "cursor", agent: store.AgentCursor, paced: true, text: long, want: oneCall("type", 1023, true)},
		{name: "cursor no_enter", agent: store.AgentCursor, paced: true, text: long, noEnter: true, want: oneCall("type", 1023, false)},
	} {
		t.Run(tc.name, func(t *testing.T) { sendPaced(t, tc) })
	}
}

// TestSendKeysMacClaudePacedFailureMapsAsTypedText (b.vr5): with the macOS
// selection on, a first piece failing other than by timeout (which typed
// nothing) maps as typed text's failed text call, and a failed Enter after the
// pieces as its failed Enter after text, timed out or failing otherwise with
// the follow-up lookup finding the session: the same error and description,
// the same trail action, and no call after the failed one but that follow-up.
// A first piece that times out is partial input (E20).
func TestSendKeysMacClaudePacedFailureMapsAsTypedText(t *testing.T) {
	// Serial: it sets the process-wide paced-typing selection with api.SetClaudePacedTypingForTest.
	t.Cleanup(api.SetClaudePacedTypingForTest(true))
	for _, tc := range []struct {
		name     string
		failure  tmux.Failure
		call     tmux.Call // scripted to fail: the pieces' text call or the named Enter
		desc     func(timeout time.Duration) apitest.DescCase
		pieces   int  // the text calls made
		followUp bool // one follow-up lookup after the failed call
		action   string
	}{
		{"first piece failed, the follow-up lookup found the session", tmux.FailNoServer, tmux.CallSendText,
			func(time.Duration) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendText, "").AfterTextFailed()
			}, 1, true, "nothing_sent"},
		{"Enter timed out", tmux.FailTimeout, tmux.CallSendKey, func(d time.Duration) apitest.DescCase {
			return apitest.DescKeysTimeout(apitest.PaneSendKeys, tmux.CallSendEnter, d)
		}, 2, false, "text_sent"},
		{"Enter failed, the follow-up lookup found the session", tmux.FailNoServer, tmux.CallSendKey,
			func(time.Duration) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendEnter, "").AfterEnterFailed(apitest.PaneSendKeys)
			}, 2, true, "text_sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, other := e.seedKeysDisagreeCase(t, keysDisagreeCase{setup: []func(*testing.T, *killEnv, *killRow){ktrRestart}})
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tc.failure}, tc.call)
			before := e.columns(t, r.ID)

			run := runVerb(e, func() (api.SendKeysResult, error) {
				return e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: strings.Repeat("s", 1023)})
			})

			assertOneName(t, run.err, "ErrTmuxUnresponsive")
			apitest.AssertDescription(t, errText(run.err), tc.desc(e.cfg.EffectiveActionTimeout()), r.Token, r.StoreID,
				apitest.OtherStoreID(r.StoreID))
			calls, tail := paKinds(run.calls), []tmux.Call{tc.call}
			if tc.followUp {
				tail = append(tail, tmux.CallLookup)
			}
			if len(calls) < len(tail) || !slices.Equal(calls[len(calls)-len(tail):], tail) ||
				len(e.rec.SocketCallsOf(tmux.CallSendText)) != tc.pieces {
				t.Errorf("tmux calls = %v; want %d pieces, then nothing after the failed call but %v", calls, tc.pieces, tail)
			}
			e.assertRowUnchanged(t, r.ID, before)
			assertKeysDisagrees(t, e, sktDisagrees(t, r.ID), r, "send-keys", other, []disagreeWant{
				{reason: "server_restarted", server: "restarted", verdict: "ours", action: tc.action, ours: true}})
		})
	}
}

// TestSendKeysMacClaudePacedStopsPartway (b.vr5): with the macOS selection
// on, the row is read again after each piece's pause, before the next piece,
// and after the 500 ms pause, before the Enter, with or without no_enter. A
// changed launch, a state the state guard refuses, a permission or question
// dialog the row moved into, or a failed reread types no further piece and
// sends no Enter: partial_input_unconfirmed counting the pieces typed, with
// the reread row's state, or the observed one when the read failed (a relay
// guard that now holds input is E20's). A send begun in a dialog state, and a
// working row, go on. A pending row's failed later piece stops there too,
// reporting the state its readiness wait saw.
func TestSendKeysMacClaudePacedStopsPartway(t *testing.T) {
	// Serial: it sets the process-wide paced-typing selection with api.SetClaudePacedTypingForTest.
	t.Cleanup(api.SetClaudePacedTypingForTest(true))
	text := strings.Repeat("s", 1025) // pieces of 512, 512 and 1 bytes
	all := len(text)
	// from changes every reread of the row (GetSpawnWithin) as change does,
	// from the pause of d taken once n bytes are typed: a piece's 100 ms,
	// before the next piece's reread, or the 500 ms before the Enter's.
	from := func(n int, d time.Duration, change func(*api.Spawn) error) func(*testing.T, *nativeSubmissionEnv, killRow) {
		return func(_ *testing.T, e *nativeSubmissionEnv, _ killRow) {
			typed := 0
			e.rec.AfterCall(tmux.CallSendText, func(c tmuxfix.SocketCall, _ error) { typed += len(c.Text) })
			sleep := e.env.Sleep
			e.env.Sleep = func(pause time.Duration) {
				if typed == n && pause == d {
					e.s.reread = func(row api.Spawn) (api.Spawn, error) {
						err := change(&row)
						return row, err
					}
				}
				sleep(pause)
			}
		}
	}
	beforeEnter := func(change func(*api.Spawn) error) func(*testing.T, *nativeSubmissionEnv, killRow) {
		return from(all, 500*time.Millisecond, change)
	}
	afterFirst := func(change func(*api.Spawn) error) func(*testing.T, *nativeSubmissionEnv, killRow) {
		return from(512, 100*time.Millisecond, change)
	}
	state := func(s string) func(*api.Spawn) error { return func(row *api.Spawn) error { row.State = s; return nil } }
	readFails := func(*api.Spawn) error { return errors.New("injected read failure") }
	for _, tc := range []struct {
		name      string
		spec      killRowSpec
		noEnter   bool
		setup     func(*testing.T, *nativeSubmissionEnv, killRow)
		attempted int    // bytes of the text calls made, as bytes_attempted
		rowState  string // err_details row_state wanted
		submits   bool   // success, with one Enter unless no_enter
	}{
		{name: "check_permission before the Enter", setup: beforeEnter(state(store.StateCheckPermission)), attempted: all,
			rowState: store.StateCheckPermission},
		{name: "ask_user before the Enter", setup: beforeEnter(state(store.StateAskUser)), attempted: all, rowState: store.StateAskUser},
		{name: "launch changed before the Enter", setup: beforeEnter(func(row *api.Spawn) error { row.LifeNumber++; return nil }),
			attempted: all, rowState: store.StateWaiting},
		{name: "reread failed before the Enter", setup: beforeEnter(readFails), attempted: all, rowState: store.StateWaiting},
		{name: "began ask_user, check_permission before the Enter", spec: killRowSpec{State: store.StateAskUser},
			setup: beforeEnter(state(store.StateCheckPermission)), attempted: all, rowState: store.StateCheckPermission},
		{name: "check_permission after the first piece", setup: afterFirst(state(store.StateCheckPermission)), attempted: 512,
			rowState: store.StateCheckPermission},
		{name: "check_permission after the first piece, no_enter", noEnter: true, setup: afterFirst(state(store.StateCheckPermission)),
			attempted: 512, rowState: store.StateCheckPermission},
		{name: "ask_user after the second piece", setup: from(1024, 100*time.Millisecond, state(store.StateAskUser)), attempted: 1024,
			rowState: store.StateAskUser},
		{name: "ended after the first piece", setup: afterFirst(state(store.StateEnded)), attempted: 512, rowState: store.StateEnded},
		{name: "reread failed after the first piece", setup: afterFirst(readFails), attempted: 512, rowState: store.StateWaiting},
		{name: "pending row, second piece failed", spec: killRowSpec{State: store.StatePending}, attempted: 1024, rowState: store.StateWaiting,
			setup: func(_ *testing.T, e *nativeSubmissionEnv, r killRow) {
				e.rec.Script(r.Socket, tmuxfix.Script{Times: 1}, tmux.CallSendText)
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailNoServer, Times: 1}, tmux.CallSendText)
			}},
		{name: "working row submits", spec: killRowSpec{State: store.StateWorking}, attempted: all, submits: true},
		{name: "began ask_user submits", spec: killRowSpec{State: store.StateAskUser}, attempted: all, submits: true},
		{name: "began check_permission submits", spec: killRowSpec{State: store.StateCheckPermission}, attempted: all, submits: true},
		{name: "began ask_user, no_enter, types every piece", spec: killRowSpec{State: store.StateAskUser}, noEnter: true, attempted: all,
			submits: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, tc.spec)
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			pending := tc.spec.State == store.StatePending
			if pending {
				e.sessionStartsWhenPolled(t, r, nil)
			}
			if tc.setup != nil {
				tc.setup(t, e, r)
			}

			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: text, NoEnter: tc.noEnter, AllowPending: pending})

			attempted := ""
			for _, c := range e.rec.SocketCallsOf(tmux.CallSendText) {
				attempted += c.Text
			}
			enters := len(ordinaryEnters(e.killEnv))
			if attempted != text[:tc.attempted] {
				t.Errorf("text calls made %d bytes; want the text's first %d, then none", len(attempted), tc.attempted)
			}
			if tc.submits {
				if want := map[bool]int{false: 1, true: 0}[tc.noEnter]; err != nil || enters != want {
					t.Errorf("send = %v with %d Enters; want success and %d", err, enters, want)
				}
				return
			}
			d := assertDeliveryError(t, err, "partial_input_unconfirmed", tc.attempted, false)
			if !d.KeysSent || d.Phase != "text_attempted" || enters != 0 || d.RowState != tc.rowState {
				t.Errorf("details = %+v with %d Enters; want keys_sent, phase text_attempted, no Enter, row_state %q",
					d, enters, tc.rowState)
			}
		})
	}
}

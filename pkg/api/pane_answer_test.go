package api_test

// pane_answer_test.go — b.146 step 2b at send-keys (rules 7 and 8, problem 2):
// read-pane's pane_sha256 and send-keys' hash check, plain send-keys'
// no_enter and key, the shape refusals, and a pane answer: its intent
// committed before its one key and sent after, and a later answer refused
// while the first sender may still be sending. What follows the key (a close
// meanwhile, a failed key or sent write, the release) is
// pane_answer_after_key_test.go's; rule 7's refusals and ErrRelayFallenBack's
// err_details are sendkeys_rule7_test.go's; record-pane-answer is
// record_pane_answer_test.go's. Fixture: pane_answer_fixture_test.go.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// paKinds is calls' kinds, in order.
func paKinds(calls []tmuxfix.SocketCall) []tmux.Call {
	out := make([]tmux.Call, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Call)
	}
	return out
}

// TestReadPaneSHA256AndSendKeysHashCheck (b.146 rule 7): read-pane's
// pane_sha256 is the SHA-256 of exactly the bytes it returns, ANSI read or
// not; send-keys given that hash captures the pane as read-pane gives it by
// default (the same n_lines, ANSI stripped) and types only while the bytes
// match; a changed pane, or an ANSI read's hash, is ErrPaneChanged with
// nothing sent, its err_details n_lines and never the new hash.
func TestReadPaneSHA256AndSendKeysHashCheck(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	pane := r.Spawn.Identity.PaneID
	e.rec.SetCapture(r.Socket, pane, "\x1b[1mfirst\x1b[0m line\n")
	read := func(ansi bool, n int) api.ReadPaneResult {
		t.Helper()
		res, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID, ANSI: ansi, NLines: n})
		if err != nil {
			t.Fatalf("read-pane (ansi %v, n_lines %d): %v", ansi, n, err)
		}
		sum := sha256.Sum256([]byte(res.Pane))
		if res.PaneSHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("pane_sha256 = %s; want the SHA-256 of the %q returned", res.PaneSHA256, res.Pane)
		}
		return res
	}
	send := func(hash string, n int) verbRun[struct{}] {
		return runVerb(e, func() (struct{}, error) {
			_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "go", ExpectPaneSHA256: hash, NLines: n})
			return struct{}{}, err
		})
	}
	plain, withANSI := read(false, 0), read(true, 0)
	if plain.Pane != "first line\n" || plain.PaneSHA256 == withANSI.PaneSHA256 {
		t.Fatalf("reads = %+v / %+v; want the stripped and the raw pane, two hashes", plain, withANSI)
	}

	for _, n := range []int{0, 7} {
		run := send(read(false, n).PaneSHA256, n)
		wantLines := n
		if n == 0 {
			wantLines = api.DefaultReadPaneLines
		}
		if run.err != nil || !slices.Equal(paKinds(run.calls), []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture,
			tmux.CallSendText, tmux.CallSendEnter}) {
			t.Fatalf("send-keys with read-pane's hash (n_lines %d) = %v, calls %v; want the capture, then the text and Enter", n, run.err, paKinds(run.calls))
		}
		if c := run.calls[2]; c.Target != pane || c.NLines != wantLines || c.ANSI {
			t.Errorf("capture = %+v; want %s, %d lines, no escapes", c, pane, wantLines)
		}
	}

	e.rec.SetCapture(r.Socket, pane, "second\n")
	newHash := api.PaneSHA256("second\n")
	for name, hash := range map[string]string{"a changed pane": plain.PaneSHA256, "an ANSI read's hash": withANSI.PaneSHA256} {
		run := send(hash, 0)
		assertOneSentinel(t, run.err, api.ErrPaneChanged)
		if !slices.Equal(paKinds(run.calls), paneReadCalls) {
			t.Errorf("%s: calls = %v; want %v, nothing sent", name, paKinds(run.calls), paneReadCalls)
		}
		details := api.ErrDetails(run.err)
		if details != (api.PaneChangedDetails{NLines: api.DefaultReadPaneLines}) || jsonOf(t, details) != `{"n_lines":25}` {
			t.Errorf("%s: err_details = %s; want exactly {\"n_lines\":25}", name, jsonOf(t, details))
		}
		if strings.Contains(run.err.Error(), newHash) || strings.Contains(jsonOf(t, details), newHash) {
			t.Errorf("%s: the refusal carries the pane's new hash: %v", name, run.err)
		}
	}
}

// TestSendKeysNoEnterAndKey (b.146 rule 8): plain send-keys still types text
// then presses Enter by default; no_enter types it alone; key sends that one
// key alone, a named key by name and one character literally, never Enter.
func TestSendKeysNoEnterAndKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		p     api.SendKeysParams
		text  string // the text call's text ("" for a named key)
		enter bool
	}{
		{"default presses Enter", api.SendKeysParams{Text: "hi"}, "hi", true},
		{"no_enter", api.SendKeysParams{Text: "hi", NoEnter: true}, "hi", false},
		{"a named key", api.SendKeysParams{Key: "Escape"}, "", false},
		{"Enter by name", api.SendKeysParams{Key: "Enter"}, "", false},
		{"one character", api.SendKeysParams{Key: "y"}, "y", false},
		{"one non-ASCII character", api.SendKeysParams{Key: "é"}, "é", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			p := tc.p
			p.ClaudeInstanceID = r.ID

			if _, err := e.sendKeys(p); err != nil {
				t.Fatalf("SendKeys: %v", err)
			}

			switch {
			case tc.enter:
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, tc.text)
			case tc.p.Key != "":
				assertOneKey(t, e.rec.SocketCalls(), r.Spawn.Identity.PaneID, tc.p.Key, tmux.CallLookup, tmux.CallListPanes)
			default:
				calls := e.rec.SocketCalls()
				if !slices.Equal(paKinds(calls), paneTextCalls) || calls[2].Text != tc.text || calls[2].PressEnter {
					t.Errorf("calls = %+v; want %v, %q typed with no Enter", calls, paneTextCalls, tc.text)
				}
			}
		})
	}
}

// TestSendKeysShapeRefusedBeforeAnything (b.146 rule 8, decision 7 A): a pane
// answer without as (allow or deny), key or expect_pane_sha256, with text or
// with more than one key, and a plain call's own misuses, are ErrInvalidFlags
// before anything is read, sent or written: no tmux call, and request A,
// fallen back, gets no hook_gone_at (which the relay guard would write).
func TestSendKeysShapeRefusedBeforeAnything(t *testing.T) {
	t.Parallel()
	tok, hash := storefix.TestRequestTokenA, api.PaneSHA256("any pane\n")
	cases := map[string]api.SendKeysParams{
		"pane answer without as":                 {RequestToken: tok, Key: "1", ExpectPaneSHA256: hash},
		"pane answer claiming unknown":           {RequestToken: tok, As: "unknown", Key: "1", ExpectPaneSHA256: hash},
		"pane answer without key":                {RequestToken: tok, As: "allow", ExpectPaneSHA256: hash},
		"pane answer without expect_pane_sha256": {RequestToken: tok, As: "allow", Key: "1"},
		"pane answer with text":                  {RequestToken: tok, As: "allow", Key: "1", ExpectPaneSHA256: hash, Text: "1"},
		"pane answer with two keys":              {RequestToken: tok, As: "allow", Key: "12", ExpectPaneSHA256: hash},
		"pane answer with two named keys":        {RequestToken: tok, As: "deny", Key: "Escape Enter", ExpectPaneSHA256: hash},
		"pane answer with a control character":   {RequestToken: tok, As: "deny", Key: "\x03", ExpectPaneSHA256: hash},
		"as without request_token":               {Text: "hi", As: "allow"},
		"key with text":                          {Text: "hi", Key: "y"},
		"no_enter with nothing to send":          {NoEnter: true},
		"a malformed expect_pane_sha256":         {Text: "hi", ExpectPaneSHA256: "abc"},
		"a negative n_lines":                     {Text: "hi", NLines: -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			before := p.request(t, tok)
			params := tc
			params.ClaudeInstanceID = p.r.ID

			err := p.sendKeys(params)

			assertOneSentinel(t, err, api.ErrInvalidFlags)
			p.assertNoTmuxCall(t)
			if after := p.request(t, tok); after.PaneAnswer != before.PaneAnswer || !after.HookGoneAt.IsZero() {
				t.Errorf("request A = pane_answer %q, hook_gone_at %v; want it untouched", after.PaneAnswer, after.HookGoneAt)
			}
		})
	}
}

// TestPaneAnswerSendsOneKeyAfterItsIntent (b.146 rule 8, problem 2; control
// c_paneintent): a valid pane answer captures the pane, commits its intent
// (pane_answer intent, pane_as, its sender, and pane_intent_at and
// hook_gone_at read on the call's clock after the capture) before its key
// goes out, sends exactly that one key and no Enter, then records sent with
// decision as and decision_reason pane, one ad.row_mutation.committed (writer
// send_keys). The request is closed but not proven gone (b.146 step 2c):
// plain send-keys is then ErrDialogMaybeOpen (the proofs that release it are
// sendkeys_hold_test.go's).
func TestPaneAnswerSendsOneKeyAfterItsIntent(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	for _, tc := range []struct{ as, key string }{{"allow", "1"}, {"deny", "Escape"}, {"allow", "Down"}} {
		t.Run(tc.as+" "+tc.key, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			stamped := p.now.Add(time.Second) // the clock once the capture, inside the intent's transaction, returns
			p.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) { p.now = stamped })
			var atKey *store.PermissionRow
			p.rec.AfterCall(paKeyCall(tc.key), func(tmuxfix.SocketCall, error) {
				pr, err := p.st.GetPermissionRequest(p.r.ID, tok)
				if err == nil {
					atKey = &pr
				}
			})

			run := p.sendKeysRun(p.answer(tok, tc.as, tc.key))

			if run.err != nil {
				t.Fatalf("pane answer: %v", run.err)
			}
			assertOneKey(t, run.calls, p.r.Spawn.Identity.PaneID, tc.key, tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
			if atKey == nil || atKey.PaneAnswer != store.PaneAnswerIntent || atKey.PaneAs != tc.as || atKey.PaneSender != paSender ||
				!atKey.PaneIntentAt.Equal(stamped) || !atKey.HookGoneAt.Equal(stamped) || atKey.Decision != "" {
				t.Fatalf("request A as the key went out = %+v; want intent %s committed by %+v at %v, after the capture", atKey, tc.as, paSender, stamped)
			}
			pr := p.request(t, tok)
			if pr.PaneAnswer != store.PaneAnswerSent || pr.Decision != tc.as || pr.DecisionReason != store.DecisionReasonPane || pr.AwaitsAnswer() {
				t.Errorf("request A after = %+v; want sent, decision %s, decision_reason pane, closed", pr, tc.as)
			}
			var committed []map[string]any
			for _, l := range readAPITrailLines(t) {
				if l["event"] == "ad.row_mutation.committed" && l["claude_instance_id"] == p.r.ID && l["mutation_kind"] == "update" {
					committed = append(committed, l)
				}
			}
			if len(committed) != 1 || committed[0]["writer_process"] != store.WriterProcessSendKeys || committed[0]["decision"] != tc.as ||
				committed[0]["decision_reason"] != store.DecisionReasonPane {
				t.Errorf("ad.row_mutation.committed = %v; want one, writer send_keys, decision %s, reason pane", committed, tc.as)
			}
			assertDialogMaybeOpen(t, p.sendKeys(p.plain("next")), tok, store.StateCheckPermission)
		})
	}
}

// TestPaneAnswerSenderDiesBetweenIntentAndSent (b.146 rule 8, problem 2;
// controls c_paneintent, c_livesender): a sender that dies after its key, with
// no sent written, leaves pane_answer intent. While it runs, a second pane
// answer and record-pane-answer are ErrPaneAnswerInProgress with err_details
// (sender_alive true) and nothing sent, and plain send-keys is still
// ErrRelayFallenBack (pane_answer intent). Once it is dead the retry needs a
// fresh hash: the old one is ErrPaneChanged, the fresh one is accepted.
func TestPaneAnswerSenderDiesBetweenIntentAndSent(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newPAEnv(t)
	firstPane := p.text
	p.sks = &paDyingStore{killStore: p.store, die: true}
	first := p.sendKeysRun(p.answer(tok, "allow", "1"))
	assertOneKey(t, first.calls, p.r.Spawn.Identity.PaneID, "1", tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
	if pr := p.request(t, tok); pr.PaneAnswer != store.PaneAnswerIntent || pr.PaneSender != paSender || !pr.PaneIntentAt.Equal(p.now) {
		t.Fatalf("request A after the first sender died = %+v; want its intent left", pr)
	}
	intentAt := p.now

	p.sks = p.store
	p.self = api.ProcessIdentity{PID: 64002, Starttime: "777", PIDNamespace: relayNS}
	p.pc.Set(p.self.PID, procfix.Alive(p.self.Starttime))
	p.setPane("pane after the first key\n")
	p.now = p.now.Add(time.Minute)
	second := p.sendKeysRun(p.answer(tok, "deny", "2"))
	assertOneSentinel(t, second.err, api.ErrPaneAnswerInProgress)
	if len(second.calls) != 0 {
		t.Errorf("calls = %v; want none", paKinds(second.calls))
	}
	d, ok := api.ErrDetails(second.err).(api.PaneAnswerInProgressDetails)
	if !ok || d.RequestToken != tok || d.PaneAs != "allow" || !d.PaneIntentAt.Equal(intentAt) || d.SenderAlive == nil ||
		!*d.SenderAlive || d.NotBefore != nil || !strings.Contains(jsonOf(t, d), `"not_before":null`) {
		t.Errorf("err_details = %s; want request A, pane_as allow, pane_intent_at %v, sender_alive true, not_before null",
			jsonOf(t, api.ErrDetails(second.err)), intentAt)
	}
	if _, err := p.recordPaneAnswer(p.outside(tok, "deny")); !errors.Is(err, api.ErrPaneAnswerInProgress) {
		t.Errorf("record-pane-answer while the sender runs = %v; want ErrPaneAnswerInProgress", err)
	}
	err := p.sendKeys(p.plain("hi"))
	if d := assertFallenBackDetails(t, err, tok, store.StateCheckPermission); d["pane_answer"] != "intent" || d["pane_as"] != "allow" {
		t.Errorf("plain send-keys' err_details = %v; want pane_answer intent, pane_as allow", d)
	}
	if pr := p.request(t, tok); pr.PaneSender != paSender || pr.PaneAnswer != store.PaneAnswerIntent {
		t.Fatalf("request A after the refusals = %+v; want the first intent kept", pr)
	}

	p.pc.Set(paSender.PID, procfix.Gone())
	stale := p.answer(tok, "deny", "2")
	stale.ExpectPaneSHA256 = api.PaneSHA256(firstPane)
	assertOneSentinel(t, p.sendKeys(stale), api.ErrPaneChanged)
	if pr := p.request(t, tok); pr.PaneSender != paSender {
		t.Errorf("request A after a stale retry = %+v; want the first intent kept", pr)
	}
	third := p.sendKeysRun(p.answer(tok, "deny", "2"))
	if third.err != nil {
		t.Fatalf("retry once the first sender is dead: %v", third.err)
	}
	assertOneKey(t, third.calls, p.r.Spawn.Identity.PaneID, "2", tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
	if pr := p.request(t, tok); pr.PaneAnswer != store.PaneAnswerSent || pr.Decision != "deny" || pr.PaneSender != p.self {
		t.Errorf("request A after the retry = %+v; want sent, deny, by the second sender", pr)
	}
}

// TestPaneAnswerUnjudgeableSenderHolds (b.146 problem 2, rule 14's time
// fallback): an intent whose sender recorded no identity, or one in another
// pid namespace, counts as in progress through Client.SendKeys until
// pane_intent_at plus the [tmux] action timeout, the pipe-close wait and 2 s:
// ErrPaneAnswerInProgress with sender_alive null and that not_before; from
// then the pane answer is accepted.
func TestPaneAnswerUnjudgeableSenderHolds(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	tm := config.Default().Tmux
	hold := tm.EffectiveActionTimeout() + tm.EffectivePipeCloseWait() + 2*time.Second
	for name, sender := range map[string]api.ProcessIdentity{
		"no sender identity":    {},
		"another pid namespace": {PID: 64003, Starttime: "9", PIDNamespace: "pid:[4026532000]"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			p.pc.Set(sender.PID, procfix.Alive(sender.Starttime))
			t0 := p.now
			if _, ok, err := p.st.RecordPaneIntent(p.r.ID, tok, "allow", sender, func() time.Time { return t0 }, store.DefaultLockWait, nil); err != nil || !ok {
				t.Fatalf("an earlier sender's intent = %v, %v", ok, err)
			}
			c, _ := p.client(t)
			api.SetClockForTest(c, func() time.Time { return p.now })
			api.SetSelfPIDNSForTest(c, func() (string, bool) { return relayNS, true })
			api.SetPaneSelfForTest(c, func() api.ProcessIdentity { return paSender })

			p.now = t0.Add(hold - time.Millisecond)
			_, err := c.SendKeys(p.answer(tok, "deny", "2"))
			assertOneSentinel(t, err, api.ErrPaneAnswerInProgress)
			d, ok := api.ErrDetails(err).(api.PaneAnswerInProgressDetails)
			if !ok || d.SenderAlive != nil || d.NotBefore == nil || !d.NotBefore.Equal(t0.Add(hold)) {
				t.Errorf("err_details = %s; want sender_alive null, not_before %v", jsonOf(t, api.ErrDetails(err)), t0.Add(hold))
			}

			p.now = t0.Add(hold)
			if _, err := c.SendKeys(p.answer(tok, "deny", "2")); err != nil {
				t.Fatalf("pane answer at not_before: %v; want it accepted", err)
			}
			if pr := p.request(t, tok); pr.PaneAnswer != store.PaneAnswerSent || pr.Decision != "deny" || pr.PaneSender != paSender {
				t.Errorf("request A = %+v; want sent, deny, by this Client's sender", pr)
			}
		})
	}
}

package api_test

// advice_follow_conflict_test.go (b.fji, the bee's ErrTmuxSessionConflict
// CONFLICT cases; inventory HO1, HO3, HO5, HO7, HO11): each conflict a
// lookup of resume, reuse, kill or a pane verb gives is re-issued while its
// condition holds (the same refusal, nothing written or sent) and once a
// human, or the holder's own exit, has cleared it (the call then does its
// work). The downstream C22 re-check protocol relies on both. Kill and the
// pane verbs look a row up by its label only, so a session merely holding
// its name (another row's, another store's, unlabelled) never refuses them.

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// adviceConflictLater is how long the caller waits before re-issuing a
// refused call: one health tick of the C22 protocol.
const adviceConflictLater = 2 * time.Minute

// The pointers and sentences the conflicts carry (inventory HO1, HO3, HO5,
// HO7, HO11; held_name.go for the holders with no pointer).
const (
	adviceLookLabels  = `a human must look, see "Operator actions" in the agent-director README; list tmux_session_name (--tmux-session-name on the CLI) shows whether a row uses a session name`
	adviceLook        = `a human must look, see "Operator actions" in the agent-director README`
	adviceEndLeftover = `ending such a session is a human's decision, see "Operator actions" in the agent-director README`
	adviceCanLook     = `a human can look, see "Operator actions" in the agent-director README`
)

// adviceConflictVerb is a verb a conflict refuses: start seeds its row (a
// finished row for resume and reuse; a waiting row for kill and the pane
// verbs, with noOwn no session of its current launch and its agent gone)
// and returns it with the call and the check that the call did its work.
type adviceConflictVerb struct {
	name  string
	start func(t *testing.T, e *killEnv, noOwn bool) (r killRow, call func() error, worked func(*testing.T))
}

// adviceConflictCase is verb refused by one condition: place puts it in place
// on r and returns how it clears; the refusal carries words and phrase;
// cleared checks the call once it has cleared (nil: worked).
type adviceConflictCase struct {
	name          string
	verb          adviceConflictVerb
	noOwn         bool
	words, phrase string
	place         func(t *testing.T, e *killEnv, r killRow) (clear func())
	cleared       func(t *testing.T, e *killEnv, err error)
}

// adviceConflictClears runs each case: refused with ErrTmuxSessionConflict
// carrying its words and phrase; re-issued later while the condition holds,
// the same refusal (name and description) with one lookup per call and
// nothing written or sent; re-issued once the condition is cleared, the
// verb's work done.
func adviceConflictClears(t *testing.T, cases []adviceConflictCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.verb.name+"/"+tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, call, worked := tc.verb.start(t, e, tc.noOwn)
			clearCond := tc.place(t, e, r)
			cols, mark, sessions := e.columns(t, r.ID), len(e.rec.SocketCalls()), e.rec.Sessions(r.Socket)

			first := call()

			adviceAssertAdvice(t, first, api.ErrTmuxSessionConflict, tc.words)
			adviceAssertPhrase(t, first, tc.phrase)
			e.clock.Advance(adviceConflictLater)
			again := call()
			if n, d := errnames.Classify(again); n != "ErrTmuxSessionConflict" || d != first.Error() {
				t.Errorf("re-issued while the condition holds: %s %q; want the same refusal %q", n, d, first)
			}
			e.assertRowUnchanged(t, r.ID, cols)
			if got := e.rec.SocketCalls()[mark:]; len(got) != 2 || got[0].Call != tmux.CallLookup || got[1].Call != tmux.CallLookup {
				t.Errorf("tmux calls by the two refused calls = %+v; want one lookup each", got)
			}
			if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
				t.Errorf("sessions on %s changed:\n got %+v\nwant %+v", r.Socket, got, sessions)
			}
			clearCond()
			e.clock.Advance(adviceConflictLater)

			err := call()

			if tc.cleared != nil {
				tc.cleared(t, e, err)
				return
			}
			if err != nil {
				t.Fatalf("re-issued once the condition cleared: %v; want success", err)
			}
			worked(t)
		})
	}
}

// adviceConflictResume is resume of a finished row whose agent is gone; it
// worked when it launched (one create, the row pending).
var adviceConflictResume = adviceConflictVerb{"resume", func(t *testing.T, e *killEnv, _ bool) (killRow, func() error, func(*testing.T)) {
	r := e.seedResumable(t, rlkSettled(e), agentGone)
	return r.killRow, func() error { _, err := e.resume(r.ID); return err }, func(t *testing.T) {
		if n, st := len(e.rec.SocketCallsOf(tmux.CallCreate)), e.columns(t, r.ID).State; n != 1 || st != store.StatePending {
			t.Errorf("after the resume: %d creates, state %v; want 1, pending", n, st)
		}
	}
}}

// adviceConflictReuse is spawn with the reuse opt-in of a finished row
// (adviceReuseSettled) under its recorded name; it worked when it launched
// the next life (assertReused).
var adviceConflictReuse = adviceConflictVerb{"reuse", func(t *testing.T, e *killEnv, _ bool) (killRow, func() error, func(*testing.T)) {
	r := adviceReuseSettled(t, e)
	p := reuseParams(t, r, reuseRequest{})
	calls := 0
	return r.killRow, func() error {
			calls = len(e.rec.SocketCalls())
			_, _, err := e.reuse(t, p)
			return err
		}, func(t *testing.T) {
			e.assertReused(t, r, reuseLife, calls, nil)
		}
}}

// adviceConflictLiveRow seeds a waiting row on a server holding a bystander
// too; with noOwn its current launch's session is gone, its agent with it.
func adviceConflictLiveRow(t *testing.T, e *killEnv, noOwn bool) killRow {
	t.Helper()
	spec := killRowSpec{}
	if noOwn {
		spec = killRowSpec{NoSession: true, Agent: agentGone}
	}
	r := e.seedRow(t, spec)
	e.ensureServer(&r)
	e.seedBystander(t, r.Socket)
	e.syncServers()
	return r
}

// adviceConflictKill is kill of a waiting row whose agent exits at its pane
// kill; it worked when it succeeded, sending a kill only to a session of the
// launch (none with noOwn), which is gone, the row's state kept.
var adviceConflictKill = adviceConflictVerb{"kill", func(t *testing.T, e *killEnv, noOwn bool) (killRow, func() error, func(*testing.T)) {
	r := adviceConflictLiveRow(t, e, noOwn)
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
	var res api.KillResult
	return r, func() (err error) { res, err = e.kill(r.ID); return err }, func(t *testing.T) {
		if res.KillSent == noOwn {
			t.Errorf("kill_sent = %v; want %v", res.KillSent, !noOwn)
		}
		if r.Session.ID != "" && seqHas(e, r.Socket, r.Session.ID) {
			t.Errorf("session %s still runs after the kill", r.Session.ID)
		}
		if st := e.columns(t, r.ID).State; st != store.StateWaiting {
			t.Errorf("state = %v; want waiting kept", st)
		}
	}
}}

// adviceConflictPane is the pane verb v on adviceConflictLiveRow's row.
func adviceConflictPane(name string, v advPaneVerb) adviceConflictVerb {
	return adviceConflictVerb{name, func(t *testing.T, e *killEnv, noOwn bool) (killRow, func() error, func(*testing.T)) {
		r := adviceConflictLiveRow(t, e, noOwn)
		call, worked := v(t, e, r)
		return r, call, worked
	}}
}

// adviceConflictReadPane is read-pane on r; it worked when it read the
// agent's pane or, with no session of the row's launch, the one leftover's.
func adviceConflictReadPane(t *testing.T, e *killEnv, r killRow) (func() error, func(*testing.T)) {
	var got api.ReadPaneResult
	return func() (err error) {
			e.setPaneTexts(r.Socket)
			got, err = e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID})
			return err
		}, func(t *testing.T) {
			pane := r.Spawn.Identity.PaneID
			if r.Session.ID == "" {
				for _, s := range e.rec.Sessions(r.Socket) {
					if s.Label.InstanceID == r.ID {
						pane = labelledPane(t, s, s.Label.Token)
					}
				}
			}
			if want := paneText(r.Socket, pane); got.Pane != want {
				t.Errorf("read-pane = %q; want %q", got.Pane, want)
			}
		}
}

// The pane verbs: send-keys and pause (advice_follow_pane_test.go's), read-pane.
var (
	adviceConflictSendKeys = adviceConflictPane("send-keys", advPaneSendKeys)
	adviceConflictPause    = adviceConflictPane("pause", advPanePause)
	adviceConflictRead     = adviceConflictPane("read-pane", adviceConflictReadPane)
)

// adviceScopeValue sets an @ad_owner value at the global scope of r's server;
// a human unsets it.
func adviceScopeValue(_ *testing.T, e *killEnv, r killRow) func() {
	e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
	return func() { e.rec.ClearScope(r.Socket, tmuxfix.ScopeGlobal) }
}

// adviceDuplicateLabel seeds a second session carrying r's current label; a
// human ends it.
func adviceDuplicateLabel(t *testing.T, e *killEnv, r killRow) func() {
	s := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
	return func() { adviceEndSession(t, e.rec, r.Socket, s.ID) }
}

// adviceHolder seeds k's session holding r's recorded name; it ends, by a
// human or by its own exit.
func adviceHolder(k holderKind) func(*testing.T, *killEnv, killRow) func() {
	return func(t *testing.T, e *killEnv, r killRow) func() {
		s := e.seedHolder(t, r, k)
		return func() { adviceEndSession(t, e.rec, r.Socket, s.ID) }
	}
}

// adviceLeftovers seeds n leftovers of earlier launches of r; a human ends
// the first end of them.
func adviceLeftovers(n, end int) func(*testing.T, *killEnv, killRow) func() {
	return func(t *testing.T, e *killEnv, r killRow) func() {
		var ids []string
		for range n {
			ids = append(ids, e.seedLeftover(t, r, newToken()).ID)
		}
		return func() {
			for _, id := range ids[:end] {
				adviceEndSession(t, e.rec, r.Socket, id)
			}
		}
	}
}

// TestAdviceFollow_HO1_ConflictingLabelsClears: HO1 "conflicting labels" ...
// "a human must look, see "Operator actions" in the agent-director README;
// list tmux_session_name (--tmux-session-name on the CLI) shows whether a row
// uses a session name".
func TestAdviceFollow_HO1_ConflictingLabelsClears(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	var cases []adviceConflictCase
	for _, v := range []adviceConflictVerb{adviceConflictResume, adviceConflictReuse, adviceConflictKill,
		adviceConflictSendKeys, adviceConflictRead, adviceConflictPause} {
		cases = append(cases, adviceConflictCase{name: "scope value", verb: v, words: "conflicting labels",
			phrase: adviceLookLabels, place: adviceScopeValue})
	}
	for _, v := range []adviceConflictVerb{adviceConflictKill, adviceConflictSendKeys, adviceConflictRead, adviceConflictPause} {
		cases = append(cases, adviceConflictCase{name: "duplicate label", verb: v, words: "conflicting labels",
			phrase: adviceLookLabels, place: adviceDuplicateLabel})
	}
	adviceConflictClears(t, cases)
}

// TestAdviceFollow_HO3_NameHolderClears: HO3 "no valid instance id" ... "a
// human must look, see "Operator actions" in the agent-director README"; also
// the holders with no pointer ("... must not be ended"), which C22 retries alike.
func TestAdviceFollow_HO3_NameHolderClears(t *testing.T) {
	t.Parallel()
	holders := []struct {
		name, phrase string
		kind         holderKind
	}{
		{"no valid instance id", adviceLook, holderNone},
		{"a different instance id", "the session is another row's agent and must not be ended", holderForeign},
		{"another agent-director store", "the session is another agent-director store's agent and must not be ended", holderOtherStore},
	}
	var cases []adviceConflictCase
	for _, v := range []adviceConflictVerb{adviceConflictResume, adviceConflictReuse} {
		for _, h := range holders {
			cases = append(cases, adviceConflictCase{name: h.name, verb: v, words: h.name, phrase: h.phrase,
				place: adviceHolder(h.kind)})
		}
	}
	adviceConflictClears(t, cases)
}

// TestAdviceFollow_HO5_PreLaunchLeftoverClears: HO5 "left over from an
// earlier life" ... "ending such a session is a human's decision, see
// "Operator actions" in the agent-director README".
func TestAdviceFollow_HO5_PreLaunchLeftoverClears(t *testing.T) {
	t.Parallel()
	var cases []adviceConflictCase
	for _, v := range []adviceConflictVerb{adviceConflictResume, adviceConflictReuse} {
		cases = append(cases, adviceConflictCase{name: "leftover", verb: v, words: "left over from an earlier life",
			phrase: adviceEndLeftover, place: adviceLeftovers(1, 1)})
	}
	adviceConflictClears(t, cases)
}

// TestAdviceFollow_HO7_KillLeftoverClears: HO7 "not this launch's session"
// ... "no kill was sent; ending such a session is a human's decision, see
// "Operator actions" in the agent-director README".
func TestAdviceFollow_HO7_KillLeftoverClears(t *testing.T) {
	t.Parallel()
	adviceConflictClears(t, []adviceConflictCase{{name: "leftover", verb: adviceConflictKill, noOwn: true,
		words: "not this launch's session", phrase: "no kill was sent; " + adviceEndLeftover, place: adviceLeftovers(1, 1)}})
}

// TestAdviceFollow_HO11_PaneLeftoverClears: HO11 "not this launch's session"
// ... "a human can look, see "Operator actions" in the agent-director README".
// Once it is ended, send-keys and pause give their gone error (the README's
// next step is find-missing); read-pane, refused for several leftovers, reads the one left.
func TestAdviceFollow_HO11_PaneLeftoverClears(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	gone := func(t *testing.T, e *killEnv, err error) {
		adviceAssertAdvice(t, err, api.ErrTmuxSendKeys, "the row's session is not there")
		e.assertNothingSent(t)
	}
	leftover := adviceConflictCase{name: "leftover", noOwn: true, words: "not this launch's session",
		phrase: adviceCanLook, place: adviceLeftovers(1, 1), cleared: gone}
	send, pause := leftover, leftover
	send.verb, pause.verb = adviceConflictSendKeys, adviceConflictPause
	adviceConflictClears(t, []adviceConflictCase{send, pause, {name: "two leftovers", verb: adviceConflictRead, noOwn: true,
		words: "more than one leftover session exists", phrase: adviceCanLook, place: adviceLeftovers(2, 1)}})
}

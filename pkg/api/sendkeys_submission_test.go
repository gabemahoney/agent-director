package api_test

// Pending fallback submission uses same-launch hook activity, never pane text (b.qcf).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func assertSubmissionUnconfirmed(t *testing.T, err error, state string) {
	t.Helper()
	assertOneName(t, err, "ErrSubmissionUnconfirmed")
	want := api.SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: state}
	if got := api.ErrDetails(err); got != want {
		t.Errorf("details = %+v; want %+v", got, want)
	}
	b, marshalErr := json.Marshal(api.ErrDetails(err))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"keys_sent": true, "submission_confirmed": false, "row_state": state}; !reflect.DeepEqual(got, want) {
		t.Errorf("wire details = %s; want %+v", b, want)
	}
}

// assertPendingInput checks the exact input writes (assertInputWrites) and
// the terminal reads: pending Claude's wait makes two (b.qcf), Codex none.
// Claude's cells also keep b.qcf's exact merged call order (b.x9o, t2.7tz.5c.8d).
func assertPendingInput(t *testing.T, e *submissionEnv, r killRow, agent, text string) {
	t.Helper()
	assertInputWrites(t, e.killEnv, r, text)
	if agent == "claude" {
		e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, text, 2)
	}
	if got, want := len(e.rec.SocketCallsOf(tmux.CallPaneTerminal)), map[string]int{"codex": 0, "claude": 2}[agent]; got != want {
		t.Errorf("terminal reads = %d; want %d", got, want)
	}
}

func TestSendKeysPendingSubmissionUnconfirmed(t *testing.T) {
	t.Parallel()
	// b.qcf: Claude fallback shares Codex's bounded hook check.
	for _, agent := range []string{"codex", "claude"} {
		for _, state := range []string{store.StatePending, store.StateWaiting} {
			for _, text := range []string{skpText, ""} {
				if agent == "claude" && text == "" {
					continue // Enter-only does not get Claude's new hook check.
				}
				t.Run(agent+"/"+state+"/text="+text, func(t *testing.T) {
					t.Parallel()
					e := newSubmissionEnv(t)
					r := e.seedRow(t, killRowSpec{State: store.StatePending, Opts: agentRowOpts(agent)})
					before := e.columns(t, r.ID)
					if agent == "claude" {
						e.sessionStartsWhenPolled(t, r, func() { before = e.columns(t, r.ID) })
					}
					e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) { row.State = state; return row, nil }

					err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: text, AllowPending: true})

					assertSubmissionUnconfirmed(t, err, state)
					name := "Codex"
					if agent == "claude" {
						name = "Claude Code"
					}
					wantMessage := fmt.Sprintf("ErrSubmissionUnconfirmed: send-keys: %s: tmux accepted the keys and Enter, but %s submission could not be confirmed (no turn activity was observed within 2 seconds); do not repeat the text or send another Enter automatically; check hook-derived get or status; if submission remains unknown, escalate to the caller or a human", r.ID, name)
					if err.Error() != wantMessage {
						t.Errorf("message = %q; want %q", err, wantMessage)
					}
					if e.slept != 2*time.Second {
						t.Errorf("wait = %v; want 2s", e.slept)
					}
					if e.s.reads != 20 {
						t.Errorf("submission reads = %d; want 20 before the deadline", e.s.reads)
					}
					assertPendingInput(t, e, r, agent, text)
					if text == "" {
						e.assertNoCalls(t, tmux.CallSendText)
						if len(e.rec.SocketCallsOf(tmux.CallSendKey))+len(e.rec.SocketCallsOf(tmux.CallSendEnter)) != 1 {
							t.Fatal("Enter-only input issued another key")
						}
					}
					e.assertNoCalls(t, tmux.CallCapture)
					e.assertRowUnchanged(t, r.ID, before)
				})
			}
		}
	}
}

func TestSendKeysPendingSubmissionActivity(t *testing.T) {
	t.Parallel()
	// b.qcf: Claude fallback shares Codex's bounded hook check.
	for _, agent := range []string{"codex", "claude"} {
		for _, state := range []string{store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
			for _, delay := range []int{0, 2} {
				t.Run(fmt.Sprintf("%s/%s/after %d waits", agent, state, delay), func(t *testing.T) {
					t.Parallel()
					e := newSubmissionEnv(t)
					r := e.seedRow(t, killRowSpec{State: store.StatePending, Opts: agentRowOpts(agent)})
					e.s.read = func(n int, row api.Spawn) (api.Spawn, error) {
						row.State = store.StateWaiting
						if n > delay {
							row.State = state
						}
						return row, nil
					}
					if err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true}); err != nil {
						t.Fatalf("same-launch activity did not confirm: %v", err)
					}
					if e.waits != delay {
						t.Errorf("waits = %d; want %d", e.waits, delay)
					}
					assertPendingInput(t, e, r, agent, skpText)
					e.assertNoCalls(t, tmux.CallCapture)
				})
			}
		}
	}
}

func TestSendKeysCodexSubmissionEvidenceLost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, state string
		change      func(*api.Spawn)
		err         error
		failAt      int
	}{
		{name: "ended", state: store.StateEnded},
		{name: "missing", state: store.StateMissing},
		{name: "new life working", state: store.StateWorking, change: func(row *api.Spawn) { row.LifeNumber++ }},
		{name: "new token working", state: store.StateWorking, change: func(row *api.Spawn) { row.Identity.Token = newToken() }},
		{name: "read failed", state: store.StatePending, err: fmt.Errorf("read evidence: %w", api.ErrStoreBusy)},
		{name: "row deleted", state: store.StatePending, err: api.ErrSpawnNotFound},
		{name: "read failed after waiting", state: store.StateWaiting, err: api.ErrStoreBusy, failAt: 2},
		{name: "read failed with 200ms remaining", state: store.StatePending, err: api.ErrStoreBusy, failAt: 19},
		{name: "read failed with 100ms remaining", state: store.StatePending, err: api.ErrStoreBusy, failAt: 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			before := e.columns(t, r.ID)
			e.s.read = func(n int, row api.Spawn) (api.Spawn, error) {
				row.State = tc.state
				if tc.change != nil {
					tc.change(&row)
				}
				if tc.failAt > n {
					return row, nil
				}
				return row, tc.err
			}
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertSubmissionUnconfirmed(t, err, tc.state)
			wantWaits := max(0, tc.failAt-1)
			if e.waits != wantWaits {
				t.Errorf("waits = %d; want %d before evidence was lost", e.waits, wantWaits)
			}
			if want := time.Duration(wantWaits) * 100 * time.Millisecond; e.slept != want {
				t.Errorf("wait = %v; want %v before evidence was lost", e.slept, want)
			}
			assertInputWrites(t, e.killEnv, r, skpText)
			e.assertNoCalls(t, tmux.CallCapture)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

func TestSendKeysCodexSubmissionBoundedClock(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fixed clock", "deadline reached during wait", "deadline passed during wait"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			sleep := e.env.Sleep
			if mode == "fixed clock" {
				now := e.clock.Now()
				e.env.Relay.Now = func() time.Time { return now }
			} else {
				extra := 3 * time.Second
				if mode == "deadline reached during wait" {
					extra = 2*time.Second - 100*time.Millisecond
				}
				e.env.Sleep = func(d time.Duration) { sleep(d); e.clock.Advance(extra) }
				e.s.read = func(n int, row api.Spawn) (api.Spawn, error) {
					if n > 1 {
						row.State = store.StateWorking
					}
					return row, nil
				}
			}
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertSubmissionUnconfirmed(t, err, store.StatePending)
			if mode == "fixed clock" && (e.s.reads > 21 || e.waits > 20) {
				t.Errorf("reads/waits = %d/%d; want bounded by 21/20", e.s.reads, e.waits)
			}
			if mode != "fixed clock" && (e.waits != 1 || e.s.reads != 1) {
				t.Errorf("reads/waits = %d/%d; want 1/1, no read at or after the deadline", e.s.reads, e.waits)
			}
		})
	}
}

func TestSendKeysCodexSubmissionRemainingReadBudget(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
		e.clock.Advance(650 * time.Millisecond)
		return row, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e.env.Context = ctx
	deadline, _ := ctx.Deadline()
	callerRemaining := time.Until(deadline)
	err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	assertSubmissionUnconfirmed(t, err, store.StatePending)
	if got := e.s.preparation; len(got) != 1 || got[0] <= 0 || got[0] > 12*time.Second || got[0] > callerRemaining {
		t.Errorf("preparation read budgets = %v; want one positive read <= 12s and caller remaining %v", got, callerRemaining)
	}
	want := []time.Duration{2 * time.Second, 1250 * time.Millisecond, 500 * time.Millisecond}
	if !reflect.DeepEqual(e.s.waits, want) {
		t.Errorf("submission read budgets = %v; want %v including time spent reading and sleeping", e.s.waits, want)
	}
	assertInputWrites(t, e.killEnv, r, skpText)
}

func TestSendKeysPendingSubmissionActivityReadDeadline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		readTime time.Duration
		confirm  bool
	}{
		{"just before deadline", 2*time.Second - time.Nanosecond, true},
		{"at deadline", 2 * time.Second, false},
		{"after deadline", 2*time.Second + time.Nanosecond, false},
	}
	// b.qcf: late Claude hook evidence cannot turn uncertainty into success.
	// State membership is covered by TestSendKeysPendingSubmissionActivity.
	for _, agent := range []string{"codex", "claude"} {
		for _, tc := range cases {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newSubmissionEnv(t)
				r := e.seedRow(t, killRowSpec{State: store.StatePending, Opts: agentRowOpts(agent)})
				e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
					e.clock.Advance(tc.readTime)
					row.State = store.StateWorking
					return row, nil
				}

				err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})

				if tc.confirm {
					if err != nil {
						t.Fatalf("activity read before deadline did not confirm: %v", err)
					}
				} else {
					assertSubmissionUnconfirmed(t, err, store.StateWorking)
				}
				if e.s.reads != 1 || e.waits != 0 {
					t.Errorf("submission reads/waits = %d/%d; want 1/0", e.s.reads, e.waits)
				}
				assertPendingInput(t, e, r, agent, skpText)
			})
		}
	}
}

func TestSendKeysCodexSubmissionConnectionContention(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	e.s.boundedReads = true // This case exercises the real store's remaining connection wait.
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	before := e.columns(t, r.ID)
	free, held, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	holderStarted := false
	release := func() { once.Do(func() { close(free) }) }
	watchdog := time.AfterFunc(5*time.Second, release)
	t.Cleanup(func() { watchdog.Stop(); release() })
	e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
		e.clock.Advance(1850 * time.Millisecond)
		return row, nil
	}
	sleep := e.env.Sleep
	e.env.Sleep = func(d time.Duration) {
		sleep(d)
		if holderStarted {
			return
		}
		holderStarted = true
		// The transaction's precondition holds the real store connection without writing.
		go func() {
			_, _, _, err := e.st.AckRelayDecision(r.ID, "absent-request", e.clock.Now(), store.DefaultLockWait, func() error {
				close(held)
				<-free
				return nil
			})
			done <- err
		}()
		select {
		case <-held:
		case err := <-done:
			t.Fatalf("hold store connection: %v", err)
		}
	}

	start := time.Now()
	err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	elapsed := time.Since(start)
	release()
	if holderStarted {
		if holdErr := <-done; holdErr != nil {
			t.Fatalf("release store connection: %v", holdErr)
		}
	}
	assertSubmissionUnconfirmed(t, err, store.StatePending)
	if !strings.Contains(err.Error(), "ErrStoreBusy") {
		t.Errorf("err = %v; want the bounded connection wait to explain the unconfirmed submission", err)
	}
	if elapsed > time.Second {
		t.Errorf("submission check took %v; want the remaining 50ms wait with scheduling allowance", elapsed)
	}
	if got := e.s.preparation; len(got) != 1 || got[0] <= 0 || got[0] > 12*time.Second {
		t.Errorf("preparation read budgets = %v; want one positive read <= 12s", got)
	}
	if want := []time.Duration{2 * time.Second, 50 * time.Millisecond}; !reflect.DeepEqual(e.s.waits, want) {
		t.Errorf("submission read budgets = %v; want %v", e.s.waits, want)
	}
	assertInputWrites(t, e.killEnv, r, skpText)
	e.assertNoCalls(t, tmux.CallCapture)
	e.assertRowUnchanged(t, r.ID, before)
}

func TestSendKeysSubmissionApplicability(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		spec     killRowSpec
		params   api.SendKeysParams
		paste    bool
		hook     bool
		terminal bool
	}{
		// b.qcf: only ordinary pending Claude fallback gains a terminal read and hook check.
		{"pending Claude", killRowSpec{State: store.StatePending}, api.SendKeysParams{Text: skpText}, false, true, true},
		{"waiting Claude", killRowSpec{}, api.SendKeysParams{Text: skpText}, false, false, false},
		{"pending Claude Enter-only", killRowSpec{State: store.StatePending}, api.SendKeysParams{}, false, false, false},
		{"pending Claude no_enter", killRowSpec{State: store.StatePending}, api.SendKeysParams{Text: skpText, NoEnter: true}, false, false, false},
		{"pending Claude key", killRowSpec{State: store.StatePending}, api.SendKeysParams{Key: "Enter"}, false, false, false},
		{"pending Claude hash", killRowSpec{State: store.StatePending}, api.SendKeysParams{Text: skpText, ExpectPaneSHA256: api.PaneSHA256("")}, false, false, false},
		{"pending Codex", codexSpec(store.StatePending, agentAlive, false), api.SendKeysParams{Text: skpText}, true, true, false},
		{"pending Cursor", killRowSpec{State: store.StatePending, Opts: agentRowOpts("cursor")}, api.SendKeysParams{Text: skpText}, false, false, false},
		{"waiting Codex", codexSpec(store.StateWaiting, agentAlive, false), api.SendKeysParams{Text: skpText}, true, false, false},
		{"working Codex", codexSpec(store.StateWorking, agentAlive, false), api.SendKeysParams{Text: skpText}, true, false, false},
		{"pending Codex no_enter", codexSpec(store.StatePending, agentAlive, false), api.SendKeysParams{Text: "manual $", NoEnter: true}, false, false, false},
		{"pending Codex named key", codexSpec(store.StatePending, agentAlive, false), api.SendKeysParams{Key: "Enter"}, false, false, false},
		{"pending Codex character key", codexSpec(store.StatePending, agentAlive, false), api.SendKeysParams{Key: "x"}, false, false, false},
		{"waiting Codex empty", codexSpec(store.StateWaiting, agentAlive, false), api.SendKeysParams{}, false, false, false},
		{"waiting Codex hash", codexSpec(store.StateWaiting, agentAlive, false), api.SendKeysParams{Text: "manual $", ExpectPaneSHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, tc.spec)
			p := tc.params
			p.InstanceID, p.AllowPending = r.ID, true
			err := e.send(p)
			if tc.hook {
				wantState := store.StatePending
				if tc.terminal {
					wantState = store.StateWaiting
				} // b.qcf: SessionStart precedes input.
				assertSubmissionUnconfirmed(t, err, wantState)
				if e.s.reads != 20 || e.slept != 2*time.Second {
					t.Errorf("hook reads/sleep=%d/%v; want 20/2s", e.s.reads, e.slept)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if e.s.reads != 0 || e.waits != 0 {
					t.Errorf("submission reads/waits = %d/%d; want none", e.s.reads, e.waits)
				}
			}
			if reads := len(e.rec.SocketCallsOf(tmux.CallPaneTerminal)); reads != map[bool]int{false: 0, true: 2}[tc.terminal] {
				t.Errorf("terminal reads=%d; want selected=%v", reads, tc.terminal)
			}
			pastes := 0
			for _, call := range e.rec.SocketCallsOf(tmux.CallSendText) {
				if call.Paste {
					pastes++
				}
			}
			if pastes != map[bool]int{false: 0, true: 1}[tc.paste] {
				t.Errorf("paste calls=%d; want selected=%v", pastes, tc.paste)
			}
		})
	}
}

func TestSendKeysCodexSubmissionTmuxFailurePrecedence(t *testing.T) {
	t.Parallel()
	for _, call := range []tmux.Call{tmux.CallSendText, tmux.CallSendKey} {
		t.Run(string(call), func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, call)
			// b.qcr: preserve the manual transport's original error contract;
			// ordinary Codex paste has separate structured-uncertainty coverage.
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true,
				ExpectPaneSHA256: fmt.Sprintf("%x", sha256.Sum256(nil))})
			assertOneName(t, err, "ErrTmuxUnresponsive")
			var want any
			if call == tmux.CallSendKey {
				want = api.SubmissionUnconfirmedDetails{
					Outcome: "submission_unconfirmed", Phase: "enter_attempted", BytesAttempted: len(skpText),
					EnterMayHaveBeenSent: true, KeysSent: true, RowState: store.StatePending,
					Reason: "text was sent; following Enter failed",
				}
			}
			if e.s.reads != 0 || e.waits != 0 || api.ErrDetails(err) != want {
				t.Errorf("submission reads/waits/details = %d/%d/%+v; want 0/0/%+v after tmux failure", e.s.reads, e.waits, api.ErrDetails(err), want)
			}
		})
	}
}

// untypedSubmissionTmux keeps the recorded transport boundary but returns an
// uncatalogued Enter error, as a third-party SendKeysTmux implementation may.
type untypedSubmissionTmux struct{ *tmuxfix.Recorder }

func (t untypedSubmissionTmux) SendKeyPane(socket, pane, key string) error {
	if err := t.Recorder.SendKeyPane(socket, pane, key); err != nil {
		return errors.New("transport reply unavailable")
	}
	return nil
}

// E3, t2.7tz.tb.ib round 2: "send-keys with empty text submits it" after a
// separate Enter failure must retain progress and never repeat successful text.
func TestAdviceFollow_E3_SeparateEnterFailurePreservesProgress(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "send-keys", "", advSendKeysEmptyTextManifest)
	for _, tc := range []struct {
		name             string
		failure          tmux.Failure
		reached, untyped bool
		advice           string
	}{
		{"timeout before Enter", tmux.FailTimeout, false, false, advSendKeysEnterTimeout},
		{"timeout after Enter", tmux.FailTimeout, true, false, advSendKeysEnterTimeout},
		{"typed failure", tmux.FailUnrecognized, false, false, advSendKeysNotSubmitted},
		{"untyped failure", tmux.FailUnrecognized, false, true, advSendKeysNotSubmitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{})
			ktrRestart(t, e.killEnv, &r) // Make the delivery consequence observable in the trail.
			before := e.columns(t, r.ID)
			in := watchPaneInput(e.killEnv, r.Spawn.Identity.PaneID, tc.reached)
			lookupsAtEnter := 0
			// The shared composer watches combined Enter. Observe this route's
			// separate key through the same composer, without changing defaults.
			e.rec.AfterCall(tmux.CallSendKey, func(c tmuxfix.SocketCall, err error) {
				lookupsAtEnter = len(e.rec.SocketCallsOf(tmux.CallLookup))
				if c.Target == r.Spawn.Identity.PaneID && c.Key == "Enter" && in.reached(err) {
					in.submit()
				}
			})
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tc.failure, Times: 1}, tmux.CallSendKey)
			var transport api.SendKeysTmux = e.rec
			if tc.untyped {
				transport = untypedSubmissionTmux{e.rec}
			}
			const text = "first\n界"
			_, err := api.SendKeys(e.s, transport, e.env, api.SendKeysParams{InstanceID: r.ID, Text: "first\r\n界"})
			assertOneName(t, err, "ErrTmuxUnresponsive")
			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, tc.advice)
			want := api.SubmissionUnconfirmedDetails{
				Outcome: "submission_unconfirmed", Phase: "enter_attempted", BytesAttempted: len(text),
				EnterMayHaveBeenSent: true, KeysSent: true, RowState: store.StateWaiting,
				Reason: "text was sent; following Enter failed",
			}
			if got := api.ErrDetails(err); got != want {
				t.Fatalf("details=%+v; want %+v", got, want)
			}
			assertInputWrites(t, e.killEnv, r, text)
			assertKeysDisagrees(t, e.killEnv, pendTrail(t, "ad.provenance.disagree", r.ID), r, "send-keys", apitest.OtherStoreID(e.storeID),
				[]disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "ours", action: "text_sent", ours: true}})
			followups := 1
			if tc.failure == tmux.FailTimeout {
				followups = 0
				adviceAssertPhrase(t, err, fmt.Sprintf("tmux key send failed: no answer within %g s", e.cfg.EffectiveActionTimeout().Seconds()))
			}
			if got := len(e.rec.SocketCallsOf(tmux.CallLookup)) - lookupsAtEnter; got != followups {
				t.Errorf("follow-up lookups=%d; want %d", got, followups)
			}
			if e.s.reads != 0 || e.waits != 0 {
				t.Fatalf("submission reads/waits=%d/%d; want no polling after transport failure", e.s.reads, e.waits)
			}
			e.assertRowUnchanged(t, r.ID, before)

			// Follow the existing advice literally; even a timed-out Enter that
			// reached the composer must not lead to a second text submission.
			e.rec.Reset()
			if err := e.send(api.SendKeysParams{InstanceID: r.ID}); err != nil {
				t.Fatalf("Enter-only follow-up: %v", err)
			}
			assertInputWrites(t, e.killEnv, r, "")
			e.assertNoCalls(t, tmux.CallSendText)
			if len(e.rec.SocketCallsOf(tmux.CallSendKey))+len(e.rec.SocketCallsOf(tmux.CallSendEnter)) != 1 {
				t.Fatal("Enter-only follow-up issued another key")
			}
			in.assertSubmittedOnce(t, text)
		})
	}
}

// t2.7tz.tb.ib round 2: a real admission failure after text cannot report
// pre_input/zero bytes or permit the following Enter under stale authority.
func TestSendKeysPostTextAdmissionRetainsProgress(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancelled caller", "changed row revision"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{})
			ktrRestart(t, e.killEnv, &r)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.env.Context = ctx
			if mode == "cancelled caller" {
				e.rec.AfterCall(tmux.CallSendText, func(tmuxfix.SocketCall, error) { cancel() })
			} else {
				e.rowWriteAfter(t, tmux.CallSendText, r)
			}
			const text = "first\n界"
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: "first\r\n界"})
			assertOneName(t, err, "ErrSubmissionUnconfirmed")
			want := api.SubmissionUnconfirmedDetails{
				Outcome: "partial_input_unconfirmed", Phase: "text_attempted", BytesAttempted: len(text),
				KeysSent: true, RowState: store.StateWaiting,
				Reason: "input admission changed after text; Enter was not sent",
			}
			if got := api.ErrDetails(err); got != want {
				t.Fatalf("details=%+v; want %+v", got, want)
			}
			adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "unconfirmed input may still submit later; do not automatically replay text or Enter")
			calls := e.rec.SocketCallsOf(tmux.CallSendText)
			if len(calls) != 1 || calls[0].Text != text || calls[0].PressEnter || calls[0].Target != r.Spawn.Identity.PaneID || calls[0].Socket != r.Socket {
				t.Fatalf("text writes=%+v; want the exact normalized bytes once to the owned pane", calls)
			}
			e.assertNoCalls(t, tmux.CallSendEnter, tmux.CallSendKey, tmux.CallCapture)
			assertKeysDisagrees(t, e.killEnv, pendTrail(t, "ad.provenance.disagree", r.ID), r, "send-keys", apitest.OtherStoreID(e.storeID),
				[]disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "ours", action: "text_sent", ours: true}})
			if e.s.reads != 0 || e.waits != 0 {
				t.Fatalf("submission reads/waits=%d/%d; want no polling after lost admission", e.s.reads, e.waits)
			}
		})
	}
}

// E10: "check hook-derived get or status" after keys were accepted.
func TestAdviceFollow_E10_SubmissionUnconfirmedInspectGet(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "send-keys", "", "ErrSubmissionUnconfirmed: never auto-retry; gateway_refused: unsubmitted, else hook-derived get/status, escalate if unknown.")
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	assertSubmissionUnconfirmed(t, err, store.StatePending)
	adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "check hook-derived get or status; if submission remains unknown, escalate to the caller or a human")
	row, err := api.Get(e.st, e.env.Relay, r.ID)
	if err != nil || row.State != store.StatePending {
		t.Fatalf("Get after unconfirmed = %+v, %v; want pending, no error", row, err)
	}
	assertInputWrites(t, e.killEnv, r, skpText)
}

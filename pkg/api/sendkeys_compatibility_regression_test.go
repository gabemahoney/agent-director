package api_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestSendKeysPlainLaunchRetainsEstablishedInput(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		t.Run(state, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{State: state})
			e.env.Input = nil // No native launch manifest: the pane is an ordinary stand-in.
			for _, p := range []api.SendKeysParams{
				{InstanceID: r.ID, Text: "manual prefix", NoEnter: true},
				{InstanceID: r.ID, Text: "first\r\n" + strings.Repeat("界", 300)},
				{InstanceID: r.ID, Text: "second prompt"},
			} {
				e.rec.Reset()
				result, err := e.send(p)
				if err != nil || result != (api.SendKeysResult{}) {
					t.Fatalf("plain input result=%+v err=%v; want established delivery without native proof", result, err)
				}
				if p.NoEnter {
					calls := e.rec.SocketCallsOf(tmux.CallSendText)
					if len(calls) != 1 || calls[0].Text != p.Text || calls[0].PressEnter || calls[0].Socket != r.Socket || calls[0].Target != r.Spawn.Identity.PaneID {
						t.Fatalf("manual text calls=%+v", calls)
					}
					e.assertNoCalls(t, tmux.CallSendEnter, tmux.CallSendKey)
				} else {
					assertTextSent(t, e.rec, r.Socket, r.Spawn.Identity.PaneID, strings.ReplaceAll(p.Text, "\r", ""))
					assertEnterSent(t, e.rec, r.Socket, r.Spawn.Identity.PaneID)
				}
			}
		})
	}
}

type unavailableDeliveryBackend struct{ opened bool }

func (b *unavailableDeliveryBackend) Open(api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	b.opened = true
	return nil, errors.New("native journal unavailable")
}

func TestSendKeysEstablishedGuardsPrecedeNativeJournal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state string
		relay skRelay
		want  error
	}{
		{"ended", store.StateEnded, skRelayOff, api.ErrSpawnNotInteractive},
		{"pending without opt in", store.StatePending, skRelayOff, api.ErrSpawnNotInteractive},
		{"relay held", store.StateCheckPermission, skRelayHeld, api.ErrSendKeysWhileRelayed},
		{"relay fallen back", store.StateCheckPermission, skRelayFallenBack, api.ErrRelayFallenBack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{State: tc.state, RelayOn: tc.relay != skRelayOff})
			if tc.relay != skRelayOff {
				storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
			}
			if tc.relay == skRelayFallenBack {
				storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA, 2*sendKeysWindow())
			}
			e.env.Relay.Now = time.Now
			backend := &unavailableDeliveryBackend{}
			e.env.Input = backend
			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: "ordinary input"})
			if !errors.Is(err, tc.want) || backend.opened {
				t.Fatalf("guard=%v native opened=%v; want %v before native I/O", err, backend.opened, tc.want)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

func TestSendKeysDialogGuardPrecedesNativeJournal(t *testing.T) {
	t.Parallel()
	p := newPAEnv(t)
	r7Acked(t, p)
	backend := &unavailableDeliveryBackend{}
	_, err := api.SendKeys(p.sks, p.rec, api.SendKeysEnv{Relay: p.view(), Input: backend}, p.plain("continue"))
	if !errors.Is(err, api.ErrDialogMaybeOpen) || backend.opened {
		t.Fatalf("guard=%v native opened=%v; want dialog hold before native I/O", err, backend.opened)
	}
	p.assertNothingSent(t)
}

type capabilityDeliveryBackend struct {
	*deliveryBackend
	supported bool
}

func (b *capabilityDeliveryBackend) Open(id api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	s, err := b.deliveryBackend.Open(id)
	if err != nil {
		return nil, err
	}
	return &capabilityDeliverySession{InputDeliverySession: s, supported: b.supported}, nil
}

type capabilityDeliverySession struct {
	api.InputDeliverySession
	supported bool
}

func (s *capabilityDeliverySession) Supports(api.InputDeliveryIdentity) bool { return s.supported }

func TestSendKeysUnsupportedNativeCapabilityKeepsEstablishedInput(t *testing.T) {
	t.Parallel()
	e := newNativeSubmissionEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateWaiting})
	e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
	e.b.ready = func() (bool, error) { t.Fatal("unsupported launch polled native readiness"); return false, nil }
	e.b.queue = func() (int, error) { t.Fatal("unsupported launch polled native queue"); return 0, nil }
	e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) {
		t.Fatal("unsupported launch read native proof")
		return nil, nil
	}
	payload := strings.Repeat("界", 300)
	result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: payload})
	if err != nil || result != (api.SendKeysResult{}) || e.b.current != nil {
		t.Fatalf("unsupported native result=%+v err=%v attempt=%+v", result, err, e.b.current)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, payload)
}

func TestSendKeysCapabilityLossNeverReplaysNativeAttempt(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "after Enter", true: "partial text"}[partial], func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
			if partial {
				e.b.queue = func() (int, error) {
					if e.b.current != nil && e.b.current.BytesAttempted > 0 {
						return 1, nil
					}
					return 0, nil
				}
			}
			params := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true, DeliveryRequestID: "owner"}
			_, err := e.send(params)
			outcome := "submission_unconfirmed"
			if partial {
				outcome = "partial_input_unconfirmed"
			}
			assertDeliveryError(t, err, outcome, len(skpText), !partial)
			owner := cloneAttempt(e.b.current)
			texts, enters := len(e.rec.SocketCallsOf(tmux.CallSendText)), len(ordinaryEnters(e.killEnv))
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			for _, id := range []string{"owner", "fresh"} {
				params.DeliveryRequestID = id
				_, err = e.send(params)
				assertDeliveryError(t, err, outcome, len(skpText), !partial)
				if !reflect.DeepEqual(owner, e.b.current) || len(e.rec.SocketCallsOf(tmux.CallSendText)) != texts || len(ordinaryEnters(e.killEnv)) != enters {
					t.Fatal("capability fallback replaced or replayed the unresolved native owner")
				}
			}
		})
	}
}

// b.qcf: raw mode alone cannot authorize input after the launch or its guards change.
// Refusals report the last row read, even after readiness succeeds.
func TestSendKeysPendingClaudeWaitsForRawMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ change, state string }{
		{"unchanged", store.StatePending}, {"waiting", store.StateWaiting},
		{"life", store.StatePending}, {"token", store.StatePending},
		{"ended", store.StateEnded}, {"working", store.StateWorking},
		{"replacement server", store.StateWaiting}, {"relay", store.StateWaiting}, {"dialog", store.StateWaiting},
		{"reread failure", store.StatePending},
		{"journal write failure", store.StateWaiting}, {"hook read failure", store.StateWaiting},
	} {
		t.Run(tc.change, func(t *testing.T) {
			t.Parallel()
			change := tc.change
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StatePending, RelayOn: change == "relay" || change == "dialog"})
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			// A finished earlier attempt makes premature manual journalling observable.
			if change != "unchanged" {
				e.b.current = &api.InputDeliveryAttempt{RequestID: "earlier", Phase: "not_started"}
			}
			prior := cloneAttempt(e.b.current)
			e.rec.SetTerminalRaw(r.Socket, r.Spawn.Identity.PaneID, false, false, true)
			reads := 0
			e.rec.AfterCall(tmux.CallPaneTerminal, func(c tmuxfix.SocketCall, err error) {
				reads++
				if err != nil || c.Socket != r.Socket || c.Target != r.Spawn.Identity.PaneID {
					t.Fatalf("terminal read = %+v, %v; want the selected pane", c, err)
				}
				e.assertNothingSent(t)
				if !reflect.DeepEqual(e.b.current, prior) || len(e.b.history) != 0 {
					t.Fatal("input journalled before raw mode")
				}
			})
			e.rec.AfterCall(tmux.CallSendText, func(_ tmuxfix.SocketCall, _ error) {
				if reads != 3 {
					t.Fatalf("text sent after %d terminal reads; want the first raw answer", reads)
				}
			})
			before := e.columns(t, r.ID)
			sleep := e.env.Sleep
			changed := false
			reason := ""
			journalWrites, hookReads, waitingReads := 0, 0, 0
			e.env.Sleep = func(d time.Duration) {
				sleep(d)
				if changed {
					return
				}
				changed = true
				if tc.state == store.StateWaiting {
					if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionStart", "raw-wait-session"); !a.Applied {
						t.Fatalf("SessionStart during wait: %+v", a)
					}
				}
				switch change {
				case "life", "token":
					e.s.reread = func(row api.Spawn) (api.Spawn, error) {
						if change == "life" {
							row.LifeNumber++
						} else {
							row.Identity.Token = tmuxfix.OtherToken
						}
						return row, nil
					}
					reason = "launch identity changed"
				case "ended", "working":
					event := "SessionEnd"
					if change == "working" {
						event = "UserPromptSubmit"
					}
					if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, event, "raw-wait-session"); !a.Applied {
						t.Fatalf("%s during wait: %+v", event, a)
					}
					reason = "ordinary composer is not waiting (state=" + tc.state + ")"
				case "reread failure":
					e.s.reread = func(row api.Spawn) (api.Spawn, error) {
						row.State = store.StateWaiting // Data accompanying a failed read is not an observation.
						return row, errors.New("injected read failure")
					}
					reason = "launch cannot be reread"
				case "journal write failure", "hook read failure":
					// b.qcf: both failures must retain waiting from this successful reread.
					e.s.reread = func(row api.Spawn) (api.Spawn, error) {
						if row.State != store.StateWaiting {
							t.Fatalf("readiness reread state=%q; want waiting", row.State)
						}
						waitingReads++
						return row, nil
					}
					if change == "journal write failure" {
						e.b.save = func(*api.InputDeliveryAttempt) error {
							journalWrites++
							return errors.New("injected journal write failure")
						}
					}
				case "replacement server":
					e.rec.RestartServer(r.Socket, tmuxfix.Server{})
					e.syncServers()
					e.seedOther(t, r.Socket, tmuxfix.SeedSession{
						Name: "unrelated-after-restart", Label: r.otherStore(tmuxfix.OtherToken),
						Panes: []tmuxfix.SeedPane{{ID: r.Spawn.Identity.PaneID, PID: e.newPID()}},
					})
					reason = "exact launch socket/label no longer verified"
				case "relay", "dialog":
					opts := []apitest.PermissionOption{apitest.PermissionTimes(e.clock.Now(), time.Time{}, time.Time{}, time.Time{}, time.Time{}, "")}
					want := api.ErrSendKeysWhileRelayed
					if change == "dialog" {
						opts = append(opts, apitest.PermissionAnswer(store.PaneAnswerOutside))
						want = api.ErrDialogMaybeOpen
					}
					if _, err := apitest.SeedPermissionRequest(e.dbPath, r.ID, "Bash", opts...); err != nil {
						t.Fatal(err)
					}
					_, refusal, err := api.EvaluateRelayGuardForTest(e.store, e.env.Relay, 0, r.Spawn, api.SendKeysParams{InstanceID: r.ID})
					if err != nil || !errors.Is(refusal, want) {
						t.Fatalf("new guard = %v, %v; want %v", refusal, err, want)
					}
					reason = "permission or dialog guard no longer permits ordinary input"
				}
				before = e.columns(t, r.ID)
			}
			if change == "hook read failure" {
				e.s.read = func(api.Spawn) (api.Spawn, error) {
					hookReads++
					return api.Spawn{}, errors.New("injected post-Enter read failure")
				}
			} else {
				e.turnStartsAfterEnter(t, r)
			}
			result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			if !changed || reads != 3 || e.slept != 200*time.Millisecond {
				t.Fatalf("changed=%v reads=%d waits=%v; want a change during two polls then raw", changed, reads, e.slept)
			}
			if change == "journal write failure" || change == "hook read failure" {
				if waitingReads != 1 {
					t.Fatalf("waiting rereads=%d; want one successful readiness reread", waitingReads)
				}
			}
			if change == "unchanged" || change == "waiting" || change == "hook read failure" {
				if change == "hook read failure" {
					assertSubmissionUnconfirmed(t, err, store.StateWaiting)
					if hookReads != 1 {
						t.Fatalf("hook reads=%d; want failure on the first post-Enter read", hookReads)
					}
					e.assertRowUnchanged(t, r.ID, before)
				} else if err != nil {
					t.Fatalf("fallback failed: %v", err)
				}
				if result != (api.SendKeysResult{}) {
					t.Fatalf("fallback result=%+v err=%v", result, err)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText, 3)
				if change != "unchanged" && (e.b.current == nil || e.b.current.Phase != "manual_input" || len(e.b.history) != 1) {
					t.Fatalf("ready fallback did not journal manual input: current=%+v history=%+v", e.b.current, e.b.history)
				}
				for _, call := range e.rec.SocketCallsOf(tmux.CallSendText) {
					if call.Paste {
						t.Fatal("Claude fallback changed literal transport to paste")
					}
				}
			} else {
				assertOneName(t, err, "ErrSubmissionUnconfirmed")
				want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: tc.state,
					Reason: reason + "; nothing was typed, so the same send-keys can be retried later"}
				if change == "journal write failure" {
					want.Reason = "cannot persist manual input intent"
					if journalWrites != 1 {
						t.Fatalf("journal writes=%d; want one failed post-readiness write", journalWrites)
					}
				}
				e.assertNothingSent(t)
				e.assertNoCalls(t, tmux.CallSendKey)
				if !reflect.DeepEqual(e.b.current, prior) || len(e.b.history) != 0 || e.s.sent {
					t.Fatal("refusal journalled or sent input")
				}
				e.assertRowUnchanged(t, r.ID, before)
				if got := api.ErrDetails(err); got != want {
					t.Fatalf("refusal=%+v; want %+v", got, want)
				}
			}
		})
	}
}

// b.qcf: neither a late raw reply nor slow revalidation may authorize input.
// Each terminal read receives what remains of the original 12-second budget.
func TestSendKeysPendingClaudeReadinessDeadline(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"raw reply", "row reread", "ownership lookup"} {
		for _, elapsed := range []time.Duration{12 * time.Second, 12500 * time.Millisecond} {
			t.Run(stage+"/"+elapsed.String(), func(t *testing.T) {
				t.Parallel()
				e := newNativeSubmissionEnv(t)
				r := e.seedRow(t, killRowSpec{State: store.StatePending})
				before := e.columns(t, r.ID)
				e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
				e.b.current = &api.InputDeliveryAttempt{RequestID: "earlier", Phase: "not_started"}
				prior := cloneAttempt(e.b.current)
				e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: time.Nanosecond, Action: 2 * time.Second})
				e.rec.SetTerminalRaw(r.Socket, r.Spawn.Identity.PaneID, false, false, false, false, false, true)
				// A late raw reply never rereads the row; later refusals have observed waiting.
				sleep := e.env.Sleep
				changed := false
				e.env.Sleep = func(d time.Duration) {
					sleep(d)
					if !changed {
						changed = true
						if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionStart", "raw-wait-session"); !a.Applied {
							t.Fatalf("SessionStart during wait: %+v", a)
						}
						before = e.columns(t, r.ID)
					}
				}
				var start time.Time
				reads := 0
				e.rec.AfterCall(tmux.CallPaneTerminal, func(c tmuxfix.SocketCall, err error) {
					if start.IsZero() {
						start = e.clock.Now().Add(-2 * time.Second)
					}
					wantBudget := 12*time.Second - time.Duration(reads)*2100*time.Millisecond
					if err != nil || c.MaxWait != wantBudget || c.MaxWait <= 0 {
						t.Fatalf("read %d: %+v, %v; want allowance %v", reads+1, c, err, wantBudget)
					}
					reads++
					if reads == 5 {
						cost := 500 * time.Millisecond // raw at 11s, leaving time for revalidation
						if stage == "raw reply" {
							cost = elapsed - 10500*time.Millisecond
						}
						e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: time.Nanosecond, Action: cost})
					}
				})
				if stage == "row reread" {
					e.s.reread = func(row api.Spawn) (api.Spawn, error) {
						e.clock.Advance(start.Add(elapsed).Sub(e.clock.Now()))
						return row, nil
					}
				}
				if stage == "ownership lookup" {
					e.rec.AfterCall(tmux.CallLookup, func(_ tmuxfix.SocketCall, _ error) {
						if reads == 6 {
							e.clock.Advance(start.Add(elapsed).Sub(e.clock.Now()))
						}
					})
				}
				_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
				assertOneName(t, err, "ErrSubmissionUnconfirmed")
				lastState := store.StateWaiting
				if stage == "raw reply" {
					lastState = store.StatePending
				}
				want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: lastState,
					Reason: "the agent's terminal did not take raw input with its launch and guards verified within 12 seconds; nothing was typed, so the same send-keys can be retried later"}
				if got := api.ErrDetails(err); got != want {
					t.Fatalf("refusal=%+v; want %+v", got, want)
				}
				if reads != 6 {
					t.Fatalf("terminal reads=%d; want six", reads)
				}
				e.assertNothingSent(t)
				e.assertNoCalls(t, tmux.CallSendKey)
				if !reflect.DeepEqual(e.b.current, prior) || len(e.b.history) != 0 || e.s.sent {
					t.Fatal("late readiness journalled or sent input")
				}
				e.assertRowUnchanged(t, r.ID, before)
			})
		}
	}
}

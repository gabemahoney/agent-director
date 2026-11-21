package api_test

// sendkeys_persisted_proof_test.go: send-keys' manual input (keys, hashed text,
// no_enter, pane answers) through the real SQLite input journal
// (sqliteSubmission) on a Codex row, whose manual input needs no native
// capability. Busy send covers Claude, whose send-keys admits a Message; the
// attempts a Claude launch persisted before are input_proof_test.go's.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// t2.7tz.tb.ib R5: failed cleanup cannot turn dispatched input into pre_input,
// nor replace a primary refusal or transport failure with a cleanup diagnosis.
func TestSendKeysSQLiteCloseFailureRetainsDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text, key     string
		noEnter, permission bool
	}{
		{name: "text and Enter", text: "manual\r\n界"},
		{name: "text only", text: "manual\r\n界", noEnter: true},
		{name: "Enter only"},
		{name: "named key", key: "Escape"},
		{name: "literal key", key: "n"},
		{name: "named Enter key", key: "Enter"},
		{name: "permission answer", key: "n", permission: true},
		{name: "pre-input refusal", text: "must not reach pane"},
		{name: "primary Enter failure", text: "manual\r\n界"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, r, _ := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, tc.permission))
			const pane = "caller inspected this pane\n"
			e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, pane)
			params := api.SendKeysParams{InstanceID: r.ID, Text: tc.text, Key: tc.key, NoEnter: tc.noEnter,
				AllowPending: true, ExpectPaneSHA256: api.PaneSHA256(pane)}
			var permission *paEnv
			var atKey store.PermissionRow
			if tc.permission {
				permission = &paEnv{killEnv: e.killEnv, r: r, now: e.clock.Now(), self: paSender, sks: e.st, agent: store.AgentCodex}
				permission.pc.Set(paSender.PID, procfix.Alive(paSender.Starttime))
				permission.seedRequest(t, storefix.TestRequestTokenA, paToolUseID, relayEnvHook)
				params.RequestToken, params.As = storefix.TestRequestTokenA, "deny"
				e.env.Relay, e.env.Self = permission.view(), func() api.ProcessIdentity { return paSender }
				e.rec.AfterCall(paKeyCall(tc.key), func(tmuxfix.SocketCall, error) { atKey = permission.request(t, params.RequestToken) })
			}
			if tc.name == "pre-input refusal" {
				params.ExpectPaneSHA256 = api.PaneSHA256("stale pane")
			}
			if tc.name == "primary Enter failure" {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallSendKey)
			}
			closed := 0
			cleanup := errors.New("injected cleanup acknowledgement loss")
			e.env.Input.(*sqliteDeliveryBackend).afterClose = func() error {
				closed++
				return cleanup
			}
			row, err := e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := api.SendKeys(e.st, e.rec, e.env, params)
			if closed != 1 || result != (api.SendKeysResult{}) {
				t.Fatalf("close count=%d result=%+v", closed, result)
			}
			if !errors.Is(err, cleanup) {
				t.Errorf("cleanup failure is not observable: %v", err)
			}
			if tc.name == "pre-input refusal" {
				assertOneName(t, err, "ErrPaneChanged")
				if got := api.ErrDetails(err); got != (api.PaneChangedDetails{NLines: api.DefaultReadPaneLines}) {
					t.Errorf("cleanup replaced primary refusal details: %+v", got)
				}
				e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendKey, tmux.CallSendEnter)
				return
			}
			want := api.SubmissionUnconfirmedDetails{Outcome: "submission_unconfirmed", Phase: "enter_attempted",
				BytesAttempted: len(strings.ReplaceAll(tc.text, "\r", "")), EnterMayHaveBeenSent: true,
				KeysSent: true, RowState: row.State, Reason: "input writer cleanup failed: " + cleanup.Error()}
			if tc.noEnter {
				want.Outcome, want.Phase, want.EnterMayHaveBeenSent = "partial_input_unconfirmed", "text_attempted", false
			}
			if tc.key != "" {
				// Record the actual key dispatch, with no added Enter. Escape has
				// no literal bytes; n is one byte; an explicitly named Enter is Enter.
				want.Outcome, want.Phase, want.EnterMayHaveBeenSent = "partial_input_unconfirmed", "manual_input", false
				switch tc.key {
				case "n":
					want.Phase, want.BytesAttempted = "text_attempted", len(tc.key)
				case "Enter":
					want.Outcome, want.Phase, want.EnterMayHaveBeenSent = "submission_unconfirmed", "enter_attempted", true
				}
			}
			if tc.name == "primary Enter failure" {
				assertOneName(t, err, "ErrTmuxUnresponsive")
				want.Reason = "text was sent; following Enter failed"
			} else {
				assertOneName(t, err, "ErrSubmissionUnconfirmed")
			}
			if got := api.ErrDetails(err); got != want {
				t.Errorf("details=%+v; want %+v", got, want)
			}
			var writes []tmuxfix.SocketCall
			for _, c := range e.rec.SocketCalls() {
				if c.Call == tmux.CallSendText || c.Call == tmux.CallSendKey || c.Call == tmux.CallSendEnter {
					if c.Socket != r.Socket || c.Target != r.Spawn.Identity.PaneID {
						t.Fatalf("wrong input target: %+v", c)
					}
					writes = append(writes, c)
				}
			}
			if tc.key != "" {
				assertOneKey(t, writes, r.Spawn.Identity.PaneID, tc.key)
			} else {
				var wantWrites []tmuxfix.SocketCall
				if tc.text != "" {
					wantWrites = append(wantWrites, tmuxfix.SocketCall{Call: tmux.CallSendText, Socket: r.Socket, Target: r.Spawn.Identity.PaneID, Text: strings.ReplaceAll(tc.text, "\r", "")})
				}
				if !tc.noEnter {
					wantWrites = append(wantWrites, tmuxfix.SocketCall{Call: tmux.CallSendKey, Socket: r.Socket, Target: r.Spawn.Identity.PaneID, Key: "Enter"})
				}
				if !reflect.DeepEqual(writes, wantWrites) {
					t.Fatalf("writes=%+v; want %+v", writes, wantWrites)
				}
			}
			if permission != nil {
				after := permission.request(t, params.RequestToken)
				if atKey.PaneAnswer != store.PaneAnswerIntent || atKey.PaneAs != "deny" || atKey.PaneSender != paSender || atKey.Decision != "" ||
					after.PaneAnswer != store.PaneAnswerSent || after.Decision != "deny" || after.DecisionReason != store.DecisionReasonPane || after.AwaitsAnswer() {
					t.Fatalf("cleanup lost permission intent/sent: at key=%+v after=%+v", atKey, after)
				}
			}
		})
	}
}

// t2.7tz.tb.ib round 3: the caller's pane hash must survive admission waits
// before the first manual dispatch, but not be compared after our own text.
func TestSendKeysSQLiteManualPaneHashAfterAdmissionWait(t *testing.T) {
	t.Parallel()
	for _, input := range []struct {
		name, text string
		noEnter    bool
	}{
		{"text and Enter", "manual\r\n界", false},
		{"text only", "manual\r\n界", true},
		{"Enter only", "", false},
	} {
		for _, mode := range []string{"unchanged", "changed during admission", "cancelled during capture"} {
			t.Run(input.name+"/"+mode, func(t *testing.T) {
				e, r, _ := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, false))
				e.env.Input = nil // Production SQLite guards; manual input needs no terminal/proof stand-in.
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				e.env.Context = ctx
				const pane = "caller inspected this pane\n"
				e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, pane)
				captured, waited := false, false
				e.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) {
					captured = true
					if waited && mode == "cancelled during capture" {
						cancel()
					}
				})
				e.rec.AfterCall(tmux.CallListPanes, func(tmuxfix.SocketCall, error) {
					if !captured || waited {
						return
					}
					// The admission query finishes later with unchanged ownership;
					// only the visible pane changes while that query is outstanding.
					waited = true
					e.clock.Advance(100 * time.Millisecond)
					if mode == "changed during admission" {
						e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "a new dialog appeared\n")
					}
				})
				e.rec.AfterCall(tmux.CallSendText, func(tmuxfix.SocketCall, error) {
					e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "our own text changed the pane\n")
				})
				result, err := e.send(api.SendKeysParams{
					InstanceID: r.ID, Text: input.text, NoEnter: input.noEnter, AllowPending: true,
					ExpectPaneSHA256: api.PaneSHA256(pane),
				})
				if !waited {
					t.Fatal("did not exercise an admission wait after the caller hash check")
				}
				if mode == "changed during admission" {
					assertOneName(t, err, "ErrPaneChanged")
					e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendKey, tmux.CallSendEnter)
					return
				}
				if mode == "cancelled during capture" {
					assertOneName(t, err, "ErrSubmissionUnconfirmed")
					want := api.SubmissionUnconfirmedDetails{
						Outcome: "not_started", Phase: "pre_input", RowState: store.StateWaiting,
						Reason: "input admission expired during pane capture",
					}
					if ctx.Err() == nil || api.ErrDetails(err) != want {
						t.Fatalf("capture cancellation details=%+v, context=%v; want %+v", api.ErrDetails(err), ctx.Err(), want)
					}
					e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendKey, tmux.CallSendEnter)
					return
				}
				if err != nil || result != (api.SendKeysResult{}) {
					t.Fatalf("unchanged pane manual send=%+v %v", result, err)
				}
				texts := e.rec.SocketCallsOf(tmux.CallSendText)
				if input.text == "" {
					e.assertNoCalls(t, tmux.CallSendText)
				} else if len(texts) != 1 || texts[0].Text != strings.ReplaceAll(input.text, "\r", "") || texts[0].PressEnter || texts[0].Socket != r.Socket || texts[0].Target != r.Spawn.Identity.PaneID {
					t.Fatalf("manual text calls=%+v", texts)
				}
				if input.noEnter {
					e.assertNoCalls(t, tmux.CallSendKey, tmux.CallSendEnter)
				} else {
					enters := ordinaryEnters(e.killEnv)
					if len(enters) != 1 || enters[0].Socket != r.Socket || enters[0].Target != r.Spawn.Identity.PaneID || len(e.rec.SocketCallsOf(tmux.CallSendKey))+len(e.rec.SocketCallsOf(tmux.CallSendEnter)) != 1 {
						t.Fatalf("manual Enter calls=%+v; want exactly one to the owned pane", enters)
					}
				}
			})
		}
	}
}

// t2.7tz.tb.ib round 4: the adjacent key paths must finish their last SQLite
// admission wait before capturing the caller's hash, then fence capture cancellation.
func TestSendKeysSQLiteKeyPaneHashAfterAdmissionWait(t *testing.T) {
	t.Parallel()
	for _, permission := range []bool{false, true} {
		for _, key := range []string{"Escape", "n"} {
			for _, mode := range []string{"unchanged", "changed during admission", "cancelled during capture"} {
				name := "single key/"
				if permission {
					name = "permission pane/"
				}
				t.Run(name+key+"/"+mode, func(t *testing.T) {
					e, r, launch := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, permission))
					_, owner := seedSQLiteOwner(t, e, r, launch, "unconfirmed")
					e.rec.Reset()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					e.env.Context = ctx
					backend := e.env.Input.(*sqliteDeliveryBackend)
					admission := &sqliteAdmissionWaitContext{Context: ctx}
					backend.ctx = admission
					const pane = "caller inspected this pane\n"
					e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, pane)
					params := api.SendKeysParams{InstanceID: r.ID, Key: key, AllowPending: true, ExpectPaneSHA256: api.PaneSHA256(pane)}
					var p *paEnv
					if permission {
						p = &paEnv{killEnv: e.killEnv, r: r, now: e.clock.Now(), self: paSender, sks: e.st, agent: store.AgentCodex}
						p.pc.Set(paSender.PID, procfix.Alive(paSender.Starttime))
						p.seedRequest(t, storefix.TestRequestTokenA, paToolUseID, relayEnvHook)
						params.RequestToken, params.As = storefix.TestRequestTokenA, "deny"
						e.env.Relay, e.env.Self = p.view(), func() api.ProcessIdentity { return paSender }
					}
					row, err := e.st.GetSpawn(r.ID)
					if err != nil {
						t.Fatal(err)
					}
					captured, listed, waited := false, false, false
					e.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) {
						captured = true
						if waited && mode == "cancelled during capture" {
							cancel()
						}
					})
					e.rec.AfterCall(tmux.CallListPanes, func(tmuxfix.SocketCall, error) {
						if captured {
							listed = true
						}
					})
					free, held, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
					var once sync.Once
					release := func() { once.Do(func() { close(free) }) }
					defer release()
					watchdog := time.AfterFunc(5*time.Second, release)
					defer watchdog.Stop()
					checks, holderStarted := 0, false
					backend.beforeCheck = func() {
						if !listed {
							return
						}
						checks++
						// After the physical-target recheck, hold the extra guard
						// which R3 incorrectly placed after the final capture.
						if checks != 2 {
							return
						}
						holderStarted = true
						go func() {
							_, _, _, err := e.st.WithContext(ctx).AckRelayDecision(r.ID, "absent", e.clock.Now(), store.DefaultLockWait, func() error {
								close(held)
								<-free
								return nil
							})
							done <- err
						}()
						select {
						case <-held:
						case err := <-done:
							t.Fatalf("hold SQLite admission: %v", err)
						}
						admission.armed.Store(true)
					}
					admission.onWait = func() {
						waited = true
						e.clock.Advance(100 * time.Millisecond)
						if mode == "changed during admission" {
							e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "a new dialog appeared\n")
						}
						release()
					}
					var atKey store.PermissionRow
					if permission {
						e.rec.AfterCall(paKeyCall(key), func(tmuxfix.SocketCall, error) { atKey = p.request(t, params.RequestToken) })
					}
					// Use the real store directly, including its optional CheckPaneIntent.
					result, err := api.SendKeys(e.st, e.rec, e.env, params)
					release()
					if holderStarted {
						if holdErr := <-done; holdErr != nil {
							t.Fatalf("release SQLite admission: %v", holdErr)
						}
					}
					if !waited {
						t.Fatal("did not reach the occupied SQLite admission lane")
					}
					switch mode {
					case "changed during admission":
						assertOneName(t, err, "ErrPaneChanged")
					case "cancelled during capture":
						assertOneName(t, err, "ErrSubmissionUnconfirmed")
						want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: row.State, Reason: "input admission expired during pane capture"}
						if ctx.Err() == nil || api.ErrDetails(err) != want {
							t.Fatalf("capture cancellation details=%+v, context=%v; want %+v", api.ErrDetails(err), ctx.Err(), want)
						}
					default:
						if err != nil || result != (api.SendKeysResult{}) {
							t.Fatalf("unchanged pane key=%+v %v", result, err)
						}
					}
					if mode != "unchanged" {
						e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendKey, tmux.CallSendEnter)
					} else {
						var writes []tmuxfix.SocketCall
						for _, call := range []tmux.Call{tmux.CallSendText, tmux.CallSendKey, tmux.CallSendEnter} {
							writes = append(writes, e.rec.SocketCallsOf(call)...)
						}
						assertOneKey(t, writes, r.Spawn.Identity.PaneID, key)
						for _, call := range e.rec.SocketCallsOf(paKeyCall(key)) {
							if call.Socket != r.Socket {
								t.Fatalf("key used socket %q; want %q", call.Socket, r.Socket)
							}
						}
					}
					_, after := sqliteAttempt(t, e, r)
					if after == nil || !after.ManualInterference || after.RequestID != owner.RequestID || after.Identity != owner.Identity || after.Payload != owner.Payload || after.Phase != owner.Phase || after.BytesAttempted != owner.BytesAttempted || after.EnterAttempted != owner.EnterAttempted || !reflect.DeepEqual(after.Proof, owner.Proof) {
						t.Fatalf("manual admission erased unresolved ordinary owner: before=%+v after=%+v", owner, after)
					}
					if permission {
						pr := p.request(t, params.RequestToken)
						if mode == "unchanged" {
							if atKey.PaneAnswer != store.PaneAnswerIntent || atKey.PaneAs != "deny" || atKey.PaneSender != paSender || atKey.Decision != "" || pr.PaneAnswer != store.PaneAnswerSent || pr.Decision != "deny" || pr.DecisionReason != store.DecisionReasonPane || pr.AwaitsAnswer() {
								t.Fatalf("permission intent/sent contract: at key=%+v after=%+v", atKey, pr)
							}
						} else if pr.PaneAnswer != store.PaneAnswerIntent || pr.PaneAs != "deny" || pr.PaneSender != (api.ProcessIdentity{}) || !pr.PaneIntentAt.IsZero() || pr.Decision != "" || !pr.AwaitsAnswer() {
							t.Fatalf("refused key retained a claim or answered permission: %+v", pr)
						}
					}
				})
			}
		}
	}
}

// TestSendKeysSQLitePermissionPaneKeepsUnresolvedOwner: a pane answer, refused for a stale
// hash and then sent, records manual interference and keeps the unresolved ordinary owner.
func TestSendKeysSQLitePermissionPaneKeepsUnresolvedOwner(t *testing.T) {
	t.Parallel()
	e, r, launch := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, true))
	_, owner := seedSQLiteOwner(t, e, r, launch, "unconfirmed")
	p := &paEnv{killEnv: e.killEnv, r: r, now: e.clock.Now(), self: paSender, sks: e.st, agent: store.AgentCodex}
	p.pc.Set(paSender.PID, procfix.Alive(paSender.Starttime))
	p.seedRequest(t, storefix.TestRequestTokenA, paToolUseID, relayEnvHook)
	p.setPane("permission request\n")
	answer := p.answer(storefix.TestRequestTokenA, "deny", "Escape")
	answer.AllowPending = true
	wrong := answer
	wrong.ExpectPaneSHA256 = api.PaneSHA256("stale pane")
	before := len(e.rec.SocketCallsOf(tmux.CallSendKey))
	if err := p.sendKeys(wrong); !errors.Is(err, api.ErrPaneChanged) {
		t.Fatalf("stale hash=%v", err)
	}
	_, same := sqliteAttempt(t, e, r)
	if !reflect.DeepEqual(same, owner) || len(e.rec.SocketCallsOf(tmux.CallSendKey)) != before {
		t.Fatal("stale pane answer changed ownership")
	}
	if err := p.sendKeys(answer); err != nil {
		t.Fatalf("authorized pane answer=%v", err)
	}
	_, after := sqliteAttempt(t, e, r)
	if !after.ManualInterference || after.RequestID != owner.RequestID || after.BytesAttempted != owner.BytesAttempted || after.EnterAttempted != owner.EnterAttempted {
		t.Fatalf("pane answer erased ordinary owner: %+v", after)
	}
	if len(e.rec.SocketCallsOf(tmux.CallSendKey)) != before+1 {
		t.Fatal("pane answer did not issue exactly one authorized key")
	}
}

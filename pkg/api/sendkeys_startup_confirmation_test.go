package api_test

// b.3cf: Codex can accept the first prompt well before its startup hooks arrive.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestSendKeysPendingCodexLateHookActivity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		delay   time.Duration
		confirm bool
	}{
		{500 * time.Millisecond, true},
		{1900 * time.Millisecond, true},
		{2500 * time.Millisecond, true},
		{3 * time.Second, true},
		{4500 * time.Millisecond, true},
		{10700 * time.Millisecond, true},
		{16 * time.Second, true},
		{19900 * time.Millisecond, true},
		{20100 * time.Millisecond, false},
	}
	for _, tc := range cases {
		t.Run(tc.delay.String(), func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			// Keep the regression usable on the old twenty-wait fixture too.
			e.env.Sleep = e.clock.Advance
			var sentAt time.Time
			e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
				if sentAt.IsZero() {
					sentAt = e.clock.Now()
				}
				// No SessionStart has arrived yet, as in the reported sighting.
				if e.clock.Now().Sub(sentAt) >= tc.delay {
					row.State = store.StateWorking
				}
				return row, nil
			}
			result, err := api.SendKeys(e.s, e.rec, e.env, api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			if tc.confirm {
				if err != nil {
					t.Errorf("hook activity %v after Enter: %v; want success", tc.delay, err)
				}
				body, marshalErr := json.Marshal(result)
				if marshalErr != nil || string(body) != "{}" {
					t.Errorf("result = %s, %v; want {}", body, marshalErr)
				}
			} else {
				assertSubmissionUnconfirmed(t, err, store.StatePending)
			}
			assertPendingInput(t, e, r, skpText)
			writes := e.rec.SocketCallsOf(tmux.CallSendText)
			if len(writes) != 1 || !writes[0].Paste {
				t.Errorf("text writes = %+v; want exactly one paste", writes)
			}
		})
	}
}

func TestSendKeysPendingClaudeSubmissionWindow(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	// Exercise the retained fallback of a launch recorded as uncovered.
	r := e.seedRow(t, killRowSpec{State: store.StatePending, Opts: []apitest.SpawnOption{
		apitest.WithAgent(store.AgentClaude), apitest.WithBusySendCapabilities(nil),
	}})
	err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	assertSubmissionUnconfirmed(t, err, store.StateWaiting)
	want := fmt.Sprintf("ErrSubmissionUnconfirmed: send-keys: %s: tmux accepted the keys and Enter, but Claude Code submission could not be confirmed (no turn activity was observed within 2 seconds); do not repeat the text or send another Enter automatically; check hook-derived get or status; if submission remains unknown, escalate to the caller or a human", r.ID)
	if err.Error() != want {
		t.Errorf("message = %q; want %q", err, want)
	}
	if e.slept != 2*time.Second || e.waits != 20 || e.s.reads != 20 {
		t.Errorf("Claude observation sleep/waits/reads = %v/%d/%d; want 2s/20/20", e.slept, e.waits, e.s.reads)
	}
	assertInputWrites(t, e.killEnv, r, skpText)
}

func TestSendKeysPendingSubmissionCallerContext(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"shorter deadline", "deadline reached during read", "cancelled during read", "cancelled during wait"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			e.env.Context = ctx
			// Leave 250ms at observation's start, after the Recorder charges
			// preparation and Enter to the virtual clock. No real timer wait.
			deadline, _ := ctx.Deadline()
			e.rec.AfterCall(tmux.CallSendEnter, func(_ tmuxfix.SocketCall, err error) {
				if err == nil {
					e.clock.Advance(deadline.Add(-250 * time.Millisecond).Sub(e.clock.Now()))
				}
			})
			wantState := store.StatePending
			switch mode {
			case "deadline reached during read", "cancelled during read":
				wantState = store.StateWorking
				e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
					if mode == "cancelled during read" {
						cancel()
					} else {
						e.clock.Advance(250 * time.Millisecond)
					}
					row.State = store.StateWorking
					return row, nil
				}
			case "cancelled during wait":
				sleep := e.env.Sleep
				e.env.Sleep = func(d time.Duration) { sleep(d); cancel() }
			}
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertSubmissionUnconfirmed(t, err, wantState)
			if !strings.Contains(err.Error(), "the call was cancelled or reached its deadline before submission was observed") {
				t.Errorf("error = %v; want caller-context uncertainty", err)
			}
			wantBudgets := []time.Duration{250 * time.Millisecond}
			wantSleep := time.Duration(0)
			if mode == "shorter deadline" {
				wantBudgets = append(wantBudgets, 150*time.Millisecond, 50*time.Millisecond)
				wantSleep = 250 * time.Millisecond
			} else if mode == "cancelled during wait" {
				wantSleep = 100 * time.Millisecond
			}
			if !reflect.DeepEqual(e.s.waits, wantBudgets) || e.slept != wantSleep {
				t.Errorf("read budgets/sleep = %v/%v; want %v/%v", e.s.waits, e.slept, wantBudgets, wantSleep)
			}
			assertPendingInput(t, e, r, skpText)
		})
	}
}

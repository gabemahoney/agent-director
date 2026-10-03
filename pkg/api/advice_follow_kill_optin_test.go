package api_test

// advice_follow_kill_optin_test.go: b.fji literal-follow tests for kill's
// operator-only finished-row opt-in (advice inventory C9, C10) on the kill
// fixture. The CLI forms are cmd/agent-director/advice_follow_kill_cli_test.go's.

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestAdviceFollow_C9_OptInStillStoppingOrStartingRetryLater: the same
// refusal at once; past the window or bound the description states, the
// retry proceeds, or gets the documented "never reported in" conflict.
func TestAdviceFollow_C9_OptInStillStoppingOrStartingRetryLater(t *testing.T) {
	// C9 ErrTmuxUnresponsive with the finished-row opt-in, own session still stopping or still starting: "...nothing was done; retry later".
	const advice = "nothing was done; retry later"
	cases := []struct {
		name   string
		row    startingRow
		phrase string        // what the refusal says the session appears to be doing
		wait   time.Duration // the limit the refusal states
		want   error         // the retry's error; nil is the kill sequence's success
	}{
		{"still stopping, reported in", startingRow{endedAgo: defWindow - 10*time.Second, age: defWindow + defBound},
			"appears to still be stopping", defWindow, nil},
		{"still starting, reported in", startingRow{endedAgo: defWindow, age: defBound - 10*time.Second},
			"appears to still be starting", defBound, nil},
		{"still starting, never reported in", startingRow{endedAgo: defBound, age: defBound - 10*time.Second},
			"appears to still be starting", defBound, api.ErrTmuxSessionConflict},
	}
	for _, state := range kosFinished {
		for _, tc := range cases {
			t.Run(state+", "+tc.name, func(t *testing.T) {
				e := newKillEnv(t)
				row := tc.row
				row.state = state
				r := e.seedStarting(t, row)
				e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
				_, first := e.killOptIn(r.ID)
				adviceAssertAdvice(t, first, api.ErrTmuxUnresponsive, advice)
				if !strings.Contains(first.Error(), tc.phrase) {
					t.Fatalf("refusal %q; want %q", first, tc.phrase)
				}
				if _, again := e.killOptIn(r.ID); again == nil || again.Error() != first.Error() {
					t.Errorf("immediate retry = %v; want the refusal unchanged: %q", again, first)
				}

				e.clock.Advance(tc.wait)
				res, err := e.killOptIn(r.ID)
				if tc.want != nil {
					adviceAssertAdvice(t, err, tc.want, "never reported in")
					if n := len(e.rec.SocketCallsOf(tmux.CallKillPane)); n != 0 {
						t.Errorf("%d pane kills; want none", n)
					}
				} else if err != nil || !res.KillSent {
					t.Fatalf("retry past the limit = %+v, %v; want the kill sequence with kill_sent true", res, err)
				} else if seqHas(e, r.Socket, r.Session.ID) {
					t.Errorf("session %s still runs after the kill", r.Session.ID)
				}
				if got := e.columns(t, r.ID).State; got != state {
					t.Errorf("state = %v; want %v kept", got, state)
				}
			})
		}
	}
}

// TestAdviceFollow_C10_OptInOnLiveRowDropTheOption: the refused call sends
// nothing; the same kill without the option ends the live row's agent.
func TestAdviceFollow_C10_OptInOnLiveRowDropTheOption(t *testing.T) {
	// C10 ErrSpawnNotResumable, the opt-in on a live row: "the finished-row option applies only to an ended or missing row; no lookup was made and nothing was sent" (follow: kill again without the option).
	for _, state := range []string{store.StateWaiting, store.StatePending} {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: state})
			e.seedBystander(t, r.Socket)
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			calls := len(e.rec.SocketCalls())
			_, err := e.killOptIn(r.ID)
			adviceAssertAdvice(t, err, api.ErrSpawnNotResumable, "the finished-row option applies only to an ended or "+
				"missing row; no lookup was made and nothing was sent")
			if got := e.rec.SocketCalls()[calls:]; len(got) != 0 {
				t.Errorf("tmux calls by the refusal = %+v; want none", got)
			}

			res, err := e.kill(r.ID)
			if err != nil || !res.KillSent {
				t.Fatalf("kill without the option = %+v, %v; want success with kill_sent true", res, err)
			}
			if seqHas(e, r.Socket, r.Session.ID) {
				t.Errorf("session %s still runs after the kill", r.Session.ID)
			}
			if got := e.columns(t, r.ID).State; got != state {
				t.Errorf("state = %v; want %v kept", got, state)
			}
		})
	}
}

package api_test

// advice_follow_kill_optin_test.go: b.fji literal-follow tests for kill's
// operator-only finished-row opt-in (advice inventory C9, C10) on the kill
// fixture. The CLI form is cmd/agent-director-admin/kill_finished_test.go's
// (TestAdviceFollow_C9_AdminStillStoppingRetryLater).

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestAdviceFollow_C9_OptInStillStoppingOrStartingRetryLater: the same
// refusal at once, nothing sent; past the window or bound the description
// states, the retry proceeds, or gets the documented "never reported in" conflict.
func TestAdviceFollow_C9_OptInStillStoppingOrStartingRetryLater(t *testing.T) {
	t.Parallel()
	// C9 ErrTmuxUnresponsive with the finished-row opt-in, own session (or this id's own abandoned launch, b.6sa) still stopping or still starting: "...nothing was done; retry later".
	const advice = "nothing was done; retry later"
	// c9Seed seeds a finished row in state and returns its id, its socket, the session the retry ends and that session's agent pid.
	type c9Seed func(t *testing.T, e *killEnv, state string) (id, socket, session string, agentPID int)
	own := func(row startingRow) c9Seed {
		return func(t *testing.T, e *killEnv, state string) (string, string, string, int) {
			s := row
			s.state = state
			r := e.seedStarting(t, s)
			return r.ID, r.Socket, r.Session.ID, r.AgentPID
		}
	}
	abandoned := func(t *testing.T, e *killEnv, state string) (string, string, string, int) {
		r, seeded := e.seedAbandoned(t, state, kabSession{age: defBound - 10*time.Second})
		return r.ID, r.Socket, seeded[0].ID, seeded[0].pid
	}
	cases := []struct {
		name   string
		seed   c9Seed
		phrase string        // what the refusal says the session appears to be doing
		extra  string        // a further phrase the refusal carries ("": none)
		wait   time.Duration // the limit the refusal states
		want   error         // the retry's error; nil is the kill sequence's success
	}{
		{"still stopping, reported in", own(startingRow{endedAgo: defWindow - 10*time.Second, age: defWindow + defBound}),
			"appears to still be stopping", "", defWindow, nil},
		{"still starting, reported in", own(startingRow{endedAgo: defWindow, age: defBound - 10*time.Second}),
			"appears to still be starting", "", defBound, nil},
		{"still starting, never reported in", own(startingRow{endedAgo: defBound, age: defBound - 10*time.Second}),
			"appears to still be starting", "", defBound, api.ErrTmuxSessionConflict},
		{"this id's own abandoned launch still starting", abandoned,
			"appears to still be starting", "this id's own abandoned launch", defBound, nil},
	}
	for _, state := range kosFinished {
		for _, tc := range cases {
			t.Run(state+", "+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				id, socket, session, agentPID := tc.seed(t, e, state)
				e.setAfterCall(tmux.CallKillPane, procfix.Gone(), agentPID)
				_, first := e.killOptIn(id)
				adviceAssertAdvice(t, first, api.ErrTmuxUnresponsive, advice)
				adviceAssertPhrase(t, first, tc.phrase)
				if tc.extra != "" {
					adviceAssertPhrase(t, first, tc.extra)
				}
				if _, again := e.killOptIn(id); again == nil || again.Error() != first.Error() {
					t.Errorf("immediate retry = %v; want the refusal unchanged: %q", again, first)
				}
				if n := len(e.rec.SocketCallsOf(tmux.CallKillPane)) + len(e.rec.SocketCallsOf(tmux.CallKillSession)); n != 0 {
					t.Errorf("%d kills by the refusals; want none", n)
				}

				e.clock.Advance(tc.wait)
				res, err := e.killOptIn(id)
				if tc.want != nil {
					adviceAssertAdvice(t, err, tc.want, "never reported in")
					if n := len(e.rec.SocketCallsOf(tmux.CallKillPane)); n != 0 {
						t.Errorf("%d pane kills; want none", n)
					}
				} else if err != nil || !res.KillSent {
					t.Fatalf("retry past the limit = %+v, %v; want the kill sequence with kill_sent true", res, err)
				} else if seqHas(e, socket, session) {
					t.Errorf("session %s still runs after the kill", session)
				}
				if got := e.columns(t, id).State; got != state {
					t.Errorf("state = %v; want %v kept", got, state)
				}
			})
		}
	}
}

// TestAdviceFollow_C10_OptInOnLiveRowDropTheOption: the refused call sends
// nothing; the same kill without the option ends the live row's agent.
func TestAdviceFollow_C10_OptInOnLiveRowDropTheOption(t *testing.T) {
	t.Parallel()
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

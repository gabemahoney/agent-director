package api_test

// resume_pending_race_test.go covers two resumes of one id (SR-8.1 step 3,
// SR-8.3, SR-8.4, SR-20.6; AC-RES-11): a loser that examined the row before
// the winner's move, refused at its lookup or at its conditional move, and a
// loser that examined it after, refused by its state guard; each writes
// nothing. On the shared fixture in resume_fixture_test.go against a real
// store; pendRefuse and the call counters are in resume_pending_test.go.

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestResumeLoserExaminedBeforeWinnersMove: a resume whose examination preceded
// another's whole resume writes nothing. When the winner's session is up at the
// loser's lookup, it carries a token other than the one the loser examined, so
// the loser refuses there with the Leftover conflict (lead decision (a)) and
// makes no other call; when the winner moves and creates after the loser's
// lookup, the loser loses at its conditional move.
func TestResumeLoserExaminedBeforeWinnersMove(t *testing.T) {
	for _, atLookup := range []bool{true, false} {
		t.Run(map[bool]string{true: "winner's session up at the loser's lookup", false: "winner moves after the loser's lookup"}[atLookup], func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded)
			winnerParent, loserParent := pendParent(t, e), pendParent(t, e)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", loserParent)
			var winErr error
			var afterWinner apitest.SpawnColumns
			var callsAfterWinner int
			var kindsAfterWinner []tmux.Call
			winner := func() {
				os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", winnerParent) //nolint:errcheck
				_, winErr = e.resume(r.ID)
				os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", loserParent) //nolint:errcheck
				afterWinner, callsAfterWinner, kindsAfterWinner = e.columns(t, r.ID), pendCalls(e.rec), pendCallKinds(e.rec)
			}
			if atLookup {
				e.store.afterGet(winner)
			} else {
				ran := false // the hook stays registered: the winner's own lookup must not rerun it
				e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
					if !ran {
						ran = true
						winner()
					}
				})
			}

			_, err := e.resume(r.ID)
			if winErr != nil {
				t.Fatalf("winner Resume: %v", winErr)
			}
			tok, _ := afterWinner.LaunchToken.(string)
			if atLookup {
				sess := e.rec.Sessions(e.socket)
				if !errors.Is(err, api.ErrTmuxSessionConflict) || errors.Is(err, api.ErrSpawnNotResumable) || len(sess) != 1 {
					t.Fatalf("loser Resume = %v, sessions %+v; want ErrTmuxSessionConflict over the winner's one session", err, sess)
				}
				apitest.AssertDescription(t, err.Error(), apitest.DescPreLaunchLeftover(r.ID,
					[]apitest.DescSession{{Name: sess[0].Name, ID: sess[0].ID}}), tok, r.Identity.Token, e.storeID)
				if got, want := pendCallKinds(e.rec), append(kindsAfterWinner, tmux.CallLookup); !slices.Equal(got, want) || pendCalls(e.rec) != callsAfterWinner+1 {
					t.Errorf("tmux calls = %v; want the winner's, then the loser's one lookup: %v", got, want)
				}
			} else {
				if !errors.Is(err, api.ErrSpawnNotResumable) {
					t.Fatalf("loser Resume = %v; want ErrSpawnNotResumable", err)
				}
				apitest.AssertDescription(t, err.Error(), apitest.DescResumeLostRace(), tok, e.storeID)
				if n := pendCalls(e.rec); n != callsAfterWinner {
					t.Errorf("loser made %d tmux calls after the winner; want none", n-callsAfterWinner)
				}
			}
			pendAssertLoserWroteNothing(t, e, r.ID, afterWinner, winnerParent)
		})
	}
}

// TestResumeLoserExaminedAfterWinnersMove: a resume that examines the row after
// another's move, while pending and once its agent reported in, is refused by
// its state guard and writes nothing.
func TestResumeLoserExaminedAfterWinnersMove(t *testing.T) {
	for _, reportedIn := range []bool{false, true} {
		name := map[bool]string{false: "winner pending", true: "winner reported in"}[reportedIn]
		t.Run(name, func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateMissing)
			winnerParent, loserParent := pendParent(t, e), pendParent(t, e)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", winnerParent)
			start := time.UnixMilli(e.moveStart().UnixMilli()).UTC()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("winner Resume: %v", err)
			}
			if reportedIn {
				pendSessionStart(t, e, r.ID, r.JSONLPath)
			}
			got := pendRefuse(t, e, r.ID, loserParent)
			if !errors.Is(got.err, api.ErrSpawnNotResumable) {
				t.Fatalf("loser Resume = %v; want ErrSpawnNotResumable", got.err)
			}
			if !reportedIn {
				tok, _ := got.before.LaunchToken.(string)
				apitest.AssertDescription(t, got.err.Error(), apitest.DescResumeLaunchInProgress(apitest.LaunchInProgress{
					InstanceID: r.ID, LaunchStart: start}), tok, e.storeID)
			} else if got.before.State != store.StateWaiting {
				t.Fatalf("row after SessionStart is %v; want waiting", got.before.State)
			}
			if got.calls != 0 {
				t.Errorf("loser made %d tmux calls; want none", got.calls)
			}
			pendAssertLoserWroteNothing(t, e, r.ID, got.before, winnerParent)
		})
	}
}

// pendAssertLoserWroteNothing checks that the row is want with the winner's
// parent, one create call was made and one move event emitted.
func pendAssertLoserWroteNothing(t *testing.T, e *resumeEnv, id string, want apitest.SpawnColumns, winnerParent string) {
	t.Helper()
	if got := e.columns(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("row after the loser = %+v; want the winner's %+v", got, want)
	}
	if want.ParentID != winnerParent {
		t.Errorf("parent_id = %#v; want the winner's %q", want.ParentID, winnerParent)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
		t.Errorf("creates = %d; want the winner's one", n)
	}
	if n := pendMoved(t, id); n != 1 {
		t.Errorf("ad.resume.moved_to_pending lines = %d; want the winner's one", n)
	}
}

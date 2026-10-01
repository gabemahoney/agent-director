package api_test

// resume_pending_race_test.go covers two resumes of one id (SR-8.1 step 3,
// SR-8.3, SR-8.4, SR-20.6; AC-RES-11): a loser that examined the row before
// the winner's move, refused by its re-read after its lookup or at its
// conditional move, a loser whose re-read finds the row deleted, and a loser
// that examined it after, refused by its state guard; each writes nothing. On
// the shared fixture in resume_fixture_test.go against a real store (the
// deleted row on the kill fixture, for its trust file); pendRefuse and the
// call counters are in resume_pending_test.go.

import (
	"errors"
	"os"
	"reflect"
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

// TestResumeLoserExaminedBeforeWinnersMove: a resume whose examination preceded
// another's whole resume writes nothing and gets the lost-race refusal. When the
// winner's session is up at the loser's lookup, it reads as left over, and the
// loser's one re-read finds the row changed (lead decision (a), revised), with no
// call after its lookup; when the winner moves and creates after the loser's
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
			if !errors.Is(err, api.ErrSpawnNotResumable) || errors.Is(err, api.ErrTmuxSessionConflict) {
				t.Fatalf("loser Resume = %v; want ErrSpawnNotResumable", err)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescResumeLostRace(), tok, r.Identity.Token, e.storeID)
			if atLookup {
				if sess := e.rec.Sessions(e.socket); len(sess) != 1 {
					t.Errorf("sessions = %+v; want the winner's one", sess)
				}
				if got, want := pendCallKinds(e.rec), append(kindsAfterWinner, tmux.CallLookup); !slices.Equal(got, want) || pendCalls(e.rec) != callsAfterWinner+1 {
					t.Errorf("tmux calls = %v; want the winner's, then the loser's one lookup: %v", got, want)
				}
			} else if n := pendCalls(e.rec); n != callsAfterWinner {
				t.Errorf("loser made %d tmux calls after the winner; want none", n-callsAfterWinner)
			}
			pendAssertLoserWroteNothing(t, e, r.ID, afterWinner, winnerParent)
		})
	}
}

// TestResumeLoserRowDeletedBeforeReRead: a left-over session (the winner's) at
// the lookup, with the row deleted after resume's read, makes the one re-read
// return the move's ErrSpawnNotFound, not the Leftover conflict; resume makes
// only that lookup and writes nothing (no move, no trust entry, no ad.resume.*).
func TestResumeLoserRowDeletedBeforeReRead(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedResumable(t, time.Hour, agentGone)
	e.seedHolder(t, r.killRow, holderOld)
	w := &hookedResumeStore{st: e.st}
	w.failMove(nil) // a move would return the injected store error instead
	w.afterGet(func() {
		if err := e.st.DeleteSpawn(r.ID); err != nil {
			t.Fatalf("DeleteSpawn: %v", err)
		}
	})
	before := e.snapshotResume(t, r)

	_, err := e.resumeWith(w, r.ID)

	if !errors.Is(err, api.ErrSpawnNotFound) || errors.Is(err, api.ErrTmuxSessionConflict) {
		t.Fatalf("Resume = %v; want ErrSpawnNotFound, not the Leftover conflict", err)
	}
	if got := e.rec.SocketCalls()[before.calls:]; len(got) != 1 || got[0].Call != tmux.CallLookup {
		t.Errorf("tmux calls = %+v; want the one lookup", got)
	}
	if n := len(e.rec.Calls()) - before.nameCalls; n != 0 {
		t.Errorf("%d name-based tmux calls; want none", n)
	}
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, before.sessions[r.Socket]) {
		t.Errorf("sessions on %s = %+v; want unchanged %+v", r.Socket, got, before.sessions[r.Socket])
	}
	r.Trust.check(t, r.CWD, false, "after the refused resume")
	for _, l := range readAPITrailLines(t)[before.mark:] {
		if ev, _ := l["event"].(string); strings.HasPrefix(ev, "ad.resume.") {
			t.Errorf("trail record %s for %v; want no ad.resume.* line", ev, l["claude_instance_id"])
		}
	}
	if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns = %v; want the row still absent", err)
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

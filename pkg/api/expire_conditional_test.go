package api_test

// expire_conditional_test.go covers expire's conditional delete (SR-12.3,
// SR-5.3, SR-10.4, AC-EXP-03, AC-EXP-04): another caller's write between the
// row's examination and its delete, injected with the fixture's beforeDelete,
// or a real reuse's reset run when expire's lookup returns.

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// expireMove is resume's move of r's row to pending; it returns the row as
// examined before the move and the version the move produced.
func expireMove(t *testing.T, e *killEnv, r killRow) (store.Spawn, int64) {
	t.Helper()
	row, err := e.st.GetSpawn(r.ID)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", r.ID, err)
	}
	res, moved, err := e.st.MoveToPending(r.ID, row.Snapshot, e.clock.Now().UnixMilli(), newToken(), r.Socket, "", store.LaunchOwner{})
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending(%s) = %v, %v; want applied", r.ID, res, err)
	}
	return row, moved
}

// expireRestore is resume's move of r's row, then its restore after a failed launch.
func expireRestore(t *testing.T, e *killEnv, r killRow) {
	t.Helper()
	row, moved := expireMove(t, e, r)
	prior := store.ResumePrior{State: row.State, EndedAtText: row.EndedAtText, PID: row.PID,
		ProcStarttime: row.ProcStarttime, LivenessUnverifiedSince: row.LivenessUnverifiedSince,
		LivenessNote: row.LivenessNote, Identity: row.Identity}
	if res, err := e.st.RestoreAfterFailedResume(r.ID, moved, prior); err != nil || res != store.CondApplied {
		t.Fatalf("RestoreAfterFailedResume(%s) = %v, %v; want applied", r.ID, res, err)
	}
}

// expireAgentHook is event from r's own agent (its recorded pane process), applied.
func expireAgentHook(event string) func(*testing.T, *killEnv, killRow) {
	return func(t *testing.T, e *killEnv, r killRow) {
		t.Helper()
		if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, event, "sess-"+uuid.NewString()[:8]); !a.Applied {
			t.Fatalf("%s on %s not applied: %s", event, r.ID, a.Reason)
		}
	}
}

// assertRowGone fails unless id has no row.
func assertRowGone(t *testing.T, e *killEnv, id string) {
	t.Helper()
	if _, err := apitest.ReadSpawnColumns(e.dbPath, id); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("row %s read: %v; want ErrSpawnNotFound", id, err)
	}
}

// TestExpireConditionalDelete: a write after examination keeps the row
// changed_since_examined (or, if it removed it, puts it in neither list); an unchanged row still goes.
func TestExpireConditionalDelete(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name         string
		state        string // the row's finished state when examined
		write        func(*testing.T, *killEnv, killRow)
		after        string // the state the write leaves; "" = removed
		rerunDeletes bool   // a later run selects the row and deletes it
	}{
		{name: "resume's move to pending", state: store.StateEnded,
			write: func(t *testing.T, e *killEnv, r killRow) { expireMove(t, e, r) }, after: store.StatePending},
		{name: "resume's restore after a failed launch", state: store.StateEnded, write: expireRestore,
			after: store.StateEnded, rerunDeletes: true},
		{name: "relaunch: its own agent's SessionStart on a missing row", state: store.StateMissing,
			write: expireAgentHook("SessionStart"), after: store.StateWaiting},
		{name: "its own agent's hook, row still ended", state: store.StateEnded,
			write: expireAgentHook("Notification"), after: store.StateEnded, rerunDeletes: true},
		{name: "deleted by another caller", state: store.StateEnded,
			write: func(t *testing.T, e *killEnv, r killRow) {
				if err := e.st.DeleteSpawn(r.ID); err != nil {
					t.Fatalf("DeleteSpawn(%s): %v", r.ID, err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := e.finishedSpec(time.Hour, agentGone)
			spec.State = tc.state
			r := e.seedRow(t, spec)
			unchanged := e.seedFinished(t, time.Hour, agentGone)
			w := e.expireStore()
			var written apitest.SpawnColumns
			ran := false
			w.beforeDelete(r.ID, func() {
				ran = true
				tc.write(t, e, r)
				if tc.after != "" {
					written = e.columns(t, r.ID)
				}
			})
			mark := trailMark(t)
			res, lg, err := e.expireWith(w, olderThan(0))
			if err != nil {
				t.Fatalf("Expire: %v", err)
			}
			if !ran {
				t.Fatalf("row %s: no delete was attempted, so the write never ran", r.ID)
			}
			if len(lg.lines) != 0 {
				t.Errorf("logged %q; want nothing", lg.lines)
			}
			want := map[string]string{unchanged.ID: ""}
			if tc.after != "" {
				want[r.ID] = "changed_since_examined"
			}
			assertExpired(t, res, mark, want)
			assertRowGone(t, e, unchanged.ID)
			if tc.after == "" {
				assertRowGone(t, e, r.ID)
				assertNoTrailSince(t, mark, r.ID)
				return
			}
			e.assertRowUnchanged(t, r.ID, written)
			if written.State != tc.after {
				t.Errorf("row %s state after the write = %v; want %s", r.ID, written.State, tc.after)
			}
			if tc.after == store.StatePending && written.LaunchStartedAt == nil {
				t.Errorf("row %s: pending with no launch start", r.ID)
			}

			mark = trailMark(t)
			again, _, err := e.expire(olderThan(0))
			if err != nil {
				t.Fatalf("second Expire: %v", err)
			}
			want = map[string]string{}
			if tc.rerunDeletes {
				want[r.ID] = ""
			}
			assertExpired(t, again, mark, want)
			if !tc.rerunDeletes {
				e.assertRowUnchanged(t, r.ID, written)
			}
		})
	}
}

// TestExpireConditionalDeleteReuseReset (AC-EXP-04): a finished row that a real reuse resets as expire's lookup of it
// returns is kept changed_since_examined, not deleted; it stays pending in its new life, and a later run skips it.
func TestExpireConditionalDeleteReuseReset(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, prior := range []string{store.StateEnded, store.StateMissing} {
		t.Run("Reuse/"+prior, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: prior, Age: time.Hour, Bare: true})
			// On r's socket: the one lookup there decides both rows, and the fixture server runs there.
			unchanged := e.seedFinished(t, time.Hour, agentGone, apitest.WithTmuxSocket(r.Socket))
			p := reuseParams(t, r, reuseRequest{})
			reused := false
			e.rec.AfterCall(tmux.CallLookup, func(c tmuxfix.SocketCall, _ error) {
				if reused || c.Socket != r.Socket {
					return
				}
				reused = true
				if _, logs, err := e.reuse(t, p); err != nil {
					t.Errorf("reuse of %s after expire's lookup: %v (log %q)", r.ID, err, logs)
				}
			})
			mark := trailMark(t)

			res, lg, err := e.expire(olderThan(0))

			if err != nil || !reused {
				t.Fatalf("Expire = %v (reuse ran: %v); want success with the reuse after its lookup", err, reused)
			}
			if len(lg.lines) != 0 {
				t.Errorf("logged %q; want nothing", lg.lines)
			}
			assertExpired(t, res, mark, map[string]string{unchanged.ID: "", r.ID: "changed_since_examined"})
			assertRowGone(t, e, unchanged.ID)
			written := e.columns(t, r.ID)
			if written.State != store.StatePending || written.LifeNumber != reuseLife+1 {
				t.Errorf("row %s {state %v, life %#v}; want pending in life %d", r.ID, written.State, written.LifeNumber, reuseLife+1)
			}

			mark = trailMark(t)
			again, _, err := e.expire(olderThan(0))
			if err != nil {
				t.Fatalf("second Expire: %v", err)
			}
			assertExpired(t, again, mark, map[string]string{})
			e.assertRowUnchanged(t, r.ID, written)
		})
	}
}

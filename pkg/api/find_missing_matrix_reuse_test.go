package api_test

// find_missing_matrix_reuse_test.go: the pending matrix (find_missing_matrix_test.go) for a reuse's pending row
// made by a real reuse, and AC-FM-18's reuse cells on a real store (SR-10.4, SR-11.3, SR-14, SR-20.6; AC-FM-18).

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// mxReuse is the matrix's reuse kind: each cell's row comes from a new real reuse (mxReusedRow).
var mxReuse = mxKind{name: "Reuse", reused: true}

// mxReusedRow is the row a real reuse left (reusePending), its launch past the default grace period at fmNow, as
// find-missing lists it, moved into the matrix's tmux world: apitest.TestSocket and tmuxfix.Token.
func mxReusedRow(t *testing.T) store.LiveSpawnIdentity {
	t.Helper()
	e := newKillEnv(t)
	fgcSetClock(e.clock, fmNow.Add(-2*fmGrace))
	r := e.reusePending(t, agentAlive, reuseRowSpec{Bare: true}, reuseRequest{})
	rows, err := e.st.ListLiveSpawnIdentities()
	if err != nil {
		t.Fatalf("ListLiveSpawnIdentities: %v", err)
	}
	for _, row := range rows {
		if row.ClaudeInstanceID == r.ID {
			if row.State != store.StatePending || row.LaunchStartedAtMillis == 0 ||
				!fmNow.After(time.UnixMilli(row.LaunchStartedAtMillis).Add(fmGrace)) {
				t.Fatalf("reused row %+v; want pending, launched before fmNow minus the grace period", row)
			}
			row.Identity.Socket, row.Identity.Token = apitest.TestSocket, tmuxfix.Token
			return row
		}
	}
	t.Fatalf("reused row %s not listed among %+v", r.ID, rows)
	return store.LiveSpawnIdentity{}
}

// TestFindMissingPendingMatrixReuse: mxMatrix for a reuse's pending row, made by a real reuse.
func TestFindMissingPendingMatrixReuse(t *testing.T) {
	t.Parallel()
	mxMatrix(t, []mxKind{mxReuse})
}

// TestFindMissingPendingMatrixReuseInsideGrace: mxInsideGrace for a reuse's pending row.
func TestFindMissingPendingMatrixReuseInsideGrace(t *testing.T) {
	t.Parallel()
	mxInsideGrace(t, []mxKind{mxReuse})
}

// TestFindMissingPendingMatrixReuseEarlierLaunch (AC-FM-18): a reuse whose create timed out making nothing, past
// grace: an earlier launch's session holding the name, whenever its clock says it was made, marks the row
// tmux_name_held with one record; a process of an earlier launch carrying the id alone marks it tmux_absent.
func TestFindMissingPendingMatrixReuseEarlierLaunch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		created time.Duration // the holder's creation time from the launch start's second
		held    bool          // an earlier launch's session holds the recorded name; else only its process runs
	}{
		{"name held, session made in the launch start's own second", 0, true},
		{"name held, clock stepped back before the session was made", -time.Hour, true},
		{"name held, clock stepped forward before the session was made", time.Hour, true},
		{"name free, only an earlier launch's process carries the id", 0, false},
	}
	for _, tc := range cases {
		t.Run("Reuse/"+tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Bare: true})
			earlier := r.Token
			r = e.reuseTimesOut(t, r, reuseRequest{}, false)
			launch := e.columns(t, r.ID).LaunchStartedAt.(int64)
			var holder tmuxfix.SeedSession
			leak := e.newPID()
			if tc.held {
				holder = e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: storedFormOf(r.Name),
					Created: time.UnixMilli(launch).Add(tc.created).Unix(), Label: tmuxfix.Valid(earlier, r.ID, r.StoreID),
					Panes: []tmuxfix.SeedPane{{AdPane: earlier}}})
			} else {
				e.pc.Set(leak, procfix.Alive(fmStart).WithEnv(map[string]string{probe.EnvKey: r.ID}))
			}
			sessions, calls, envReads := e.rec.Sessions(r.Socket), len(e.rec.SocketCalls()), e.pc.EnvReads()
			fgcSetClock(e.clock, time.UnixMilli(launch).Add(fmGrace+time.Second))
			c, _ := e.client(t)
			mark := trailLen(t)

			res, err := c.FindMissing(context.Background())

			if err != nil {
				t.Fatalf("FindMissing: %v", err)
			}
			assertLists(t, res, []string{r.ID}, nil)
			if st := e.columns(t, r.ID).State; st != store.StateMissing {
				t.Errorf("state = %v; want missing", st)
			}
			hr := hnRow{id: r.ID, name: r.Name, token: r.Token}
			recs := ptRecords(t, mark, "ad.launch.name_held", r.ID)
			if tc.held {
				assertMarkTick(t, mark, hr, "tmux_name_held", "leftover")
				assertSweepRecord(t, recs, hr, r.Socket, e.storeID, hnWant{holder: &holder, carries: true, current: false,
					lookup: "leftover", rowResult: "marked_missing"})
			} else {
				assertMarkTick(t, mark, hr, "tmux_absent", "gone")
				if len(recs) != 0 {
					t.Errorf("ad.launch.name_held records = %v; want none", recs)
				}
			}
			if got := callKinds(e.rec)[calls:]; !slices.Equal(got, []tmux.Call{tmux.CallLookup}) || len(e.rec.Calls()) != 0 {
				t.Errorf("sweep tmux calls = %v (name-based %d); want one lookup", got, len(e.rec.Calls()))
			}
			if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
				t.Errorf("sessions changed:\nbefore %+v\nafter  %+v", sessions, got)
			}
			if n := e.pc.EnvReads() - envReads; slices.Contains(e.pc.StartTimeCalls(), leak) || n != 0 {
				t.Errorf("sweep read the earlier launch's process %d or an environment (%d reads)", leak, n)
			}
		})
	}
}

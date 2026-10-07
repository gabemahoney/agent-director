package api_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's sweep tmux time budget and call bounds (SR-13.5, SR-13.3, SR-11.5, SR-3.15; AC-FM-04, AC-FM-11,
// AC-CFG-02), in virtual time on the shared tmuxfix.Clock; budgets come from internal/config. A hung socket's
// stop is find_missing_sockets_test.go's.

// fmbCfgBudgetSeconds is the sweep budget AC-CFG-02 configures.
const fmbCfgBudgetSeconds = 5

// fmbCallsToSpend is how many calls charged per each it takes to spend budget: the last one is the call in flight
// when the budget runs out.
func fmbCallsToSpend(budget, per time.Duration) int {
	return int((budget + per - 1) / per)
}

// fmbSocket is the i-th test socket; the Recorder answers a socket with no server bound as no-socket (Gone).
func fmbSocket(i int) string { return fmt.Sprintf("%s-%02d", apitest.TestSocket, i) }

// fmbToken records token as the row's launch token.
func fmbToken(token string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.Identity.Token = token }
}

// fmbCall is a socket-taking call's kind and socket.
type fmbCall struct {
	call   tmux.Call
	socket string
}

// fmbCalls returns the kind and socket of every call rec saw, in order.
func fmbCalls(rec *tmuxfix.Recorder) []fmbCall {
	var out []fmbCall
	for _, c := range rec.SocketCalls() {
		out = append(out, fmbCall{c.Call, c.Socket})
	}
	return out
}

// fmbAssertLookups fails unless rec saw exactly one lookup per socket, in sockets' order, and no other call.
func fmbAssertLookups(t *testing.T, rec *tmuxfix.Recorder, sockets []string) {
	t.Helper()
	var want []fmbCall
	for _, s := range sockets {
		want = append(want, fmbCall{tmux.CallLookup, s})
	}
	if got := fmbCalls(rec); !slices.Equal(got, want) {
		t.Errorf("tmux calls = %+v; want one lookup on each of %v", got, sockets)
	}
}

// fmbSeedRows seeds n working rows with no pane on e's store, row i alone on fmbSocket(i), in id order; odd rows
// record a SessionStart process whose start time is unreadable. It returns the ids and sockets in row order and
// each row's "not called" note.
func fmbSeedRows(t *testing.T, e *killEnv, n int) (ids, sockets []string, notCalled map[string]string) {
	t.Helper()
	prefix := "fmb-" + uuid.NewString()[:8]
	notCalled = map[string]string{}
	for i := range n {
		sock := fmbSocket(i)
		opts := []apitest.SpawnOption{apitest.WithNoPane(), apitest.WithTmuxSocket(sock)}
		note := "process_not_seen_tmux_unchecked"
		if i%2 == 1 {
			pid := e.newPID()
			e.pc.Set(pid, procfix.Unreadable())
			opts = append(opts, apitest.WithPID(pid), apitest.WithProcStarttime(apitest.LinuxProcStarttime))
			note = "probe_eacces"
		}
		id, err := apitest.SeedSpawn(e.dbPath, fmt.Sprintf("%s-%02d", prefix, i), store.StateWorking, "", "off", "",
			false, opts...)
		if err != nil {
			t.Fatalf("SeedSpawn: %v", err)
		}
		ids, sockets, notCalled[id] = append(ids, id), append(sockets, sock), note
	}
	return ids, sockets, notCalled
}

// TestFindMissingBudgetStopsCalls: Client.FindMissing at the default budget (no setting) and at a configured one,
// each lookup answering Gone after 1 s: calls stop once the budget is spent (the call that spends it is discarded),
// rows from there get the "not called" notes, and the run succeeds.
func TestFindMissingBudgetStopsCalls(t *testing.T) {
	t.Parallel()
	const perCall = time.Second
	for _, tc := range []struct {
		name     string
		settings []apitest.TmuxSetting
		budget   time.Duration
	}{
		{"default", nil, fmBudget},
		{"configured", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxSweepBudgetSeconds, fmbCfgBudgetSeconds)},
			fmbCfgBudgetSeconds * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: perCall})
			calls := fmbCallsToSpend(tc.budget, perCall)
			ids, sockets, notCalled := fmbSeedRows(t, e, calls+5)
			c, _ := e.client(t, tc.settings...)
			start := e.clock.Now()

			res, err := c.FindMissing(context.Background())
			if err != nil {
				t.Fatalf("FindMissing: %v", err)
			}
			fmbAssertLookups(t, e.rec, sockets[:calls])
			if got, want := e.clock.Now().Sub(start), time.Duration(calls)*perCall; got != want {
				t.Errorf("tmux time = %v; want %v", got, want)
			}
			assertLists(t, res, ids[:calls-1], ids[calls-1:])
			for _, id := range ids[calls-1:] {
				if got := e.columns(t, id); got.State != store.StateWorking || got.LivenessNote != notCalled[id] {
					t.Errorf("%s: state %v note %v; want %s, %s", id, got.State, got.LivenessNote, store.StateWorking, notCalled[id])
				}
			}
		})
	}
}

// TestFindMissingBudgetOneSocketThousandRows: SR-13.3's worst case, 1,000 rows on one socket each needing a lookup
// and an adoption, makes exactly one lookup and one pane listing and no other tmux call.
func TestFindMissingBudgetOneSocketThousandRows(t *testing.T) {
	t.Parallel()
	rows := make([]store.LiveSpawnIdentity, 1000)
	for i := range rows {
		rows[i] = liveRow(fmt.Sprintf("w%04d", i), fmbToken(fmt.Sprintf("%016x", i+1)))
	}
	rec := fmOurs(rows...)
	st := &fakeFindMissingStore{rows: rows}

	mustSweep(t, st, procfix.New(), fmSweep{tmux: rec})
	want := []fmbCall{{tmux.CallLookup, apitest.TestSocket}, {tmux.CallListPanes, apitest.TestSocket}}
	if got := fmbCalls(rec); !slices.Equal(got, want) {
		t.Errorf("tmux calls = %+v; want %+v", got, want)
	}
	if got := len(st.ids("adopt")); got != len(rows) {
		t.Errorf("adoptions = %d; want %d", got, len(rows))
	}
}

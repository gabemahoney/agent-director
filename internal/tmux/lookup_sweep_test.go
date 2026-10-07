package tmux_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Sweep lookups (SR-3.15, SR-13.3, SR-13.5; AC-FM-04, AC-FM-11, AC-EXP-05,
// AC-EXP-08): one lookup per socket, the per-socket stop rule and the run's
// tmux time budget on the virtual clock, over sweepRun.

// TestSweepNoCallBeforeNeed: building a sweep reads no clock and makes no call.
func TestSweepNoCallBeforeNeed(t *testing.T) {
	r := newSweepRun(t, sweepQuery, sockA)
	r.seed(sockA, lblCurrent, tmuxfix.SeedSession{})
	r.sweep(noBudget)
	if calls := r.Rec.SocketCalls(); len(calls) != 0 || r.Reads != 0 || len(r.PC.StartTimeCalls()) != 0 {
		t.Errorf("NewSweep made calls %v, %d clock reads, start-time reads %v", calls, r.Reads, r.PC.StartTimeCalls())
	}
}

// TestSweepOneLookupPerSocket: rows interleaved over three sockets take one
// lookup per socket, at its first row, and each row is Classify on that answer.
func TestSweepOneLookupPerSocket(t *testing.T) {
	name := tmuxfix.StoredNames()[0]
	r := newSweepRun(t, sweepQuery, sockA, sockB, sockC)
	r.seed(sockA, lblCurrent, tmuxfix.SeedSession{}).seed(sockA, lblOld, tmuxfix.SeedSession{Name: name.Stored})
	r.seed(sockB, lblOld, tmuxfix.SeedSession{})
	r.seed(sockC, lblCurrent, tmuxfix.SeedSession{})
	sw := r.sweep(noBudget)
	rows := []struct {
		row    tmux.Launch
		holder string
		token  string
		calls  int // socket calls so far
	}{
		{r.row(sockA), "", "ours", 1},
		{r.row(sockB, rowInstanceID("agent-w")), "", "gone", 2},
		{r.row(sockC, rowNoServer), "", "ours", 3},
		{r.row(sockA), name.Raw, "ours", 3},
		{r.row(sockB), "", "leftover", 3},
		{r.row(sockA, rowServer(lookupOther)), "", "ours", 3},
		{r.row(sockC), "", "ours", 3},
	}
	for i, tc := range rows {
		got := sw.Lookup(tc.row, tc.holder)
		r.check(got, tc.row, tc.holder)
		if got.Token() != tc.token || len(r.Rec.SocketCalls()) != tc.calls {
			t.Errorf("row %d: token %q after %d calls, want %q after %d", i, got.Token(), len(r.Rec.SocketCalls()), tc.token, tc.calls)
		}
		if tc.holder != "" && got.Holder == nil {
			t.Errorf("row %d: no holder of %q reported from the held answer", i, tc.holder)
		}
	}
	var order []string
	for _, c := range r.Rec.SocketCallsOf(tmux.CallLookup) {
		order = append(order, c.Socket)
	}
	if !slices.Equal(order, []string{sockA, sockB, sockC}) || len(r.Rec.SocketCalls()) != len(order) {
		t.Errorf("calls %v, want only one lookup on each of A, B, C in first-row order", r.Rec.SocketCalls())
	}
	if r.Reads != 2*len(order) || r.PC.EnvReads() != 0 {
		t.Errorf("%d clock reads for %d calls, %d environment reads; want two per call and none", r.Reads, len(order), r.PC.EnvReads())
	}
}

// TestSweepOutcomesAndStopRule: each lookup outcome maps as Lookup's does; an
// unreadable or unavailable one stops only its socket, the rest stop nothing.
func TestSweepOutcomesAndStopRule(t *testing.T) {
	failing := func(fl tmux.Failure) func(*sweepRun) {
		return func(r *sweepRun) { r.fail(sockA, fl, tmux.CallLookup) }
	}
	stopped := [3]string{"cant_tell", "not_run", "not_run"}
	unavailable := [3]string{"tmux_unavailable", "not_run", "not_run"}
	noServer := [3]string{"different_server", "gone", "different_server"}
	cases := []struct {
		name   string
		setup  func(r *sweepRun)
		tokens [3]string // socket A's three rows; not_run: Skipped
	}{
		{"timeout stops", failing(tmux.FailTimeout), stopped},
		{"binary unavailable stops", failing(tmux.FailUnavailable), unavailable},
		{"socket permission stops", failing(tmux.FailSocketDenied), unavailable},
		{"no server stops nothing", failing(tmux.FailNoServer), noServer},
		{"different server stops nothing", func(r *sweepRun) {
			r.Rec.RebindServer(sockA, lookupOther)
			r.seed(sockA, lblCurrent, tmuxfix.SeedSession{})
			r.PC.Set(lookupOther.PID, procfix.Alive(lookupOther.ProcStart))
		}, [3]string{"different_server", "ours", "different_server"}},
		{"scope value stops nothing", func(r *sweepRun) {
			r.Rec.SetScope(sockA, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}, [3]string{"provenance_conflict", "provenance_conflict", "provenance_conflict"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newSweepRun(t, sweepQuery, sockA, sockB)
			r.seed(sockA, lblCurrent, tmuxfix.SeedSession{}).seed(sockB, lblCurrent, tmuxfix.SeedSession{})
			tc.setup(r)
			sw := r.sweep(noBudget)
			a, other := r.row(sockA), r.row(sockA, rowServer(lookupOther))
			steps := []struct {
				row tmux.Launch
				k   int // index into tokens; -1 for socket B
			}{{a, 0}, {r.row(sockB), -1}, {other, 1}, {r.row(sockB, rowInstanceID("agent-w")), -1}, {a, 2}}
			for _, st := range steps {
				got := sw.Lookup(st.row, "")
				switch {
				case st.k < 0:
					r.check(got, st.row, "")
				case tc.tokens[st.k] == "not_run":
					checkSkipped(t, got)
				default:
					r.check(got, st.row, "")
					if got.Token() != tc.tokens[st.k] {
						t.Errorf("socket A row %d: token %q, want %q", st.k, got.Token(), tc.tokens[st.k])
					}
				}
			}
			r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1})
		})
	}
}

// TestSweepBudget: the call that brings the time in calls to the budget has
// its row Skipped; nothing is called or handed out afterwards, on any socket.
func TestSweepBudget(t *testing.T) {
	const budget = 2 * time.Second
	shapes := []struct {
		name    string
		over    time.Duration // added to the budget
		between bool          // advance the clock by ten budgets between rows
		calls   int           // calls made; the last one's row is Skipped
	}{
		{"total equal to budget skips that row", 0, false, 4},
		{"total just below budget judges that row", time.Nanosecond, false, 5},
		{"time between rows spends nothing", 0, true, 4},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			q := budget / 4
			sockets := []string{sweepSocket(0), sweepSocket(1), sweepSocket(2), sweepSocket(3), sweepSocket(4), sweepSocket(5)}
			r := newSweepRun(t, q, sockets...)
			want := map[string]int{}
			for i, sock := range sockets {
				r.seed(sock, lblCurrent, tmuxfix.SeedSession{})
				if i < s.calls {
					want[sock] = 1
				}
			}
			sw := r.sweep(budget + s.over)
			var idle time.Duration
			// One row per socket, then socket 0's again: its answer is held.
			for i, sock := range append(sockets, sockets[0]) {
				if s.between && i > 0 {
					r.Clock.Advance(10 * budget)
					idle += 10 * budget
				}
				row := r.row(sock)
				if got := sw.Lookup(row, ""); i < s.calls-1 {
					r.check(got, row, "")
				} else {
					checkSkipped(t, got)
				}
			}
			r.checkCalls(tmux.CallLookup, want)
			if spent := r.Clock.Now().Sub(sweepT0) - idle; spent > budget+s.over+q {
				t.Errorf("time in calls %v, want at most budget plus one call (%v)", spent, budget+s.over+q)
			}
		})
	}
}

// TestSweepBudgetClockSteppedBack: a clock stepped back during a call (a
// lookup's here; a listing's is charged the same way) leaves the time spent
// unchanged; a refund would let the third row be judged.
func TestSweepBudgetClockSteppedBack(t *testing.T) {
	r := newSweepRun(t, sweepQuery, sockA, sockB, sockC)
	for _, sock := range []string{sockA, sockB, sockC} {
		r.seed(sock, lblCurrent, tmuxfix.SeedSession{})
	}
	r.Rec.AfterCall(tmux.CallLookup, func(c tmuxfix.SocketCall, _ error) {
		if c.Socket == sockB {
			r.Clock.Advance(-3 * sweepQuery)
		}
	})
	sw := r.sweep(2 * sweepQuery)
	a, b, c := r.row(sockA), r.row(sockB), r.row(sockC)
	r.check(sw.Lookup(a, ""), a, "")
	r.check(sw.Lookup(b, ""), b, "")
	checkSkipped(t, sw.Lookup(c, ""))
	r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1, sockC: 1})
}

// TestSweepBudgetAfterStopRule: a socket stopped by the stop rule stays
// stopped; the budget then stops the sockets that were not.
func TestSweepBudgetAfterStopRule(t *testing.T) {
	r := newSweepRun(t, sweepQuery, sockA, sockB, sockC)
	r.fail(sockA, tmux.FailTimeout, tmux.CallLookup)
	r.seed(sockB, lblCurrent, tmuxfix.SeedSession{}).seed(sockC, lblCurrent, tmuxfix.SeedSession{})
	sw := r.sweep(3 * sweepQuery)
	a, b, c := r.row(sockA), r.row(sockB), r.row(sockC)
	r.check(sw.Lookup(a, ""), a, "")
	r.check(sw.Lookup(b, ""), b, "")
	checkSkipped(t, sw.Lookup(a, "")) // stop rule, budget not yet spent
	checkSkipped(t, sw.Lookup(c, "")) // its call spends the budget
	checkSkipped(t, sw.Lookup(b, "")) // held answer, budget spent
	checkSkipped(t, sw.Lookup(a, ""))
	r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1, sockC: 1})
}

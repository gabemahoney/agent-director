package tmux_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The sweep's adoption pane listing (SR-3.6, SR-3.7, SR-3.15, SR-13.3,
// SR-13.5; AC-FM-04, AC-FM-11, AC-EXP-08): one listing per socket under the
// lookups' stop rule and budget, over sweepRun.

// paneSession is a current-labelled session whose first pane carries the
// default row's pane label and whose second carries none.
var paneSession = tmuxfix.SeedSession{Panes: []tmuxfix.SeedPane{{AdPane: tmuxfix.Token}, {Index: 1}}}

// newPaneRun is a sweep run with paneSession on each socket.
func newPaneRun(t *testing.T, sockets ...string) *sweepRun {
	r := newSweepRun(t, sweepQuery, sockets...)
	for _, s := range sockets {
		r.seed(s, lblCurrent, paneSession)
	}
	return r
}

// listingFailing is the lookup's result for row under failure fl, as the
// pane listing's call reports it.
func listingFailing(r *sweepRun, fl tmux.Failure, row tmux.Launch) tmux.Result {
	w := r.lookupFailing(fl, row, "")
	if w.Cause != nil {
		c := *w.Cause
		c.Call = tmux.CallListPanes
		w.Cause = &c
	}
	return w
}

// checkListingSkipped asserts got is a bare Skipped listing.
func checkListingSkipped(t *testing.T, got tmux.PaneListing) {
	t.Helper()
	if got.Listed || got.Panes != nil {
		t.Errorf("listing %+v, want none", got)
	}
	checkSkipped(t, got.Result)
}

// TestSweepListPanesOncePerSocket: a socket's first request lists it; later
// requests get the held listing, AdPane included, as their own copy.
func TestSweepListPanesOncePerSocket(t *testing.T) {
	r := newPaneRun(t, sockA, sockB)
	sw := r.sweep(noBudget)
	a1, a2, b1 := r.row(sockA), r.row(sockA, rowNoServer), r.row(sockB)
	for _, row := range []tmux.Launch{a1, a2, b1} {
		r.check(sw.Lookup(row, ""), row, "")
	}
	first := sw.ListPanes(r.Rec, a1)
	held := append([]tmux.Pane(nil), first.Panes...)
	first.Panes[0] = tmux.Pane{}
	second := sw.ListPanes(r.Rec, a2)
	for _, got := range []tmux.PaneListing{second, sw.ListPanes(r.Rec, b1)} {
		pane, match := tmux.PaneByToken(got.Panes, tmuxfix.Token)
		if !got.Listed || !reflect.DeepEqual(got.Result, tmux.Result{}) || len(got.Panes) != 2 ||
			match != tmux.PaneOne || pane.AdPane != tmuxfix.Token {
			t.Errorf("listing %+v: pane by token %+v (%d), want the labelled pane of two", got, pane, match)
		}
	}
	if !reflect.DeepEqual(second.Panes, held) {
		t.Errorf("held listing %+v, want the first listing %+v", second.Panes, held)
	}
	r.checkCalls(tmux.CallListPanes, map[string]int{sockA: 1, sockB: 1})
	r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1})
	if calls := len(r.Rec.SocketCalls()); r.Reads != 2*calls || r.PC.EnvReads() != 0 {
		t.Errorf("%d clock reads for %d calls, %d environment reads; want two per call and none", r.Reads, calls, r.PC.EnvReads())
	}
}

// TestSweepExpireStyleMakesNoListing: a sweep that only looks up makes no
// listing call.
func TestSweepExpireStyleMakesNoListing(t *testing.T) {
	r := newPaneRun(t, sockA, sockB)
	sw := r.sweep(noBudget)
	for _, row := range []tmux.Launch{r.row(sockA), r.row(sockB), r.row(sockA, rowNoServer)} {
		r.check(sw.Lookup(row, ""), row, "")
	}
	r.checkCalls(tmux.CallListPanes, map[string]int{})
	r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1})
}

// TestSweepListingFailures: a listing failure maps as the lookup's does; an
// unreadable or unavailable one stops only its socket, held answers included.
func TestSweepListingFailures(t *testing.T) {
	cases := []struct {
		name  string
		fail  tmux.Failure
		stops bool
	}{
		{"timeout stops", tmux.FailTimeout, true},
		{"unrecognised reply stops", tmux.FailUnrecognized, true},
		{"binary unavailable stops", tmux.FailUnavailable, true},
		{"socket permission stops", tmux.FailSocketDenied, true},
		{"no server stops nothing", tmux.FailNoServer, false},
		{"no socket stops nothing", tmux.FailNoSocket, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPaneRun(t, sockA, sockB)
			r.fail(sockA, tc.fail, tmux.CallListPanes)
			sw := r.sweep(noBudget)
			a1, a2, b1 := r.row(sockA), r.row(sockA, rowServer(lookupOther)), r.row(sockB)
			r.check(sw.Lookup(a1, ""), a1, "")
			if got := sw.ListPanes(r.Rec, a1); got.Listed || !reflect.DeepEqual(got.Result, listingFailing(r, tc.fail, a1)) {
				t.Errorf("listing %+v, want the lookup's result for the same failure %+v", got, listingFailing(r, tc.fail, a1))
			}
			look, list := sw.Lookup(a2, ""), sw.ListPanes(r.Rec, a2)
			if tc.stops {
				checkSkipped(t, look)
				checkListingSkipped(t, list)
			} else {
				r.check(look, a2, "")
				if list.Listed || !reflect.DeepEqual(list.Result, listingFailing(r, tc.fail, a2)) {
					t.Errorf("held failure %+v, want it mapped for the second row", list)
				}
			}
			r.check(sw.Lookup(b1, ""), b1, "")
			if got := sw.ListPanes(r.Rec, b1); !got.Listed {
				t.Errorf("other socket's listing %+v, want listed", got)
			}
			r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1})
			r.checkCalls(tmux.CallListPanes, map[string]int{sockA: 1, sockB: 1})
		})
	}
}

// TestSweepListingAfterLookupStop: once a lookup failure stops a socket, a
// listing request there is Skipped with no call.
func TestSweepListingAfterLookupStop(t *testing.T) {
	for _, fl := range []tmux.Failure{tmux.FailTimeout, tmux.FailUnrecognized, tmux.FailUnavailable, tmux.FailSocketDenied} {
		t.Run(fl.String(), func(t *testing.T) {
			r := newPaneRun(t, sockA)
			r.fail(sockA, fl, tmux.CallLookup)
			sw := r.sweep(noBudget)
			a := r.row(sockA)
			r.check(sw.Lookup(a, ""), a, "")
			checkListingSkipped(t, sw.ListPanes(r.Rec, a))
			r.checkCalls(tmux.CallListPanes, map[string]int{})
		})
	}
}

// TestSweepListingBudget: listings and lookups spend one budget; the listing
// that spends it is discarded and nothing of either kind follows.
func TestSweepListingBudget(t *testing.T) {
	r := newPaneRun(t, sockA, sockB, sockC)
	budget := 3 * sweepQuery
	sw := r.sweep(budget)
	a, b, c := r.row(sockA), r.row(sockB), r.row(sockC)
	r.check(sw.Lookup(a, ""), a, "")
	r.check(sw.Lookup(b, ""), b, "")
	checkListingSkipped(t, sw.ListPanes(r.Rec, a)) // its call spends the budget
	checkListingSkipped(t, sw.ListPanes(r.Rec, b))
	checkSkipped(t, sw.Lookup(a, "")) // held answer
	checkSkipped(t, sw.Lookup(c, ""))
	r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1})
	r.checkCalls(tmux.CallListPanes, map[string]int{sockA: 1})
	if spent := r.Clock.Now().Sub(sweepT0); spent > budget+sweepQuery {
		t.Errorf("time in calls %v, want at most budget plus one call", spent)
	}
}

// TestSweepListingHeldWithheldOnceBudgetSpent: a socket's held listing is
// not handed out once another socket's call has spent the budget.
func TestSweepListingHeldWithheldOnceBudgetSpent(t *testing.T) {
	r := newPaneRun(t, sockA, sockB)
	sw := r.sweep(3 * sweepQuery)
	a1, a2, b := r.row(sockA), r.row(sockA, rowNoServer), r.row(sockB)
	r.check(sw.Lookup(a1, ""), a1, "")
	if got := sw.ListPanes(r.Rec, a1); !got.Listed {
		t.Errorf("listing %+v, want listed", got)
	}
	checkSkipped(t, sw.Lookup(b, "")) // its call spends the budget
	checkListingSkipped(t, sw.ListPanes(r.Rec, a1))
	checkListingSkipped(t, sw.ListPanes(r.Rec, a2))
	r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1})
	r.checkCalls(tmux.CallListPanes, map[string]int{sockA: 1})
}

// TestSweepNonPositiveBudget: a budget of zero or less is spent from the
// start (fails closed): every row is Skipped and the clock is never read.
func TestSweepNonPositiveBudget(t *testing.T) {
	for name, budget := range map[string]time.Duration{"zero budget": 0, "negative budget": -1} {
		t.Run(name, func(t *testing.T) {
			r := newPaneRun(t, sockA, sockB)
			sw := r.sweep(budget)
			for _, row := range []tmux.Launch{r.row(sockA), r.row(sockB), r.row(sockA, rowNoServer)} {
				checkSkipped(t, sw.Lookup(row, ""))
				checkListingSkipped(t, sw.ListPanes(r.Rec, row))
			}
			if calls := r.Rec.SocketCalls(); len(calls) != 0 || r.Reads != 0 {
				t.Errorf("budget %v: calls %v, %d clock reads; want none", budget, calls, r.Reads)
			}
		})
	}
}

// TestSweepListingClockSteppedBack:a clock stepped back during a listing
// refunds nothing; a refund would let the next lookup be judged.
func TestSweepListingClockSteppedBack(t *testing.T) {
	r := newPaneRun(t, sockA, sockB)
	r.Rec.AfterCall(tmux.CallListPanes, func(tmuxfix.SocketCall, error) { r.Clock.Advance(-3 * sweepQuery) })
	sw := r.sweep(2 * sweepQuery)
	a, b := r.row(sockA), r.row(sockB)
	r.check(sw.Lookup(a, ""), a, "")
	if got := sw.ListPanes(r.Rec, a); !got.Listed {
		t.Errorf("listing %+v, want listed", got)
	}
	checkSkipped(t, sw.Lookup(b, ""))
	r.checkCalls(tmux.CallLookup, map[string]int{sockA: 1, sockB: 1})
	r.checkCalls(tmux.CallListPanes, map[string]int{sockA: 1})
}

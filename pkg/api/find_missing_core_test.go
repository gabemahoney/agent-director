package api_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// find-missing's core sweep behaviour on the fake store (SR-11.1, SR-11.2, SR-11.3, SR-11.6, SR-20.6); fakes and
// runFindMissing live in find_missing_test.go.

// TestFindMissingNoChangesWhenAllAlive: rows whose recorded process (SessionStart or pane) is alive with its
// recorded start time and carry no note get no write, no tmux call and are in neither list.
func TestFindMissingNoChangesWhenAllAlive(t *testing.T) {
	pc := procfix.New()
	pc.Set(201, procfix.Alive(fmStart))
	pc.Set(202, procfix.Alive(fmStart))
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(201, fmStart)),
		liveRow("b", withPane(202, fmStart)),
	}}
	rec := tmuxfix.NewRecorder()

	res := mustSweep(t, st, pc, fmSweep{tmux: rec})
	assertLists(t, res, nil, nil)
	if len(st.calls) != 0 {
		t.Errorf("store writes = %+v; want none", st.calls)
	}
	if got := pc.StartTimeCalls(); !slices.Equal(got, []int{201, 202}) {
		t.Errorf("StartTime calls = %v; want [201 202]", got)
	}
	assertLookups(t, rec, 0)
}

// TestFindMissingTransitionsUnprobeableRows: rows whose recorded agent process is dead are marked proc_absent;
// rows whose process cannot be checked take one lookup on their socket and, Gone, are marked tmux_absent.
func TestFindMissingTransitionsUnprobeableRows(t *testing.T) {
	pc := procfix.New()
	pc.Set(302, procfix.Alive(fmStart))
	pc.Set(303, procfix.Zombie())
	pc.Set(304, procfix.Unreadable())
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(301, fmStart)),
		liveRow("b", withPane(302, fmStart)),
		liveRow("c", withPane(303, fmStart)),
		liveRow("d", withSessionStart(304, fmStart)),
		liveRow("e"),
	}}
	rec := tmuxfix.NewRecorder()
	before := len(readAPITrailLines(t))

	res := mustSweep(t, st, pc, fmSweep{tmux: rec})
	assertLists(t, res, []string{"a", "c", "d", "e"}, nil)
	if got := st.ids("mark"); !equalStrings(got, []string{"a", "c", "d", "e"}) {
		t.Errorf("marks = %v; want [a c d e]", got)
	}
	for id, reason := range map[string]string{"a": "proc_absent", "c": "proc_absent", "d": "tmux_absent", "e": "tmux_absent"} {
		assertMarkReason(t, before, id, reason)
	}
	assertLookups(t, rec, 1)
}

// TestFindMissingNullPidFallbackGuardFree: after a reboot every recorded process is gone and every such row is
// marked with no refusal; a row with no identity is marked tmux_absent when its lookup is Gone.
func TestFindMissingNullPidFallbackGuardFree(t *testing.T) {
	pc := procfix.New() // empty table: every pid answers gone
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(401, fmStart)),
		liveRow("b", withPane(402, fmStart)),
		liveRow("c", withSessionStart(403, fmStart), withPane(403, fmStart)),
		liveRow("d"),
	}}
	before := len(readAPITrailLines(t))

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, []string{"a", "b", "c", "d"}, nil)
	assertMarkReason(t, before, "d", "tmux_absent")
	if got := pc.StartTimeCalls(); !slices.Equal(got, []int{401, 402, 403}) {
		t.Errorf("StartTime calls = %v; want [401 402 403] (none for the row with no identity)", got)
	}
}

// TestFindMissingZeroLiveRowsIsNoopSuccess: no live rows is a no-op: no reader or tmux call, no write, no log
// line, empty non-nil lists.
func TestFindMissingZeroLiveRowsIsNoopSuccess(t *testing.T) {
	pc := procfix.New()
	st := &fakeFindMissingStore{}
	lg := &recordingLogger{}
	rec := tmuxfix.NewRecorder()

	res := mustSweep(t, st, pc, fmSweep{lg: lg, tmux: rec})
	assertLists(t, res, nil, nil)
	if res.IDs == nil || res.UnverifiedIDs == nil {
		t.Errorf("ids=%#v unverified_ids=%#v; want non-nil empty slices", res.IDs, res.UnverifiedIDs)
	}
	if len(pc.StartTimeCalls()) != 0 || len(st.calls) != 0 || len(lg.lines) != 0 {
		t.Errorf("reader calls=%v writes=%+v log=%v; want none", pc.StartTimeCalls(), st.calls, lg.lines)
	}
	assertLookups(t, rec, 0)
}

// TestFindMissingPendingRowIsScanned: pending rows past their grace period are judged (SR-11.2): a gone launch
// pane process is marked proc_absent, a row with no identity whose lookup is Gone tmux_absent.
func TestFindMissingPendingRowIsScanned(t *testing.T) {
	launch := fmNow.Add(-fmGrace - time.Second).UnixMilli()
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("p-1", withLaunch(store.StatePending, launch), withPane(501, fmStart)),
		liveRow("p-2", withLaunch(store.StatePending, launch)),
	}}
	before := len(readAPITrailLines(t))

	res := mustFindMissing(t, st, procfix.New())
	assertLists(t, res, []string{"p-1", "p-2"}, nil)
	assertMarkReason(t, before, "p-1", "proc_absent")
	assertMarkReason(t, before, "p-2", "tmux_absent")
}

// TestFindMissingResultIDsSorted: ids come back sorted whatever the read order.
func TestFindMissingResultIDsSorted(t *testing.T) {
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("z", withSessionStart(601, fmStart)),
		liveRow("a", withSessionStart(602, fmStart)),
		liveRow("m", withSessionStart(603, fmStart)),
	}}

	res := mustFindMissing(t, st, procfix.New())
	assertLists(t, res, []string{"a", "m", "z"}, nil)
}

// TestFindMissingUnverifiedIDsNeverNull: unverified_ids is non-nil when empty and sorted when populated.
func TestFindMissingUnverifiedIDsNeverNull(t *testing.T) {
	pc := procfix.New()
	pc.Set(700, procfix.Alive(fmStart))
	res := mustFindMissing(t, &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(700, fmStart)),
	}}, pc)
	if res.UnverifiedIDs == nil {
		t.Errorf("UnverifiedIDs = nil; want non-nil ([]) empty slice")
	}

	for pid := 701; pid <= 703; pid++ {
		pc.Set(pid, procfix.Unreadable())
	}
	res = mustSweep(t, &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("z", withSessionStart(701, fmStart)),
		liveRow("a", withSessionStart(702, fmStart)),
		liveRow("m", withSessionStart(703, fmStart)),
	}}, pc, fmSweep{tmux: fmCantTell()})
	assertLists(t, res, nil, []string{"a", "m", "z"})
}

// fmMarkPaths are the two ways a row is marked: its process is dead (proc_absent), or it cannot be checked and
// its lookup is Gone (tmux_absent).
var fmMarkPaths = []struct {
	name   string
	proc   procfix.Process
	reason string
}{
	{"process path", procfix.Gone(), "proc_absent"},
	{"tmux path", procfix.Unreadable(), "tmux_absent"},
}

// TestFindMissingMarkingOrderPinned: a noted dead row gets exactly the guarded mark (liveness clear folded in),
// then its tick, then the permission-request close, on both paths; a separate clear fails.
func TestFindMissingMarkingOrderPinned(t *testing.T) {
	for _, p := range fmMarkPaths {
		t.Run(p.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(7, p.proc)
			st := &fakeFindMissingStore{
				rows:    []store.LiveSpawnIdentity{liveRow("ord-1", withSessionStart(7, fmStart), withNote("probe_eacces"))},
				trailAt: func() int { return len(readAPITrailLines(t)) },
			}
			before := len(readAPITrailLines(t))

			mustFindMissing(t, st, pc)
			if got := st.ops("ord-1"); !equalStrings(got, []string{"mark", "close"}) {
				t.Fatalf("writes on ord-1 = %v; want [mark close] (no separate clear)", got)
			}
			lines, tick := readAPITrailLines(t), -1
			for i := before; i < len(lines) && tick < 0; i++ {
				l := lines[i]
				if l["event"] == "ad.find_missing.tick" && l["claude_instance_id"] == "ord-1" && l["reconciliation_reason"] == p.reason {
					tick = i
				}
			}
			if tick < st.calls[0].at || tick >= st.calls[1].at {
				t.Errorf("%s tick at trail line %d; want after the mark (line %d) and before the close (line %d)",
					p.reason, tick, st.calls[0].at, st.calls[1].at)
			}
		})
	}
}

// TestFindMissingAlreadyTerminalRowMarkOnly: a mark, on either path, that finds the row changed or absent writes
// nothing more: no close, not counted, in neither list.
func TestFindMissingAlreadyTerminalRowMarkOnly(t *testing.T) {
	for _, p := range fmMarkPaths {
		for name, res := range map[string]store.CondResult{"changed": store.CondChanged, "absent": store.CondAbsent} {
			t.Run(p.name+"/"+name, func(t *testing.T) {
				pc := procfix.New()
				pc.Set(3, p.proc)
				st := &fakeFindMissingStore{
					rows:    []store.LiveSpawnIdentity{liveRow("gone", withSessionStart(3, fmStart))},
					answers: map[string]map[string]fmAnswer{"mark": {"gone": {res: res}}},
				}

				got := mustFindMissing(t, st, pc)
				assertLists(t, got, nil, nil)
				if ops := st.ops("gone"); !equalStrings(ops, []string{"mark"}) {
					t.Errorf("writes on gone = %v; want [mark] only", ops)
				}
			})
		}
	}
}

// TestFindMissingListErrorAborts: a live-row read error fails the sweep before any reader or tmux call.
func TestFindMissingListErrorAborts(t *testing.T) {
	pc := procfix.New()
	rec := tmuxfix.NewRecorder()
	if _, err := runFindMissing(&fakeFindMissingStore{listErr: errSentinel}, pc, fmSweep{tmux: rec}); err == nil {
		t.Fatalf("FindMissing: nil error; want the list error to bubble up")
	}
	if got := pc.StartTimeCalls(); len(got) != 0 {
		t.Errorf("StartTime calls = %v; want none", got)
	}
	assertLookups(t, rec, 0)
}

// errSentinel (a shared package-level error, declared in sendkeys_test.go) is reused by the list-error test.

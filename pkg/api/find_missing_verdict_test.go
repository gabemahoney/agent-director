package api_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
)

// find-missing's per-row outcome by the recorded process's start-time answer, with the fake store (SR-3.8,
// SR-11.1, SR-11.4); fakes and runFindMissing live in find_missing_test.go.
// TestFindMissingStartTimeVerdict: the recorded process's start-time answer decides the row: dead marks it,
// alive clears only a carried note, unreadable notes it probe_eacces; every write carries the read snapshot.
func TestFindMissingStartTimeVerdict(t *testing.T) {
	cases := []struct {
		name       string
		proc       procfix.Process
		note       string // the note the row carries
		wantOps    []string
		wantNote   string // the note written, if any
		ids, unver []string
	}{
		{name: "gone marks", proc: procfix.Gone(), wantOps: []string{"mark", "close"}, ids: []string{"r"}},
		{name: "zombie marks", proc: procfix.Zombie(), wantOps: []string{"mark", "close"}, ids: []string{"r"}},
		{name: "other start time marks", proc: procfix.Alive(fmOtherStart), wantOps: []string{"mark", "close"}, ids: []string{"r"}},
		{name: "alive without note untouched", proc: procfix.Alive(fmStart)},
		{name: "alive with note cleared", proc: procfix.Alive(fmStart), note: "probe_eacces", wantOps: []string{"clear"}},
		{name: "unreadable noted", proc: procfix.Unreadable(), wantOps: []string{"note"}, wantNote: "probe_eacces", unver: []string{"r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(800, tc.proc)
			r := liveRow("r", withSessionStart(800, fmStart), withNote(tc.note))
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

			res := mustFindMissing(t, st, pc)
			assertLists(t, res, tc.ids, tc.unver)
			if got := st.ops("r"); !equalStrings(got, tc.wantOps) {
				t.Errorf("writes = %v; want %v", got, tc.wantOps)
			}
			for _, c := range st.calls {
				if c.op != "close" && c.snap != r.Snapshot {
					t.Errorf("%s guarded on %+v; want the read snapshot %+v", c.op, c.snap, r.Snapshot)
				}
				if c.op == "note" && c.note != tc.wantNote {
					t.Errorf("note written = %q; want %q", c.note, tc.wantNote)
				}
			}
			if got := pc.StartTimeCalls(); !slices.Equal(got, []int{800}) {
				t.Errorf("StartTime calls = %v; want [800] (one read of the recorded pid)", got)
			}
		})
	}
}

// TestFindMissingCheckerUnknownIsolatesRow: an unreadable row is noted probe_eacces and never marked, while a
// dead sibling in the same sweep is still marked and closed.
func TestFindMissingCheckerUnknownIsolatesRow(t *testing.T) {
	pc := procfix.New()
	pc.Set(10, procfix.Unreadable())
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("walled", withSessionStart(10, fmStart)),
		liveRow("dead-2", withSessionStart(20, fmStart)),
	}}

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, []string{"dead-2"}, []string{"walled"})
	if got := st.ops("walled"); !equalStrings(got, []string{"note"}) || st.calls[0].note != "probe_eacces" {
		t.Errorf("writes on walled = %+v; want one note probe_eacces", st.calls)
	}
	if got := st.ops("dead-2"); !equalStrings(got, []string{"mark", "close"}) {
		t.Errorf("writes on dead-2 = %v; want [mark close]", got)
	}
}

// TestFindMissingUnknownRepeatStillUnverified: an unreadable row already noted probe_eacces gets no write on a
// repeat sweep and is still in unverified_ids.
func TestFindMissingUnknownRepeatStillUnverified(t *testing.T) {
	pc := procfix.New()
	pc.Set(10, procfix.Unreadable())
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("walled", withSessionStart(10, fmStart), withNote("probe_eacces")),
	}}

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, nil, []string{"walled"})
	if len(st.calls) != 0 {
		t.Errorf("writes = %+v; want none (equal note)", st.calls)
	}
}

// TestFindMissingPartialIdentityPidOnlyFallsBack: a pid-only identity (no start time) reading gone is marked;
// reading alive proves nothing, so the row is noted probe_eacces, never marked (Task 2's lookup decides it).
func TestFindMissingPartialIdentityPidOnlyFallsBack(t *testing.T) {
	cases := []struct {
		name       string
		identity   fmRowOpt
		proc       procfix.Process
		ids, unver []string
	}{
		{"sessionstart pid-only gone", withSessionStart(900, ""), procfix.Gone(), []string{"r"}, nil},
		{"pane pid-only gone", withPane(900, ""), procfix.Gone(), []string{"r"}, nil},
		{"sessionstart pid-only alive", withSessionStart(900, ""), procfix.Alive(fmStart), nil, []string{"r"}},
		{"pane pid-only alive", withPane(900, ""), procfix.Alive(fmStart), nil, []string{"r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(900, tc.proc)
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{liveRow("r", tc.identity)}}

			res := mustFindMissing(t, st, pc)
			assertLists(t, res, tc.ids, tc.unver)
			if tc.unver != nil && (len(st.calls) != 1 || st.calls[0].note != "probe_eacces") {
				t.Errorf("writes = %+v; want one note probe_eacces", st.calls)
			}
		})
	}
}

// TestFindMissingPartialIdentityStarttimeOnlyFallsBack: a start time with no pid records no process, so the
// reader is not called and the row is noted process_not_seen_tmux_unchecked, never marked (Task 2's lookup).
func TestFindMissingPartialIdentityStarttimeOnlyFallsBack(t *testing.T) {
	for name, identity := range map[string]fmRowOpt{
		"sessionstart": withSessionStart(0, fmStart),
		"pane":         withPane(0, fmStart),
	} {
		t.Run(name, func(t *testing.T) {
			pc := procfix.New()
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{liveRow("st-only", identity)}}

			res := mustFindMissing(t, st, pc)
			assertLists(t, res, nil, []string{"st-only"})
			if len(st.calls) != 1 || st.calls[0].note != "process_not_seen_tmux_unchecked" {
				t.Errorf("writes = %+v; want one note process_not_seen_tmux_unchecked", st.calls)
			}
			if got := pc.StartTimeCalls(); len(got) != 0 {
				t.Errorf("StartTime calls = %v; want none", got)
			}
		})
	}
}

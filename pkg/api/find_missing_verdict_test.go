package api_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// find-missing's per-row outcome by the recorded process's start-time answer, with the fake store (SR-3.8,
// SR-11.1, SR-11.4); fakes and runFindMissing live in find_missing_test.go.
// TestFindMissingStartTimeVerdict: the recorded process's start-time answer decides the row: dead marks it,
// alive clears only a carried note, unreadable with a lookup that cannot tell notes it probe_eacces.
func TestFindMissingStartTimeVerdict(t *testing.T) {
	t.Parallel()
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

			res := mustSweep(t, st, pc, fmSweep{tmux: fmCantTell()})
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

// fmUnknownLookups are the lookups that decide a row whose process cannot be checked: Gone marks it tmux_absent;
// Ours and Can't tell (unreadable) leave it unverified, noted probe_eacces.
var fmUnknownLookups = []struct {
	name   string
	tmux   func(rows ...store.LiveSpawnIdentity) *tmuxfix.Recorder
	marked bool
}{
	{"gone", func(...store.LiveSpawnIdentity) *tmuxfix.Recorder { return tmuxfix.NewRecorder() }, true},
	{"ours", fmOurs, false},
	{"cant tell", func(...store.LiveSpawnIdentity) *tmuxfix.Recorder { return fmCantTell() }, false},
}

// unknownRow is a row recording its server and a pane process pid (start fmStart), so an Ours lookup adopts nothing.
func unknownRow(id string, pid int, opts ...fmRowOpt) store.LiveSpawnIdentity {
	return liveRow(id, append([]fmRowOpt{withServer(), withPane(pid, fmStart)}, opts...)...)
}

// TestFindMissingCheckerUnknownIsolatesRow: unreadable rows take one lookup on their socket and are never marked
// unless it is Gone, while a dead sibling in the same sweep is still marked and closed.
func TestFindMissingCheckerUnknownIsolatesRow(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, lk := range fmUnknownLookups {
		t.Run(lk.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(10, procfix.Unreadable())
			pc.Set(11, procfix.Unreadable())
			walled := []store.LiveSpawnIdentity{unknownRow("walled-1", 10), unknownRow("walled-2", 11)}
			st := &fakeFindMissingStore{rows: append(slices.Clone(walled), liveRow("dead", withSessionStart(20, fmStart)))}
			rec := lk.tmux(walled...)
			before := len(readAPITrailLines(t))

			res := mustSweep(t, st, pc, fmSweep{tmux: rec})
			wantOps, ids, unver := []string{"note"}, []string{"dead"}, []string{"walled-1", "walled-2"}
			if lk.marked {
				wantOps, ids, unver = []string{"mark", "close"}, []string{"dead", "walled-1", "walled-2"}, nil
			}
			assertLists(t, res, ids, unver)
			for _, w := range walled {
				if got := st.ops(w.ClaudeInstanceID); !equalStrings(got, wantOps) {
					t.Errorf("writes on %s = %v; want %v", w.ClaudeInstanceID, got, wantOps)
				}
				if lk.marked {
					assertMarkReason(t, before, w.ClaudeInstanceID, "tmux_absent")
				}
			}
			for _, c := range st.calls {
				if c.op == "note" && c.note != "probe_eacces" {
					t.Errorf("note on %s = %q; want probe_eacces", c.id, c.note)
				}
			}
			if got := st.ops("dead"); !equalStrings(got, []string{"mark", "close"}) {
				t.Errorf("writes on dead = %v; want [mark close]", got)
			}
			assertMarkReason(t, before, "dead", "proc_absent")
			assertLookups(t, rec, 1)
		})
	}
}

// TestFindMissingUnknownRepeatStillUnverified: an unreadable row already noted probe_eacces gets no write on a
// repeat sweep and stays unverified when its lookup is Ours or Can't tell; Gone marks it.
func TestFindMissingUnknownRepeatStillUnverified(t *testing.T) {
	t.Parallel()
	for _, lk := range fmUnknownLookups {
		t.Run(lk.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(10, procfix.Unreadable())
			r := unknownRow("walled", 10, withNote("probe_eacces"))
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

			res := mustSweep(t, st, pc, fmSweep{tmux: lk.tmux(r)})
			if lk.marked {
				assertLists(t, res, []string{"walled"}, nil)
				if got := st.ops("walled"); !equalStrings(got, []string{"mark", "close"}) {
					t.Errorf("writes = %v; want [mark close]", got)
				}
				return
			}
			assertLists(t, res, nil, []string{"walled"})
			if len(st.calls) != 0 {
				t.Errorf("writes = %+v; want none (equal note)", st.calls)
			}
		})
	}
}

// TestFindMissingPartialIdentityPidOnlyFallsBack: a pid-only identity (no start time) reading gone is marked
// proc_absent; reading alive proves nothing, so its lookup decides: Gone marks it tmux_absent.
func TestFindMissingPartialIdentityPidOnlyFallsBack(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	cases := []struct {
		name     string
		identity fmRowOpt
		proc     procfix.Process
		reason   string
	}{
		{"sessionstart pid-only gone", withSessionStart(900, ""), procfix.Gone(), "proc_absent"},
		{"pane pid-only gone", withPane(900, ""), procfix.Gone(), "proc_absent"},
		{"sessionstart pid-only alive", withSessionStart(900, ""), procfix.Alive(fmStart), "tmux_absent"},
		{"pane pid-only alive", withPane(900, ""), procfix.Alive(fmStart), "tmux_absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(900, tc.proc)
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{liveRow("r", tc.identity)}}
			before := len(readAPITrailLines(t))

			res := mustFindMissing(t, st, pc)
			assertLists(t, res, []string{"r"}, nil)
			assertMarkReason(t, before, "r", tc.reason)
			if got := pc.StartTimeCalls(); !slices.Equal(got, []int{900}) {
				t.Errorf("StartTime calls = %v; want [900]", got)
			}
		})
	}
}

// TestFindMissingPartialIdentityStarttimeOnlyFallsBack: a start time with no pid records no process, so the
// reader is not called and the lookup decides: Gone marks the row tmux_absent.
func TestFindMissingPartialIdentityStarttimeOnlyFallsBack(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for name, identity := range map[string]fmRowOpt{
		"sessionstart": withSessionStart(0, fmStart),
		"pane":         withPane(0, fmStart),
	} {
		t.Run(name, func(t *testing.T) {
			pc := procfix.New()
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{liveRow("st-only", identity)}}
			before := len(readAPITrailLines(t))

			res := mustFindMissing(t, st, pc)
			assertLists(t, res, []string{"st-only"}, nil)
			assertMarkReason(t, before, "st-only", "tmux_absent")
			if got := pc.StartTimeCalls(); len(got) != 0 {
				t.Errorf("StartTime calls = %v; want none", got)
			}
		})
	}
}

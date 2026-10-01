package api_test

// find_missing_notes_unusable_test.go: find-missing's note for a row whose recorded tmux session name cannot be
// used (SR-3.2, SR-11.3, SR-11.4, SR-11.6; AC-LKP-10, AC-LKP-15): its entry tick, its precedence over every
// tmux-path note, the overwrite, same-note, clear, same-life guard and store-error rules, and no tmux call. The
// names, notes and kinds come from unusable_name_fixture_test.go.

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fmuNamed records name as the row's tmux session name.
func fmuNamed(name string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.TmuxSessionName = name }
}

// fmuReps is the first unusable-name fixture of each kind, in SR-3.2's order.
func fmuReps() []unusableNameFixture {
	var out []unusableNameFixture
	for _, tok := range unusableNameTokens() {
		for _, f := range unusableNameFixtures() {
			if f.kind == tok.kind {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// assertNoTmux fails unless rec saw no tmux call at all.
func assertNoTmux(t *testing.T, rec *tmuxfix.Recorder) {
	t.Helper()
	if len(rec.SocketCalls()) != 0 || len(rec.Calls()) != 0 {
		t.Errorf("tmux calls = %+v %+v; want none", rec.SocketCalls(), rec.Calls())
	}
}

// assertOneNote fails unless st's only write on id is one note write of note guarded on snap.
func assertOneNote(t *testing.T, st *fakeFindMissingStore, id, note string, snap store.RowSnapshot) {
	t.Helper()
	var got []storeCall
	for _, c := range st.calls {
		if c.id == id {
			got = append(got, c)
		}
	}
	if len(got) != 1 || got[0].op != "note" || got[0].note != note || got[0].snap != snap {
		t.Errorf("writes on %s = %+v; want one note %q guarded on %+v", id, got, note, snap)
	}
}

// TestFindMissingUnusableNameNoteEntryTick: each fixture name, evidence unknown or none recorded, gets its note in
// one guarded write and one entry tick with that token, is unverified, and makes no tmux call.
func TestFindMissingUnusableNameNoteEntryTick(t *testing.T) {
	for _, f := range unusableNameFixtures() {
		for _, ev := range noteRows {
			t.Run(f.label+"/"+ev.name, func(t *testing.T) {
				r := ev.row(fmuNamed(f.raw))
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
				rec := tmuxfix.NewRecorder() // no server: a usable name would be looked up and marked
				before := trailLen(t)

				res := mustSweep(t, st, fmUnreadable(31), fmSweep{tmux: rec})
				assertLists(t, res, nil, []string{"n"})
				assertOneNote(t, st, "n", f.note, r.Snapshot)
				ticks := ticksSince(t, before, "n")
				if len(ticks) != 1 {
					t.Fatalf("ticks = %v; want exactly one", ticks)
				}
				assertTick(t, ticks[0], f.note, "", nil)
				assertNoTmux(t, rec)
			})
		}
	}
}

// TestFindMissingUnusableNameFaultPrecedence: a name with a control character and a character tmux rewrites
// gets the control-character note (SR-3.2's order).
func TestFindMissingUnusableNameFaultPrecedence(t *testing.T) {
	control := unusableNameTokenOf(tmux.UnusableControl).note
	for name, raw := range map[string]string{"colon and ESC": "ad:\x1b", "invalid UTF-8 and DEL": "bad\xff\x7f"} {
		t.Run(name, func(t *testing.T) {
			r := noteRows[0].row(fmuNamed(raw))
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

			mustSweep(t, st, fmUnreadable(31), fmSweep{})
			assertOneNote(t, st, "n", control, r.Snapshot)
		})
	}
}

// fmuPathCase is a world giving a usable row of row's evidence the tmux-path note note: an ntCases lookup, an
// earlier row stopping the socket, or a spent budget.
type fmuPathCase struct {
	name, note string
	row        func(id string, o ...fmRowOpt) store.LiveSpawnIdentity
	tmux       func(r store.LiveSpawnIdentity, storeID string) *tmuxfix.Recorder
	stopper    bool          // an earlier usable row ("a") on the socket whose lookup reads unreadable
	budget     time.Duration // 0: the default
}

// fmuPathCases are ntCases plus the "not called" worlds: socket stopped, budget spent.
func fmuPathCases() []fmuPathCase {
	var out []fmuPathCase
	for _, c := range ntCases {
		out = append(out, fmuPathCase{name: c.name, note: c.note, row: c.row, tmux: c.tmux})
	}
	cantTell := func(store.LiveSpawnIdentity, string) *tmuxfix.Recorder { return fmCantTell() }
	ours := func(r store.LiveSpawnIdentity, id string) *tmuxfix.Recorder { return ntOurs(r, id) }
	return append(out,
		fmuPathCase{name: "socket stopped, evidence unknown", note: "probe_eacces", row: ntUnknownRow, tmux: cantTell, stopper: true},
		fmuPathCase{name: "socket stopped, no evidence", note: "process_not_seen_tmux_unchecked", row: ntBareRow, tmux: cantTell, stopper: true},
		fmuPathCase{name: "budget spent, evidence unknown", note: "probe_eacces", row: ntUnknownRow, tmux: ours, budget: fmBudgetSpent},
		fmuPathCase{name: "budget spent, no evidence", note: "process_not_seen_tmux_unchecked", row: ntBareRow, tmux: ours, budget: fmBudgetSpent},
	)
}

// TestFindMissingUnusableNameAheadOfPathNotes: in each world that gives a usable row a tmux-path note, the same
// row with an unusable name gets its own note instead, and no tmux call is made for it.
func TestFindMissingUnusableNameAheadOfPathNotes(t *testing.T) {
	for _, w := range fmuPathCases() {
		for _, f := range append(fmuReps(), usableNameFixture()) {
			t.Run(w.name+"/"+f.label, func(t *testing.T) {
				rec := w.tmux(w.row("n"), tmuxfix.StoreID) // the world of the row's own usable name
				r := w.row("n", fmuNamed(f.raw))
				rows, calls := []store.LiveSpawnIdentity{r}, 0
				if w.stopper {
					rows, calls = append(rows, ntUnknownRow("a")), 1 // "a" sorts first and takes the one lookup
				}
				st := &fakeFindMissingStore{rows: rows}
				before := trailLen(t)

				mustSweep(t, st, ntChecker(), fmSweep{tmux: rec, budget: w.budget})
				want := f.note
				if f.kind == tmux.UnusableNone {
					want = w.note // the control: the same row and world, a usable name
				}
				assertOneNote(t, st, "n", want, r.Snapshot)
				assertNoteTick(t, before, "n", want)
				if f.kind != tmux.UnusableNone && len(rec.SocketCalls())+len(rec.Calls()) != calls {
					t.Errorf("tmux calls = %+v %+v; want %d (none for the unusable row)", rec.SocketCalls(), rec.Calls(), calls)
				}
			})
		}
	}
}

// TestFindMissingUnusableNameNoteOverwrite: the same note is not rewritten and does not tick; any other note
// (a tmux-path note, provenance_conflict or another unusable-name note) is overwritten with no tick.
func TestFindMissingUnusableNameNoteOverwrite(t *testing.T) {
	froms := append([]string{}, ntNotes...)
	for _, tok := range unusableNameTokens() {
		froms = append(froms, tok.note)
	}
	for _, f := range fmuReps() {
		for _, from := range froms {
			t.Run(f.label+"/from "+from, func(t *testing.T) {
				r := noteRows[0].row(fmuNamed(f.raw), withNote(from))
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
				rec := tmuxfix.NewRecorder()
				before := trailLen(t)

				res := mustSweep(t, st, fmUnreadable(31), fmSweep{tmux: rec})
				assertLists(t, res, nil, []string{"n"})
				if from == f.note {
					if len(st.calls) != 0 {
						t.Errorf("writes = %+v; want none for the same note", st.calls)
					}
				} else {
					assertOneNote(t, st, "n", f.note, r.Snapshot)
				}
				assertNoteTick(t, before, "n", "")
				assertNoTmux(t, rec)
			})
		}
	}
}

// TestFindMissingUnusableNameNoteRealStore: on a real store, entry writes the note and its first unverified time
// with one tick; an overwrite keeps that time; the same note leaves the row untouched; none makes a tmux call.
func TestFindMissingUnusableNameNoteRealStore(t *testing.T) {
	const since = "2026-09-01 10:00:00"
	for _, f := range fmuReps() {
		cases := []struct {
			name  string
			opts  []apitest.SpawnOption
			delta int64
			tick  string
		}{
			{"entry from no note", nil, 1, f.note},
			{"overwrite of probe_eacces", []apitest.SpawnOption{apitest.WithLivenessNote("probe_eacces"),
				apitest.WithLivenessUnverifiedSince(since)}, 1, ""},
			{"same note", []apitest.SpawnOption{apitest.WithLivenessNote(f.note), apitest.WithLivenessUnverifiedSince(since)}, 0, ""},
		}
		for _, tc := range cases {
			t.Run(f.label+"/"+tc.name, func(t *testing.T) {
				s, dbPath := seedPaneRow(t, append([]apitest.SpawnOption{apitest.WithTmuxSessionName(f.raw)}, tc.opts...)...)
				was := readRow(t, dbPath)
				rec := tmuxfix.NewRecorder()
				before := trailLen(t)

				res := mustSweep(t, s, paneChecker(procfix.Unreadable()), fmSweep{tmux: rec})
				assertLists(t, res, nil, []string{"r"})
				now := readRow(t, dbPath)
				if now.LivenessNote != f.note || now.RowVersion.(int64)-was.RowVersion.(int64) != tc.delta {
					t.Errorf("note %v, row_version %v -> %v; want %s, +%d", now.LivenessNote, was.RowVersion, now.RowVersion, f.note, tc.delta)
				}
				switch {
				case tc.delta == 0 && !reflect.DeepEqual(now, was):
					t.Errorf("row changed: %+v -> %+v; want untouched", was, now)
				case tc.opts == nil && now.LivenessUnverifiedSince == nil:
					t.Errorf("liveness_unverified_since = NULL; want set on entry")
				case tc.opts != nil && now.LivenessUnverifiedSince != since:
					t.Errorf("liveness_unverified_since = %v; want the first stored %s", now.LivenessUnverifiedSince, since)
				}
				assertNoteTick(t, before, "r", tc.tick)
				assertNoTmux(t, rec)
			})
		}
	}
}

// TestFindMissingUnusableNameNoteClearedWhenAlive: a row carrying its unusable-name note whose process is now
// verified alive gets one guarded clear, no tick, no tmux call, and is in neither list.
func TestFindMissingUnusableNameNoteClearedWhenAlive(t *testing.T) {
	for _, f := range fmuReps() {
		t.Run(f.label, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(41, procfix.Alive(fmStart))
			r := liveRow("alive", withSessionStart(41, fmStart), fmuNamed(f.raw), withNote(f.note))
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
			rec := tmuxfix.NewRecorder()
			before := trailLen(t)

			res := mustSweep(t, st, pc, fmSweep{tmux: rec})
			assertLists(t, res, nil, nil)
			if len(st.calls) != 1 || st.calls[0].op != "clear" || st.calls[0].snap != r.Snapshot {
				t.Errorf("writes = %+v; want one clear guarded on %+v", st.calls, r.Snapshot)
			}
			assertNoteTick(t, before, "alive", "")
			assertNoTmux(t, rec)
		})
	}
}

// TestFindMissingUnusableNameNoteRefusedOrFailed: a note write that finds the row changed or absent, or fails in
// the store, ticks nothing and lists the row nowhere; a failure is logged, and a later dead row is still marked.
func TestFindMissingUnusableNameNoteRefusedOrFailed(t *testing.T) {
	storeErr := errors.New("disk I/O error")
	answers := map[string]fmAnswer{"changed": {res: store.CondChanged}, "absent": {res: store.CondAbsent},
		"store error": {err: storeErr}}
	for _, f := range fmuReps() {
		for name, a := range answers {
			t.Run(f.label+"/"+name, func(t *testing.T) {
				st := &fakeFindMissingStore{
					rows:    []store.LiveSpawnIdentity{liveRow("bad", withSessionStart(52, fmStart), fmuNamed(f.raw)), liveRow("next", withSessionStart(59, fmStart))},
					answers: map[string]map[string]fmAnswer{"note": {"bad": a}},
				}
				res, lg, before := sweepFake(t, st, guardedWritesChecker())
				assertLists(t, res, []string{"next"}, nil)
				assertOneNote(t, st, "bad", f.note, st.readSnap("bad"))
				assertNoteTick(t, before, "bad", "")
				logged := len(lg.lines) == 1 && strings.Contains(lg.lines[0], "bad") && strings.Contains(lg.lines[0], storeErr.Error())
				if (a.err != nil) != logged || (a.err == nil && len(lg.lines) != 0) {
					t.Errorf("log = %v; want one line naming bad and the error only on a store error", lg.lines)
				}
			})
		}
	}
}

// TestFindMissingUnusableNameChangedBetweenReadAndWrite: on a real store, a row relaunched or deleted between the
// read and the guarded note write gets no note and no tick and is in neither list.
func TestFindMissingUnusableNameChangedBetweenReadAndWrite(t *testing.T) {
	changes := map[string]func(t *testing.T, s *store.Store, dbPath string){
		"relaunch": func(t *testing.T, _ *store.Store, dbPath string) {
			if a := apitest.ApplyAgentHook(t, dbPath, "r", "SessionStart", "sess-relaunch"); !a.Applied {
				t.Fatalf("SessionStart not applied: %+v", a)
			}
		},
		"delete": func(t *testing.T, s *store.Store, _ string) {
			if err := s.DeleteSpawn("r"); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		},
	}
	for _, f := range fmuReps() {
		for name, change := range changes {
			t.Run(f.label+"/"+name, func(t *testing.T) {
				s, dbPath := seedPaneRow(t, apitest.WithTmuxSessionName(f.raw))
				var changed apitest.SpawnColumns
				var changedErr error
				is := interleavedStore{Store: s, between: func() {
					change(t, s, dbPath)
					changed, changedErr = apitest.ReadSpawnColumns(dbPath, "r")
				}}

				res, lg, before := sweepReal(t, is, paneChecker(procfix.Unreadable()))
				assertLists(t, res, nil, nil)
				now, err := apitest.ReadSpawnColumns(dbPath, "r")
				if (err == nil) != (changedErr == nil) || !reflect.DeepEqual(now, changed) {
					t.Errorf("row after sweep = %+v (err %v); want as the change left it %+v (err %v)", now, err, changed, changedErr)
				}
				assertNoteTick(t, before, "r", "")
				if len(lg.lines) != 0 {
					t.Errorf("log = %v; want none", lg.lines)
				}
			})
		}
	}
}

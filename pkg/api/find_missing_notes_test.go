package api_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's SR-11.4 note/tick rules and SR-11.6's same-life guard on the process path: the fake store
// (find_missing_test.go) scripts Changed/Absent/error; real stores prove the stored columns and version deltas.

// ticksSince returns id's ad.find_missing.tick lines written after the first before trail lines.
func ticksSince(t *testing.T, before int, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, row := range readAPITrailLines(t)[before:] {
		if row["event"] == "ad.find_missing.tick" && row["claude_instance_id"] == id {
			out = append(out, row)
		}
	}
	return out
}

// sweepFake runs one sweep of st judged by pc, with no tmux call (spent budget: rows reaching the lookup are not
// called), and returns the result, the log and the trail checkpoint taken before it.
func sweepFake(t *testing.T, st *fakeFindMissingStore, pc *procfix.Checker) (api.FindMissingResult, *recordingLogger, int) {
	t.Helper()
	before := len(readAPITrailLines(t))
	lg := &recordingLogger{}
	res, err := runFindMissing(st, pc, fmSweep{lg: lg, budget: fmBudgetSpent})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	return res, lg, before
}

// fmUnreadable is a fake checker on which pid's start time cannot be read.
func fmUnreadable(pid int) *procfix.Checker {
	pc := procfix.New()
	pc.Set(pid, procfix.Unreadable())
	return pc
}

// noteRows are the two not-called outcomes: evidence unknown (unreadable pid) and no identity recorded.
var noteRows = []struct {
	name string
	note string
	row  func(opts ...fmRowOpt) store.LiveSpawnIdentity
}{
	{"unknown evidence", "probe_eacces", func(o ...fmRowOpt) store.LiveSpawnIdentity {
		return liveRow("n", append([]fmRowOpt{withSessionStart(31, fmStart)}, o...)...)
	}},
	{"no identity", "process_not_seen_tmux_unchecked", func(o ...fmRowOpt) store.LiveSpawnIdentity { return liveRow("n", o...) }},
}

// TestFindMissingNoteFirstWriteTicks: a row with no note gets one guarded note write and one tick with that
// note's token, prior_state/new_state null, and is in unverified_ids.
func TestFindMissingNoteFirstWriteTicks(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, tc := range noteRows {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.row()
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

			res, _, before := sweepFake(t, st, fmUnreadable(31))
			assertLists(t, res, nil, []string{"n"})
			if len(st.calls) != 1 || st.calls[0].op != "note" || st.calls[0].note != tc.note || st.calls[0].snap != r.Snapshot {
				t.Fatalf("writes = %+v; want one note %q guarded on %+v", st.calls, tc.note, r.Snapshot)
			}
			ticks := ticksSince(t, before, "n")
			if len(ticks) != 1 {
				t.Fatalf("ticks = %v; want exactly one", ticks)
			}
			assertAPITrailStr(t, ticks[0], "reconciliation_reason", tc.note)
			assertAPITrailStr(t, ticks[0], "source", "ad_find_missing")
			for _, k := range []string{"prior_state", "new_state"} {
				if v, ok := ticks[0][k]; !ok || v != nil {
					t.Errorf("[%s] = %v (present %v); want null", k, v, ok)
				}
			}
		})
	}
}

// TestFindMissingNoteEqualNoWrite: a row already carrying the reason's note gets no write and no tick, and is
// still in unverified_ids.
func TestFindMissingNoteEqualNoWrite(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, tc := range noteRows {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{tc.row(withNote(tc.note))}}

			res, _, before := sweepFake(t, st, fmUnreadable(31))
			assertLists(t, res, nil, []string{"n"})
			if len(st.calls) != 0 {
				t.Errorf("writes = %+v; want none", st.calls)
			}
			if ticks := ticksSince(t, before, "n"); len(ticks) != 0 {
				t.Errorf("ticks = %v; want none", ticks)
			}
		})
	}
}

// TestFindMissingNoteOverwriteNoTick: a different note (provenance_conflict included) is overwritten with the
// reason's note by one guarded write, with no tick; the row is in unverified_ids.
func TestFindMissingNoteOverwriteNoTick(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, seeded := range []string{"tmux_server_changed", "provenance_conflict"} {
		for _, tc := range noteRows {
			t.Run(seeded+" to "+tc.note, func(t *testing.T) {
				r := tc.row(withNote(seeded))
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

				res, _, before := sweepFake(t, st, fmUnreadable(31))
				assertLists(t, res, nil, []string{"n"})
				if len(st.calls) != 1 || st.calls[0].op != "note" || st.calls[0].note != tc.note || st.calls[0].snap != r.Snapshot {
					t.Errorf("writes = %+v; want one note %q guarded on the read snapshot", st.calls, tc.note)
				}
				if ticks := ticksSince(t, before, "n"); len(ticks) != 0 {
					t.Errorf("ticks = %v; want none", ticks)
				}
			})
		}
	}
}

// TestFindMissingAliveClearsEveryNote: a verified-alive row carrying any note gets one guarded clear, no tick,
// and is in neither list.
func TestFindMissingAliveClearsEveryNote(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	notes := []string{"probe_eacces", "process_not_seen_tmux_unchecked", "tmux_server_changed", "provenance_conflict"}
	for _, note := range notes {
		t.Run(note, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(41, procfix.Alive(fmStart))
			r := liveRow("alive", withSessionStart(41, fmStart), withNote(note))
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

			res, _, before := sweepFake(t, st, pc)
			assertLists(t, res, nil, nil)
			if len(st.calls) != 1 || st.calls[0].op != "clear" || st.calls[0].snap != r.Snapshot {
				t.Errorf("writes = %+v; want one clear guarded on %+v", st.calls, r.Snapshot)
			}
			if ticks := ticksSince(t, before, "alive"); len(ticks) != 0 {
				t.Errorf("ticks = %v; want none", ticks)
			}
		})
	}
}

// guardedWrites are the three guarded writes, each with a row and checker that lead the sweep to it.
var guardedWrites = []struct {
	op  string
	row func(id string) store.LiveSpawnIdentity
}{
	{"mark", func(id string) store.LiveSpawnIdentity { return liveRow(id, withSessionStart(51, fmStart)) }},
	{"note", func(id string) store.LiveSpawnIdentity { return liveRow(id, withSessionStart(52, fmStart)) }},
	{"clear", func(id string) store.LiveSpawnIdentity {
		return liveRow(id, withSessionStart(53, fmStart), withNote("tmux_server_changed"))
	}},
}

// guardedWritesChecker answers 51 gone (mark), 52 unreadable (note) and 53 alive (clear).
func guardedWritesChecker() *procfix.Checker {
	pc := procfix.New()
	pc.Set(52, procfix.Unreadable())
	pc.Set(53, procfix.Alive(fmStart))
	return pc
}

// TestFindMissingRefusedWriteNotListed: a mark, note write or clear that finds the row changed or absent gets
// no tick, no permission-request close, no log line, and leaves the row in neither list.
func TestFindMissingRefusedWriteNotListed(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, w := range guardedWrites {
		for name, res := range map[string]store.CondResult{"changed": store.CondChanged, "absent": store.CondAbsent} {
			t.Run(w.op+" "+name, func(t *testing.T) {
				st := &fakeFindMissingStore{
					rows:    []store.LiveSpawnIdentity{w.row("r")},
					answers: map[string]map[string]fmAnswer{w.op: {"r": {res: res}}},
				}

				got, lg, before := sweepFake(t, st, guardedWritesChecker())
				assertLists(t, got, nil, nil)
				if ops := st.ops("r"); !equalStrings(ops, []string{w.op}) {
					t.Errorf("writes = %v; want [%s] only", ops, w.op)
				}
				if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
					t.Errorf("ticks = %v; want none", ticks)
				}
				if len(lg.lines) != 0 {
					t.Errorf("log = %v; want none", lg.lines)
				}
			})
		}
	}
}

// TestFindMissingStoreErrorLoggedAndContinues: a store error on a mark, note write or clear is logged, the row
// gets no tick or close and is in neither list, the sweep succeeds and a later dead row is still marked.
func TestFindMissingStoreErrorLoggedAndContinues(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	storeErr := errors.New("disk I/O error")
	for _, w := range guardedWrites {
		t.Run(w.op, func(t *testing.T) {
			st := &fakeFindMissingStore{
				rows:    []store.LiveSpawnIdentity{w.row("bad"), liveRow("next", withSessionStart(59, fmStart))},
				answers: map[string]map[string]fmAnswer{w.op: {"bad": {err: storeErr}}},
			}

			res, lg, before := sweepFake(t, st, guardedWritesChecker())
			assertLists(t, res, []string{"next"}, nil)
			if ops := st.ops("bad"); !equalStrings(ops, []string{w.op}) {
				t.Errorf("writes on bad = %v; want [%s] only", ops, w.op)
			}
			if len(lg.lines) != 1 || !strings.Contains(lg.lines[0], "bad") || !strings.Contains(lg.lines[0], storeErr.Error()) {
				t.Errorf("log = %v; want one line naming bad and the store error", lg.lines)
			}
			if ticks := ticksSince(t, before, "bad"); len(ticks) != 0 {
				t.Errorf("ticks on bad = %v; want none", ticks)
			}
			if ticks := ticksSince(t, before, "next"); len(ticks) != 1 {
				t.Errorf("ticks on next = %v; want one (proc_absent)", ticks)
			}
		})
	}
}

// TestFindMissingEachWriteGuardedOnItsOwnSnapshot: in one sweep the mark, note write and clear each carry
// exactly the snapshot the live-row read returned for that row.
func TestFindMissingEachWriteGuardedOnItsOwnSnapshot(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	var rows []store.LiveSpawnIdentity
	for i, w := range guardedWrites {
		r := w.row(w.op)
		r.Snapshot.RowVersion = int64(10 + i)
		rows = append(rows, r)
	}
	st := &fakeFindMissingStore{rows: rows}

	sweepFake(t, st, guardedWritesChecker())
	for _, r := range rows {
		var guarded []store.RowSnapshot
		for _, c := range st.calls {
			if c.id == r.ClaudeInstanceID && c.op != "close" {
				guarded = append(guarded, c.snap)
			}
		}
		if len(guarded) != 1 || guarded[0] != r.Snapshot {
			t.Errorf("%s guarded on %+v; want once on %+v", r.ClaudeInstanceID, guarded, r.Snapshot)
		}
	}
}

// ── real store ──────────────────────────────────────────────────────────────

// notePanePID is the pane process recorded on every real-store row (with start time fmStart).
const notePanePID = 4321

// seedPaneRow seeds a working row "r" in a fresh store whose pane process is notePanePID; returns the open
// store and its path.
func seedPaneRow(t *testing.T, opts ...apitest.SpawnOption) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seedPaneRowAt(t, dbPath, true, "", opts...)
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath
}

// seedPaneRowAt seeds the working row "r" (pane notePanePID, start fmStart) at dbPath with sessionID.
func seedPaneRowAt(t *testing.T, dbPath string, create bool, sessionID string, opts ...apitest.SpawnOption) {
	t.Helper()
	tok, err := spawn.NewLaunchToken()
	if err != nil {
		t.Fatalf("NewLaunchToken: %v", err)
	}
	pane := apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token: tok, Socket: apitest.TestSocket, PaneID: "%1", PanePID: notePanePID, PaneStarttime: fmStart,
	})
	if _, err := apitest.SeedSpawn(dbPath, "r", store.StateWorking, "/tmp", "off", sessionID, create, append([]apitest.SpawnOption{pane}, opts...)...); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
}

// readRow reads row "r"'s columns, failing the test on any error.
func readRow(t *testing.T, dbPath string) apitest.SpawnColumns {
	t.Helper()
	c, err := apitest.ReadSpawnColumns(dbPath, "r")
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	return c
}

// paneChecker answers notePanePID with p.
func paneChecker(p procfix.Process) *procfix.Checker {
	pc := procfix.New()
	pc.Set(notePanePID, p)
	return pc
}

// sweepReal runs one sweep of s judged by pc, with no tmux call (spent budget), and returns the result, the log
// and the trail checkpoint.
func sweepReal(t *testing.T, s api.FindMissingStore, pc *procfix.Checker) (api.FindMissingResult, *recordingLogger, int) {
	t.Helper()
	before := len(readAPITrailLines(t))
	lg := &recordingLogger{}
	res, err := runFindMissing(s, pc, fmSweep{lg: lg, budget: fmBudgetSpent})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	return res, lg, before
}

// TestFindMissingNoteOverwriteKeepsFirstUnverifiedSince: on a real store an overwritten note keeps
// liveness_unverified_since as first stored, advances row_version by one and ticks nothing.
func TestFindMissingNoteOverwriteKeepsFirstUnverifiedSince(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	s, dbPath := seedPaneRow(t, apitest.WithLivenessNote("tmux_server_changed"),
		apitest.WithLivenessUnverifiedSince("2026-09-01 10:00:00"))
	was := readRow(t, dbPath)

	res, _, before := sweepReal(t, s, paneChecker(procfix.Unreadable()))
	assertLists(t, res, nil, []string{"r"})
	now := readRow(t, dbPath)
	if now.LivenessNote != "probe_eacces" {
		t.Errorf("liveness_note = %v; want probe_eacces", now.LivenessNote)
	}
	if !reflect.DeepEqual(now.LivenessUnverifiedSince, was.LivenessUnverifiedSince) || was.LivenessUnverifiedSince == nil {
		t.Errorf("liveness_unverified_since = %v; want the first stored %v", now.LivenessUnverifiedSince, was.LivenessUnverifiedSince)
	}
	if now.RowVersion.(int64) != was.RowVersion.(int64)+1 {
		t.Errorf("row_version %v -> %v; want +1", was.RowVersion, now.RowVersion)
	}
	if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
		t.Errorf("ticks = %v; want none", ticks)
	}
}

// TestFindMissingAliveRealStore: a verified-alive row with no note is not written (row_version unchanged); one
// with a note has note and unverified time cleared at +1 version. Neither ticks nor is listed.
func TestFindMissingAliveRealStore(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	cases := []struct {
		name  string
		opts  []apitest.SpawnOption
		delta int64
	}{
		{"no note untouched", nil, 0},
		{"note cleared", []apitest.SpawnOption{apitest.WithLivenessNote("provenance_conflict"),
			apitest.WithLivenessUnverifiedSince("2026-09-01 10:00:00")}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := seedPaneRow(t, tc.opts...)
			was := readRow(t, dbPath)

			res, _, before := sweepReal(t, s, paneChecker(procfix.Alive(fmStart)))
			assertLists(t, res, nil, nil)
			now := readRow(t, dbPath)
			if tc.delta == 0 && !reflect.DeepEqual(now, was) {
				t.Errorf("row changed: %+v -> %+v; want untouched", was, now)
			}
			if now.RowVersion.(int64)-was.RowVersion.(int64) != tc.delta || now.LivenessNote != nil || now.LivenessUnverifiedSince != nil {
				t.Errorf("row_version %v -> %v, note %v, since %v; want +%d, NULL, NULL",
					was.RowVersion, now.RowVersion, now.LivenessNote, now.LivenessUnverifiedSince, tc.delta)
			}
			if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
				t.Errorf("ticks = %v; want none", ticks)
			}
		})
	}
}

// interleavedStore is a real store that runs between once after its live-row read returns, before any write.
type interleavedStore struct {
	*store.Store
	between func()
}

func (s interleavedStore) ListLiveSpawnIdentities() ([]store.LiveSpawnIdentity, error) {
	rows, err := s.Store.ListLiveSpawnIdentities()
	if err == nil {
		s.between()
	}
	return rows, err
}

// TestFindMissingChangedBetweenReadAndWrite (AC-FM-07/08, process path): a row relaunched, reused, deleted or
// written by its own agent's hook between the read and the guarded write is not marked, noted or cleared.
func TestFindMissingChangedBetweenReadAndWrite(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	writes := []struct {
		op   string
		proc procfix.Process
		opts []apitest.SpawnOption
	}{
		{"mark", procfix.Gone(), nil},
		{"note", procfix.Unreadable(), []apitest.SpawnOption{apitest.WithLivenessNote("tmux_server_changed"),
			apitest.WithLivenessUnverifiedSince("2026-09-01 10:00:00")}},
		{"clear", procfix.Alive(fmStart), []apitest.SpawnOption{apitest.WithLivenessNote("probe_eacces")}},
	}
	changes := []struct {
		name  string
		apply func(t *testing.T, s *store.Store, dbPath string)
	}{
		{"relaunch", func(t *testing.T, _ *store.Store, dbPath string) {
			if a := apitest.ApplyAgentHook(t, dbPath, "r", "SessionStart", "sess-relaunch"); !a.Applied {
				t.Fatalf("SessionStart not applied: %+v", a)
			}
		}},
		{"own agent hook", func(t *testing.T, _ *store.Store, dbPath string) {
			if a := apitest.ApplyAgentHook(t, dbPath, "r", "Stop", ""); !a.Applied {
				t.Fatalf("Stop not applied: %+v", a)
			}
		}},
		{"reuse", func(t *testing.T, s *store.Store, dbPath string) {
			if err := s.DeleteSpawn("r"); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
			seedPaneRowAt(t, dbPath, false, "sess-reuse")
		}},
		{"delete", func(t *testing.T, s *store.Store, _ string) {
			if err := s.DeleteSpawn("r"); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}},
	}
	for _, w := range writes {
		for _, ch := range changes {
			t.Run(w.op+" after "+ch.name, func(t *testing.T) {
				s, dbPath := seedPaneRow(t, w.opts...)
				var (
					changed    apitest.SpawnColumns
					changedErr error
				)
				is := interleavedStore{Store: s, between: func() {
					ch.apply(t, s, dbPath)
					changed, changedErr = apitest.ReadSpawnColumns(dbPath, "r")
				}}

				res, lg, before := sweepReal(t, is, paneChecker(w.proc))
				assertLists(t, res, nil, nil)
				now, err := apitest.ReadSpawnColumns(dbPath, "r")
				if (err == nil) != (changedErr == nil) || !reflect.DeepEqual(now, changed) {
					t.Errorf("row after sweep = %+v (err %v); want as the change left it %+v (err %v)", now, err, changed, changedErr)
				}
				if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
					t.Errorf("ticks = %v; want none", ticks)
				}
				if len(lg.lines) != 0 {
					t.Errorf("log = %v; want none", lg.lines)
				}
			})
		}
	}
}

package api_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's SR-11.4 note and tick rules on the tmux path (split from find_missing_notes_test.go by size):
// the lookup decides the note; entry, equal, overwrite and provenance_conflict re-entry (AC-FM-03), stale
// notes cleared with no tmux call (AC-FM-13), and Skipped rows' "not called" notes.

// ntPanePID is the unreadable pane process of an "evidence unknown" row.
const ntPanePID = 61

// ntOtherServer answers on apitest.TestSocket in place of fmServer, whose process ntChecker keeps alive: a row
// recording fmServer reads it as a different server.
var ntOtherServer = tmuxfix.Server{PID: fmServer.PID + 1, Start: fmServer.Start + 60, ProcStart: fmStart}

// ntChecker answers ntPanePID unreadable and fmServer's process alive.
func ntChecker() *procfix.Checker {
	pc := procfix.New()
	pc.Set(ntPanePID, procfix.Unreadable())
	pc.Set(fmServer.PID, procfix.Alive(fmServer.ProcStart))
	return pc
}

// ntUnknownRow records fmServer and a pane whose process ntChecker cannot read (evidence unknown).
func ntUnknownRow(id string, o ...fmRowOpt) store.LiveSpawnIdentity {
	return liveRow(id, append([]fmRowOpt{withServer(), withPane(ntPanePID, fmStart)}, o...)...)
}

// ntBareRow records fmServer and no process evidence and no pane (a lost create reply).
func ntBareRow(id string, o ...fmRowOpt) store.LiveSpawnIdentity {
	return liveRow(id, append([]fmRowOpt{withServer()}, o...)...)
}

// ntOurs binds fmServer on apitest.TestSocket holding r's own session under its recorded name, labelled with
// r's current label for storeID, with panes (none: one unlabelled pane).
func ntOurs(r store.LiveSpawnIdentity, storeID string, panes ...tmuxfix.SeedPane) *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, fmServer).SeedSessions(apitest.TestSocket,
		tmuxfix.SeedSession{Name: r.TmuxSessionName, Label: ntLabel(r, storeID), Panes: panes})
}

// ntLabel is r's current label under storeID.
func ntLabel(r store.LiveSpawnIdentity, storeID string) tmux.Label {
	return tmuxfix.Valid(r.Identity.Token, r.ClaudeInstanceID, storeID)
}

// ntDuplicate is ntOurs plus a second session carrying r's current label: provenance_conflict.
func ntDuplicate(r store.LiveSpawnIdentity, storeID string) *tmuxfix.Recorder {
	return ntOurs(r, storeID).SeedSessions(apitest.TestSocket,
		tmuxfix.SeedSession{Name: r.TmuxSessionName + "-b", Label: ntLabel(r, storeID)})
}

// ntScopeValue is ntOurs with an @ad_owner value at global scope: provenance_conflict.
func ntScopeValue(r store.LiveSpawnIdentity, storeID string) *tmuxfix.Recorder {
	return ntOurs(r, storeID).SetScope(apitest.TestSocket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
}

// ntMovedServer is ntOtherServer holding r's own session: a different server (tmux_server_changed).
func ntMovedServer(r store.LiveSpawnIdentity, storeID string) *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, ntOtherServer).SeedSessions(apitest.TestSocket,
		tmuxfix.SeedSession{Name: r.TmuxSessionName, Label: ntLabel(r, storeID)})
}

// ntCase is one lookup scenario leaving its row unverified with note.
type ntCase struct {
	name string
	note string
	row  func(id string, o ...fmRowOpt) store.LiveSpawnIdentity
	tmux func(r store.LiveSpawnIdentity, storeID string) *tmuxfix.Recorder
}

// ntCases covers every SR-11.3 note the lookup can decide.
var ntCases = []ntCase{
	{"ours, evidence unknown", "probe_eacces", ntUnknownRow,
		func(r store.LiveSpawnIdentity, id string) *tmuxfix.Recorder { return ntOurs(r, id) }},
	{"unreadable, evidence unknown", "probe_eacces", ntUnknownRow,
		func(store.LiveSpawnIdentity, string) *tmuxfix.Recorder { return fmCantTell() }},
	{"ours, two token panes", "process_not_seen_session_present", ntBareRow,
		func(r store.LiveSpawnIdentity, id string) *tmuxfix.Recorder {
			return ntOurs(r, id, tmuxfix.SeedPane{AdPane: r.Identity.Token}, tmuxfix.SeedPane{Index: 1, AdPane: r.Identity.Token})
		}},
	{"ours, pane listing timed out", "process_not_seen_session_present", ntBareRow,
		func(r store.LiveSpawnIdentity, id string) *tmuxfix.Recorder {
			return ntOurs(r, id, tmuxfix.SeedPane{AdPane: r.Identity.Token}).
				Script(apitest.TestSocket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallListPanes)
		}},
	{"different server", "tmux_server_changed", ntBareRow, ntMovedServer},
	{"two current labels", "provenance_conflict", ntBareRow, ntDuplicate},
	{"scope value", "provenance_conflict", ntUnknownRow, ntScopeValue},
	{"unreadable, no evidence", "process_not_seen_tmux_unchecked", ntBareRow,
		func(store.LiveSpawnIdentity, string) *tmuxfix.Recorder { return fmCantTell() }},
	{"unavailable, no evidence", "process_not_seen_tmux_unchecked", ntBareRow,
		func(store.LiveSpawnIdentity, string) *tmuxfix.Recorder {
			return tmuxfix.NewRecorder().Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnavailable}, tmux.CallLookup)
		}},
}

// ntNotes are the five notes a live row can carry into a tmux-path sweep.
var ntNotes = []string{"probe_eacces", "process_not_seen_session_present", "process_not_seen_tmux_unchecked",
	"tmux_server_changed", "provenance_conflict"}

// sweepTmux runs one sweep of s judged by pc with rec as tmux (and budget, 0 = default); it returns the result
// and the trail checkpoint taken before it.
func sweepTmux(t *testing.T, s api.FindMissingStore, pc api.ProcChecker, rec *tmuxfix.Recorder, budget time.Duration) (api.FindMissingResult, int) {
	t.Helper()
	before := len(readAPITrailLines(t))
	return mustSweep(t, s, pc, fmSweep{tmux: rec, budget: budget}), before
}

// assertNoteTick fails unless id has exactly one note tick with reason since before (none when reason is ""),
// with prior_state and new_state present and null.
func assertNoteTick(t *testing.T, before int, id, reason string) {
	t.Helper()
	ticks := ticksSince(t, before, id)
	if reason == "" {
		if len(ticks) != 0 {
			t.Errorf("ticks on %s = %v; want none", id, ticks)
		}
		return
	}
	if len(ticks) != 1 {
		t.Fatalf("ticks on %s = %v; want exactly one %s", id, ticks, reason)
	}
	assertAPITrailStr(t, ticks[0], "reconciliation_reason", reason)
	assertAPITrailStr(t, ticks[0], "source", "ad_find_missing")
	for _, k := range []string{"prior_state", "new_state"} {
		if v, ok := ticks[0][k]; !ok || v != nil {
			t.Errorf("[%s] = %v (present %v); want null", k, v, ok)
		}
	}
}

// TestFindMissingTmuxNoteTickRules: each lookup-decided note, from no note, the same note and every other note,
// is written once (never when equal) and ticks only on entry from no note or into provenance_conflict (SR-11.4).
func TestFindMissingTmuxNoteTickRules(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, tc := range ntCases {
		type transition struct {
			from  string
			write bool
			tick  string
		}
		trs := []transition{{"", true, tc.note}, {tc.note, false, ""}}
		for _, from := range ntNotes {
			if from == tc.note {
				continue
			}
			tr := transition{from, true, ""}
			if tc.note == "provenance_conflict" {
				tr.tick = "provenance_conflict"
			}
			trs = append(trs, tr)
		}
		for _, tr := range trs {
			from := tr.from
			if from == "" {
				from = "no note"
			}
			t.Run(tc.name+" from "+from, func(t *testing.T) {
				r := tc.row("n", withNote(tr.from))
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
				rec := tc.tmux(r, tmuxfix.StoreID)

				res, before := sweepTmux(t, st, ntChecker(), rec, 0)
				assertLists(t, res, nil, []string{"n"})
				want := []string(nil)
				if tr.write {
					want = []string{"note"}
				}
				if ops := st.ops("n"); !equalStrings(ops, want) || (tr.write && st.calls[0].note != tc.note) {
					t.Errorf("writes = %+v; want %v with note %s", st.calls, want, tc.note)
				}
				assertNoteTick(t, before, "n", tr.tick)
				if n := len(rec.SocketCallsOf(tmux.CallLookup)); n != 1 {
					t.Errorf("lookups = %d; want 1", n)
				}
			})
		}
	}
}

// ── real store ──────────────────────────────────────────────────────────────

// ntSeed seeds a working row "r" recording tmuxfix.Token, fmServer and pane ntPanePID in a fresh store and
// returns the open store, its path and the row as the live-row read returns it.
func ntSeed(t *testing.T, opts ...apitest.SpawnOption) (*store.Store, string, store.LiveSpawnIdentity) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	id := apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token: tmuxfix.Token, Socket: apitest.TestSocket,
		ServerPID: fmServer.PID, ServerStart: fmServer.Start, ServerStarttime: fmServer.ProcStart,
		PaneID: "%1", PanePID: ntPanePID, PaneStarttime: fmStart,
	})
	if _, err := apitest.SeedSpawn(dbPath, "r", store.StateWorking, "/tmp", "off", "", true, append([]apitest.SpawnOption{id}, opts...)...); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rows, err := s.ListLiveSpawnIdentities()
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListLiveSpawnIdentities = %+v, %v; want one row", rows, err)
	}
	return s, dbPath, rows[0]
}

// TestFindMissingNoteConflictReentryRealStore (AC-FM-03): a row flagged with the process note enters
// provenance_conflict with one tick, repeats (duplicate or scope value) write and tick nothing, a different
// reason overwrites without a tick, and re-entry ticks again; liveness_unverified_since keeps its first value.
func TestFindMissingNoteConflictReentryRealStore(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	s, dbPath, r := ntSeed(t)
	steps := []struct {
		name  string
		tmux  func(r store.LiveSpawnIdentity, storeID string) *tmuxfix.Recorder
		note  string
		tick  string
		delta int64
	}{
		{"process note", func(r store.LiveSpawnIdentity, id string) *tmuxfix.Recorder { return ntOurs(r, id) }, "probe_eacces", "probe_eacces", 1},
		{"process note again", func(r store.LiveSpawnIdentity, id string) *tmuxfix.Recorder { return ntOurs(r, id) }, "probe_eacces", "", 0},
		{"conflict entered", ntDuplicate, "provenance_conflict", "provenance_conflict", 1},
		{"conflict repeated", ntDuplicate, "provenance_conflict", "", 0},
		{"conflict by scope value", ntScopeValue, "provenance_conflict", "", 0},
		{"different reason", ntMovedServer, "tmux_server_changed", "", 1},
		{"conflict re-entered", ntDuplicate, "provenance_conflict", "provenance_conflict", 1},
	}
	var since any
	for i, st := range steps {
		was := readRow(t, dbPath)
		res, before := sweepTmux(t, s, ntChecker(), st.tmux(r, s.StoreID()), 0)
		now := readRow(t, dbPath)
		t.Logf("step %d %s: note %v, version %v -> %v", i, st.name, now.LivenessNote, was.RowVersion, now.RowVersion)
		assertLists(t, res, nil, []string{"r"})
		if now.LivenessNote != st.note {
			t.Errorf("%s: liveness_note = %v; want %s", st.name, now.LivenessNote, st.note)
		}
		if d := now.RowVersion.(int64) - was.RowVersion.(int64); d != st.delta {
			t.Errorf("%s: row_version delta = %d; want %d", st.name, d, st.delta)
		}
		if i == 0 {
			since = now.LivenessUnverifiedSince
		}
		if since == nil || !reflect.DeepEqual(now.LivenessUnverifiedSince, since) {
			t.Errorf("%s: liveness_unverified_since = %v; want the first stored %v", st.name, now.LivenessUnverifiedSince, since)
		}
		assertNoteTick(t, before, "r", st.tick)
	}
}

// TestFindMissingConflictNoteNeverOnAnotherLife (AC-FM-03): a row relaunched by its own agent or deleted
// between its lookup and the conflict note write is left as the change left it: no note, no tick, neither list.
func TestFindMissingConflictNoteNeverOnAnotherLife(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	changes := map[string]func(t *testing.T, s *store.Store, dbPath string){
		"relaunch": func(t *testing.T, _ *store.Store, dbPath string) {
			if a := apitest.ApplyAgentHook(t, dbPath, "r", "SessionStart", "sess-relaunch"); !a.Applied {
				t.Errorf("SessionStart not applied: %+v", a)
			}
		},
		"delete": func(t *testing.T, s *store.Store, _ string) {
			if err := s.DeleteSpawn("r"); err != nil {
				t.Errorf("DeleteSpawn: %v", err)
			}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, dbPath, r := ntSeed(t)
			var (
				changed    apitest.SpawnColumns
				changedErr error
			)
			rec := ntDuplicate(r, s.StoreID()).AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				change(t, s, dbPath)
				changed, changedErr = apitest.ReadSpawnColumns(dbPath, "r")
			})

			res, before := sweepTmux(t, s, ntChecker(), rec, 0)
			assertLists(t, res, nil, nil)
			now, err := apitest.ReadSpawnColumns(dbPath, "r")
			if (err == nil) != (changedErr == nil) || !reflect.DeepEqual(now, changed) {
				t.Errorf("row after sweep = %+v (err %v); want as the change left it %+v (err %v)", now, err, changed, changedErr)
			}
			if err == nil && now.LivenessNote != nil {
				t.Errorf("liveness_note = %v; want none", now.LivenessNote)
			}
			assertNoteTick(t, before, "r", "")
		})
	}
}

// TestFindMissingStaleNoteClearedNoTmuxCall (AC-FM-13): a row carrying any note whose process (SessionStart or
// pane) is alive gets one clear guarded on the read snapshot, no tick, no tmux call, and is in neither list.
func TestFindMissingStaleNoteClearedNoTmuxCall(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	by := map[string]fmRowOpt{"session start": withSessionStart(71, fmStart), "pane": withPane(72, fmStart)}
	for _, note := range ntNotes {
		for how, opt := range by {
			t.Run(note+" by "+how, func(t *testing.T) {
				pc := ntChecker()
				pc.Set(71, procfix.Alive(fmStart))
				pc.Set(72, procfix.Alive(fmStart))
				r := liveRow("a", withServer(), opt, withNote(note))
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
				rec := ntOurs(r, tmuxfix.StoreID)

				res, before := sweepTmux(t, st, pc, rec, 0)
				assertLists(t, res, nil, nil)
				if ops := st.ops("a"); !equalStrings(ops, []string{"clear"}) {
					t.Errorf("writes = %v; want [clear]", ops)
				}
				assertNoteTick(t, before, "a", "")
				if calls := rec.SocketCalls(); len(calls) != 0 {
					t.Errorf("tmux calls = %+v; want none", calls)
				}
			})
		}
	}
}

// TestFindMissingStaleNoteClearedRealStore (AC-FM-13): on a real store an alive row's note and unverified time
// are cleared at +1 version; a row without a note is not written (version delta 0). No tmux call, no tick.
func TestFindMissingStaleNoteClearedRealStore(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, note := range append([]string{""}, ntNotes...) {
		t.Run("note "+note, func(t *testing.T) {
			var opts []apitest.SpawnOption
			delta := int64(0)
			if note != "" {
				opts, delta = []apitest.SpawnOption{apitest.WithLivenessNote(note),
					apitest.WithLivenessUnverifiedSince("2026-09-01 10:00:00")}, 1
			}
			s, dbPath, r := ntSeed(t, opts...)
			pc := ntChecker()
			pc.Set(ntPanePID, procfix.Alive(fmStart))
			rec := ntOurs(r, s.StoreID())
			was := readRow(t, dbPath)

			res, before := sweepTmux(t, s, pc, rec, 0)
			assertLists(t, res, nil, nil)
			now := readRow(t, dbPath)
			if d := now.RowVersion.(int64) - was.RowVersion.(int64); d != delta || now.LivenessNote != nil || now.LivenessUnverifiedSince != nil {
				t.Errorf("row_version delta %d, note %v, since %v; want %d, NULL, NULL", d, now.LivenessNote, now.LivenessUnverifiedSince, delta)
			}
			assertNoteTick(t, before, "r", "")
			if calls := rec.SocketCalls(); len(calls) != 0 {
				t.Errorf("tmux calls = %+v; want none", calls)
			}
		})
	}
}

// TestFindMissingSkippedRowsKeepNotCalledNotes: rows Skipped after their socket stopped, or with the budget
// spent, get the "not called" note by their evidence (entry ticks, overwrite does not, an equal note is not
// rewritten) and stay in unverified_ids.
func TestFindMissingSkippedRowsKeepNotCalledNotes(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	rows := []store.LiveSpawnIdentity{
		ntBareRow("a"),
		ntUnknownRow("b"),
		ntBareRow("c", withNote("process_not_seen_tmux_unchecked")),
		ntUnknownRow("d", withNote("provenance_conflict")),
	}
	want := []struct {
		id, note, tick string
	}{
		{"a", "process_not_seen_tmux_unchecked", "process_not_seen_tmux_unchecked"},
		{"b", "probe_eacces", "probe_eacces"},
		{"c", "", ""},
		{"d", "probe_eacces", ""},
	}
	cases := []struct {
		name    string
		tmux    *tmuxfix.Recorder
		budget  time.Duration
		lookups int
	}{
		{"socket stopped", fmCantTell(), 0, 1},
		{"budget spent", ntOurs(rows[0], tmuxfix.StoreID), fmBudgetSpent, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeFindMissingStore{rows: rows}

			res, before := sweepTmux(t, st, ntChecker(), tc.tmux, tc.budget)
			assertLists(t, res, nil, []string{"a", "b", "c", "d"})
			if calls := tc.tmux.SocketCalls(); len(calls) != tc.lookups {
				t.Errorf("tmux calls = %+v; want %d lookup(s)", calls, tc.lookups)
			}
			for _, w := range want {
				var notes []string
				for _, c := range st.calls {
					if c.id == w.id && c.op == "note" {
						notes = append(notes, c.note)
					}
				}
				if (w.note == "" && len(notes) != 0) || (w.note != "" && !equalStrings(notes, []string{w.note})) {
					t.Errorf("%s: note writes = %v; want [%s]", w.id, notes, w.note)
				}
				assertNoteTick(t, before, w.id, w.tick)
			}
		})
	}
}

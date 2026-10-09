package api_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's SR-11.4 note and tick rules on the tmux path: the lookup decides the note; entry, equal,
// overwrite and provenance_conflict re-entry (AC-FM-03), stale notes cleared with no tmux call (AC-FM-13), and
// Skipped rows' "not called" notes.

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

// assertNoteTick fails unless id has exactly one note tick with reason since before (none when reason is ""):
// prior_state and new_state null and no lookup field (assertTick).
func assertNoteTick(t *testing.T, before int, id, reason string) {
	t.Helper()
	ticks := ticksSince(t, before, id)
	switch {
	case reason == "" && len(ticks) != 0:
		t.Errorf("ticks on %s = %v; want none", id, ticks)
	case reason != "" && len(ticks) != 1:
		t.Errorf("ticks on %s = %v; want exactly one %s", id, ticks, reason)
	case reason != "":
		assertTick(t, ticks[0], reason, "", nil)
	}
}

// TestFindMissingTmuxNoteTickRules: each lookup-decided note, from no note, the same note and every other note
// (unreported, the note of a live pending row, included; b.kdf), is written once (never when equal) and ticks only
// on entry from no note or into provenance_conflict (SR-11.4).
func TestFindMissingTmuxNoteTickRules(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	for _, tc := range ntCases {
		type transition struct {
			from  string
			write bool
			tick  string
		}
		trs := []transition{{"", true, tc.note}, {tc.note, false, ""}}
		for _, from := range append(slices.Clone(ntNotes), "unreported") {
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

				res, _, before := sweepFrom(t, st, ntChecker(), fmSweep{tmux: rec})
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
	s, dbPath := seedPaneRow(t, append([]apitest.SpawnOption{apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token: tmuxfix.Token, Socket: apitest.TestSocket,
		ServerPID: fmServer.PID, ServerStart: fmServer.Start, ServerStarttime: fmServer.ProcStart,
		PaneID: "%1", PanePID: ntPanePID, PaneStarttime: fmStart,
	})}, opts...)...)
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
		res, _, before := sweepFrom(t, s, ntChecker(), fmSweep{tmux: st.tmux(r, s.StoreID())})
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

// TestFindMissingStaleNoteClearedRealStore (AC-FM-13): on a real store an alive row's note (a tmux-path or an
// unusable-name note) and unverified time are cleared at +1 version; a row without a note is not written (version
// delta 0). No tmux call, no tick, in neither list.
func TestFindMissingStaleNoteClearedRealStore(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	notes := append([]string{""}, ntNotes...)
	for _, tok := range unusableNameTokens() {
		notes = append(notes, tok.note)
	}
	for _, note := range notes {
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

			res, _, before := sweepFrom(t, s, pc, fmSweep{tmux: rec})
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

			res, _, before := sweepFrom(t, st, ntChecker(), fmSweep{tmux: tc.tmux, budget: tc.budget})
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

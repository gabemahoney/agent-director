package api_test

// find_missing_unreported_test.go: find-missing's unreported note of a live pending row (b.kdf, b.146 rule 10): the
// bug's repro and the agent's later hooks, the note's precedence and its round trip through another note; and a
// mark whose permission-request close fails (rule 12). Real stores, procfix and a tmuxfix.Recorder. The note's
// version guard is TestFindMissingChangedBetweenReadAndWrite's.

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unreportedEnv is one real store holding a pending row, its agent's pane process and a Recorder.
type unreportedEnv struct {
	s       *store.Store
	dbPath  string
	id      string
	panePID int
	pc      *procfix.Checker
	rec     *tmuxfix.Recorder
}

// newUnreportedEnv is b.kdf's repro: a launch's pending row (InsertPending), launched one second past the default
// grace period at fmNow, whose SessionStart hook never wrote. Its pane is recorded by the launch's identity write
// or, after a lost create reply (lostReply), only in its own labelled session; either way its agent is alive.
func newUnreportedEnv(t *testing.T, lostReply bool) *unreportedEnv {
	t.Helper()
	e := &unreportedEnv{dbPath: filepath.Join(t.TempDir(), "state.db"), id: "unreported-" + uuid.NewString()[:8],
		pc: procfix.New(), rec: tmuxfix.NewRecorder()}
	s, err := store.OpenOrInit(e.dbPath)
	if err != nil {
		t.Fatalf("store.OpenOrInit: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e.s = s
	if err := s.InsertPending(store.Spawn{ClaudeInstanceID: e.id, CWD: "/tmp", TmuxSessionName: "cd-" + e.id, RelayMode: "off",
		LaunchStartedAtMillis: fmNow.Add(-fmGrace - time.Second).UnixMilli(),
		Identity:              store.LaunchIdentity{Token: trailToken, Socket: apitest.TestSocket}}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if lostReply {
		e.panePID = e.rec.SeedRowSession(t, e.dbPath, e.id).Panes[0].PID
	} else {
		e.panePID = notePanePID
		pane := store.LaunchIdentity{PaneID: "%1", PanePID: e.panePID, PaneStarttime: fmStart}
		if res, err := s.RecordLaunchIdentity(e.id, 0, trailToken, pane); err != nil || res != store.CondApplied {
			t.Fatalf("RecordLaunchIdentity = %v, %v; want CondApplied", res, err)
		}
	}
	e.pc.Set(e.panePID, procfix.Alive(fmStart))
	return e
}

// cols reads the row raw.
func (e *unreportedEnv) cols(t *testing.T) apitest.SpawnColumns {
	t.Helper()
	c, err := apitest.ReadSpawnColumns(e.dbPath, e.id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	return c
}

// sweep runs one sweep of e's store with tmux rec (nil: e.rec), failing on a log line; it returns the result and
// the trail checkpoint taken before it.
func (e *unreportedEnv) sweep(t *testing.T, rec *tmuxfix.Recorder) (api.FindMissingResult, int) {
	t.Helper()
	if rec == nil {
		rec = e.rec
	}
	res, lg, mark := sweepFrom(t, e.s, e.pc, fmSweep{tmux: rec})
	if len(lg.lines) != 0 {
		t.Errorf("log = %q; want none", lg.lines)
	}
	return res, mark
}

// assertTimestamp fails unless v is a CURRENT_TIMESTAMP text.
func assertTimestamp(t *testing.T, what string, v any) {
	t.Helper()
	if s, _ := v.(string); s == "" {
		t.Errorf("%s = %#v; want a timestamp", what, v)
	} else if _, err := time.Parse(time.DateTime, s); err != nil {
		t.Errorf("%s = %q; want a timestamp: %v", what, s, err)
	}
}

// TestFindMissingNotesLivePendingRowUnreported is b.kdf's regression (the bug's repro): a pending row past its grace
// period whose agent process is alive and whose SessionStart hook never wrote stays pending, noted unreported, its
// launch_started_at kept (CSCB) and the time first noted set, with one tick, in neither list; a second sweep
// changes nothing (no row_version bump, the time kept); the agent's next hook, a late SessionStart or a gated
// UserPromptSubmit, records its session, moves the row to waiting or working and clears the note. Its pane is
// recorded, or adopted from its session after a lost create reply (the adoption's record says left_unreported).
func TestFindMissingNotesLivePendingRowUnreported(t *testing.T) {
	t.Parallel()
	hooks := []struct{ event, state string }{{"SessionStart", store.StateWaiting}, {"UserPromptSubmit", store.StateWorking}}
	for _, lostReply := range []bool{false, true} {
		for _, hook := range hooks {
			name := map[bool]string{false: "pane recorded", true: "lost create reply, pane adopted"}[lostReply] + "/late " + hook.event
			t.Run(name, func(t *testing.T) {
				t.Parallel() // one id per cell
				e := newUnreportedEnv(t, lostReply)
				before := e.cols(t)

				res, mark := e.sweep(t, nil)

				assertLists(t, res, nil, nil)
				after := e.cols(t)
				if after.State != store.StatePending || after.LivenessNote != "unreported" || after.LaunchStartedAt != before.LaunchStartedAt ||
					after.ClaudeSessionID != nil || after.PID != nil {
					t.Errorf("row = state %v, note %v, launch start %v, session %v, pid %v; want pending, unreported, %v kept, NULL, NULL",
						after.State, after.LivenessNote, after.LaunchStartedAt, after.ClaudeSessionID, after.PID, before.LaunchStartedAt)
				}
				assertTimestamp(t, "liveness_unverified_since", after.LivenessUnverifiedSince)
				if after.PanePID != int64(e.panePID) {
					t.Errorf("pane_pid = %v; want %d", after.PanePID, e.panePID)
				}
				writes := int64(1) // the note
				if lostReply {
					writes++ // the adoption first
					assertOneDisagree(t, mark, e.id, "adopted", "left_unreported", 1)
				} else if calls := e.rec.SocketCalls(); len(calls) != 0 {
					t.Errorf("tmux calls = %+v; want none (the process decides)", calls)
				}
				if after.RowVersion != before.RowVersion.(int64)+writes {
					t.Errorf("row_version %v -> %v; want +%d", before.RowVersion, after.RowVersion, writes)
				}
				assertNoteTick(t, mark, e.id, "unreported")

				res, mark = e.sweep(t, nil)
				assertLists(t, res, nil, nil)
				if again := e.cols(t); !reflect.DeepEqual(again, after) {
					t.Errorf("a second sweep changed the noted row:\n got  %+v\n want %+v", again, after)
				}
				assertNoteTick(t, mark, e.id, "")

				const sessionID = "sess-late-hook"
				if got := storefix.ApplyAgentHook(t, e.s, e.id, hook.event, sessionID); !got.Applied {
					t.Fatalf("late %s = %+v; want applied", hook.event, got)
				}
				late := e.cols(t)
				if late.State != hook.state || late.ClaudeSessionID != sessionID || late.LivenessNote != nil || late.LivenessUnverifiedSince != nil {
					t.Errorf("after the late %s: state %v, session %v, note %v, since %v; want %s, %s, NULL, NULL", hook.event,
						late.State, late.ClaudeSessionID, late.LivenessNote, late.LivenessUnverifiedSince, hook.state, sessionID)
				}
				if hook.event == "SessionStart" && (late.PID != int64(e.panePID) || late.ProcStarttime != fmStart) {
					t.Errorf("SessionStart identity = pid %v, proc start %v; want %d, %s", late.PID, late.ProcStarttime, e.panePID, fmStart)
				}
			})
		}
	}
}

// TestFindMissingUnreportedNeverReplacesProvenanceConflict (CSCB): a live pending row past its grace period, its pane
// recorded, noted provenance_conflict keeps that note and its time: the alive sweep neither clears nor replaces it,
// writes and ticks nothing, and lists it in neither list.
func TestFindMissingUnreportedNeverReplacesProvenanceConflict(t *testing.T) {
	t.Parallel()
	e := newUnreportedEnv(t, false)
	sp, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if res, err := e.s.SetLivenessNoteIfSameLife(e.id, sp.Snapshot, "provenance_conflict"); err != nil || res != store.CondApplied {
		t.Fatalf("note provenance_conflict = %v, %v; want CondApplied", res, err)
	}
	before := e.cols(t)

	res, mark := e.sweep(t, nil)

	assertLists(t, res, nil, nil)
	if after := e.cols(t); !reflect.DeepEqual(after, before) {
		t.Errorf("row changed:\n got  %+v\n want %+v", after, before)
	}
	assertNoteTick(t, mark, e.id, "")
}

// TestFindMissingUnreportedRoundTrip: a noted row whose agent becomes unreadable is noted probe_eacces, in
// unverified_ids; alive again it is noted unreported again, in neither list; only the first note ticks, a sweep
// finding it alive and noted writes nothing, and every later step keeps the time first noted (backdated after the
// first, so a rewrite in the same second shows).
func TestFindMissingUnreportedRoundTrip(t *testing.T) {
	t.Parallel()
	e := newUnreportedEnv(t, false)
	const first = "2026-01-01 00:00:00"
	steps := []struct {
		name       string
		proc       procfix.Process
		note       string
		tick       string
		unverified bool
		delta      int64
	}{
		{"alive: noted unreported", procfix.Alive(fmStart), "unreported", "unreported", false, 1},
		{"unreadable: noted probe_eacces", procfix.Unreadable(), "probe_eacces", "", true, 1},
		{"alive again: noted unreported again", procfix.Alive(fmStart), "unreported", "", false, 1},
		{"alive, already noted: nothing written", procfix.Alive(fmStart), "unreported", "", false, 0},
	}
	for i, st := range steps {
		e.pc.Set(e.panePID, st.proc)
		was := e.cols(t)

		res, mark := e.sweep(t, fmCantTell())

		now := e.cols(t)
		var unverified []string
		if st.unverified {
			unverified = []string{e.id}
		}
		assertLists(t, res, nil, unverified)
		if now.State != store.StatePending || now.LivenessNote != st.note || now.RowVersion.(int64)-was.RowVersion.(int64) != st.delta {
			t.Errorf("%s: state %v, note %v, row_version %v -> %v; want pending, %s, +%d", st.name, now.State, now.LivenessNote,
				was.RowVersion, now.RowVersion, st.note, st.delta)
		}
		assertNoteTick(t, mark, e.id, st.tick)
		if i == 0 {
			assertTimestamp(t, st.name+": liveness_unverified_since", now.LivenessUnverifiedSince)
			e.setFirstNoted(t, first)
		} else if now.LivenessUnverifiedSince != first {
			t.Errorf("%s: liveness_unverified_since = %v; want the time first noted %s kept", st.name, now.LivenessUnverifiedSince, first)
		}
	}
}

// setFirstNoted sets the row's liveness_unverified_since to at through a raw connection, row_version unchanged.
func (e *unreportedEnv) setFirstNoted(t *testing.T, at string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+e.dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE spawns SET liveness_unverified_since = ? WHERE claude_instance_id = ?`, at, e.id); err != nil {
		t.Fatalf("set liveness_unverified_since of %s: %v", e.id, err)
	}
}

// TestFindMissingMarkCloseFailureLeavesRowLive (b.146 rule 12): a dead agent's row whose open permission request
// cannot be closed is not marked either (one transaction): it stays live, its request stays open, nothing is
// ticked or listed, and one log line names it.
func TestFindMissingMarkCloseFailureLeavesRowLive(t *testing.T) {
	t.Parallel()
	dbPath, id := filepath.Join(t.TempDir(), "state.db"), "close-fail-"+uuid.NewString()[:8]
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateCheckPermission, "/tmp", "on", "", true,
		apitest.WithLaunchIdentity(store.LaunchIdentity{Token: trailToken, Socket: apitest.TestSocket, PaneID: "%1",
			PanePID: notePanePID, PaneStarttime: fmStart})); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	perm, err := apitest.SeedPermissionRequest(dbPath, id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	storefix.InjectWriteFailure(t, dbPath, storefix.WriteFailPermissionDecision, id)
	before, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}

	res, lg, mark := sweepFrom(t, s, paneChecker(procfix.Gone()), fmSweep{})

	assertLists(t, res, nil, nil)
	if after, err := apitest.ReadSpawnColumns(dbPath, id); err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("row changed (%v):\n before %+v\n after  %+v", err, before, after)
	}
	if pr, err := s.GetPermissionRequest(id, perm.RequestToken); err != nil || pr.Decision != "" {
		t.Errorf("permission request = %+v, %v; want it still open", pr, err)
	}
	if ticks := ticksSince(t, mark, id); len(ticks) != 0 {
		t.Errorf("ticks = %v; want none", ticks)
	}
	if len(lg.lines) != 1 || !strings.Contains(lg.lines[0], "("+id+")") || !strings.Contains(lg.lines[0], "injected write failure") {
		t.Errorf("log = %q; want one line naming %s and the store error", lg.lines, id)
	}
}

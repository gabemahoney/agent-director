package api_test

// resume_pending_test.go covers resume's move to pending (SR-8.3, SR-8.4,
// SR-8.6, SR-14, SR-20.6; AC-RES-08, AC-RES-09, AC-RES-18): the move's
// columns after the one pre-launch lookup, its visibility and the
// launch-in-progress refusal, on the shared fixture in resume_fixture_test.go
// against a real store. Two resumes in each order (AC-RES-11) are in
// resume_pending_race_test.go, SessionStart after the move in
// resume_pending_hook_test.go, and a second resume inside the stopping window
// (AC-RES-13) in resume_pending_stopping_test.go.

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pendStartedAt is the started_at the move tests seed, far from the clock's
// time, so a launch start taken from started_at shows.
var pendStartedAt = time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)

// pendTrail returns the trail lines of event for instance id.
func pendTrail(t *testing.T, event, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readAPITrailLines(t) {
		if l["event"] == event && l["claude_instance_id"] == id {
			out = append(out, l)
		}
	}
	return out
}

// pendMoved counts id's ad.resume.moved_to_pending lines.
func pendMoved(t *testing.T, id string) int {
	t.Helper()
	return len(pendTrail(t, "ad.resume.moved_to_pending", id))
}

// pendParent seeds a live row a caller can name as its parent and returns its id.
func pendParent(t *testing.T, e *resumeEnv) string {
	t.Helper()
	id, err := apitest.SeedSpawn(e.dbPath, "parent-"+uuid.NewString()[:8], store.StateWaiting, t.TempDir(), "off", "", false)
	if err != nil {
		t.Fatalf("SeedSpawn(parent): %v", err)
	}
	return id
}

// pendCalls is every tmux call rec recorded, name-based and socket-taking.
func pendCalls(rec *tmuxfix.Recorder) int { return len(rec.Calls()) + len(rec.SocketCalls()) }

// pendCallKinds is the kind of each socket-taking call rec recorded, in order.
func pendCallKinds(rec *tmuxfix.Recorder) []tmux.Call {
	var out []tmux.Call
	for _, c := range rec.SocketCalls() {
		out = append(out, c.Call)
	}
	return out
}

// pendRowNullOr returns nil for "" and s otherwise (a column's raw NULL or text).
func pendRowNullOr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestResumeMoveToPendingColumns: one lookup on the row's socket precedes the
// move (SR-8.1 step 3); the move clears the old process, liveness and tmux
// identity, records the clock's launch start (after the lookup), a new token,
// the create's socket and the caller's parent, keeps the session columns, and
// emits once.
func TestResumeMoveToPendingColumns(t *testing.T) {
	cases := []struct {
		name, state string
		parent      bool
	}{
		{"ended row, caller with a parent", store.StateEnded, true},
		{"ended row, bare shell", store.StateEnded, false},
		{"missing row, caller with a parent", store.StateMissing, true},
		{"missing row, bare shell", store.StateMissing, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedRow(t, resumableSpec{State: tc.state, Opts: []apitest.SpawnOption{apitest.WithStartedAt(pendStartedAt)}})
			histBefore, err := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID)
			if err != nil {
				t.Fatalf("ReadSessionHistoryAllLives: %v", err)
			}
			parent := ""
			if tc.parent {
				parent = pendParent(t, e)
				t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent)
			}
			wantStart := e.moveStart().UnixMilli()
			var moved apitest.SpawnColumns
			var callsAtMove []tmux.Call
			e.store.afterMove(func() { moved, callsAtMove = e.columns(t, r.ID), pendCallKinds(e.rec) })

			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if want := []tmux.Call{tmux.CallLookup}; !slices.Equal(callsAtMove, want) || e.rec.SocketCalls()[0].Socket != e.socket {
				t.Errorf("tmux calls before the move = %v on %q; want %v on the row's socket %q", callsAtMove, e.rec.SocketCalls()[0].Socket, want, e.socket)
			}
			if calls := e.rec.Calls(); len(calls) != 0 {
				t.Errorf("name-based tmux calls = %v; want none", calls)
			}
			b := r.Before
			if moved.State != store.StatePending {
				t.Errorf("state after the move = %v; want pending", moved.State)
			}
			cleared := map[string]any{"pid": moved.PID, "proc_starttime": moved.ProcStarttime, "ended_at": moved.EndedAt,
				"liveness_unverified_since": moved.LivenessUnverifiedSince, "liveness_note": moved.LivenessNote,
				"tmux_server_pid": moved.TmuxServerPID, "tmux_server_started": moved.TmuxServerStarted,
				"tmux_server_starttime": moved.TmuxServerStarttime, "pane_id": moved.PaneID,
				"pane_pid": moved.PanePID, "pane_starttime": moved.PaneStarttime}
			for col, v := range cleared {
				if v != nil {
					t.Errorf("%s after the move = %#v; want NULL", col, v)
				}
			}
			if moved.LaunchStartedAt != wantStart || wantStart == pendStartedAt.UnixMilli() {
				t.Errorf("launch_started_at = %#v; want the clock's %d, not started_at's %d", moved.LaunchStartedAt, wantStart, pendStartedAt.UnixMilli())
			}
			creates := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(creates) != 1 {
				t.Fatalf("creates = %d; want 1", len(creates))
			}
			tok, _ := moved.LaunchToken.(string)
			if !spawnTokenRE.MatchString(tok) || moved.LaunchToken == b.LaunchToken || creates[0].Token != tok {
				t.Errorf("launch_token = %#v (before %#v, create's %q); want a new token the create labels with", moved.LaunchToken, b.LaunchToken, creates[0].Token)
			}
			if moved.TmuxSocket != creates[0].Socket || creates[0].Socket != e.socket {
				t.Errorf("tmux_socket = %#v, create's socket %q; want both %q", moved.TmuxSocket, creates[0].Socket, e.socket)
			}
			if moved.ParentID != pendRowNullOr(parent) {
				t.Errorf("parent_id = %#v; want %#v", moved.ParentID, pendRowNullOr(parent))
			}
			kept := func(c apitest.SpawnColumns) []any {
				return []any{c.ClaudeSessionID, c.JSONLPath, c.LifeNumber, c.StartedAt, c.LastSeenAt, c.NoPreTrust,
					c.CWD, c.TmuxSessionName, c.ClaudeArgs, c.RelayMode, c.Labels, c.ExtraEnv}
			}
			if !reflect.DeepEqual(kept(moved), kept(b)) {
				t.Errorf("kept columns after the move = %#v; want %#v", kept(moved), kept(b))
			}
			if bv, _ := b.RowVersion.(int64); moved.RowVersion != bv+1 {
				t.Errorf("row_version after the move = %#v; want %d", moved.RowVersion, bv+1)
			}
			// The identity write used the version the move returned: one more, and the new pane recorded.
			after := e.columns(t, r.ID)
			sess := e.rec.Sessions(e.socket)
			if mv, _ := moved.RowVersion.(int64); after.RowVersion != mv+1 || len(sess) != 1 || after.PaneID != sess[0].Panes[0].ID {
				t.Errorf("after the identity write: row_version %#v, pane %#v; want %#v+1 and the created pane (sessions %+v)", after.RowVersion, after.PaneID, moved.RowVersion, sess)
			}
			histAfter, err := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID)
			if err != nil || !reflect.DeepEqual(histAfter, histBefore) {
				t.Errorf("history = %+v (%v); want %+v", histAfter, err, histBefore)
			}
			lines := pendTrail(t, "ad.resume.moved_to_pending", r.ID)
			if len(lines) != 1 {
				t.Fatalf("ad.resume.moved_to_pending lines = %d; want 1", len(lines))
			}
			assertAPITrailStr(t, lines[0], "prior_state", tc.state)
			assertAPITrailStr(t, lines[0], "claude_session_id", r.SessionID)
			assertAPITrailStr(t, lines[0], "source", "ad_resume")
		})
	}
}

// TestResumePendingVisibleOnEverySurface: after the move, status, get and list
// show pending with the resume's move time (after its lookup) as the launch
// start; get keeps the session id and prior_sessions.
func TestResumePendingVisibleOnEverySurface(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded, apitest.WithStartedAt(pendStartedAt))
	want := time.UnixMilli(e.moveStart().UnixMilli()).UTC()
	if _, err := e.resume(r.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	at := func(p *time.Time) string {
		if p == nil {
			return "<nil>"
		}
		return p.Format(time.RFC3339Nano)
	}
	st, err := e.c.Status(r.ID)
	if err != nil || st.State != store.StatePending || st.LaunchStartedAt == nil || !st.LaunchStartedAt.Equal(want) {
		t.Errorf("Status = {%s %s} (%v); want pending at %s", st.State, at(st.LaunchStartedAt), err, want.Format(time.RFC3339Nano))
	}
	g, err := e.c.Get(r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if g.State != store.StatePending || g.LaunchStartedAt == nil || !g.LaunchStartedAt.Equal(want) || g.StartedAt.Equal(want) {
		t.Errorf("Get = {%s launch %s started %s}; want pending at %s, not started_at", g.State, at(g.LaunchStartedAt), g.StartedAt, want.Format(time.RFC3339Nano))
	}
	if g.ClaudeSessionID != r.SessionID || len(g.PriorSessions) != 1 || g.PriorSessions[0].ClaudeSessionID != r.HistorySessionID {
		t.Errorf("Get session %q, prior_sessions %+v; want %q and [%s]", g.ClaudeSessionID, g.PriorSessions, r.SessionID, r.HistorySessionID)
	}
	l, err := e.c.List(api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	i := slices.IndexFunc(l.Spawns, func(s api.ListRow) bool { return s.ClaudeInstanceID == r.ID })
	if i < 0 || l.Spawns[i].State != store.StatePending || l.Spawns[i].LaunchStartedAt == nil || !l.Spawns[i].LaunchStartedAt.Equal(want) {
		t.Errorf("List row %d of %+v; want %s pending at %s", i, l.Spawns, r.ID, want.Format(time.RFC3339Nano))
	}
}

// pendRefusal is one refused resume: the row just before and after it, its
// error and the tmux calls and move events it added.
type pendRefusal struct {
	before, after apitest.SpawnColumns
	err           error
	calls, moved  int
}

// pendRefuse resumes id as a caller whose parent is callerParent and records
// what the call changed.
func pendRefuse(t *testing.T, e *resumeEnv, id, callerParent string) pendRefusal {
	t.Helper()
	prev := os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")
	// Restored on return; newResumeEnv's t.Setenv restores it at cleanup too.
	os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", callerParent) //nolint:errcheck
	defer os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", prev)   //nolint:errcheck
	r := pendRefusal{before: e.columns(t, id)}
	calls, moved := pendCalls(e.rec), pendMoved(t, id)
	_, r.err = e.resume(id)
	r.after = e.columns(t, id)
	r.calls, r.moved = pendCalls(e.rec)-calls, pendMoved(t, id)-moved
	return r
}

// TestResumeRefusesLaunchInProgress: a resume of any pending row gets the
// launch-in-progress refusal, makes no tmux call and writes nothing.
func TestResumeRefusesLaunchInProgress(t *testing.T) {
	// seededNoStart seeds a pending row whose launch_started_at is raw: nil
	// (NULL) or an int64 outside years 0 to 9999, which reads as absent (SR-5.5).
	seededNoStart := func(raw any) func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
		return func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			opt := apitest.WithNoLaunchStartedAt()
			if ms, ok := raw.(int64); ok {
				opt = apitest.WithLaunchStartedAt(ms)
			}
			id, err := apitest.SeedSpawn(e.dbPath, "", store.StatePending, t.TempDir(), "off", "sess-"+uuid.NewString()[:8], false, opt)
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			if got := e.columns(t, id).LaunchStartedAt; got != raw {
				t.Fatalf("seeded launch_started_at = %#v; want %#v", got, raw)
			}
			if err := apitest.SeedParentChild(e.dbPath, pendParent(t, e), id); err != nil {
				t.Fatalf("SeedParentChild: %v", err)
			}
			return id, nil, 0
		}
	}
	cases := []struct {
		name string
		// setup makes a pending row and returns its id, the refusal when it
		// ran one itself (via pendRefuse, caller parent p; nil: refuse after
		// setup) and the launch start in milliseconds it recorded (0: none).
		setup func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64)
	}{
		{"moved by another resume, before its create", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			r := e.seedResumable(t, store.StateEnded)
			var got pendRefusal
			e.store.afterMove(func() { got = pendRefuse(t, e, r.ID, p) })
			start := e.moveStart().UnixMilli()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("first Resume: %v", err)
			}
			return r.ID, &got, start
		}},
		{"moved by another resume, session up and not reported in", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			r := e.seedResumable(t, store.StateMissing)
			start := e.moveStart().UnixMilli()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("first Resume: %v", err)
			}
			if len(e.rec.Sessions(e.socket)) != 1 {
				t.Fatalf("sessions = %+v; want the first resume's", e.rec.Sessions(e.socket))
			}
			return r.ID, nil, start
		}},
		{"seeded pending with no launch start", seededNoStart(nil)},
		{"seeded pending at year 10000", seededNoStart(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())},
		{"seeded pending before year 0", seededNoStart(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() - 1)},
		{"plain spawn whose launch failed", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			id := uuid.NewString()
			if _, err := e.c.Spawn(api.SpawnParams{ClaudeInstanceID: id, CWD: t.TempDir()}); !errors.Is(err, api.ErrTmuxSessionCreate) {
				t.Fatalf("Spawn = %v; want ErrTmuxSessionCreate", err)
			}
			start, _ := e.columns(t, id).LaunchStartedAt.(int64)
			if start == 0 {
				t.Fatalf("plain spawn's row records no launch start")
			}
			return id, nil, start
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			callerParent := pendParent(t, e)
			id, ran, wantStart := tc.setup(t, e, callerParent)
			if ran == nil {
				r := pendRefuse(t, e, id, callerParent)
				ran = &r
			}
			got := *ran
			if got.before.State != store.StatePending {
				t.Fatalf("row before the refusal is %v; want pending", got.before.State)
			}
			p := apitest.LaunchInProgress{InstanceID: id}
			if wantStart != 0 {
				p.LaunchStart = time.UnixMilli(wantStart).UTC()
				if got.before.LaunchStartedAt != wantStart {
					t.Errorf("launch_started_at = %#v; want %d", got.before.LaunchStartedAt, wantStart)
				}
			}
			if !errors.Is(got.err, api.ErrSpawnNotResumable) {
				t.Fatalf("Resume = %v; want ErrSpawnNotResumable", got.err)
			}
			tok, _ := got.before.LaunchToken.(string)
			apitest.AssertDescription(t, got.err.Error(), apitest.DescResumeLaunchInProgress(p), tok, e.storeID)
			if got.calls != 0 || got.moved != 0 {
				t.Errorf("the refusal made %d tmux calls and %d move events; want none", got.calls, got.moved)
			}
			if !reflect.DeepEqual(got.after, got.before) {
				t.Errorf("row after the refusal = %+v; want unchanged %+v", got.after, got.before)
			}
		})
	}
}

// TestResumeRowDeletedBeforeMove: a row deleted between examination and move
// gives ErrSpawnNotFound, with no create and no move event.
func TestResumeRowDeletedBeforeMove(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	e.store.afterGet(func() {
		if err := e.st.DeleteSpawn(r.ID); err != nil {
			t.Fatalf("DeleteSpawn: %v", err)
		}
	})
	if _, err := e.resume(r.ID); !errors.Is(err, api.ErrSpawnNotFound) {
		t.Fatalf("Resume = %v; want ErrSpawnNotFound", err)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
		t.Errorf("creates = %d; want none", n)
	}
	if n := pendMoved(t, r.ID); n != 0 {
		t.Errorf("ad.resume.moved_to_pending lines = %d; want none", n)
	}
	if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns = %v; want the row absent", err)
	}
}

// TestResumeFromArchivedHistoryMovesStoredRow: a resume that falls back to an
// archived session still applies its move, keeping the stored session id.
func TestResumeFromArchivedHistoryMovesStoredRow(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	if err := os.Remove(r.JSONLPath); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	if _, err := e.resume(r.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) != 1 {
		t.Fatalf("creates = %d; want 1", len(creates))
	}
	if i := slices.Index(creates[0].Command, "--resume"); i < 0 || i+1 >= len(creates[0].Command) || creates[0].Command[i+1] != r.HistorySessionID {
		t.Errorf("argv = %q; want --resume %s", creates[0].Command, r.HistorySessionID)
	}
	if cols := e.columns(t, r.ID); cols.State != store.StatePending || cols.ClaudeSessionID != r.SessionID {
		t.Errorf("row {state %v, session %v}; want pending, %s", cols.State, cols.ClaudeSessionID, r.SessionID)
	}
	lines := pendTrail(t, "ad.resume.moved_to_pending", r.ID)
	if len(lines) != 1 {
		t.Fatalf("ad.resume.moved_to_pending lines = %d; want 1", len(lines))
	}
	assertAPITrailStr(t, lines[0], "claude_session_id", r.SessionID)
}

package api_test

// resume_pending_test.go covers resume's move to pending (SR-8.3, SR-8.4,
// SR-8.6, SR-14, SR-20.6; AC-RES-08, AC-RES-09, AC-RES-11, AC-RES-18): the
// move's columns, its visibility, SessionStart after it, the launch-in-progress
// refusal and two resumes in each order, on the shared fixture in
// resume_fixture_test.go against a real store.

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/hook"
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

// pendSessionStart delivers a SessionStart for id through the hook handler on
// the real store, reporting transcript (whose basename is the session id).
func pendSessionStart(t *testing.T, e *resumeEnv, id, transcript string) {
	t.Helper()
	payload := `{"hook_event_name":"SessionStart","transcript_path":"` + transcript + `"}`
	env := func(k string) string {
		if k == "AGENT_DIRECTOR_INSTANCE_ID" {
			return id
		}
		return ""
	}
	if err := hook.Handle(context.Background(), strings.NewReader(payload), io.Discard, e.st,
		hook.HandleConfig{Env: env}, nil); err != nil {
		t.Fatalf("hook.Handle(SessionStart): %v", err)
	}
}

// pendRowNullOr returns nil for "" and s otherwise (a column's raw NULL or text).
func pendRowNullOr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestResumeMoveToPendingColumns: the move clears the old process, liveness and
// tmux identity, records the clock's launch start, a new token, the create's
// socket and the caller's parent, keeps the session columns, and emits once.
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
			wantStart := e.clock.Now().UnixMilli()
			var moved apitest.SpawnColumns
			e.store.afterMove(func() { moved = e.columns(t, r.ID) })

			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("Resume: %v", err)
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
// show pending with the resume time as the launch start; get keeps the session
// id and prior_sessions.
func TestResumePendingVisibleOnEverySurface(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded, apitest.WithStartedAt(pendStartedAt))
	want := time.UnixMilli(e.clock.Now().UnixMilli()).UTC()
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

// TestResumeSessionStartAfterMoveTurnsRowWaiting: SessionStart through the hook
// path after the move makes the row waiting with no launch start, prior_state pending.
func TestResumeSessionStartAfterMoveTurnsRowWaiting(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateMissing)
	if _, err := e.resume(r.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	seen := len(pendTrail(t, "ad.spawn.state_transition", r.ID))
	pendSessionStart(t, e, r.ID, r.JSONLPath)

	cols := e.columns(t, r.ID)
	if cols.State != store.StateWaiting || cols.LaunchStartedAt != nil {
		t.Errorf("row {state %v, launch_started_at %#v}; want waiting, NULL", cols.State, cols.LaunchStartedAt)
	}
	lines := pendTrail(t, "ad.spawn.state_transition", r.ID)[seen:]
	if len(lines) != 1 {
		t.Fatalf("ad.spawn.state_transition lines from SessionStart = %d; want 1", len(lines))
	}
	assertAPITrailStr(t, lines[0], "prior_state", store.StatePending)
	assertAPITrailStr(t, lines[0], "new_state", store.StateWaiting)
	assertAPITrailStr(t, lines[0], "triggering_event_name", "SessionStart")
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
			start := e.clock.Now().UnixMilli()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("first Resume: %v", err)
			}
			return r.ID, &got, start
		}},
		{"moved by another resume, session up and not reported in", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			r := e.seedResumable(t, store.StateMissing)
			start := e.clock.Now().UnixMilli()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("first Resume: %v", err)
			}
			if len(e.rec.Sessions(e.socket)) != 1 {
				t.Fatalf("sessions = %+v; want the first resume's", e.rec.Sessions(e.socket))
			}
			return r.ID, nil, start
		}},
		{"seeded pending with no launch start", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			id, err := apitest.SeedSpawn(e.dbPath, "", store.StatePending, t.TempDir(), "off", "sess-"+uuid.NewString()[:8], false,
				apitest.WithNoLaunchStartedAt())
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			if err := apitest.SeedParentChild(e.dbPath, pendParent(t, e), id); err != nil {
				t.Fatalf("SeedParentChild: %v", err)
			}
			return id, nil, 0
		}},
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

// TestResumeLoserExaminedBeforeWinnersMove: a resume whose examination preceded
// another's move loses at its conditional write and writes nothing.
func TestResumeLoserExaminedBeforeWinnersMove(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	winnerParent, loserParent := pendParent(t, e), pendParent(t, e)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", loserParent)
	var winErr error
	var afterWinner apitest.SpawnColumns
	e.store.afterGet(func() {
		os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", winnerParent) //nolint:errcheck
		_, winErr = e.resume(r.ID)
		os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", loserParent) //nolint:errcheck
		afterWinner = e.columns(t, r.ID)
	})

	_, err := e.resume(r.ID)
	if winErr != nil {
		t.Fatalf("winner Resume: %v", winErr)
	}
	if !errors.Is(err, api.ErrSpawnNotResumable) {
		t.Fatalf("loser Resume = %v; want ErrSpawnNotResumable", err)
	}
	tok, _ := afterWinner.LaunchToken.(string)
	apitest.AssertDescription(t, err.Error(), apitest.DescResumeLostRace(), tok, e.storeID)
	pendAssertLoserWroteNothing(t, e, r.ID, afterWinner, winnerParent)
}

// TestResumeLoserExaminedAfterWinnersMove: a resume that examines the row after
// another's move, while pending and once its agent reported in, is refused by
// its state guard and writes nothing.
func TestResumeLoserExaminedAfterWinnersMove(t *testing.T) {
	for _, reportedIn := range []bool{false, true} {
		name := map[bool]string{false: "winner pending", true: "winner reported in"}[reportedIn]
		t.Run(name, func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateMissing)
			winnerParent, loserParent := pendParent(t, e), pendParent(t, e)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", winnerParent)
			start := time.UnixMilli(e.clock.Now().UnixMilli()).UTC()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("winner Resume: %v", err)
			}
			if reportedIn {
				pendSessionStart(t, e, r.ID, r.JSONLPath)
			}
			got := pendRefuse(t, e, r.ID, loserParent)
			if !errors.Is(got.err, api.ErrSpawnNotResumable) {
				t.Fatalf("loser Resume = %v; want ErrSpawnNotResumable", got.err)
			}
			if !reportedIn {
				tok, _ := got.before.LaunchToken.(string)
				apitest.AssertDescription(t, got.err.Error(), apitest.DescResumeLaunchInProgress(apitest.LaunchInProgress{
					InstanceID: r.ID, LaunchStart: start}), tok, e.storeID)
			} else if got.before.State != store.StateWaiting {
				t.Fatalf("row after SessionStart is %v; want waiting", got.before.State)
			}
			if got.calls != 0 {
				t.Errorf("loser made %d tmux calls; want none", got.calls)
			}
			pendAssertLoserWroteNothing(t, e, r.ID, got.before, winnerParent)
		})
	}
}

// pendAssertLoserWroteNothing checks that the row is want with the winner's
// parent, one create call was made and one move event emitted.
func pendAssertLoserWroteNothing(t *testing.T, e *resumeEnv, id string, want apitest.SpawnColumns, winnerParent string) {
	t.Helper()
	if got := e.columns(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("row after the loser = %+v; want the winner's %+v", got, want)
	}
	if want.ParentID != winnerParent {
		t.Errorf("parent_id = %#v; want the winner's %q", want.ParentID, winnerParent)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
		t.Errorf("creates = %d; want the winner's one", n)
	}
	if n := pendMoved(t, id); n != 1 {
		t.Errorf("ad.resume.moved_to_pending lines = %d; want the winner's one", n)
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

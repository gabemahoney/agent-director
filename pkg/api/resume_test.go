package api_test

// resume_test.go covers resume's guards and its launch (SR-8, SR-20.6): the
// move to pending and its outcomes, the parent id, the restore after a failed
// create and a name held at the pre-launch lookup, through recordingResumeStore over the shared
// resume fixture (resume_fixture_test.go) and its tmuxfix.Recorder. Transcript
// resolution is in resume_transcript_test.go.

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// recordingResumeStore wraps the fixture's store, recording each history read's
// life and each move's and restore's arguments and result. historyErr fails the
// history read; a non-zero moveRes answers the move without writing.
type recordingResumeStore struct {
	*hookedResumeStore
	historyErr   error
	moveRes      api.CondResult
	historyLives []int64
	moves        []resumeMoveCall
	restores     []resumeRestoreCall
}

// resumeMoveCall is one MoveToPending call: its arguments, result and version.
type resumeMoveCall struct {
	id                    string
	examined              api.RowSnapshot
	launchStartMs         int64
	token, socket, parent string
	res                   api.CondResult
	version               int64
}

// resumeRestoreCall is one RestoreAfterFailedResume call and its result.
type resumeRestoreCall struct {
	id           string
	movedVersion int64
	prior        api.ResumePrior
	res          api.CondResult
}

func (r *recordingResumeStore) ListSessionHistory(id string, life int64) ([]api.SessionHistoryEntry, error) {
	r.historyLives = append(r.historyLives, life)
	if r.historyErr != nil {
		return nil, r.historyErr
	}
	return r.hookedResumeStore.ListSessionHistory(id, life)
}

func (r *recordingResumeStore) MoveToPending(id string, examined api.RowSnapshot, startMs int64, token, socket, parent string) (api.CondResult, int64, error) {
	c := resumeMoveCall{id: id, examined: examined, launchStartMs: startMs, token: token, socket: socket, parent: parent, res: r.moveRes}
	var err error
	if c.res == 0 {
		c.res, c.version, err = r.hookedResumeStore.MoveToPending(id, examined, startMs, token, socket, parent)
	}
	r.moves = append(r.moves, c)
	return c.res, c.version, err
}

func (r *recordingResumeStore) RestoreAfterFailedResume(id string, movedVersion int64, prior api.ResumePrior) (api.CondResult, error) {
	res, err := r.hookedResumeStore.RestoreAfterFailedResume(id, movedVersion, prior)
	r.restores = append(r.restores, resumeRestoreCall{id: id, movedVersion: movedVersion, prior: prior, res: res})
	return res, err
}

// resume runs resume on id with r as its store and the rest of e.
func (r *recordingResumeStore) resume(e *resumeEnv, id string) (api.ResumeResult, error) {
	return api.Resume(r, e.rec, e.pc, e.cfg, e.storeID, e.clock.Now, e.lg, api.ResumeParams{ClaudeInstanceID: id})
}

// newRecordingEnv returns a fresh resume fixture and a recording store over it.
func newRecordingEnv(t *testing.T) (*resumeEnv, *recordingResumeStore) {
	e := newResumeEnv(t)
	return e, &recordingResumeStore{hookedResumeStore: e.store}
}

// seedBareResumeRow seeds an ended row in cwd on e.socket, life resumableLife,
// with a NULL jsonl_path, no history and no transcript; opts are applied last.
func seedBareResumeRow(t *testing.T, e *resumeEnv, cwd, sessionID string, opts ...apitest.SpawnOption) string {
	t.Helper()
	id := "resume-" + uuid.NewString()[:8]
	opts = append([]apitest.SpawnOption{apitest.WithTmuxSocket(e.socket), apitest.WithLifeNumber(resumableLife),
		apitest.WithRawClaudeArgs(`["--model","opus"]`)}, opts...)
	if _, err := apitest.SeedSpawn(e.dbPath, id, store.StateEnded, cwd, "off", sessionID, false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	return id
}

// resumeFromCaller seeds a row callerID and makes it the caller, so the move's
// parent id names a row.
func resumeFromCaller(t *testing.T, e *resumeEnv, callerID string) {
	t.Helper()
	if _, err := apitest.SeedSpawn(e.dbPath, callerID, store.StateWaiting, t.TempDir(), "off", "", false); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", callerID, err)
	}
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", callerID)
}

// columnSnapshot is the row snapshot (SR-5.3) of the raw columns cols.
func columnSnapshot(cols apitest.SpawnColumns) api.RowSnapshot {
	str := func(v any) string { s, _ := v.(string); return s }
	num := func(v any) int64 { n, _ := v.(int64); return n }
	return api.RowSnapshot{RowVersion: num(cols.RowVersion), StartedAt: str(cols.StartedAt),
		ClaudeSessionID: str(cols.ClaudeSessionID), PID: int(num(cols.PID)),
		ProcStarttime: str(cols.ProcStarttime), TmuxSessionName: str(cols.TmuxSessionName)}
}

// assertNotMoved asserts no move and no restore and, when before is non-nil,
// that id's row still equals *before.
func assertNotMoved(t *testing.T, e *resumeEnv, rs *recordingResumeStore, id string, before *apitest.SpawnColumns) {
	t.Helper()
	if len(rs.moves) != 0 || len(rs.restores) != 0 {
		t.Errorf("moves %d, restores %d; want none", len(rs.moves), len(rs.restores))
	}
	if before != nil {
		if after := e.columns(t, id); !reflect.DeepEqual(after, *before) {
			t.Errorf("row changed:\n before %+v\n after  %+v", *before, after)
		}
	}
}

// assertNothingLaunched is assertNotMoved plus no tmux call at all, the lookup
// included: a guard refuses before it (SR-3.11, SR-8.1).
func assertNothingLaunched(t *testing.T, e *resumeEnv, rs *recordingResumeStore, id string, before *apitest.SpawnColumns) {
	t.Helper()
	assertNotMoved(t, e, rs, id, before)
	assertNoTmuxCalls(t, e.rec)
}

// assertResumed asserts one move carrying snap, no restore, and exactly one
// lookup then one create, both on the move's socket, the create running
// `claude --resume session`; it returns the move and the create.
func assertResumed(t *testing.T, e *resumeEnv, rs *recordingResumeStore, snap api.RowSnapshot, session string) (resumeMoveCall, tmuxfix.SocketCall) {
	t.Helper()
	calls := e.rec.SocketCalls()
	if kinds := callKinds(e.rec); len(rs.moves) != 1 || len(rs.restores) != 0 ||
		!reflect.DeepEqual(kinds, []tmux.Call{tmux.CallLookup, tmux.CallCreate}) {
		t.Fatalf("moves %d, restores %d, tmux calls %v; want 1, 0, [lookup create]", len(rs.moves), len(rs.restores), kinds)
	}
	mv, cr := rs.moves[0], calls[1]
	if mv.examined != snap {
		t.Errorf("move examined %+v; want the stored row's snapshot %+v", mv.examined, snap)
	}
	if calls[0].Socket != mv.socket || cr.Socket != mv.socket {
		t.Errorf("lookup socket %q, create socket %q; want the move's %q", calls[0].Socket, cr.Socket, mv.socket)
	}
	if len(cr.Command) < 3 || cr.Command[0] != "claude" || cr.Command[1] != "--resume" || cr.Command[2] != session {
		t.Errorf("command = %v; want `claude --resume %s ...`", cr.Command, session)
	}
	return mv, cr
}

// TestResumeGuardsRefuseWithoutMove: an unknown id, a live or pending row and a
// row with no session id are refused with no move, no tmux call and no write.
func TestResumeGuardsRefuseWithoutMove(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		state     string // "" = no row
		noSession bool
		want      error
	}{
		{"unknown id", "", false, api.ErrSpawnNotFound},
		{store.StateWaiting, store.StateWaiting, false, api.ErrSpawnNotResumable},
		{store.StateWorking, store.StateWorking, false, api.ErrSpawnNotResumable},
		{store.StateAskUser, store.StateAskUser, false, api.ErrSpawnNotResumable},
		{store.StateCheckPermission, store.StateCheckPermission, false, api.ErrSpawnNotResumable},
		{store.StatePending, store.StatePending, false, api.ErrSpawnNotResumable},
		{"no session id", store.StateEnded, true, api.ErrNoSessionId},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, rs := newRecordingEnv(t)
			id := "resume-absent"
			switch {
			case tc.noSession:
				id = seedBareResumeRow(t, e, t.TempDir(), "")
			case tc.state != "":
				id = e.seedResumable(t, tc.state).ID
			}
			var before *apitest.SpawnColumns
			if tc.state != "" {
				cols := e.columns(t, id)
				before = &cols
			}
			if _, err := rs.resume(e, id); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			assertNothingLaunched(t, e, rs, id, before)
		})
	}
}

// TestResumeIdlessStaleTmuxSessionReturnsErrTmuxSessionConflict: an unlabelled
// session holding the row's name refuses at the lookup; nothing else is called.
func TestResumeIdlessStaleTmuxSessionReturnsErrTmuxSessionConflict(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	e.rec.StartServer(r.Identity.Socket, tmuxfix.Server{PID: resumableServerPID, Start: resumableSrvStart}).
		SeedSessions(r.Identity.Socket, tmuxfix.SeedSession{Name: r.Name})
	held := e.rec.Sessions(r.Identity.Socket)
	_, err := rs.resume(e, r.ID)
	if !errors.Is(err, api.ErrTmuxSessionConflict) {
		t.Fatalf("err = %v; want ErrTmuxSessionConflict", err)
	}
	assertOnlyCatalogued(t, err, "ErrTmuxSessionConflict")
	apitest.AssertDescription(t, err.Error(), apitest.DescHeldNoValidID(apitest.HeldName{Name: r.Name,
		SessionID: held[0].ID, BeforeLaunch: true}), r.Identity.Token, e.storeID)
	assertNotMoved(t, e, rs, r.ID, &r.Before)
	if calls := e.rec.SocketCalls(); len(calls) != 1 || calls[0].Call != tmux.CallLookup || calls[0].Socket != r.Identity.Socket {
		t.Errorf("socket tmux calls = %+v; want one lookup on %s", calls, r.Identity.Socket)
	}
	if n := len(e.rec.Calls()); n != 0 {
		t.Errorf("%d name-based tmux calls; want none", n)
	}
	if got := e.rec.Sessions(r.Identity.Socket); !reflect.DeepEqual(got, held) {
		t.Errorf("sessions = %+v; want the holder untouched %+v", got, held)
	}
}

// TestResumeMoveOutcomes: a move that finds the row changed or removed, or
// fails in the store, returns its error after the lookup alone: no create,
// no restore, no write.
func TestResumeMoveOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		res      api.CondResult // 0: the move fails with a store error
		wantName string
		want     error // nil: ErrInternal, no sentinel
		desc     apitest.DescCase
	}{
		{"row changed", api.CondChanged, "ErrSpawnNotResumable", api.ErrSpawnNotResumable, apitest.DescResumeLostRace()},
		{"row deleted", api.CondAbsent, "ErrSpawnNotFound", api.ErrSpawnNotFound, apitest.DescCase{Name: "ErrSpawnNotFound, removed before the move"}},
		{"store error", 0, "ErrInternal", nil, apitest.DescResumeMoveStoreError()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, rs := newRecordingEnv(t)
			r := e.seedResumable(t, store.StateEnded)
			rs.moveRes = tc.res
			if tc.res == 0 {
				e.store.failMove(nil)
			}
			_, err := rs.resume(e, r.ID)
			name, desc := errnames.Classify(err)
			if name != tc.wantName || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v (%s); want %s", err, name, tc.wantName)
			}
			if len(rs.moves) != 1 {
				t.Fatalf("moves = %d; want 1", len(rs.moves))
			}
			apitest.AssertDescription(t, desc, tc.desc, rs.moves[0].token, r.Identity.Token, e.storeID)
			if kinds := callKinds(e.rec); len(rs.restores) != 0 || !reflect.DeepEqual(kinds, []tmux.Call{tmux.CallLookup}) {
				t.Errorf("restores %d, tmux calls %v; want none, [lookup]", len(rs.restores), kinds)
			}
			if after := e.columns(t, r.ID); !reflect.DeepEqual(after, r.Before) {
				t.Errorf("row changed:\n before %+v\n after  %+v", r.Before, after)
			}
		})
	}
}

// TestResumeHappyPathLaunchesAndUpdatesParent: one move (examined snapshot, a
// new token, the create's socket, parent caller-id) and one labelled create.
func TestResumeHappyPathLaunchesAndUpdatesParent(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	e, rs := newRecordingEnv(t)
	resumeFromCaller(t, e, "caller-id")
	r := e.seedResumable(t, store.StateEnded)
	start := e.moveStart()
	res, err := rs.resume(e, r.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.ClaudeInstanceID != r.ID {
		t.Errorf("ClaudeInstanceID = %q; want %q", res.ClaudeInstanceID, r.ID)
	}
	mv, cr := assertResumed(t, e, rs, columnSnapshot(r.Before), r.SessionID)
	if mv.id != r.ID || mv.parent != "caller-id" || mv.socket != r.Identity.Socket || mv.launchStartMs != start.UnixMilli() {
		t.Errorf("move = %+v; want id %s, parent caller-id, socket %s, launch start %d",
			mv, r.ID, r.Identity.Socket, start.UnixMilli())
	}
	if mv.token == "" || mv.token == r.Identity.Token || cr.Token != mv.token {
		t.Errorf("move token %q, create token %q, old token %q; want a new token on both", mv.token, cr.Token, r.Identity.Token)
	}
	if cr.Target != r.Name || cr.Cwd != r.CWD || cr.InstanceID != r.ID || cr.StoreID != e.storeID {
		t.Errorf("create = %+v; want name %s, cwd %s, id %s, store id %s", cr, r.Name, r.CWD, r.ID, e.storeID)
	}
	if cmd := cr.Command; len(cmd) != 7 || cmd[3] != "--settings" || cmd[5] != "--model" || cmd[6] != "opus" {
		t.Errorf("command = %v; want claude --resume <id> --settings <json> --model opus", cmd)
	}
	for k, v := range map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": r.ID, "AGENT_DIRECTOR_LABEL_PROJECT": "resume-fixture"} {
		if cr.Envs[k] != v {
			t.Errorf("env %s = %q; want %q", k, cr.Envs[k], v)
		}
	}
	if got := e.columns(t, r.ID).ParentID; got != "caller-id" {
		t.Errorf("stored parent_id = %v; want caller-id", got)
	}
}

// TestResumeFromBareShellSetsParentNull: with no caller instance id the move's
// parent id is empty and parent_id is stored NULL.
func TestResumeFromBareShellSetsParentNull(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	if _, err := rs.resume(e, r.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if mv, _ := assertResumed(t, e, rs, columnSnapshot(r.Before), r.SessionID); mv.parent != "" {
		t.Errorf("move parent = %q; want \"\"", mv.parent)
	}
	if got := e.columns(t, r.ID).ParentID; got != nil {
		t.Errorf("stored parent_id = %v; want NULL", got)
	}
}

// TestResumeMissingStateAlsoResumes: a missing row resumes as an ended one does.
func TestResumeMissingStateAlsoResumes(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	r := e.seedResumable(t, store.StateMissing)
	if _, err := rs.resume(e, r.ID); err != nil {
		t.Fatalf("Resume from missing: %v", err)
	}
	assertResumed(t, e, rs, columnSnapshot(r.Before), r.SessionID)
}

// TestResumeLaunchFailureRestoresRow: a failed create gives the move, then one
// applied restore of the prior values with the move's version (parent kept).
func TestResumeLaunchFailureRestoresRow(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	e, rs := newRecordingEnv(t)
	resumeFromCaller(t, e, "caller-id")
	r := e.seedResumable(t, store.StateEnded)
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallCreate)
	_, err := rs.resume(e, r.ID)
	if !errors.Is(err, api.ErrTmuxSessionCreate) {
		t.Fatalf("err = %v; want ErrTmuxSessionCreate", err)
	}
	if len(rs.moves) != 1 || len(rs.restores) != 1 {
		t.Fatalf("moves %d, restores %d; want 1, 1", len(rs.moves), len(rs.restores))
	}
	mv, rst := rs.moves[0], rs.restores[0]
	wantPrior := api.ResumePrior{State: store.StateEnded, EndedAtText: resumableEndedAt, PID: resumableClaudePID,
		ProcStarttime: apitest.LinuxProcStarttime, LivenessUnverifiedSince: resumableUnverSnc,
		LivenessNote: resumableNote, Identity: r.Identity}
	if rst.id != r.ID || rst.movedVersion != mv.version || rst.prior != wantPrior || rst.res != api.CondApplied {
		t.Errorf("restore = %+v; want id %s, moved version %d, prior %+v, applied", rst, r.ID, mv.version, wantPrior)
	}
	apitest.AssertDescription(t, err.Error(), apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: r.Name}).
		AfterResumeRestore(apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded}),
		mv.token, r.Identity.Token, e.storeID)
	after := e.columns(t, r.ID)
	if after.ParentID != "caller-id" {
		t.Errorf("stored parent_id = %v; want caller-id (the move's, kept)", after.ParentID)
	}
	after.ParentID, after.RowVersion = r.Before.ParentID, r.Before.RowVersion
	if !reflect.DeepEqual(after, r.Before) {
		t.Errorf("row not restored:\n before %+v\n after  %+v", r.Before, after)
	}
}

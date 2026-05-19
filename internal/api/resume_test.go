package api_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/spawn"
	"github.com/gabemahoney/claude-director/internal/store"
)

// recordingResumeStore captures every store call resume makes so the
// tests can pin both the precondition guards (which short-circuit
// before any DB write) and the parent_id mutation on the happy path.
type recordingResumeStore struct {
	row              store.Spawn
	getErr           error
	setParentErr     error
	setParentArgs    [2]string
	setParentCalls   int
}

func (r *recordingResumeStore) GetSpawn(_ string) (store.Spawn, error) {
	if r.getErr != nil {
		return store.Spawn{}, r.getErr
	}
	return r.row, nil
}

func (r *recordingResumeStore) SetParentID(id, parent string) error {
	r.setParentCalls++
	r.setParentArgs = [2]string{id, parent}
	return r.setParentErr
}

// recordingResumeTmux is the tmux fake. hasSessionResult / hasSessionErr
// drive the collision-check branch; newSessionCalls records the launch
// argv so happy-path tests can verify `claude --resume <session_id>`
// is composed correctly.
type recordingResumeTmux struct {
	hasSessionResult bool
	hasSessionErr    error
	newSessionErr    error
	newSessionCalls  int
	gotName          string
	gotCwd           string
	gotCommand       []string
	gotEnvs          map[string]string
}

func (r *recordingResumeTmux) HasSession(name string) (bool, error) {
	return r.hasSessionResult, r.hasSessionErr
}

func (r *recordingResumeTmux) NewSession(name, cwd string, envs map[string]string, command []string) error {
	r.newSessionCalls++
	r.gotName = name
	r.gotCwd = cwd
	r.gotEnvs = envs
	r.gotCommand = command
	return r.newSessionErr
}

// seedJsonl writes a minimal placeholder JSONL file at the path
// JsonlPath(cwd, sessionID) resolves to. Returns the resolved path
// so tests that want to delete it can.
func seedJsonl(t *testing.T, cwd, sessionID string) string {
	t.Helper()
	p, err := spawn.JsonlPath(cwd, sessionID)
	if err != nil {
		t.Fatalf("JsonlPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir jsonl parent: %v", err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return p
}

func baseRow() store.Spawn {
	return store.Spawn{
		ClaudeInstanceID: "id-r-1",
		State:            store.StateEnded,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-r-1",
		RelayMode:        "off",
		ClaudeSessionID:  "session-uuid-1",
		ClaudeArgs:       []string{"--model", "opus"},
		Labels:           map[string]string{"project": "foo"},
	}
}

func TestResumeUnknownIdReturnsErrSpawnNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := &recordingResumeStore{getErr: store.ErrSpawnNotFound}
	tm := &recordingResumeTmux{}
	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "absent"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
	if tm.newSessionCalls != 0 {
		t.Errorf("NewSession called for absent id")
	}
	if st.setParentCalls != 0 {
		t.Errorf("SetParentID called before guard passed")
	}
}

func TestResumeLiveStateReturnsNotResumable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, state := range []string{
		store.StatePending, store.StateWaiting, store.StateWorking,
		store.StateAskUser, store.StateCheckPermission,
	} {
		t.Run(state, func(t *testing.T) {
			row := baseRow()
			row.State = state
			st := &recordingResumeStore{row: row}
			tm := &recordingResumeTmux{}
			_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id"})
			if !errors.Is(err, api.ErrSpawnNotResumable) {
				t.Fatalf("state=%s: err = %v; want ErrSpawnNotResumable", state, err)
			}
			if tm.newSessionCalls != 0 || st.setParentCalls != 0 {
				t.Errorf("side effects on guard error: tmux=%d parent=%d",
					tm.newSessionCalls, st.setParentCalls)
			}
		})
	}
}

func TestResumeMissingSessionIdReturnsNoSessionId(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	row := baseRow()
	row.ClaudeSessionID = ""
	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}
	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id"})
	if !errors.Is(err, api.ErrNoSessionId) {
		t.Fatalf("err = %v; want ErrNoSessionId", err)
	}
	if tm.newSessionCalls != 0 || st.setParentCalls != 0 {
		t.Errorf("side effects on guard error")
	}
}

func TestResumeJsonlMissingReturnsErrJsonlMissing(t *testing.T) {
	// Do NOT seed the JSONL — the row says it should exist but the
	// disk says otherwise. Pre-flight Stat fails.
	t.Setenv("HOME", t.TempDir())
	st := &recordingResumeStore{row: baseRow()}
	tm := &recordingResumeTmux{}
	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id"})
	if !errors.Is(err, api.ErrJsonlMissing) {
		t.Fatalf("err = %v; want ErrJsonlMissing", err)
	}
	if tm.newSessionCalls != 0 || st.setParentCalls != 0 {
		t.Errorf("side effects on guard error")
	}
}

func TestResumeStaleTmuxSessionReturnsErrTmuxSessionCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	row := baseRow()
	seedJsonl(t, row.CWD, row.ClaudeSessionID)
	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{hasSessionResult: true}

	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id"})
	if err == nil || !errors.Is(err, errTmuxSentinel{}) && !containsStr(err.Error(), "tmux session") {
		// Match either the typed sentinel from internal/tmux or the
		// readable message. The CLI's errCatalog maps tmux.ErrTmuxSessionCreate
		// to the canonical name; what matters here is that resume
		// surfaced the collision rather than calling NewSession.
		t.Fatalf("err = %v; want tmux session collision", err)
	}
	if tm.newSessionCalls != 0 || st.setParentCalls != 0 {
		t.Errorf("side effects on collision: tmux=%d parent=%d",
			tm.newSessionCalls, st.setParentCalls)
	}
}

// errTmuxSentinel is a stand-in so the test compiles without importing
// internal/tmux directly (the errors.Is check below uses the typed
// sentinel — but we also accept the message form as a fallback).
type errTmuxSentinel struct{}

func (errTmuxSentinel) Error() string { return "tmux session create" }
func (errTmuxSentinel) Is(target error) bool {
	return target != nil && target.Error() == "tmux: new-session failed"
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestResumeHappyPathLaunchesAndUpdatesParent(t *testing.T) {
	// Set caller env so parent_id derivation has something to bind.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_DIRECTOR_INSTANCE_ID", "caller-id")

	row := baseRow()
	seedJsonl(t, row.CWD, row.ClaudeSessionID)
	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}

	res, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.ClaudeInstanceID != "id-r-1" {
		t.Errorf("ClaudeInstanceID = %q; want id-r-1", res.ClaudeInstanceID)
	}

	if tm.newSessionCalls != 1 {
		t.Fatalf("NewSession called %d times; want 1", tm.newSessionCalls)
	}
	if tm.gotName != "cd-r-1" {
		t.Errorf("session name = %q; want cd-r-1", tm.gotName)
	}
	if tm.gotCwd != "/tmp" {
		t.Errorf("session cwd = %q; want /tmp", tm.gotCwd)
	}

	// Command must carry `claude --resume <session_id> --settings <json>`
	// followed by the user claude_args.
	if len(tm.gotCommand) < 5 {
		t.Fatalf("command too short: %v", tm.gotCommand)
	}
	if tm.gotCommand[0] != "claude" || tm.gotCommand[1] != "--resume" ||
		tm.gotCommand[2] != "session-uuid-1" || tm.gotCommand[3] != "--settings" {
		t.Errorf("command prefix wrong: %v", tm.gotCommand[:4])
	}
	// User claude_args from the row are appended after --settings <json>.
	tail := tm.gotCommand[len(tm.gotCommand)-2:]
	if tail[0] != "--model" || tail[1] != "opus" {
		t.Errorf("command tail missing user claude_args: %v", tail)
	}

	if tm.gotEnvs["CLAUDE_DIRECTOR_INSTANCE_ID"] != "id-r-1" {
		t.Errorf("env CLAUDE_DIRECTOR_INSTANCE_ID = %q; want id-r-1",
			tm.gotEnvs["CLAUDE_DIRECTOR_INSTANCE_ID"])
	}
	if tm.gotEnvs["CLAUDE_DIRECTOR_LABEL_PROJECT"] != "foo" {
		t.Errorf("label env lost on resume: %v", tm.gotEnvs)
	}

	// parent_id derivation: caller env had CLAUDE_DIRECTOR_INSTANCE_ID=caller-id.
	if st.setParentCalls != 1 || st.setParentArgs != [2]string{"id-r-1", "caller-id"} {
		t.Errorf("SetParentID calls=%d args=%v; want 1 [id-r-1 caller-id]",
			st.setParentCalls, st.setParentArgs)
	}
}

func TestResumeFromBareShellSetsParentNull(t *testing.T) {
	// No CLAUDE_DIRECTOR_INSTANCE_ID in caller env → SetParentID is
	// called with empty parent (the store writes NULL).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_DIRECTOR_INSTANCE_ID", "")

	row := baseRow()
	seedJsonl(t, row.CWD, row.ClaudeSessionID)
	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}

	if _, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if st.setParentArgs[1] != "" {
		t.Errorf("parent = %q; want \"\" (bare-shell resume nulls parent)", st.setParentArgs[1])
	}
}

func TestResumeMissingStateAlsoResumes(t *testing.T) {
	// Per Epic 9 AC #2: a row marked `missing` by find-missing resumes
	// the same way an `ended` row does.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_DIRECTOR_INSTANCE_ID", "")
	row := baseRow()
	row.State = store.StateMissing
	seedJsonl(t, row.CWD, row.ClaudeSessionID)
	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}

	if _, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"}); err != nil {
		t.Fatalf("Resume from missing: %v", err)
	}
	if tm.newSessionCalls != 1 {
		t.Errorf("NewSession not called on resume-from-missing")
	}
}

func TestResumeLaunchFailureLeavesRowUnchanged(t *testing.T) {
	// tmux.NewSession returns an error after the parent update. The
	// row's state and ended_at are NOT touched by the resume verb
	// itself — only the eventual SessionStart hook flips them. So a
	// caller seeing the error can retry without DB cleanup.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_DIRECTOR_INSTANCE_ID", "")
	row := baseRow()
	seedJsonl(t, row.CWD, row.ClaudeSessionID)
	st := &recordingResumeStore{row: row}
	launchErr := errors.New("tmux: simulated failure")
	tm := &recordingResumeTmux{newSessionErr: launchErr}

	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"})
	if !errors.Is(err, launchErr) {
		t.Fatalf("err = %v; want launch err to bubble", err)
	}
	// SetParentID DID run (it happens before launch) — that's
	// intentional. The next retry will overwrite it with the same
	// or a different parent.
	if st.setParentCalls != 1 {
		t.Errorf("SetParentID calls = %d; want 1 (resume sets parent before launch)", st.setParentCalls)
	}
}

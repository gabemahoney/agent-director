package api_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// recordingResumeStore captures every store call resume makes so the
// tests can pin both the precondition guards (which short-circuit
// before any DB write) and the parent_id mutation on the happy path.
type recordingResumeStore struct {
	row            store.Spawn
	getErr         error
	setParentErr   error
	setParentArgs  [2]string
	setParentCalls int
	// history is returned by ListSessionHistory (b.v2c AC6). Nil = no archived
	// sessions, which is the default existing tests rely on.
	history    []store.SessionHistoryEntry
	historyErr error
}

func (r *recordingResumeStore) GetSpawn(_ string) (store.Spawn, error) {
	if r.getErr != nil {
		return store.Spawn{}, r.getErr
	}
	return r.row, nil
}

func (r *recordingResumeStore) ListSessionHistory(_ string) ([]store.SessionHistoryEntry, error) {
	return r.history, r.historyErr
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
	// b.v2c AC2: ErrJsonlMissing now means "a path was recorded/composed and has
	// since rotted" — distinct from ErrJsonlNeverWritten. To land on
	// ErrJsonlMissing (not the never-written sentinel) the row must have HISTORY
	// to have lost: seed one archived session whose transcript is also gone. The
	// current session's fallback path and the archived path both stat-miss, so
	// the verb reports ErrJsonlMissing naming the computed current-session path.
	t.Setenv("HOME", t.TempDir())
	row := baseRow() // JSONLPath == "" → fall back to computed path
	st := &recordingResumeStore{
		row: row,
		history: []store.SessionHistoryEntry{
			{ClaudeSessionID: "prior-rotted", JSONLPath: "/nonexistent/prior-rotted.jsonl"},
		},
	}
	tm := &recordingResumeTmux{}
	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id"})
	if !errors.Is(err, api.ErrJsonlMissing) {
		t.Fatalf("err = %v; want ErrJsonlMissing", err)
	}
	// The message names the mode-appropriate (computed) path.
	computed, perr := spawn.JsonlPath(row.CWD, row.ClaudeSessionID)
	if perr != nil {
		t.Fatalf("compute expected path: %v", perr)
	}
	if !containsStr(err.Error(), computed) {
		t.Errorf("err %q does not name the computed path %q", err.Error(), computed)
	}
	if tm.newSessionCalls != 0 || st.setParentCalls != 0 {
		t.Errorf("side effects on guard error")
	}
}

// TestResumeNeverWrittenReturnsErrJsonlNeverWritten pins the b.v2c AC2 sentinel
// split from the resume side: a row with a session id but a NULL jsonl_path and
// NO archived session history has genuinely never produced a transcript (the
// freshly-restarted, un-messaged bot). Resume must return ErrJsonlNeverWritten,
// NOT ErrJsonlMissing, so an operator can tell "nothing was ever written" apart
// from "history existed but the file is gone".
//
// PRE-FIX (single sentinel) this shape returned ErrJsonlMissing; the errors.Is
// assertion below fails against the old code, making this a regression anchor.
func TestResumeNeverWrittenReturnsErrJsonlNeverWritten(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	row := baseRow() // JSONLPath == "" (NULL), no history seeded
	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}
	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id"})
	if !errors.Is(err, api.ErrJsonlNeverWritten) {
		t.Fatalf("err = %v; want ErrJsonlNeverWritten", err)
	}
	// It is NOT the rotted-path sentinel — the two must stay distinct.
	if errors.Is(err, api.ErrJsonlMissing) {
		t.Errorf("err also matches ErrJsonlMissing; the two sentinels must be distinct")
	}
	if tm.newSessionCalls != 0 || st.setParentCalls != 0 {
		t.Errorf("side effects on guard error")
	}
}

// TestResumeRecoversRotatedSessionFromHistory is the b.v2c AC6 REGRESSION test
// for bug mode (b) — the rotation case. When a session rotates (CSCB fleet
// restart), the prior session's transcript is archived into session_history. If
// the current session has no live transcript, resume must fall back to the most
// recent archived session whose transcript still exists on disk — recovering the
// orphaned history rather than abandoning it — and relaunch `claude --resume`
// against the RECOVERED session id.
//
// PRE-FIX there is no session_history and resume cannot see the prior
// transcript, so it errors out; POST-FIX resume finds the archived transcript,
// rotates the row's session id to it, and NewSession fires with --resume naming
// the archived id.
func TestResumeRecoversRotatedSessionFromHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

	// The archived (rotated-away) session's transcript still exists on disk.
	const priorSession = "prior-session-uuid"
	priorPath := filepath.Join(t.TempDir(), priorSession+".jsonl")
	if err := os.WriteFile(priorPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write prior transcript: %v", err)
	}

	// Current session has a NULL jsonl_path (the freshly-rotated, un-messaged
	// session) — guard that its computed fallback location does not exist so the
	// pass can only come from the history recovery.
	row := baseRow()
	computed, err := spawn.JsonlPath(row.CWD, row.ClaudeSessionID)
	if err != nil {
		t.Fatalf("compute current path: %v", err)
	}
	if _, serr := os.Stat(computed); !os.IsNotExist(serr) {
		t.Fatalf("current computed path %q unexpectedly exists", computed)
	}

	st := &recordingResumeStore{
		row: row,
		history: []store.SessionHistoryEntry{
			{ClaudeSessionID: priorSession, JSONLPath: priorPath},
		},
	}
	tm := &recordingResumeTmux{}

	if _, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"}); err != nil {
		t.Fatalf("Resume: %v; want recovery via session history", err)
	}
	if tm.newSessionCalls != 1 {
		t.Fatalf("NewSession called %d times; want 1 (resume recovered archived transcript)", tm.newSessionCalls)
	}
	// The relaunch must --resume the RECOVERED (archived) session id, not the
	// dead current one.
	if len(tm.gotCommand) < 3 || tm.gotCommand[1] != "--resume" || tm.gotCommand[2] != priorSession {
		t.Errorf("command = %v; want `claude --resume %s ...`", tm.gotCommand, priorSession)
	}
}

// TestResumeRecoversHistoryEntryWithEmptyPathViaConfigDir covers the resume
// recovery branch where an archived history entry has an EMPTY JSONLPath (a
// legacy/NULL-path rotation record): resume recomputes the transcript location
// via the CLAUDE_CONFIG_DIR / default slug rule and recovers it when a file
// exists there. The transcript is planted at the recomputed slug location under
// a custom CLAUDE_CONFIG_DIR; the current session's own fallback location is
// guarded absent so a pass can only come from the history-recompute branch.
func TestResumeRecoversHistoryEntryWithEmptyPathViaConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

	// A custom CLAUDE_CONFIG_DIR the row carries; the recomputed history path
	// must resolve under it.
	cfgDir := filepath.Join(t.TempDir(), "custom-claude-config")

	row := baseRow()
	row.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}

	// Guard the premise: the CURRENT session's fallback location (under cfgDir)
	// must NOT exist, so a pass cannot come from the current-session fallback.
	currentFallback, err := spawn.JsonlPathIn(cfgDir, row.CWD, row.ClaudeSessionID)
	if err != nil {
		t.Fatalf("compute current fallback: %v", err)
	}
	if _, serr := os.Stat(currentFallback); !os.IsNotExist(serr) {
		t.Fatalf("current fallback %q unexpectedly exists (stat err=%v)", currentFallback, serr)
	}

	// The archived session has an EMPTY recorded path; plant its transcript at
	// the recomputed slug location under cfgDir so resume must recompose it.
	const priorSession = "prior-session-emptypath"
	planted := apitest.SeedJsonlUnder(t, cfgDir, row.CWD, priorSession)

	st := &recordingResumeStore{
		row: row,
		history: []store.SessionHistoryEntry{
			{ClaudeSessionID: priorSession, JSONLPath: ""},
		},
	}
	tm := &recordingResumeTmux{}

	if _, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"}); err != nil {
		t.Fatalf("Resume: %v; want recovery via recomputed history path %q", err, planted)
	}
	if tm.newSessionCalls != 1 {
		t.Fatalf("NewSession called %d times; want 1 (resume recovered recomputed archived transcript)", tm.newSessionCalls)
	}
	// The relaunch must --resume the recovered archived session id.
	if len(tm.gotCommand) < 3 || tm.gotCommand[1] != "--resume" || tm.gotCommand[2] != priorSession {
		t.Errorf("command = %v; want `claude --resume %s ...`", tm.gotCommand, priorSession)
	}
}

// TestResumeHistoryWalkFallsBackToRecomputedPathForNewerRottedEntry is the
// b.5jm/1 (AC2) REGRESSION test. It seeds two archived history entries, newest
// first:
//   - NEWER: a non-empty recorded jsonl_path that has ROTTED (does not exist),
//     but whose recomputed CLAUDE_CONFIG_DIR-aware path DOES exist on disk.
//   - OLDER: an intact recorded path that exists.
//
// Resume must select the NEWER session — mirroring the persisted→fallback
// two-step the current session already gets. PRE-FIX the walk only recomputed a
// fallback for an EMPTY recorded path, so a non-empty-but-rotted newer entry was
// skipped outright and the OLDER entry won, silently reattaching resume to older
// history. POST-FIX the newer entry's recomputed path is stat'd and wins.
func TestResumeHistoryWalkFallsBackToRecomputedPathForNewerRottedEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

	cfgDir := filepath.Join(t.TempDir(), "custom-claude-config")

	row := baseRow()
	row.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}

	// Guard the premise: the CURRENT session's own fallback must NOT exist, so a
	// pass can only come from a history entry.
	currentFallback, err := spawn.JsonlPathIn(cfgDir, row.CWD, row.ClaudeSessionID)
	if err != nil {
		t.Fatalf("compute current fallback: %v", err)
	}
	if _, serr := os.Stat(currentFallback); !os.IsNotExist(serr) {
		t.Fatalf("current fallback %q unexpectedly exists", currentFallback)
	}

	// NEWER entry: recorded path is rotted, but its recomputed slug path exists.
	const newerSession = "newer-rotted-session"
	newerRecomputed := apitest.SeedJsonlUnder(t, cfgDir, row.CWD, newerSession)
	newerRotted := filepath.Join(t.TempDir(), "rotted", newerSession+".jsonl")
	if _, serr := os.Stat(newerRotted); !os.IsNotExist(serr) {
		t.Fatalf("newer rotted path %q unexpectedly exists", newerRotted)
	}
	if newerRotted == newerRecomputed {
		t.Fatalf("test setup: rotted and recomputed paths collide (%q)", newerRotted)
	}

	// OLDER entry: an intact recorded path that also exists on disk.
	const olderSession = "older-intact-session"
	olderPath := filepath.Join(t.TempDir(), olderSession+".jsonl")
	if err := os.WriteFile(olderPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write older transcript: %v", err)
	}

	st := &recordingResumeStore{
		row: row,
		// ListSessionHistory returns newest-first: newer entry precedes older.
		history: []store.SessionHistoryEntry{
			{ClaudeSessionID: newerSession, JSONLPath: newerRotted},
			{ClaudeSessionID: olderSession, JSONLPath: olderPath},
		},
	}
	tm := &recordingResumeTmux{}

	if _, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"}); err != nil {
		t.Fatalf("Resume: %v; want recovery via newer entry's recomputed path", err)
	}
	if tm.newSessionCalls != 1 {
		t.Fatalf("NewSession called %d times; want 1", tm.newSessionCalls)
	}
	// The NEWER session must win — not the older intact entry.
	if len(tm.gotCommand) < 3 || tm.gotCommand[1] != "--resume" || tm.gotCommand[2] != newerSession {
		t.Errorf("command = %v; want `claude --resume %s ...` (newer entry, not older %s)",
			tm.gotCommand, newerSession, olderSession)
	}
}

// TestResumeListSessionHistoryErrorPropagates covers resume's error-injection
// path: when ListSessionHistory fails (after both persisted and fallback
// candidates are absent), resume surfaces the error rather than silently
// swallowing it.
func TestResumeListSessionHistoryErrorPropagates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

	// baseRow has JSONLPath == "" and no CLAUDE_CONFIG_DIR, so both the persisted
	// and computed-fallback candidates are absent, driving resume to consult
	// ListSessionHistory — which is rigged to fail.
	row := baseRow()
	computed, err := spawn.JsonlPath(row.CWD, row.ClaudeSessionID)
	if err != nil {
		t.Fatalf("compute path: %v", err)
	}
	if _, serr := os.Stat(computed); !os.IsNotExist(serr) {
		t.Fatalf("computed fallback %q unexpectedly exists", computed)
	}

	histErr := errors.New("history read boom")
	st := &recordingResumeStore{row: row, historyErr: histErr}
	tm := &recordingResumeTmux{}

	_, err = api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"})
	if !errors.Is(err, histErr) {
		t.Fatalf("err = %v; want wrapped %v", err, histErr)
	}
	if tm.newSessionCalls != 0 {
		t.Errorf("NewSession fired despite history read failure")
	}
}

// TestResumePrefersPersistedJsonlPath proves the pre-flight consults the
// persisted jsonl_path and NOT the computed slug-rule location. The row's
// JSONLPath points at a real temp file that is deliberately elsewhere; the
// computed location is left absent (SeedJsonl is never called). Resume
// proceeding past the JSONL pre-flight (NewSession fires) can only happen if
// the persisted path was the one Stat'd.
func TestResumePrefersPersistedJsonlPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

	// A real transcript at a path unrelated to the slug rule.
	persisted := filepath.Join(t.TempDir(), "custom-config", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(persisted), 0o700); err != nil {
		t.Fatalf("mkdir persisted parent: %v", err)
	}
	if err := os.WriteFile(persisted, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write persisted jsonl: %v", err)
	}

	// Guard the premise: the computed location must NOT exist, so a pass
	// cannot come from the fallback path.
	row := baseRow()
	computed, err := spawn.JsonlPath(row.CWD, row.ClaudeSessionID)
	if err != nil {
		t.Fatalf("compute path: %v", err)
	}
	if _, serr := os.Stat(computed); !os.IsNotExist(serr) {
		t.Fatalf("computed location %q unexpectedly exists (stat err=%v)", computed, serr)
	}

	row.JSONLPath = persisted
	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}

	if _, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"}); err != nil {
		t.Fatalf("Resume: %v; want proceed via persisted jsonl_path", err)
	}
	if tm.newSessionCalls != 1 {
		t.Errorf("NewSession called %d times; want 1 (resume proceeded past pre-flight)", tm.newSessionCalls)
	}
}

// TestResumeFallbackSuccess is the b.1ba table-driven proof that resume
// resolves a transcript through the CONFIG_DIR-aware fallback and proceeds
// (NewSession fires exactly once) across every shape the fallback must handle.
// It folds together the five same-shape fallback-success tests (legacy NULL
// row healing via CONFIG_DIR, path-rot healing via CONFIG_DIR, persisted-rot
// healing via ~/.claude, legacy NULL → computed ~/.claude, no-CONFIG_DIR-key →
// ~/.claude) plus the AC7 value-semantics cases (empty / whitespace-only /
// relative / ~-prefixed CLAUDE_CONFIG_DIR all falling through to ~/.claude).
//
// Each case declares where the transcript is seeded (under ~/.claude via
// SeedJsonl, or under a per-case custom config dir via SeedJsonlUnder), the
// ExtraEnv it puts on the row, an optional rotted persisted jsonl_path, and
// whether the fallback is expected to resolve under $HOME/.claude (guarded by
// homeFallback so a pass cannot silently come from the wrong branch).
func TestResumeFallbackSuccess(t *testing.T) {
	type seedMode int
	const (
		seedNone      seedMode = iota // no transcript seeded (unused here)
		seedHome                      // transcript under $HOME/.claude
		seedConfigDir                 // transcript under the case's customCfgDir
	)

	cases := []struct {
		name string
		// extraEnv builder is given the per-case custom config dir (a fresh
		// t.TempDir()) so a case can wire CLAUDE_CONFIG_DIR to it, to a bogus
		// value, or omit the key entirely.
		extraEnv func(customCfgDir string) map[string]string
		// seed decides where the on-disk transcript is planted.
		seed seedMode
		// persistedRot, when true, sets a stat-failing persisted jsonl_path so
		// the fallback must fire on a rotted (not merely NULL) path.
		persistedRot bool
		// homeFallback asserts the resolved fallback lives under $HOME/.claude;
		// the per-case guard checks the ~/.claude slug path is what gets hit.
		homeFallback bool
	}{
		{
			name:         "legacy NULL row heals via CONFIG_DIR",
			extraEnv:     func(cfg string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": cfg} },
			seed:         seedConfigDir,
			homeFallback: false,
		},
		{
			name:         "path rot heals via CONFIG_DIR",
			extraEnv:     func(cfg string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": cfg} },
			seed:         seedConfigDir,
			persistedRot: true,
			homeFallback: false,
		},
		{
			name:         "persisted rot heals via default HOME (no CONFIG_DIR key)",
			extraEnv:     func(string) map[string]string { return nil },
			seed:         seedHome,
			persistedRot: true,
			homeFallback: true,
		},
		{
			name:         "legacy NULL row falls back to computed HOME path",
			extraEnv:     func(string) map[string]string { return nil },
			seed:         seedHome,
			homeFallback: true,
		},
		{
			name:         "ExtraEnv present but no CONFIG_DIR key falls back to HOME",
			extraEnv:     func(string) map[string]string { return map[string]string{"SOME_OTHER_KEY": "value"} },
			seed:         seedHome,
			homeFallback: true,
		},
		// ── AC7 value-semantics: non-absolute/empty CONFIG_DIR ≡ absent → HOME ──
		{
			name:         "empty-string CONFIG_DIR treated as absent → HOME",
			extraEnv:     func(string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": ""} },
			seed:         seedHome,
			homeFallback: true,
		},
		{
			name:         "whitespace-only CONFIG_DIR treated as absent → HOME",
			extraEnv:     func(string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": "   "} },
			seed:         seedHome,
			homeFallback: true,
		},
		{
			name:         "relative CONFIG_DIR treated as absent → HOME",
			extraEnv:     func(string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": "rel/dir"} },
			seed:         seedHome,
			homeFallback: true,
		},
		{
			name:         "tilde-prefixed CONFIG_DIR treated as absent → HOME",
			extraEnv:     func(string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": "~/cfg"} },
			seed:         seedHome,
			homeFallback: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

			customCfgDir := t.TempDir()
			row := baseRow() // JSONLPath == "" (legacy NULL) unless persistedRot
			row.ExtraEnv = c.extraEnv(customCfgDir)
			if c.persistedRot {
				row.JSONLPath = filepath.Join(t.TempDir(), "rotted", "gone.jsonl")
			}

			switch c.seed {
			case seedHome:
				apitest.SeedJsonl(t, row.CWD, row.ClaudeSessionID)
			case seedConfigDir:
				apitest.SeedJsonlUnder(t, customCfgDir, row.CWD, row.ClaudeSessionID)
			}

			// Premise guard on homeFallback cases: for a CONFIG_DIR fallback the
			// default ~/.claude path must NOT also exist (so a pass cannot come
			// from the wrong branch); for a HOME fallback the ~/.claude path is
			// exactly where the transcript was seeded and MUST exist.
			def, err := spawn.JsonlPath(row.CWD, row.ClaudeSessionID)
			if err != nil {
				t.Fatalf("compute default path: %v", err)
			}
			if c.homeFallback {
				if _, serr := os.Stat(def); serr != nil {
					t.Fatalf("HOME-fallback premise: default ~/.claude path %q must exist (stat err=%v)", def, serr)
				}
			} else {
				if _, serr := os.Stat(def); !os.IsNotExist(serr) {
					t.Fatalf("CONFIG_DIR-fallback premise: default ~/.claude path %q must be absent (stat err=%v)", def, serr)
				}
			}

			st := &recordingResumeStore{row: row}
			tm := &recordingResumeTmux{}
			if _, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"}); err != nil {
				t.Fatalf("Resume: %v; want proceed via fallback", err)
			}
			if tm.newSessionCalls != 1 {
				t.Errorf("NewSession called %d times; want 1 (resume proceeded past pre-flight)", tm.newSessionCalls)
			}
		})
	}
}

// TestResumeBothCandidatesAbsentReturnsErrJsonlMissing covers AC 3 and AC 4:
// when neither the persisted path nor the CONFIG_DIR fallback exists, resume
// raises ErrJsonlMissing and the message names every tried path with its
// source label (persisted / fallback).
func TestResumeBothCandidatesAbsentReturnsErrJsonlMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	configDir := t.TempDir() // exists, but no transcript seeded under it
	row := baseRow()
	row.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": configDir}
	persisted := filepath.Join(t.TempDir(), "custom", "gone.jsonl")
	row.JSONLPath = persisted

	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}
	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"})
	if !errors.Is(err, api.ErrJsonlMissing) {
		t.Fatalf("err = %v; want ErrJsonlMissing", err)
	}

	// AC 4: both candidate paths named, each with its source label.
	fallback, ferr := spawn.JsonlPathIn(configDir, row.CWD, row.ClaudeSessionID)
	if ferr != nil {
		t.Fatalf("compute fallback path: %v", ferr)
	}
	msg := err.Error()
	if !containsStr(msg, "persisted "+persisted) {
		t.Errorf("err %q missing persisted source label for %q", msg, persisted)
	}
	if !containsStr(msg, "fallback "+fallback) {
		t.Errorf("err %q missing fallback source label for %q", msg, fallback)
	}
	if tm.newSessionCalls != 0 || st.setParentCalls != 0 {
		t.Errorf("side effects on guard error")
	}
}

// TestResumeNullPathBothAbsentReportsSingleFallback covers the AC 4 variant
// where jsonl_path is NULL and no history exists: the row never produced a
// transcript, so the sentinel is ErrJsonlNeverWritten (b.v2c AC2). The message
// must still carry exactly one attempt — the fallback — with no spurious
// persisted entry.
func TestResumeNullPathBothAbsentReportsSingleFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	configDir := t.TempDir()
	row := baseRow() // JSONLPath == "" (NULL)
	row.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": configDir}

	st := &recordingResumeStore{row: row}
	tm := &recordingResumeTmux{}
	_, err := api.Resume(st, tm, config.Default(), api.ResumeParams{ClaudeInstanceID: "id-r-1"})
	if !errors.Is(err, api.ErrJsonlNeverWritten) {
		t.Fatalf("err = %v; want ErrJsonlNeverWritten", err)
	}
	fallback, _ := spawn.JsonlPathIn(configDir, row.CWD, row.ClaudeSessionID)
	msg := err.Error()
	if !containsStr(msg, "fallback "+fallback) {
		t.Errorf("err %q missing fallback entry for %q", msg, fallback)
	}
	if containsStr(msg, "persisted ") {
		t.Errorf("err %q has spurious persisted entry for a NULL jsonl_path", msg)
	}
}

func TestResumeStaleTmuxSessionReturnsErrTmuxSessionCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	row := baseRow()
	apitest.SeedJsonl(t, row.CWD, row.ClaudeSessionID)
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
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "caller-id")

	row := baseRow()
	apitest.SeedJsonl(t, row.CWD, row.ClaudeSessionID)
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

	if tm.gotEnvs["AGENT_DIRECTOR_INSTANCE_ID"] != "id-r-1" {
		t.Errorf("env AGENT_DIRECTOR_INSTANCE_ID = %q; want id-r-1",
			tm.gotEnvs["AGENT_DIRECTOR_INSTANCE_ID"])
	}
	if tm.gotEnvs["AGENT_DIRECTOR_LABEL_PROJECT"] != "foo" {
		t.Errorf("label env lost on resume: %v", tm.gotEnvs)
	}

	// parent_id derivation: caller env had AGENT_DIRECTOR_INSTANCE_ID=caller-id.
	if st.setParentCalls != 1 || st.setParentArgs != [2]string{"id-r-1", "caller-id"} {
		t.Errorf("SetParentID calls=%d args=%v; want 1 [id-r-1 caller-id]",
			st.setParentCalls, st.setParentArgs)
	}
}

func TestResumeFromBareShellSetsParentNull(t *testing.T) {
	// No AGENT_DIRECTOR_INSTANCE_ID in caller env → SetParentID is
	// called with empty parent (the store writes NULL).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

	row := baseRow()
	apitest.SeedJsonl(t, row.CWD, row.ClaudeSessionID)
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
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	row := baseRow()
	row.State = store.StateMissing
	apitest.SeedJsonl(t, row.CWD, row.ClaudeSessionID)
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
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	row := baseRow()
	apitest.SeedJsonl(t, row.CWD, row.ClaudeSessionID)
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

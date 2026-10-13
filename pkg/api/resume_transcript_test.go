package api_test

// resume_transcript_test.go covers how resume finds the transcript it resumes
// (SR-8.7; b.1ba, b.v2c, b.5jm, b.nje), through the real store (whose read
// keeps the current-id entry, so the rule is pkg/api's): the persisted path,
// the CLAUDE_CONFIG_DIR-aware current-session fallback, and refusals that
// never select older history (SR-6.8.9). A Codex row's check (SR-7.4)
// tries only its current recorded path,
// and never a path composed from its cwd and session id. Which CLAUDE_CONFIG_DIR
// values are usable is internal/spawn's TestConfigDirUsable.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// histCurrent is the session id every histEnv row is seeded with.
const histCurrent = "sess-current"

// histBase anchors seeded recorded_at values; a larger histEntry.age is older.
var histBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// histEntry is one history entry; onDisk plants its recorded path (noPath: none), recomputedOnDisk its recomputed one.
type histEntry struct {
	id                               string
	life                             int64
	age                              int
	onDisk, recomputedOnDisk, noPath bool
}

// histRow is the seeded row (ended by default): persisted is "" (NULL),
// "rotted" or "present"; cfg is "" (none), "dir" (a per-test directory) or a
// literal value; current plants the session's transcript at its fallback path
// (the one a Claude row composes, which a codex row never looks at).
type histRow struct {
	life                  int64
	persisted, cfg, state string
	current               bool
	entries               []histEntry
	agent                 string // "" (claude) or "codex": its transcripts' paths are rollout paths
	args                  string // the row's raw agent_args text; "" leaves the fixture's
}

// histEnv is one seeded row on env.socket with its own cwd, config dir ("" if unusable) and recorded-path dir.
type histEnv struct {
	t                       *testing.T
	env                     *resumeEnv
	cwd, cfgDir, recDir, id string
	codex                   bool
}

func newHistEnv(t *testing.T, row histRow) *histEnv {
	t.Helper()
	e := &histEnv{t: t, env: newResumeEnv(t), cwd: t.TempDir(), recDir: t.TempDir(), codex: row.agent == "codex"}
	var opts []apitest.SpawnOption
	if row.agent != "" {
		opts = agentRowOpts(row.agent)
	}
	opts = append(opts, apitest.WithTmuxSocket(e.env.socket), apitest.WithLifeNumber(row.life))
	if row.args != "" {
		opts = append(opts, apitest.WithRawAgentArgs(row.args))
	}
	switch row.cfg {
	case "":
	case "dir":
		e.cfgDir = t.TempDir()
		opts = append(opts, apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": e.cfgDir}))
	default:
		opts = append(opts, apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": row.cfg}))
	}
	switch row.persisted {
	case "present":
		plantJsonl(t, e.persistedPath())
		opts = append(opts, apitest.WithJsonlPath(e.persistedPath()))
	case "blocked":
		if err := os.WriteFile(filepath.Dir(e.persistedPath()), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts = append(opts, apitest.WithJsonlPath(e.persistedPath()))
	case "rotted":
		opts = append(opts, apitest.WithJsonlPath(e.persistedPath()))
	}
	if row.current {
		plantJsonl(t, e.recomputed(histCurrent))
	}
	for _, h := range row.entries {
		path := e.recorded(h.id)
		if h.noPath {
			path = ""
		}
		if h.onDisk {
			plantJsonl(t, path)
		}
		if h.recomputedOnDisk {
			plantJsonl(t, e.recomputed(h.id))
		}
		opts = append(opts, apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: h.id, JSONLPath: path,
			Life: h.life, RecordedAt: histBase.Add(-time.Duration(h.age) * time.Minute)}))
	}
	state := row.state
	if state == "" {
		state = store.StateEnded
	}
	id, err := apitest.SeedSpawn(e.env.dbPath, "", state, e.cwd, "", histCurrent, false, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	e.id = id
	return e
}

func plantJsonl(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// persistedPath is the row's jsonl_path; it differs from every recorded path.
func (e *histEnv) persistedPath() string {
	if e.codex {
		return codexfix.RolloutPath(e.t, filepath.Join(e.recDir, "persisted"), histCurrent, false)
	}
	return filepath.Join(e.recDir, "persisted", histCurrent+".jsonl")
}

// recorded is the recorded path seeded on sid's history entry.
func (e *histEnv) recorded(sid string) string {
	if e.codex {
		return codexfix.RolloutPath(e.t, e.recDir, sid, false)
	}
	return filepath.Join(e.recDir, sid+".jsonl")
}

// recomputed is sid's path under the row's cwd and config directory, or ~/.claude.
func (e *histEnv) recomputed(sid string) string {
	p, err := spawn.JsonlPath(e.cwd, sid)
	if e.cfgDir != "" {
		p, err = spawn.JsonlPathIn(e.cfgDir, e.cwd, sid)
	}
	if err != nil {
		e.t.Fatalf("transcript path of %s: %v", sid, err)
	}
	return p
}

// errHistory is the history read's failure historyErrStore injects.
var errHistory = errors.New("history read boom")

// historyErrStore fails resume's history read with errHistory.
type historyErrStore struct{ *hookedResumeStore }

func (historyErrStore) ListSessionHistory(string, int64) ([]api.SessionHistoryEntry, error) {
	return nil, errHistory
}

// TestResumeTranscript: the session relaunched (the move keeps the row's
// session id, which ad.resume.moved_to_pending names, AC6), or the refusal
// naming the paths tried in order, with no tmux call and the row unchanged.
func TestResumeTranscript(t *testing.T) {
	t.Parallel()
	fallback := func(e *histEnv) string { return "fallback " + e.recomputed(histCurrent) }
	cases := []histCase{
		{name: "persisted current wins over older transcript", row: histRow{persisted: "present", entries: []histEntry{{id: "sess-older", onDisk: true}}}, want: histCurrent},
		{name: "NULL path heals via config dir", row: histRow{cfg: "dir", current: true}, want: histCurrent},
		{name: "non-ENOENT persisted stat failure heals via config dir", row: histRow{persisted: "blocked", cfg: "dir", current: true}, want: histCurrent},
		{name: "rotted path heals via config dir", row: histRow{persisted: "rotted", cfg: "dir", current: true}, want: histCurrent},
		{name: "rotted path heals via default folder", row: histRow{persisted: "rotted", current: true}, want: histCurrent},
		{name: "relative config dir uses default folder", row: histRow{cfg: "rel/dir", current: true}, want: histCurrent},
		{name: "NULL path only current-id history is never written", row: histRow{entries: []histEntry{{id: histCurrent, onDisk: true}}}, wantErr: api.ErrJsonlNeverWritten},
		{name: "NULL path only earlier-life history is never written", row: histRow{life: 1, entries: []histEntry{{id: "sess-older", onDisk: true}}}, wantErr: api.ErrJsonlNeverWritten},
		{name: "rotation refuses older on-disk sessions", row: histRow{persisted: "present", state: store.StateWaiting}, hooks: true, wantErr: api.ErrJsonlMissing},
		{name: "codex current wins over older rollout", row: histRow{agent: "codex", persisted: "present", entries: []histEntry{{id: "sess-older", onDisk: true}}}, want: histCurrent},
		{name: "codex replays unrelated stored arguments", row: histRow{agent: "codex", persisted: "present", args: `["--remote","ws://x","--model","gpt"]`}, want: histCurrent, wantPass: []string{"--remote", "ws://x", "--model", "gpt"}},
		{name: "codex NULL path never written", row: histRow{agent: "codex", current: true}, wantErr: api.ErrJsonlNeverWritten},
		{name: "codex only current-id history never written", row: histRow{agent: "codex", entries: []histEntry{{id: histCurrent, onDisk: true}}}, wantErr: api.ErrJsonlNeverWritten},
		{name: "history read fails", historyErr: true, wantErr: errHistory},
	}
	for _, agent := range []string{"claude", "codex"} {
		for _, persisted := range []string{"", "rotted"} {
			cases = append(cases, histCase{name: agent + " missing current " + persisted + " never opens older history after folder change",
				row:     histRow{agent: agent, persisted: persisted, cfg: "dir", current: agent == "codex", entries: []histEntry{{id: "sess-newer", age: 1, recomputedOnDisk: true}, {id: "sess-older", age: 2, onDisk: true}}},
				wantErr: api.ErrJsonlMissing, named: func(e *histEnv) []string {
					var paths []string
					if persisted != "" {
						paths = append(paths, "persisted "+e.persistedPath())
					}
					if agent == "claude" {
						paths = append(paths, fallback(e))
					} else if persisted == "" {
						paths = append(paths, "no transcript path is recorded for the current session")
					}
					return paths
				}})
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runHistCase(t, tc)
		})
	}
}

// histCase is one TestResumeTranscript case: the row, and the relaunch or refusal expected.
type histCase struct {
	name       string
	row        histRow
	hooks      bool // the agent restarts and its SessionEnd ends the row first
	historyErr bool // the history read fails
	want       string
	wantErr    error // when want is ""
	// named paths must appear in this order; notNamed must not appear.
	named, notNamed func(e *histEnv) []string
	// wantPass is the pass-through arguments a Codex relaunch carries before any `--`; nil skips the check.
	wantPass []string
}

// resumedSession is the session id the create's command resumes, as `claude --resume <id>` or `codex resume <id>` gives it.
func resumedSession(t *testing.T, cmd []string, codex bool) (string, bool) {
	t.Helper()
	if codex {
		l := codexfix.Split(t, cmd)
		it, ok := l.Resume()
		return it.SessionID, ok && l.Program == codexProgram
	}
	if len(cmd) < 3 || cmd[1] != "--resume" {
		return "", false
	}
	return cmd[2], true
}

// runHistCase seeds tc's row, resumes it and checks the outcome tc names.
func runHistCase(t *testing.T, tc histCase) {
	t.Helper()
	e := newHistEnv(t, tc.row)
	if tc.hooks {
		if got := apitest.ApplyAgentHook(t, e.env.dbPath, e.id, "SessionStart", "sess-restarted",
			apitest.HookTranscript(e.recorded("sess-restarted"), false)); !got.Applied {
			t.Fatalf("SessionStart = %+v; want applied", got)
		}
		if got := apitest.ApplyAgentHook(t, e.env.dbPath, e.id, "SessionEnd", ""); !got.Applied {
			t.Fatalf("SessionEnd = %+v; want applied", got)
		}
	}
	before := e.env.columns(t, e.id)
	var trustBefore []byte
	if e.cfgDir != "" {
		trustBefore, _ = os.ReadFile(filepath.Join(e.cfgDir, ".claude.json"))
	}
	var s api.ResumeStore = e.env.store
	if tc.historyErr {
		s = historyErrStore{e.env.store}
	}
	mark := trailMark(t)

	_, err := api.Resume(s, e.env.rec, e.env.pc, e.env.cfg, e.env.storeID, e.env.clock.Now, e.env.lg,
		api.ResumeParams{InstanceID: e.id})

	if tc.want != "" {
		if err != nil {
			t.Fatalf("Resume: %v; want relaunch of %s", err, tc.want)
		}
		// One create, on the row's recorded socket, resumes the chosen session.
		c := e.env.rec.SocketCallsOf(tmux.CallCreate)
		if len(c) != 1 || c[0].Socket != e.env.socket {
			t.Fatalf("creates = %+v; want one on %s", c, e.env.socket)
		}
		if sid, ok := resumedSession(t, c[0].Command, e.codex); !ok || sid != tc.want {
			t.Errorf("create runs %q; want %s resuming %s", c[0].Command, map[bool]string{false: "claude --resume", true: "codex resume"}[e.codex], tc.want)
		}
		if tc.wantPass != nil {
			if got := codexfix.Split(t, c[0].Command).PassThrough(); !reflect.DeepEqual(got, tc.wantPass) {
				t.Errorf("replayed pass-through arguments = %q; want %q", got, tc.wantPass)
			}
		}
		if c := e.env.columns(t, e.id); c.State != store.StatePending || c.SessionID != before.SessionID {
			t.Errorf("row {state %v, session %v}; want pending, %v kept", c.State, c.SessionID, before.SessionID)
		}
		assertResumeEvents(t, mark, e.id, "ad.resume.moved_to_pending")
		if l := pendTrail(t, "ad.resume.moved_to_pending", e.id); len(l) == 1 {
			assertAPITrailStr(t, l[0], "session_id", before.SessionID.(string))
		}
		return
	}
	if !errors.Is(err, tc.wantErr) {
		t.Fatalf("err = %v; want %v", err, tc.wantErr)
	}
	if errors.Is(err, api.ErrJsonlMissing) && errors.Is(err, api.ErrJsonlNeverWritten) {
		t.Errorf("err %v matches both ErrJsonlMissing and ErrJsonlNeverWritten", err)
	}
	msg, last := err.Error(), -1
	if !tc.historyErr && strings.Contains(msg, "history ") {
		t.Errorf("refusal includes historical attempts: %s", msg)
	}
	if e.codex && strings.Contains(msg, "fallback ") {
		t.Errorf("Codex refusal includes a recomputed candidate: %s", msg)
	}
	for _, h := range tc.row.entries {
		if h.id != histCurrent && strings.Contains(msg, h.id) {
			t.Errorf("refusal names older session %q: %s", h.id, msg)
		}
	}
	if tc.named != nil {
		for _, p := range tc.named(e) {
			i := strings.Index(msg, p)
			if i < 0 {
				t.Errorf("err %q does not name %q", msg, p)
				continue
			}
			if i < last {
				t.Errorf("err %q names %q out of order", msg, p)
			}
			last = i
		}
	}
	if tc.notNamed != nil {
		for _, p := range tc.notNamed(e) {
			if strings.Contains(msg, p) {
				t.Errorf("err %q names %q; want it outside the visible history", msg, p)
			}
		}
	}
	if n, s := len(e.env.rec.Calls()), len(e.env.rec.SocketCalls()); n != 0 || s != 0 {
		t.Errorf("tmux calls = %d name-based, %d socket; want none on refusal", n, s)
	}
	if e.cfgDir != "" {
		trustAfter, _ := os.ReadFile(filepath.Join(e.cfgDir, ".claude.json"))
		if !reflect.DeepEqual(trustBefore, trustAfter) {
			t.Error("refused resume changed trust file")
		}
	}
	if after := e.env.columns(t, e.id); !reflect.DeepEqual(before, after) {
		t.Errorf("row changed on refusal:\nbefore %+v\nafter  %+v", before, after)
	}
}

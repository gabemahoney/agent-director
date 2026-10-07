package api_test

// resume_transcript_test.go covers how resume finds the transcript it resumes
// (SR-8.7; b.1ba, b.v2c, b.5jm, b.nje), through the real store (whose read
// keeps the current-id entry, so the rule is pkg/api's): the persisted path,
// the CLAUDE_CONFIG_DIR-aware fallback, the visible history newest first, and
// the ErrJsonl* refusals naming every path tried. Which CLAUDE_CONFIG_DIR
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
// literal value; current plants the session's transcript at its fallback path.
type histRow struct {
	life                  int64
	persisted, cfg, state string
	current               bool
	entries               []histEntry
}

// histEnv is one seeded row on env.socket with its own cwd, config dir ("" if unusable) and recorded-path dir.
type histEnv struct {
	t                       *testing.T
	env                     *resumeEnv
	cwd, cfgDir, recDir, id string
}

func newHistEnv(t *testing.T, row histRow) *histEnv {
	t.Helper()
	e := &histEnv{t: t, env: newResumeEnv(t), cwd: t.TempDir(), recDir: t.TempDir()}
	opts := []apitest.SpawnOption{apitest.WithTmuxSocket(e.env.socket), apitest.WithLifeNumber(row.life)}
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
	return filepath.Join(e.recDir, "persisted", histCurrent+".jsonl")
}

// recorded is the recorded path seeded on sid's history entry.
func (e *histEnv) recorded(sid string) string {
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
	history := func(e *histEnv, ids ...string) []string {
		var out []string
		for _, id := range ids {
			out = append(out, "history "+e.recorded(id), "history "+e.recomputed(id))
		}
		return out
	}
	cases := []struct {
		name       string
		row        histRow
		hooks      bool // the agent restarts and its SessionEnd ends the row first
		historyErr bool // the history read fails
		want       string
		wantErr    error // when want is ""
		// named paths must appear in this order; notNamed must not appear.
		named, notNamed func(e *histEnv) []string
	}{
		// The current session's own candidates (b.1ba, b.nje).
		{name: "persisted path wins", row: histRow{persisted: "present"}, want: histCurrent},
		{name: "NULL path heals via CLAUDE_CONFIG_DIR", row: histRow{cfg: "dir", current: true}, want: histCurrent},
		{name: "rotted path heals via CLAUDE_CONFIG_DIR", row: histRow{persisted: "rotted", cfg: "dir", current: true}, want: histCurrent},
		{name: "rotted path heals via ~/.claude", row: histRow{persisted: "rotted", current: true}, want: histCurrent},
		{name: "NULL path, relative CLAUDE_CONFIG_DIR treated as absent: ~/.claude", row: histRow{cfg: "rel/dir", current: true}, want: histCurrent},
		// The visible history (b.v2c AC6, b.5jm, SR-8.7).
		{name: "archived session's recorded transcript", row: histRow{entries: []histEntry{{id: "sess-older", age: 1, onDisk: true}}},
			want: "sess-older"},
		{name: "archived entry with no recorded path, at its path under CLAUDE_CONFIG_DIR", row: histRow{cfg: "dir",
			entries: []histEntry{{id: "sess-older", age: 1, noPath: true, recomputedOnDisk: true}}}, want: "sess-older"},
		{name: "newer entry's rotted path recomputed beats an older intact one (b.5jm/1)", row: histRow{cfg: "dir",
			entries: []histEntry{{id: "sess-newer", age: 1, recomputedOnDisk: true}, {id: "sess-older", age: 2, onDisk: true}}},
			want: "sess-newer"},
		{name: "current-id entry on disk is skipped for an older one", row: histRow{entries: []histEntry{
			{id: histCurrent, age: 1, onDisk: true}, {id: "sess-older", age: 2, onDisk: true}}}, want: "sess-older"},
		{name: "newest entry of the life wins", row: histRow{entries: []histEntry{
			{id: "sess-newer", age: 1, onDisk: true}, {id: "sess-older", age: 2, onDisk: true}}}, want: "sess-newer"},
		{name: "gone newer entry falls through to the older", row: histRow{entries: []histEntry{
			{id: "sess-newer", age: 1}, {id: "sess-older", age: 2, onDisk: true}}}, want: "sess-older"},
		{name: "life-1 row takes its life-1 entry over a newer life-0 one", row: histRow{life: 1, entries: []histEntry{
			{id: "sess-newer", age: 1, onDisk: true}, {id: "sess-older", life: 1, age: 2, onDisk: true}}}, want: "sess-older"},
		// AC-REUSE-24: a rotation the SessionStart hook archived stays a candidate. SR-22.9: only the
		// row's own pane moves the row, so it is live when its agent restarts with a new id whose
		// transcript is not yet written (the hook archives histCurrent and nulls jsonl_path), and that
		// agent's SessionEnd finishes it.
		{name: "rotation archived by the hook stays a candidate", row: histRow{life: 1, persisted: "present", state: store.StateWaiting,
			entries: []histEntry{{id: "sess-older", life: 1, age: 1, onDisk: true}}}, hooks: true, want: histCurrent},
		// The refusals, decided on the visible history (AC2).
		{name: "NULL path, only the current-id entry: never written", row: histRow{cfg: "dir",
			entries: []histEntry{{id: histCurrent, age: 1}}}, wantErr: api.ErrJsonlNeverWritten,
			named:    func(e *histEnv) []string { return []string{fallback(e)} },
			notNamed: func(e *histEnv) []string { return []string{e.recorded(histCurrent), "history ", "persisted"} }},
		{name: "rotted path, only the current-id entry: missing", row: histRow{persisted: "rotted", cfg: "dir",
			entries: []histEntry{{id: histCurrent, age: 1}}}, wantErr: api.ErrJsonlMissing,
			named:    func(e *histEnv) []string { return []string{"persisted " + e.persistedPath(), fallback(e)} },
			notNamed: func(e *histEnv) []string { return []string{e.recorded(histCurrent), "history "} }},
		{name: "older ids only, no CLAUDE_CONFIG_DIR: every candidate named, newest first", row: histRow{entries: []histEntry{
			{id: "sess-newer", age: 1}, {id: "sess-older", age: 2}}}, wantErr: api.ErrJsonlMissing,
			named: func(e *histEnv) []string {
				return append([]string{fallback(e)}, history(e, "sess-newer", "sess-older")...)
			}},
		{name: "never-messaged life-1 row with only a life-0 entry: never written", row: histRow{life: 1, cfg: "dir",
			entries: []histEntry{{id: "sess-life0", age: 1, onDisk: true, recomputedOnDisk: true}}}, wantErr: api.ErrJsonlNeverWritten,
			named: func(e *histEnv) []string { return []string{fallback(e)} }, notNamed: func(*histEnv) []string { return []string{"sess-life0"} }},
		{name: "life-1 entry gone, newer life-0 one on disk: names only life 1", row: histRow{life: 1, cfg: "dir", entries: []histEntry{
			{id: "sess-life0", age: 1, onDisk: true, recomputedOnDisk: true}, {id: "sess-life1", life: 1, age: 2}}},
			wantErr: api.ErrJsonlMissing, named: func(e *histEnv) []string { return append([]string{fallback(e)}, history(e, "sess-life1")...) },
			notNamed: func(*histEnv) []string { return []string{"sess-life0"} }},
		{name: "history read fails", historyErr: true, wantErr: errHistory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
			var s api.ResumeStore = e.env.store
			if tc.historyErr {
				s = historyErrStore{e.env.store}
			}
			mark := trailMark(t)

			_, err := api.Resume(s, e.env.rec, e.env.pc, e.env.cfg, e.env.storeID, e.env.clock.Now, e.env.lg,
				api.ResumeParams{ClaudeInstanceID: e.id})

			if tc.want != "" {
				if err != nil {
					t.Fatalf("Resume: %v; want relaunch of %s", err, tc.want)
				}
				// One create, on the row's recorded socket, resumes the chosen session.
				if c := e.env.rec.SocketCallsOf(tmux.CallCreate); len(c) != 1 || c[0].Socket != e.env.socket ||
					len(c[0].Command) < 3 || c[0].Command[1] != "--resume" || c[0].Command[2] != tc.want {
					t.Errorf("creates = %+v; want one on %s running claude --resume %s", c, e.env.socket, tc.want)
				}
				if c := e.env.columns(t, e.id); c.State != store.StatePending || c.ClaudeSessionID != before.ClaudeSessionID {
					t.Errorf("row {state %v, session %v}; want pending, %v kept", c.State, c.ClaudeSessionID, before.ClaudeSessionID)
				}
				assertResumeEvents(t, mark, e.id, "ad.resume.moved_to_pending")
				if l := pendTrail(t, "ad.resume.moved_to_pending", e.id); len(l) == 1 {
					assertAPITrailStr(t, l[0], "claude_session_id", before.ClaudeSessionID.(string))
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
			if after := e.env.columns(t, e.id); !reflect.DeepEqual(before, after) {
				t.Errorf("row changed on refusal:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

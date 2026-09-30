package api_test

import (
	"errors"
	"fmt"
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

// Resume's visible history (SR-8.7) through the real store: the current-session
// rule, the ErrJsonl* choice, and life-1 rows that never see life-0 entries.
// The store read keeps the current-id entry, so these cases prove the rule
// lives in pkg/api.

// histCurrent is the session id every histEnv row is seeded with.
const histCurrent = "sess-current"

// histBase anchors seeded recorded_at values; a larger histEntry.age is older.
var histBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// histEntry is one seeded session_history entry. Every entry has a recorded
// path; onDisk plants it, recomputedOnDisk plants the row-config-dir path.
type histEntry struct {
	id               string
	life             int64
	age              int
	onDisk           bool
	recomputedOnDisk bool
}

// histRow describes the seeded row (ended unless state says otherwise);
// persisted is "", "rotted" or "present".
type histRow struct {
	life      int64
	persisted string
	entries   []histEntry
	state     string // default ended
}

// histEnv is one seeded row in the shared resume fixture (resume_fixture_test.go),
// with its own config dir, cwd and recorded-path dir. The row records env.socket,
// whose per-user directory exists, so resume's create runs on it.
type histEnv struct {
	t      *testing.T
	env    *resumeEnv
	cwd    string
	cfgDir string
	recDir string
	id     string
}

func newHistEnv(t *testing.T, row histRow) *histEnv {
	t.Helper()
	e := &histEnv{
		t:      t,
		env:    newResumeEnv(t),
		cwd:    t.TempDir(),
		cfgDir: t.TempDir(),
		recDir: t.TempDir(),
	}
	t.Setenv("CLAUDE_CONFIG_DIR", e.cfgDir)

	opts := []apitest.SpawnOption{
		apitest.WithTmuxSocket(e.env.socket),
		apitest.WithLifeNumber(row.life),
		apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": e.cfgDir}),
	}
	switch row.persisted {
	case "present":
		plantJsonl(t, e.persistedPath())
		opts = append(opts, apitest.WithJsonlPath(e.persistedPath()))
	case "rotted":
		opts = append(opts, apitest.WithJsonlPath(e.persistedPath()))
	}
	for _, h := range row.entries {
		if h.onDisk {
			plantJsonl(t, e.recorded(h.id))
		}
		if h.recomputedOnDisk {
			apitest.SeedJsonlUnder(t, e.cfgDir, e.cwd, h.id)
		}
		opts = append(opts, apitest.WithSessionHistory(apitest.SessionHistorySeed{
			SessionID:  h.id,
			JSONLPath:  e.recorded(h.id),
			Life:       h.life,
			RecordedAt: histBase.Add(-time.Duration(h.age) * time.Minute),
		}))
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

// recomputed is sid's config-dir-aware path under the row's cwd.
func (e *histEnv) recomputed(sid string) string {
	p, err := spawn.JsonlPathIn(e.cfgDir, e.cwd, sid)
	if err != nil {
		e.t.Fatalf("JsonlPathIn(%s): %v", sid, err)
	}
	return p
}

func (e *histEnv) resume() error {
	_, err := e.env.resume(e.id)
	return err
}

// resumedSession returns the session id the single relaunch passed to
// --resume; the create must run on the row's recorded socket.
func (e *histEnv) resumedSession() string {
	e.t.Helper()
	calls := e.env.rec.SocketCallsOf(tmux.CallCreate)
	if len(calls) != 1 {
		e.t.Fatalf("create calls = %d; want 1", len(calls))
	}
	if calls[0].Socket != e.env.socket {
		e.t.Errorf("create on socket %q; want the row's %q", calls[0].Socket, e.env.socket)
	}
	cmd := calls[0].Command
	if len(cmd) < 3 || cmd[1] != "--resume" {
		e.t.Fatalf("command = %v; want `claude --resume <id> ...`", cmd)
	}
	return cmd[2]
}

func (e *histEnv) columns() apitest.SpawnColumns {
	e.t.Helper()
	c, err := apitest.ReadSpawnColumns(e.env.dbPath, e.id)
	if err != nil {
		e.t.Fatalf("ReadSpawnColumns: %v", err)
	}
	return c
}

// TestResumeHistorySelectsVisibleCandidate pins which history entry resume
// relaunches: never the current-id entry, never another life's entry.
func TestResumeHistorySelectsVisibleCandidate(t *testing.T) {
	cases := []struct {
		name string
		row  histRow
		want string
	}{
		{
			name: "current-id entry on disk is skipped for older same-life entry",
			row: histRow{life: 0, entries: []histEntry{
				{id: histCurrent, age: 1, onDisk: true},
				{id: "sess-older", age: 2, onDisk: true},
			}},
			want: "sess-older",
		},
		{
			name: "current-id rule at life 1",
			row: histRow{life: 1, entries: []histEntry{
				{id: histCurrent, life: 1, age: 1, onDisk: true},
				{id: "sess-older", life: 1, age: 2, onDisk: true},
			}},
			want: "sess-older",
		},
		{
			name: "life-1 row picks its life-1 entry over a newer life-0 entry",
			row: histRow{life: 1, entries: []histEntry{
				{id: "sess-newer", life: 0, age: 1, onDisk: true},
				{id: "sess-older", life: 1, age: 2, onDisk: true},
			}},
			want: "sess-older",
		},
		{
			name: "within-life contrast: newest life-0 entry wins",
			row: histRow{life: 0, entries: []histEntry{
				{id: "sess-newer", age: 1, onDisk: true},
				{id: "sess-older", age: 2, onDisk: true},
			}},
			want: "sess-newer",
		},
		{
			name: "within-life contrast: never-messaged life-0 row resumes its entry",
			row: histRow{life: 0, entries: []histEntry{
				{id: "sess-older", age: 1, onDisk: true, recomputedOnDisk: true},
			}},
			want: "sess-older",
		},
		{
			name: "within-life contrast: gone newer entry falls through to older",
			row: histRow{life: 0, entries: []histEntry{
				{id: "sess-newer", age: 1},
				{id: "sess-older", age: 2, onDisk: true},
			}},
			want: "sess-older",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newHistEnv(t, c.row)
			if err := e.resume(); err != nil {
				t.Fatalf("Resume: %v; want relaunch of %s", err, c.want)
			}
			if got := e.resumedSession(); got != c.want {
				t.Errorf("--resume %s; want %s", got, c.want)
			}
		})
	}
}

// TestResumeHistoryRefusals pins the ErrJsonl* choice and the paths named, all
// decided on the visible history; a refusal never touches tmux or the row.
func TestResumeHistoryRefusals(t *testing.T) {
	cases := []struct {
		name    string
		row     histRow
		wantErr error
		// named paths must appear in this order; notNamed must not appear.
		named    func(e *histEnv) []string
		notNamed func(e *histEnv) []string
	}{
		{
			name: "empty persisted path and only the current-id entry: never written",
			row: histRow{life: 0, entries: []histEntry{
				{id: histCurrent, age: 1},
			}},
			wantErr:  api.ErrJsonlNeverWritten,
			named:    func(e *histEnv) []string { return []string{"fallback " + e.recomputed(histCurrent)} },
			notNamed: func(e *histEnv) []string { return []string{e.recorded(histCurrent), "history "} },
		},
		{
			name: "rotted persisted path and only the current-id entry: missing, entry path not named",
			row: histRow{life: 0, persisted: "rotted", entries: []histEntry{
				{id: histCurrent, age: 1},
			}},
			wantErr: api.ErrJsonlMissing,
			named: func(e *histEnv) []string {
				return []string{"persisted " + e.persistedPath(), "fallback " + e.recomputed(histCurrent)}
			},
			notNamed: func(e *histEnv) []string { return []string{e.recorded(histCurrent), "history "} },
		},
		{
			name: "older ids only: every history candidate named, newest first",
			row: histRow{life: 0, entries: []histEntry{
				{id: "sess-newer", age: 1},
				{id: "sess-older", age: 2},
			}},
			wantErr: api.ErrJsonlMissing,
			named: func(e *histEnv) []string {
				return []string{
					"fallback " + e.recomputed(histCurrent),
					"history " + e.recorded("sess-newer"),
					"history " + e.recomputed("sess-newer"),
					"history " + e.recorded("sess-older"),
					"history " + e.recomputed("sess-older"),
				}
			},
			notNamed: func(*histEnv) []string { return nil },
		},
		{
			name: "never-messaged life-1 row with only a life-0 entry on disk: never written",
			row: histRow{life: 1, entries: []histEntry{
				{id: "sess-life0", life: 0, age: 1, onDisk: true, recomputedOnDisk: true},
			}},
			wantErr:  api.ErrJsonlNeverWritten,
			named:    func(e *histEnv) []string { return []string{"fallback " + e.recomputed(histCurrent)} },
			notNamed: func(*histEnv) []string { return []string{"sess-life0"} },
		},
		{
			name: "life-1 entry gone and newer life-0 entry on disk: missing names only life 1",
			row: histRow{life: 1, entries: []histEntry{
				{id: "sess-life0", life: 0, age: 1, onDisk: true, recomputedOnDisk: true},
				{id: "sess-life1", life: 1, age: 2},
			}},
			wantErr: api.ErrJsonlMissing,
			named: func(e *histEnv) []string {
				return []string{
					"fallback " + e.recomputed(histCurrent),
					"history " + e.recorded("sess-life1"),
					"history " + e.recomputed("sess-life1"),
				}
			},
			notNamed: func(*histEnv) []string { return []string{"sess-life0"} },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newHistEnv(t, c.row)
			before := e.columns()

			err := e.resume()
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v; want %v", err, c.wantErr)
			}
			other := api.ErrJsonlMissing
			if c.wantErr == api.ErrJsonlMissing {
				other = api.ErrJsonlNeverWritten
			}
			if errors.Is(err, other) {
				t.Errorf("err %v also matches %v", err, other)
			}

			msg, last := err.Error(), -1
			for _, p := range c.named(e) {
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
			for _, p := range c.notNamed(e) {
				if strings.Contains(msg, p) {
					t.Errorf("err %q names %q; want it outside the visible history", msg, p)
				}
			}

			if n, s := len(e.env.rec.Calls()), len(e.env.rec.SocketCalls()); n != 0 || s != 0 {
				t.Errorf("tmux calls = %d name-based, %d socket; want none on refusal", n, s)
			}
			if after := e.columns(); !reflect.DeepEqual(before, after) {
				t.Errorf("row changed on refusal:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

// TestResumeHistoryKeepsHookRotationWithinLife pins AC-REUSE-24's first
// sentence: a rotation archived by the SessionStart hook stays a candidate.
func TestResumeHistoryKeepsHookRotationWithinLife(t *testing.T) {
	for _, life := range []int64{0, 1} {
		t.Run(fmt.Sprintf("life %d", life), func(t *testing.T) {
			// SR-22.9: only the row's own pane process moves the row, so the
			// row is live (it records a pane) when its agent restarts, and
			// that agent's SessionEnd then finishes it.
			e := newHistEnv(t, histRow{life: life, persisted: "present", state: store.StateWaiting, entries: []histEntry{
				{id: "sess-older", life: life, age: 1, onDisk: true},
			}})
			// A restarted session reports a new id whose transcript is not yet
			// written: the hook archives histCurrent and nulls jsonl_path.
			if got := apitest.ApplyAgentHook(t, e.env.dbPath, e.id, "SessionStart", "sess-restarted",
				apitest.HookTranscript(e.recorded("sess-restarted"), false)); !got.Applied {
				t.Fatalf("SessionStart = %+v; want applied", got)
			}
			if got := apitest.ApplyAgentHook(t, e.env.dbPath, e.id, "SessionEnd", ""); !got.Applied {
				t.Fatalf("SessionEnd = %+v; want applied", got)
			}
			if err := e.resume(); err != nil {
				t.Fatalf("Resume: %v; want relaunch of the rotated session", err)
			}
			if got := e.resumedSession(); got != histCurrent {
				t.Errorf("--resume %s; want rotated %s (newest entry of life %d)", got, histCurrent, life)
			}
		})
	}
}

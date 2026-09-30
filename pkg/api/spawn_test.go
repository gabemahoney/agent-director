package api_test

// spawn_test.go covers Client.Spawn's handling of explicit instance ids
// (SR-9.1, AC-SPN-02): control-character ids are rejected first with
// ErrInvalidFlags and leave no row and no tmux session; empty and printable
// ids still spawn; existing control-character rows stay listable. It also
// covers the collision pre-check (SR-9.3, AC-SPN-03): a failed store read is
// ErrInternal, while a live row is still ErrInstanceIdCollision. It holds the
// shared spawn fixture; the recorded launch is in spawn_launch_test.go.

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// spawnEnv is an isolated Client wired to a tmuxfix.Recorder (nil with
// fake-tmux), its start-time reader, clock, captured log, paths and socket.
type spawnEnv struct {
	c      *api.Client
	rec    *tmuxfix.Recorder
	pc     *procfix.Checker
	clock  *tmuxfix.Clock
	logs   *bytes.Buffer
	dbPath string
	home   string
	socket string // the socket a launch resolves with TMUX unset
}

// newSpawnEnv builds a spawnEnv over a fresh store and empty config under a
// temp HOME and a per-test TMUX_TMPDIR, with a Recorder as its tmux client.
func newSpawnEnv(t *testing.T) spawnEnv { return buildSpawnEnv(t, "") }

// buildSpawnEnv is newSpawnEnv; a non-empty tmuxCommand wires the production
// client running that binary instead of a Recorder.
func buildSpawnEnv(t *testing.T, tmuxCommand string) spawnEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "") // no parent id from the host shell
	tmpdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", tmpdir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	env := spawnEnv{dbPath: filepath.Join(home, "state.db"), home: home, logs: &bytes.Buffer{},
		pc: procfix.New(), clock: tmuxfix.NewClock(time.Date(2026, 9, 29, 12, 0, 0, 123_000_000, time.UTC)),
		socket: filepath.Join(userSocketDir(tmpdir), "default")}
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	opts := api.Options{StorePath: env.dbPath, ConfigPath: cfgPath, CreateIfMissing: true,
		Logger: log.New(env.logs, "", 0), TmuxCommand: tmuxCommand}
	if tmuxCommand == "" {
		env.rec = tmuxfix.NewRecorder()
		opts.TmuxClient = env.rec
	}
	if env.c, err = api.New(opts); err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = env.c.Close() })
	api.SetClockForTest(env.c, env.clock.Now)
	api.SetProcCheckerForTest(env.c, env.pc)
	return env
}

// userSocketDir is tmux's per-user socket directory under base.
func userSocketDir(base string) string {
	return filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
}

// assertNoTmuxCalls fails when the Recorder saw any name-based or socket call.
func assertNoTmuxCalls(t *testing.T, rec *tmuxfix.Recorder) {
	t.Helper()
	if n, m := len(rec.Calls()), len(rec.SocketCalls()); n != 0 || m != 0 {
		t.Errorf("tmux calls: %d name-based, %d socket; want none", n, m)
	}
}

// listIDs returns the instance ids of every row List reports.
func listIDs(t *testing.T, c *api.Client) []string {
	t.Helper()
	res, err := c.List(api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make([]string, 0, len(res.Spawns))
	for _, r := range res.Spawns {
		ids = append(ids, r.ClaudeInstanceID)
	}
	return ids
}

// assertInvalidFlags checks err is ErrInvalidFlags with the control-character
// description case, without the id or its printable marker.
func assertInvalidFlags(t *testing.T, err error, id, marker string) {
	t.Helper()
	if !errors.Is(err, api.ErrInvalidFlags) {
		t.Fatalf("Spawn err = %v; want ErrInvalidFlags", err)
	}
	apitest.AssertDescription(t, err.Error(), apitest.DescInstanceIDControlChar(id), marker)
}

// TestSpawnRejectsControlCharacterInstanceID: every byte 0x00-0x1f or 0x7f,
// at any position, returns ErrInvalidFlags with no row and no tmux call.
func TestSpawnRejectsControlCharacterInstanceID(t *testing.T) {
	const marker = "idmark"
	cases := []struct {
		name string
		id   string
	}{
		{"newline middle", marker + "\n-a"},
		{"tab leading", "\t" + marker},
		{"NUL trailing", marker + "\x00"},
		{"ESC middle", marker + "\x1b[31m"},
		{"unit separator 0x1f", marker + "\x1f-b"},
		{"DEL trailing", marker + "\x7f"},
		{"carriage return", marker + "\r"},
		{"only a newline", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			_, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id})
			assertInvalidFlags(t, err, tc.id, marker)
			assertNoTmuxCalls(t, env.rec)
			if _, err := apitest.ReadSpawnColumns(env.dbPath, tc.id); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("ReadSpawnColumns err = %v; want ErrSpawnNotFound", err)
			}
			if ids := listIDs(t, env.c); len(ids) != 0 {
				t.Errorf("List ids = %q; want none", ids)
			}
		})
	}
}

// TestSpawnControlCharacterIDCheckedFirst: ErrInvalidFlags wins over cwd,
// template, session-name and collision failures, so no template is loaded.
func TestSpawnControlCharacterIDCheckedFirst(t *testing.T) {
	const marker = "firstmark"
	id := marker + "\tx"
	cases := []struct {
		name  string
		setup func(t *testing.T, env spawnEnv, p *api.SpawnParams)
	}{
		{"missing cwd", func(_ *testing.T, _ spawnEnv, p *api.SpawnParams) { p.CWD = "" }},
		{"nonexistent cwd", func(t *testing.T, _ spawnEnv, p *api.SpawnParams) {
			p.CWD = filepath.Join(t.TempDir(), "absent")
		}},
		{"nonexistent template", func(_ *testing.T, _ spawnEnv, p *api.SpawnParams) {
			p.Template = "no-such-template"
		}},
		{"malformed template", func(t *testing.T, env spawnEnv, p *api.SpawnParams) {
			dir := filepath.Join(env.home, ".agent-director", "templates")
			if _, err := apitest.SeedTemplate(dir, "broken", "not = [valid toml"); err != nil {
				t.Fatalf("SeedTemplate: %v", err)
			}
			p.Template = "broken"
		}},
		{"invalid session name", func(_ *testing.T, _ spawnEnv, p *api.SpawnParams) {
			p.TmuxSessionName, p.TmuxSessionNameSupplied = "bad:name", true
		}},
		{"live row with the same id", func(t *testing.T, env spawnEnv, _ *api.SpawnParams) {
			if _, err := apitest.SeedSpawn(env.dbPath, id, store.StateWaiting, "", "", "", false); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			p := api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id}
			tc.setup(t, env, &p)
			_, err := env.c.Spawn(p)
			assertInvalidFlags(t, err, id, marker)
			assertNoTmuxCalls(t, env.rec)
		})
	}
}

// TestSpawnAcceptsEmptyAndPrintableInstanceID: an empty id mints a fresh
// UUID and printable ids (space, '~', non-ASCII) spawn; neither is over-rejected.
func TestSpawnAcceptsEmptyAndPrintableInstanceID(t *testing.T) {
	suffix := uuid.NewString()[:8]
	cases := []struct {
		name string
		id   string
	}{
		{"empty mints fresh id", ""},
		{"plain printable", "agent-" + suffix},
		{"space tilde and UTF-8", "id é ~" + suffix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			res, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			got := res.ClaudeInstanceID
			if tc.id == "" {
				if _, perr := uuid.Parse(got); perr != nil {
					t.Errorf("minted id %q is not a UUID: %v", got, perr)
				}
			} else if got != tc.id {
				t.Errorf("ClaudeInstanceID = %q; want %q", got, tc.id)
			}
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("create calls = %d; want 1", n)
			}
			if ids := listIDs(t, env.c); len(ids) != 1 || ids[0] != got {
				t.Errorf("List ids = %q; want [%q]", ids, got)
			}
		})
	}
}

// TestSpawnControlCharacterRowStillListed: a pre-existing row whose id holds
// a control character is unaffected and still returned by List.
func TestSpawnControlCharacterRowStillListed(t *testing.T) {
	env := newSpawnEnv(t)
	id := "legacy\trow-" + uuid.NewString()[:8]
	if _, err := apitest.SeedSpawn(env.dbPath, id, store.StateWaiting, "", "", "", false); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	if ids := listIDs(t, env.c); len(ids) != 1 || ids[0] != id {
		t.Errorf("List ids = %q; want [%q]", ids, id)
	}
}

// failingCollisionReader is a spawn.CollisionChecker whose store read fails.
type failingCollisionReader struct{ err error }

func (f failingCollisionReader) SpawnState(string) (string, bool, error) { return "", false, f.err }

// TestSpawnPreCheckReadFailureIsErrInternal: a failed pre-check read is
// ErrInternal (even when the store error wraps a sentinel) and creates nothing.
func TestSpawnPreCheckReadFailureIsErrInternal(t *testing.T) {
	cases := []struct {
		name    string
		readErr error
	}{
		{"plain store error", errors.New("store: live spawn lookup: disk I/O error")},
		{"wraps ErrInstanceIdCollision", fmt.Errorf("store: live spawn lookup: %w", spawn.ErrInstanceIdCollision)},
		{"wraps ErrSpawnNotFound", fmt.Errorf("store: live spawn lookup: %w", store.ErrSpawnNotFound)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			claudeJSON := filepath.Join(env.home, ".claude.json")
			if err := os.WriteFile(claudeJSON, []byte("{}"), 0o600); err != nil {
				t.Fatalf("write .claude.json: %v", err)
			}
			id := "precheck-" + uuid.NewString()[:8]
			_, err := api.SpawnWithCollisionReader(env.c, failingCollisionReader{err: tc.readErr},
				api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})
			if err == nil {
				t.Fatal("SpawnWithCollisionReader err = nil; want ErrInternal")
			}
			if errors.Is(err, spawn.ErrInstanceIdCollision) {
				t.Errorf("err %v matches ErrInstanceIdCollision", err)
			}
			for _, e := range errnames.Catalog {
				if errors.Is(err, e.Err) {
					t.Errorf("err %v matches catalogued %s", err, e.Name)
				}
			}
			name, desc := errnames.Classify(err)
			if name != "ErrInternal" {
				t.Errorf("Classify name = %q; want ErrInternal", name)
			}
			apitest.AssertDescription(t, desc, apitest.DescPreCheckRead())
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); len(ids) != 0 {
				t.Errorf("List ids = %q; want none", ids)
			}
			if b, err := os.ReadFile(claudeJSON); err != nil || string(b) != "{}" {
				t.Errorf(".claude.json = %q (err %v); want untouched {}", b, err)
			}
		})
	}
}

// TestSpawnLiveRowStillCollides: a pending or live row with the same id is
// still ErrInstanceIdCollision on the ordinary path, with no tmux call.
func TestSpawnLiveRowStillCollides(t *testing.T) {
	for _, state := range []string{store.StatePending, store.StateWorking} {
		t.Run(state, func(t *testing.T) {
			env := newSpawnEnv(t)
			id := "collide-" + uuid.NewString()[:8]
			if _, err := apitest.SeedSpawn(env.dbPath, id, state, "", "", "", false); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			before, err := env.c.List(api.ListParams{})
			if err != nil {
				t.Fatalf("List before: %v", err)
			}
			_, err = env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})
			if !errors.Is(err, spawn.ErrInstanceIdCollision) {
				t.Fatalf("Spawn err = %v; want ErrInstanceIdCollision", err)
			}
			if name, _ := errnames.Classify(err); name != "ErrInstanceIdCollision" {
				t.Errorf("Classify name = %q; want ErrInstanceIdCollision", name)
			}
			assertNoTmuxCalls(t, env.rec)
			after, err := env.c.List(api.ListParams{})
			if err != nil {
				t.Fatalf("List after: %v", err)
			}
			if !reflect.DeepEqual(before.Spawns, after.Spawns) {
				t.Errorf("rows changed:\nbefore %+v\nafter  %+v", before.Spawns, after.Spawns)
			}
		})
	}
}

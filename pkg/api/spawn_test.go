package api_test

// spawn_test.go covers Client.Spawn's handling of explicit instance ids
// (SR-9.1, AC-SPN-02): control-character ids are rejected first with
// ErrInvalidFlags and leave no row and no tmux session; empty and printable
// ids still spawn; existing control-character rows stay listable.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// controlIDPhrase is the description every rejected control-character id carries.
const controlIDPhrase = "the instance id contains a control character"

// spawnEnv is an isolated Client wired to a tmuxfix.Recorder, plus its paths.
type spawnEnv struct {
	c      *api.Client
	rec    *tmuxfix.Recorder
	dbPath string
	home   string
}

// newSpawnEnv builds a Client over a fresh store and empty config under a
// temp HOME (spawn pre-trusts under HOME), with a Recorder as its tmux client.
func newSpawnEnv(t *testing.T) spawnEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "state.db")
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rec := tmuxfix.NewRecorder()
	c, err := api.New(api.Options{
		StorePath:       dbPath,
		ConfigPath:      cfgPath,
		CreateIfMissing: true,
		TmuxClient:      rec,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return spawnEnv{c: c, rec: rec, dbPath: dbPath, home: home}
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
// phrase and without the id or its printable marker in the description.
func assertInvalidFlags(t *testing.T, err error, id, marker string) {
	t.Helper()
	if !errors.Is(err, api.ErrInvalidFlags) {
		t.Fatalf("Spawn err = %v; want ErrInvalidFlags", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, controlIDPhrase) {
		t.Errorf("description %q lacks %q", msg, controlIDPhrase)
	}
	if strings.Contains(msg, id) || strings.Contains(msg, marker) {
		t.Errorf("description %q quotes the id", msg)
	}
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
			if n := len(env.rec.CallsOfKind(tmuxfix.CallNewSession)); n != 1 {
				t.Errorf("NewSession calls = %d; want 1", n)
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

package mcp_test

// pretrust_result_test.go pins pre_trust on the MCP spawn and resume results
// (SR-22.6 "The field" and "Surfaces"; AC-SPN-08): the JSON an MCP caller
// receives from tools/call carries pre_trust ok when the folder-trust entry
// was written, failed when the .claude.json is missing (the launch still
// succeeds) and, for resume, skipped when the row records the opt-out. Every
// case points CLAUDE_CONFIG_DIR at a per-test directory, so the real
// ~/.claude.json is never read or written. MCP spawn never sends no-pre-trust
// (bug b.7or is out of scope).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ptSeedJSON is the compact .claude.json a config directory starts with, so
// any rewrite (which indents) changes its bytes.
const ptSeedJSON = `{"numStartups":7,"projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`

// ptConfig is a per-test CLAUDE_CONFIG_DIR and its .claude.json as seeded
// (before is nil when the file is absent).
type ptConfig struct {
	dir, path string
	before    []byte
}

// ptSeedConfig makes a config directory; withFile puts a .claude.json lacking
// any entry for the test's folders into it.
func ptSeedConfig(t *testing.T, withFile bool) ptConfig {
	t.Helper()
	dir := t.TempDir()
	c := ptConfig{dir: dir, path: filepath.Join(dir, ".claude.json")}
	if withFile {
		c.before = []byte(ptSeedJSON)
		if err := os.WriteFile(c.path, c.before, 0o600); err != nil {
			t.Fatalf("write %s: %v", c.path, err)
		}
	}
	return c
}

// env is the extra env pointing a launch's CLAUDE_CONFIG_DIR at c.
func (c ptConfig) env() map[string]string {
	return map[string]string{"CLAUDE_CONFIG_DIR": c.dir}
}

// check asserts the file trusts cwd (trusted) or is exactly as seeded (still
// absent when it was).
func (c ptConfig) check(t *testing.T, cwd string, trusted bool) {
	t.Helper()
	got, err := os.ReadFile(c.path)
	if !trusted {
		if c.before == nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s = %q (%v); want still absent", c.path, got, err)
			}
		} else if err != nil || string(got) != string(c.before) {
			t.Errorf("%s = %q (%v); want byte-identical to %q", c.path, got, err, c.before)
		}
		return
	}
	var top struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err != nil || json.Unmarshal(got, &top) != nil || top.Projects[cwd]["hasTrustDialogAccepted"] != true {
		t.Errorf("%s = %q (%v); want projects[%q].hasTrustDialogAccepted true", c.path, got, err, cwd)
	}
}

// ptEnv is a live dispatcher over a Client on a temp store with a
// non-existent config file and a tmuxfix.Recorder; socket is the default
// socket under the per-test TMUX_TMPDIR, whose per-user directory exists.
type ptEnv struct {
	d         mcp.Dispatcher
	rec       *tmuxfix.Recorder
	storePath string
	socket    string
}

// newPreTrustEnv builds a ptEnv with TMUX unset and no caller instance id.
func newPreTrustEnv(t *testing.T) *ptEnv {
	t.Helper()
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	t.Setenv("TMUX", "")
	tmpdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", tmpdir)
	sockDir := filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	dir := t.TempDir()
	e := &ptEnv{rec: tmuxfix.NewRecorder(), storePath: filepath.Join(dir, "state.db"),
		socket: filepath.Join(sockDir, "default")}
	client, err := api.New(api.Options{
		StorePath:       e.storePath,
		ConfigPath:      filepath.Join(dir, "absent", "config.toml"),
		CreateIfMissing: true,
		TmuxClient:      e.rec,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	e.d = mcp.NewLiveDispatcher(client)
	return e
}

// assertPreTrust checks the tool's JSON result carries pre_trust want.
func assertPreTrust(t *testing.T, obj map[string]json.RawMessage, want string) {
	t.Helper()
	if raw, ok := obj["pre_trust"]; !ok || string(raw) != `"`+want+`"` {
		t.Errorf("pre_trust = %s (present=%v); want %q", raw, ok, want)
	}
}

// assertCreates checks the Recorder saw exactly one create, so the launch ran.
func assertCreates(t *testing.T, rec *tmuxfix.Recorder) {
	t.Helper()
	if n := len(rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
		t.Errorf("creates = %d; want 1 (the launch proceeds whatever pre-trust did)", n)
	}
}

// TestPreTrustMCPSpawnResult: an MCP spawn with only cwd and extra-env reports
// ok and writes the entry when the .claude.json lacks it, and reports failed
// and still spawns when the file is missing.
func TestPreTrustMCPSpawnResult(t *testing.T) {
	cases := []struct {
		name     string
		withFile bool
		want     string
		trusted  bool
	}{
		{"ok when the entry is lacking", true, "ok", true},
		{"failed when .claude.json is missing", false, "failed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newPreTrustEnv(t)
			c := ptSeedConfig(t, tc.withFile)
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks: %v", err)
			}
			args, err := json.Marshal(map[string]any{"cwd": cwd, "extra-env": c.env()})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}

			obj := callToolText(t, e.d, "spawn", string(args))
			assertPreTrust(t, obj, tc.want)
			if id := obj["claude_instance_id"]; len(id) <= 2 {
				t.Errorf("claude_instance_id = %s; want the new row's id", id)
			}
			c.check(t, cwd, tc.trusted)
			assertCreates(t, e.rec)
		})
	}
}

// TestPreTrustMCPResumeResult: an MCP resume of a finished row reports skipped
// and leaves the .claude.json byte-identical when the row records the opt-out,
// ok and writes the entry when pre-trust is allowed, and failed but still
// relaunches when the file is missing.
func TestPreTrustMCPResumeResult(t *testing.T) {
	cases := []struct {
		name     string
		opts     []apitest.SpawnOption
		withFile bool
		want     string
		trusted  bool
	}{
		{"skipped for an opted-out row", []apitest.SpawnOption{apitest.WithNoPreTrust()}, true, "skipped", false},
		{"ok for an allowed row", nil, true, "ok", true},
		{"failed when .claude.json is missing", nil, false, "failed", false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newPreTrustEnv(t)
			c := ptSeedConfig(t, tc.withFile)
			id := fmt.Sprintf("mcp-pretrust-resume-%d", i)
			session := "sess-" + id
			cwd := t.TempDir()
			opts := append([]apitest.SpawnOption{
				apitest.WithTmuxSocket(e.socket),
				apitest.WithJsonlPath(apitest.SeedJsonlUnder(t, c.dir, cwd, session)),
				apitest.WithExtraEnv(c.env()),
			}, tc.opts...)
			if _, err := apitest.SeedSpawn(e.storePath, id, store.StateEnded, cwd, "off", session, false, opts...); err != nil {
				t.Fatalf("SeedSpawn(%s): %v", id, err)
			}

			obj := callToolText(t, e.d, "resume", `{"claude_instance_id":"`+id+`"}`)
			assertPreTrust(t, obj, tc.want)
			c.check(t, cwd, tc.trusted)
			assertCreates(t, e.rec)
			cols, err := apitest.ReadSpawnColumns(e.storePath, id)
			if err != nil {
				t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
			}
			if cols.State != store.StatePending {
				t.Errorf("state after resume = %v; want pending", cols.State)
			}
		})
	}
}

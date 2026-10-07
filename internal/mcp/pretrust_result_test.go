package mcp_test

// pretrust_result_test.go pins pre_trust on the MCP spawn and resume results
// (SR-22.6 "The field" and "Surfaces"; AC-SPN-08; b.7or's no_pre_trust): the
// result carries ok when the folder-trust entry was written, failed when the
// .claude.json is missing (the launch still runs) and skipped for the opt-out.
// Every case points CLAUDE_CONFIG_DIR at a per-test directory, so the real
// ~/.claude.json is never read or written. The verbs' full pre-trust matrix is
// pkg/api's.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
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

// assertPreTrust checks the tool's JSON result carries pre_trust want.
func assertPreTrust(t *testing.T, obj map[string]json.RawMessage, want string) {
	t.Helper()
	if raw, ok := obj["pre_trust"]; !ok || string(raw) != `"`+want+`"` {
		t.Errorf("pre_trust = %s (present=%v); want %q", raw, ok, want)
	}
}

// TestPreTrustMCPSpawnResult: an MCP spawn reports ok and writes the entry when
// the .claude.json lacks it, failed (and still spawns) when the file is
// missing, and with no_pre_trust skipped, the file byte-identical and the
// opt-out recorded on the row.
func TestPreTrustMCPSpawnResult(t *testing.T) {
	cases := []struct {
		name          string
		withFile      bool
		noPreTrust    bool
		want          string
		trusted       bool
		wantNoPreTrst string // the row's no_pre_trust
	}{
		{"ok when the entry is lacking", true, false, "ok", true, "0"},
		{"failed when .claude.json is missing", false, false, "failed", false, "0"},
		{"skipped with no_pre_trust", true, true, "skipped", false, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			c := ptSeedConfig(t, tc.withFile)
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks: %v", err)
			}
			extra := map[string]any{}
			if tc.noPreTrust {
				extra["no_pre_trust"] = true
			}

			obj := callToolText(t, e.d, "spawn", spawnArgs(t, cwd, c, extra))

			assertPreTrust(t, obj, tc.want)
			c.check(t, cwd, tc.trusted)
			assertCreates(t, e.rec)
			var id string
			if err := json.Unmarshal(obj["claude_instance_id"], &id); err != nil || id == "" {
				t.Fatalf("claude_instance_id = %s; want the new row's id", obj["claude_instance_id"])
			}
			if npt := fmt.Sprint(readColumns(t, e.storePath, id).NoPreTrust); npt != tc.wantNoPreTrst {
				t.Errorf("row no_pre_trust = %v; want %s", npt, tc.wantNoPreTrst)
			}
		})
	}
}

// TestPreTrustMCPResumeResult: an MCP resume of a finished row reports skipped
// and leaves the .claude.json byte-identical when the row records the opt-out,
// and ok and writes the entry when pre-trust is allowed; it relaunches either way.
func TestPreTrustMCPResumeResult(t *testing.T) {
	cases := []struct {
		name    string
		opts    []apitest.SpawnOption
		want    string
		trusted bool
	}{
		{"skipped for an opted-out row", []apitest.SpawnOption{apitest.WithNoPreTrust()}, "skipped", false},
		{"ok for an allowed row", nil, "ok", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			c := ptSeedConfig(t, true)
			id := fmt.Sprintf("mcp-pretrust-resume-%d", i)
			session := "sess-" + id
			cwd := t.TempDir()
			opts := append([]apitest.SpawnOption{apitest.WithTmuxSocket(e.socket),
				apitest.WithJsonlPath(apitest.SeedJsonlUnder(t, c.dir, cwd, session)), apitest.WithExtraEnv(c.env())}, tc.opts...)
			if _, err := apitest.SeedSpawn(e.storePath, id, store.StateEnded, cwd, "off", session, false, opts...); err != nil {
				t.Fatalf("SeedSpawn(%s): %v", id, err)
			}

			obj := callToolText(t, e.d, "resume", `{"claude_instance_id":"`+id+`"}`)

			assertPreTrust(t, obj, tc.want)
			c.check(t, cwd, tc.trusted)
			assertCreates(t, e.rec)
			if st := readColumns(t, e.storePath, id).State; st != store.StatePending {
				t.Errorf("state after resume = %v; want pending", st)
			}
		})
	}
}

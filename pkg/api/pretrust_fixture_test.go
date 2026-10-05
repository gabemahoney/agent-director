package api_test

// pretrust_fixture_test.go is the shared .claude.json fixture of the pre-trust
// tests (spawn_pretrust_test.go, resume_pretrust_test.go): a per-test config
// directory (absolute, or named by a relative path) seeded in one of three
// states, and the one checker of its trust entry.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// trustFile is the state of the .claude.json a test's config directory holds.
type trustFile int

const (
	trustLacksEntry trustFile = iota // present, trusting another folder only
	trustMissing                     // absent
	trustUnwritable                  // present, in a directory no temp file can be created in
)

// trustSeedJSON is the compact .claude.json a config directory starts with, so
// any rewrite (which indents) changes its bytes.
const trustSeedJSON = `{"numStartups":7,"projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`

// trustConfig is a config directory (as the extra env names it) and its
// .claude.json as seeded (before is nil when the file is absent).
type trustConfig struct {
	dir, path string
	before    []byte
}

// seedTrustConfig puts a .claude.json in state file into dir. The unwritable
// state (a 0500 directory) is skipped as root, where permissions do not bite.
func seedTrustConfig(t *testing.T, dir string, file trustFile) trustConfig {
	t.Helper()
	c := trustConfig{dir: dir, path: filepath.Join(dir, ".claude.json")}
	if file == trustMissing {
		return c
	}
	c.before = []byte(trustSeedJSON)
	if err := os.WriteFile(c.path, c.before, 0o600); err != nil {
		t.Fatalf("write %s: %v", c.path, err)
	}
	if file == trustUnwritable {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions; the unwritable .claude.json cannot be simulated")
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}
	return c
}

// seedRelativeTrustConfig is seedTrustConfig for the relative CLAUDE_CONFIG_DIR
// "rel" (b.nje): the process moves into a fresh temp dir until the test ends
// (cwdfix.Temp; not parallel) and file is seeded in its rel, where "rel"
// resolves from there.
func seedRelativeTrustConfig(t *testing.T, file trustFile) trustConfig {
	t.Helper()
	wd := cwdfix.Temp(t)
	if err := os.Mkdir(filepath.Join(wd, "rel"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := seedTrustConfig(t, filepath.Join(wd, "rel"), file)
	c.dir = "rel"
	return c
}

// extraEnv is the extra env pointing a launch's CLAUDE_CONFIG_DIR at c.
func (c trustConfig) extraEnv() map[string]string {
	return map[string]string{"CLAUDE_CONFIG_DIR": c.dir}
}

// env is the seed option pointing a row's CLAUDE_CONFIG_DIR at c.
func (c trustConfig) env() apitest.SpawnOption { return apitest.WithExtraEnv(c.extraEnv()) }

// reset writes the seeded bytes back, removing an entry a resume wrote.
func (c trustConfig) reset(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(c.path, c.before, 0o600); err != nil {
		t.Fatalf("reset %s: %v", c.path, err)
	}
}

// check asserts, at when, that the file trusts cwd (trusted) or is exactly as
// seeded (still absent when it was).
func (c trustConfig) check(t *testing.T, cwd string, trusted bool, when string) {
	t.Helper()
	got, err := os.ReadFile(c.path)
	if !trusted {
		if c.before == nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s: %s = %q (%v); want still absent", when, c.path, got, err)
			}
		} else if err != nil || string(got) != string(c.before) {
			t.Errorf("%s: %s = %q (%v); want unchanged %q", when, c.path, got, err, c.before)
		}
		return
	}
	var top struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err != nil || json.Unmarshal(got, &top) != nil || top.Projects[cwd]["hasTrustDialogAccepted"] != true {
		t.Errorf("%s: %s = %q (%v); want projects[%q].hasTrustDialogAccepted true", when, c.path, got, err, cwd)
	}
}

// checkPreTrustJSON asserts that result's JSON encoding carries pre_trust with
// the value want, pinning the field's tag.
func checkPreTrustJSON(t *testing.T, result any, want string) {
	t.Helper()
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal %#v: %v", result, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	if got, ok := m["pre_trust"]; !ok || got != want {
		t.Errorf("JSON %s: pre_trust = %v (present %v); want %q", b, got, ok, want)
	}
}

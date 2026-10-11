package mcp_test

// extra_env_key_test.go pins b.vpb on MCP, which passes extra_env through as
// sent: a key that is empty or holds '=' or a NUL byte is refused at spawn
// validation. pkg/api covers the other malformed keys, templates, resume and
// the advice.

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMCPSpawnRefusesInvalidEnvKey: the key CLAUDE_CONFIG_DIR=<dir>, which tmux
// would read as CLAUDE_CONFIG_DIR, is ErrReservedEnvKey quoting it and saying it
// contains '=', with no row, no tmux call and that directory's .claude.json
// untouched.
func TestMCPSpawnRefusesInvalidEnvKey(t *testing.T) {
	e, c := newReuseParamEnv(t)
	key := "CLAUDE_CONFIG_DIR=" + c.dir
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	data := toolErrorData(t, callTool(t, e.d, "spawn", paramJSON(t, map[string]any{"cwd": cwd, "extra_env": map[string]string{key: ""}})))

	want := "ErrReservedEnvKey: extra_env key " + strconv.Quote(key) + " is not a valid env-var name: it contains '='"
	if data.ErrName != "ErrReservedEnvKey" || !strings.HasPrefix(data.ErrDescription, want) {
		t.Errorf("refusal = %s: %q\nwant ErrReservedEnvKey starting %q", data.ErrName, data.ErrDescription, want)
	}
	assertNothingCreated(t, e.d, e.rec)
	c.check(t, cwd, false)
}

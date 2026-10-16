package installsh_test

// The order of install.sh's refusals (SR-11.1): the refusal for a host with
// no Claude Code, Codex or Cursor agent (exit 2) comes after every pre-flight check of
// config.toml and the store, so a host with no agent and a config install.sh
// cannot read is told about the config (exit 5), whatever its form, and fixes
// that first. TestAgentNeitherFound has the exit 2 refusal itself.

import (
	"path/filepath"
	"testing"
)

func TestNeitherAgentLosesToEveryConfigRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, h *moveHome)
		err    string
		stderr string // a phrase that names the config's problem
	}{
		{name: "bad-busy-timeout",
			setup: func(t *testing.T, h *moveHome) {
				h.Write(filepath.Join(h.New, "config.toml"), "[store]\nbusy_timeout_ms = \"soon\"\n", 0o600)
			},
			err: "ErrConfigMalformed", stderr: "busy_timeout_ms"},
		{name: "bad-launch-line",
			setup: func(t *testing.T, h *moveHome) {
				h.Write(filepath.Join(h.New, "config.toml"), "[launch]\nclaude_path = \"relative/claude\"\n", 0o600)
			},
			err: "ErrConfigMalformed", stderr: "the [launch] table"},
		{name: "bad-store-db-path",
			setup: func(t *testing.T, h *moveHome) {
				h.Write(filepath.Join(h.New, "config.toml"), "[store]\ndb_path = 42\n", 0o600)
			},
			err: "ErrConfigMalformed", stderr: "refusing to install"},
		{name: "config-the-migration-refuses",
			setup: func(t *testing.T, h *moveHome) {
				h.migLay(h.New, migStoreBelow, "[defaults]\ndisable_askuserquestion = true\ndisable_ask_user = true\n")
			},
			err: "ErrConfigMalformed", stderr: "sets the question-tool key more than once"},
		{name: "template-the-migration-refuses",
			setup: func(t *testing.T, h *moveHome) {
				h.migLay(h.New, migStoreBelow, migNewConfig, migTemplate("bad", "name = \"bad\"\nclaude_args = [\"a\"]\nclaude_args = [\"b\"]\n"))
			},
			err: "ErrTemplateMalformed", stderr: "sets claude_args more than once"},
		{name: "store-version-unreadable",
			setup: func(t *testing.T, h *moveHome) {
				h.Write(filepath.Join(h.New, "state.db"), "this is not a database\n", 0o600)
			},
			err: "ErrVersionUnreadable", stderr: "reading state.db's schema version FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			tc.setup(t, h)
			before := h.Snapshot()

			res := h.Install()

			moveWant{Code: 5, Err: tc.err, Stderr: []string{tc.stderr, "Nothing was installed or changed."},
				NoStderr: []string{"no supported agent (Claude Code, Codex or Cursor)"}, NoStdout: []string{"no supported agent (Claude Code, Codex or Cursor)"}}.Check(t, h, res)
			if d := moveDiff(before, h.Snapshot()); d != "" {
				t.Errorf("a refused install changed HOME:\n%s", d)
			}
		})
	}
}

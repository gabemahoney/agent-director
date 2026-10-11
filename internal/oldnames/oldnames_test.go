package oldnames_test

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"testing"

	"github.com/gabemahoney/agent-director/internal/oldnames"
)

// TestFind: every surface finds its old names (Config and Template in any
// letter case, CLI and MCP exactly) and nothing else (SR-4.3).
func TestFind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		surface oldnames.Surface
		old     string
		want    string // the new name; "" when not found
	}{
		{"cli instance id", oldnames.CLI, "claude-instance-id", "instance-id"},
		{"cli args", oldnames.CLI, "claude-args", "agent-args"},
		{"mcp instance id", oldnames.MCP, "claude_instance_id", "instance_id"},
		{"mcp args", oldnames.MCP, "claude_args", "agent_args"},
		{"config key", oldnames.Config, "disable_askuserquestion", "disable_ask_user"},
		{"config key upper case", oldnames.Config, "DISABLE_ASKUSERQUESTION", "disable_ask_user"},
		{"config key mixed case", oldnames.Config, "Disable_AskUserQuestion", "disable_ask_user"},
		{"template key", oldnames.Template, "claude_args", "agent_args"},
		{"template key upper case", oldnames.Template, "CLAUDE_ARGS", "agent_args"},
		{"template key mixed case", oldnames.Template, "Claude_Args", "agent_args"},
		{"cli is case sensitive", oldnames.CLI, "Claude-Args", ""},
		{"mcp is case sensitive", oldnames.MCP, "CLAUDE_INSTANCE_ID", ""},
		{"cli does not take the mcp spelling", oldnames.CLI, "claude_args", ""},
		{"mcp does not take the cli spelling", oldnames.MCP, "claude-args", ""},
		{"config has no template name", oldnames.Config, "claude_args", ""},
		{"template has no config name", oldnames.Template, "disable_askuserquestion", ""},
		{"a new name is not old", oldnames.MCP, "instance_id", ""},
		{"an unknown name", oldnames.Config, "nope", ""},
		{"an empty name", oldnames.CLI, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := oldnames.Find(tc.surface, tc.old)
			if ok != (tc.want != "") {
				t.Fatalf("Find(%v, %q) found = %v; want %v", tc.surface, tc.old, ok, tc.want != "")
			}
			if ok && (got.New != tc.want || got.Surface != tc.surface) {
				t.Errorf("Find(%v, %q) = %+v; want new name %q on that surface", tc.surface, tc.old, got, tc.want)
			}
		})
	}
}

// TestEntryTexts: the three refusal texts, word for word.
func TestEntryTexts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                       string
		entry                      oldnames.Entry
		flagText, keyText, cfgText string
	}{
		{
			"flag entry",
			oldnames.Entry{Surface: oldnames.CLI, Old: "claude-instance-id", New: "instance-id"},
			"--claude-instance-id is no longer accepted; use --instance-id",
			"claude-instance-id is no longer accepted; use instance-id",
			"[defaults] claude-instance-id is no longer accepted; use instance-id",
		},
		{
			"mcp entry",
			oldnames.Entry{Surface: oldnames.MCP, Old: "claude_args", New: "agent_args"},
			"--claude_args is no longer accepted; use --agent_args",
			"claude_args is no longer accepted; use agent_args",
			"[defaults] claude_args is no longer accepted; use agent_args",
		},
		{
			"config entry",
			oldnames.Entry{Surface: oldnames.Config, Old: "disable_askuserquestion", New: "disable_ask_user"},
			"--disable_askuserquestion is no longer accepted; use --disable_ask_user",
			"disable_askuserquestion is no longer accepted; use disable_ask_user",
			"[defaults] disable_askuserquestion is no longer accepted; use disable_ask_user",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.entry.FlagText(); got != tc.flagText {
				t.Errorf("FlagText() = %q; want %q", got, tc.flagText)
			}
			if got := tc.entry.KeyText(); got != tc.keyText {
				t.Errorf("KeyText() = %q; want %q", got, tc.keyText)
			}
			if got := tc.entry.ConfigText(); got != tc.cfgText {
				t.Errorf("ConfigText() = %q; want %q", got, tc.cfgText)
			}
		})
	}
}

// newFlagSet returns a quiet FlagSet that defines each named string flag.
func newFlagSet(names ...string) *flag.FlagSet {
	fs := flag.NewFlagSet("verb", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, n := range names {
		fs.String(n, "", "")
	}
	return fs
}

// TestRefuseFlag: RefuseFlag answers only for an old flag the FlagSet's own
// parse stopped at, when that FlagSet defines the new flag; otherwise the
// caller keeps the original error.
func TestRefuseFlag(t *testing.T) {
	t.Parallel()
	const idText = "--claude-instance-id is no longer accepted; use --instance-id"
	const argsText = "--claude-args is no longer accepted; use --agent-args"
	cases := []struct {
		name  string
		flags []string
		args  []string
		want  string // "" when RefuseFlag must report false
	}{
		{"old id, two tokens", []string{"instance-id"}, []string{"--claude-instance-id", "x"}, idText},
		{"old id, one dash", []string{"instance-id"}, []string{"-claude-instance-id", "x"}, idText},
		{"old id, equals form", []string{"instance-id"}, []string{"--claude-instance-id=x"}, idText},
		{"old args, equals form", []string{"agent-args"}, []string{"--claude-args=--foo"}, argsText},
		{"old flag after a valid one", []string{"cwd", "instance-id"}, []string{"--cwd", "/tmp", "--claude-instance-id=x"}, idText},
		{"new flag not defined keeps the unknown-flag error", []string{"cwd"}, []string{"--claude-args=--foo"}, ""},
		{"only the other new flag is defined", []string{"agent-args"}, []string{"--claude-instance-id=x"}, ""},
		{"an unknown flag that is not old", []string{"instance-id"}, []string{"--bogus"}, ""},
		{"a flag missing its value", []string{"instance-id"}, []string{"--instance-id"}, ""},
		{"old flag is a value of another flag", []string{"instance-id", "text"}, []string{"--text", "--claude-instance-id"}, ""},
		{"old flag after --", []string{"instance-id"}, []string{"--", "--claude-instance-id"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := newFlagSet(tc.flags...)
			err := fs.Parse(tc.args)
			got, ok := oldnames.RefuseFlag(fs, err)
			if ok != (tc.want != "") || got != tc.want {
				t.Errorf("RefuseFlag after Parse(%q) = (%q, %v); want (%q, %v); parse error %v", tc.args, got, ok, tc.want, tc.want != "", err)
			}
		})
	}
}

// TestRefuseFlagOtherErrors: an error that is not an unknown-flag error is
// never refused.
func TestRefuseFlagOtherErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"help", flag.ErrHelp},
		{"wrapped help", fmt.Errorf("verb: %w", flag.ErrHelp)},
		{"other text", errors.New("boom")},
		{"prefix not at the start", errors.New("verb: flag provided but not defined: -claude-args")},
		{"old name without the prefix", errors.New("-claude-args")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, ok := oldnames.RefuseFlag(newFlagSet("instance-id", "agent-args"), tc.err); ok || got != "" {
				t.Errorf("RefuseFlag(%v) = (%q, %v); want (\"\", false)", tc.err, got, ok)
			}
		})
	}
}

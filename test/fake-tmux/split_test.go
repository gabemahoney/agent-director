package main

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
)

// TestSplitCommandsLikeTmux checks the fake splits raw argv on a final
// unescaped ";" as real tmux does, and unescapes a final `\;`.
func TestSplitCommandsLikeTmux(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want [][]string
	}{
		{"final semicolon ends command keeping text", []string{"send-keys", "-l", "--", "a;", "kill-pane"},
			[][]string{{"send-keys", "-l", "--", "a"}, {"kill-pane"}}},
		{"escaped final semicolon is one argument", []string{"send-keys", "-l", "--", `a\;`},
			[][]string{{"send-keys", "-l", "--", "a;"}}},
		{"escaped lone semicolon", []string{"send-keys", "-l", "--", `\;`},
			[][]string{{"send-keys", "-l", "--", ";"}}},
		{"escaped backslash before escaped semicolon", []string{"send-keys", "-l", "--", `a\\;`},
			[][]string{{"send-keys", "-l", "--", `a\;`}}},
		{"escaped semicolon after space", []string{"send-keys", "-l", "--", `a \;`},
			[][]string{{"send-keys", "-l", "--", "a ;"}}},
		{"escaped semicolon after dash text", []string{"send-keys", "-l", "--", `-x\;`},
			[][]string{{"send-keys", "-l", "--", "-x;"}}},
		{"escaped semicolon after semicolon", []string{"send-keys", "-l", "--", `;\;`},
			[][]string{{"send-keys", "-l", "--", ";;"}}},
		{"inner semicolons kept", []string{"send-keys", "-l", "--", ";a", "a;b"},
			[][]string{{"send-keys", "-l", "--", ";a", "a;b"}}},
		{"create chain separator", []string{"new-session", "-d", "--", "claude", ";", "set-option", "-F"},
			[][]string{{"new-session", "-d", "--", "claude"}, {"set-option", "-F"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitCommands(tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitCommands(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestEmptyCommandRejected checks argv holding an empty command exits 2
// (real tmux drops it silently; the fake fails loudly on purpose).
func TestEmptyCommandRejected(t *testing.T) {
	t.Setenv(faketmuxfix.EnvLog, "")
	for name, cmds := range map[string][]string{
		"unescaped final semicolon then nothing": {"send-keys", "-t", "%0", "-l", "--", "a;"},
		"lone semicolon text":                    {"send-keys", "-t", "%0", "-l", "--", ";"},
		"leading separator":                      {";", "list-sessions"},
		"double separator":                       {"list-sessions", ";", ";", "list-panes"},
	} {
		t.Run(name, func(t *testing.T) {
			argv := append([]string{"-S", t.TempDir() + "/sock"}, cmds...)
			if got := socketMain(argv); got != 2 {
				t.Errorf("socketMain(%q) = %d, want 2", argv, got)
			}
		})
	}
}

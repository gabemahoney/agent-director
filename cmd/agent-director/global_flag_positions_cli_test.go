package main_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.t1b: words after spawn's -- are native arguments, never global flags.
// A nonempty tail must also replace template args (b.hb2).
func TestCLISpawnGlobalWordsAfterSeparator(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name     string
		template bool
		args     func(decoy string) []string
	}{
		{"control model", false, func(string) []string { return []string{"--model", "m"} }},
		{"home two token", false, func(d string) []string { return []string{"--home", d} }},
		{"store path equals", false, func(d string) []string { return []string{"--store-path=" + filepath.Join(d, "s.db")} }},
		{"template global only tail", true, func(string) []string { return []string{"--create-if-missing=true"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd, decoy := t.TempDir(), t.TempDir(), t.TempDir()
			args := []string{"spawn", "--cwd", cwd, "--agent", "claude", "--no-pre-trust"}
			if tc.template {
				seedTemplate(t, home, "T", apitest.TemplateFields{Agent: "claude", AgentArgs: []string{"--model", "template-model"}})
				args = append(args, "--template", "T")
			}
			want := tc.args(decoy)
			args = append(append(args, "--"), want...)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			t.Logf("spawn %q: exit=%d stdout=%q stderr=%q decoy_tree=%q", want, code, stdout, stderr, homeTree(t, decoy))
			assertHomeTree(t, decoy)
			var res spawnResult
			if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &res) != nil || res.InstanceID == "" {
				t.Fatalf("spawn: exit=%d stdout=%q stderr=%q; want 0 and an id", code, stdout, stderr)
			}
			row := getRow(t, home, fakeDir, res.InstanceID)
			if got := jsonOf(t, row["agent_args"]); got != jsonOf(t, want) {
				t.Errorf("agent_args=%s; want %s", got, jsonOf(t, want))
			}
		})
	}
}

// b.t1b: get's id and send-keys' text own their values, including globals.
// An unknown id must reach lookup without opening a decoy store or HOME.
func TestCLIFlagValuesNamedLikeGlobalFlags(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const id = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	for _, tc := range []struct {
		name string
		args func(decoy string) []string
	}{
		{"control get non global", func(string) []string { return []string{"get", "--instance-id", "--not-global=x"} }},
		{"get store path", func(d string) []string {
			return []string{"get", "--instance-id", "--store-path=" + filepath.Join(d, "s.db")}
		}},
		{"get home", func(d string) []string { return []string{"get", "--instance-id", "--home=" + d} }},
		{"control send non global", func(string) []string {
			return []string{"send-keys", "--instance-id", id, "--text", "--not-global"}
		}},
		{"send store path", func(d string) []string {
			return []string{"send-keys", "--instance-id", id, "--text", "--store-path=" + filepath.Join(d, "s.db")}
		}},
		{"send home two token", func(string) []string {
			return []string{"send-keys", "--instance-id", id, "--text", "--home"}
		}},
		{"send tmux command", func(d string) []string {
			return []string{"send-keys", "--instance-id", id, "--text", "--tmux-command=" + filepath.Join(d, "not-tmux")}
		}},
		{"send create if missing", func(string) []string {
			return []string{"send-keys", "--instance-id", id, "--text", "--create-if-missing=false"}
		}},
		{"control equals text home", func(string) []string {
			return []string{"send-keys", "--instance-id", id, "--text=--home"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, decoy := t.TempDir(), t.TempDir()
			args := tc.args(decoy)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			t.Logf("%q: exit=%d stdout=%q stderr=%q decoy_tree=%q", args, code, stdout, stderr, homeTree(t, decoy))
			assertHomeTree(t, decoy)
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrSpawnNotFound")
			if args[0] == "get" && env.ErrDescription != args[2] {
				t.Errorf("missing id = %q; want exactly %q", env.ErrDescription, args[2])
			}
		})
	}
}

// b.t1b: --tmux-command used as a string flag's value must not change the
// tmux executable. A successful spawn exercises the selected fake end to end.
func TestCLISpawnTmuxCommandAsFlagValue(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	id := spawnOK(t, home, fakeDir, "--cwd", t.TempDir(), "--tmux-session-name", "--tmux-command", "--no-pre-trust")
	if got := getRow(t, home, fakeDir, id)["tmux_session_name"]; got != "--tmux-command" {
		t.Errorf("tmux_session_name=%q; want --tmux-command", got)
	}
	assertInvocationKinds(t, home, "new-session")
}

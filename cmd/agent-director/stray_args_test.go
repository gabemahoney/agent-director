package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// b.hb2, b.6ir: every verb here refuses leftovers before dispatch, including
// flags hidden behind the first word. Spawn exempts only agent args after --;
// TestCLISpawnDelimitedAgentArgs and TestSpawnCLITemplateAgentArgs cover those.
// New manifest verbs need a case here.
func TestCLIFlagVerbsRejectStrayArguments(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const token = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	exempt := []string{"get-many", "help", "version", "hook", "cursor-host", "tool-guard"}
	base := map[string][]string{
		"spawn":              {"--cwd", "/tmp", "--agent", "claude", "--no-pre-trust"},
		"status":             {"--instance-id", token},
		"get":                {"--instance-id", token},
		"send-keys":          {"--instance-id", token},
		"force-delivery":     {"--instance-id", token},
		"get-force":          {"--force-id", token},
		"get-delivery":       {"--caller-uuid", token, "--delivery-request-id", "message"},
		"read-pane":          {"--instance-id", token},
		"kill":               {"--instance-id", token},
		"pause":              {"--instance-id", token},
		"list":               {},
		"list-subagents":     {"--instance-id", token},
		"make-template":      {"--name", "stray-test", "--agent", "claude"},
		"decide":             {"--instance-id", token, "--request-token", token, "--decision", "allow"},
		"get-permission":     {"--request-token", token},
		"record-pane-answer": {"--request-token", token, "--as", "allow", "--expect-pane-sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"resume":             {"--instance-id", token},
		"sweep":              {},
		"find-missing":       {},
		"expire":             {"--older-than", "1000d"},
		"serve":              {"--stdio"},
		"trail-emit":         {"relay-attempt", "--token", token, "--endpoint", "/tmp/relay.sock", "--outcome", "200", "--instance-id", token},
	}
	for _, verb := range manifest.Verbs {
		if slices.Contains(exempt, verb.Name) {
			continue
		}
		t.Run(verb.Name, func(t *testing.T) {
			flags, ok := base[verb.Name]
			if !ok {
				t.Fatalf("add a minimal valid call for manifest verb %q, or document its positional exemption", verb.Name)
			}
			for _, suffix := range [][]string{{"STRAY"}, {"STRAY", "--no-such-flag"}} {
				t.Run(suffix[len(suffix)-1], func(t *testing.T) {
					home := t.TempDir()
					socket := spawnSocket(t, home)
					faketmuxfix.Tables{}.Write(t, socket, fakeTable())
					var effects noActionEffects
					if verb.Name == "trail-emit" {
						effects = observeNoActionEffects(t, home, "")
					}
					args := append([]string{verb.Name}, flags...)
					args = append(args, suffix...)
					// stdin stays open: serve must refuse, rather than start a
					// server that only happens to exit successfully on EOF.
					stdout, stderr, code, timedOut := execCLI(t, cliOpts{
						env:      append(fakeTmuxEnv(t, home, fakeDir), "HOME="+home),
						holdOpen: true, deadline: surfaceDeadline,
					}, args...)
					if timedOut {
						t.Fatalf("%q started long-lived work instead of refusing leftovers; stderr=%q", args, stderr)
					}
					assertStrayArguments(t, stdout, stderr, code, "STRAY")
					if verb.Name == "trail-emit" {
						effects.assertUnchanged(t, 0)
					}
				})
			}
		})
	}
}

// b.6ir: a prompt before -- must not hide --template, create a row, or reach
// tmux. Without --agent, argument refusal must precede ErrAgentMissing too.
// A later -- cannot rescue a bare word; -- used as a string value is not one.
func TestCLISpawnRejectsUndelimitedAgentArgs(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name        string
		args, words []string
		refusalOnly bool
	}{
		{"explicit agent", []string{"--cwd", "<cwd>", "--agent", "claude", "prompt", "--template", "T"}, []string{"prompt", "--template", "T"}, false},
		{"template agent hidden", []string{"--cwd", "<cwd>", "prompt", "--template", "T"}, []string{"prompt", "--template", "T"}, false},
		{"later separator does not rescue prompt", []string{"--cwd", "<cwd>", "--agent", "claude", "prompt", "--", "--model", "M"}, []string{"prompt", "--", "--model", "M"}, false},
		{"cwd consumes dashdash as its value", []string{"--cwd", "--", "x"}, []string{"x"}, true},
		{"bare boolean does not consume prompt", []string{"--cwd", "<cwd>", "--agent", "claude", "--no-pre-trust", "prompt"}, []string{"prompt"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			seedTemplate(t, home, "T", apitest.TemplateFields{Agent: "claude", AgentArgs: []string{"--model", "template-model"}})
			args := []string{"spawn"}
			for _, arg := range tc.args {
				if arg == "<cwd>" {
					arg = cwd
				}
				args = append(args, arg)
			}
			var effects noActionEffects
			if !tc.refusalOnly {
				effects = observeNoActionEffects(t, home, "")
			}
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			if !tc.refusalOnly {
				if calls := fakeTmuxInvocations(t, home); len(calls) != 0 {
					t.Errorf("undelimited agent args invoked tmux: %q", calls)
				}
				if ids := listIDs(t, home, fakeDir); len(ids) != 0 {
					t.Errorf("undelimited agent args stored rows: %q", ids)
				}
			}
			for _, word := range tc.words {
				assertStrayArguments(t, stdout, stderr, code, word)
			}
			if !tc.refusalOnly {
				effects.assertUnchanged(t, 0)
			}
		})
	}
}

// b.6ir: only words after -- are agent args, including words that look like
// spawn flags. A template before -- still supplies its agent and settings.
// String flag values may themselves be -- or look like flags; boolean flags
// consume no following value. Further separators belong to the native argv.
func TestCLISpawnDelimitedAgentArgs(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name        string
		flags, args []string
		sessionName string
	}{
		{"prompt replaces template args", nil, []string{"prompt"}, ""},
		{"later template flag belongs to agent", nil, []string{"prompt", "--template", "T"}, ""},
		{"equals string value is dashdash", []string{"--tmux-session-name=--"}, []string{"x"}, "--"},
		{"string value looks like a flag", []string{"--tmux-session-name", "--cwd"}, []string{"x"}, "--cwd"},
		{"second separator is native argument", nil, []string{"--", "native"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			seedTemplate(t, home, "T", apitest.TemplateFields{
				Agent: "claude", AgentArgs: []string{"--model", "template-model"},
				RelayMode: "off", Labels: map[string]string{"role": "b6ir-template"},
			})
			args := append([]string{"spawn", "--cwd", cwd, "--template", "T", "--no-pre-trust"}, tc.flags...)
			args = append(args, "--")
			args = append(args, tc.args...)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			var result spawnResult
			if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &result) != nil || result.InstanceID == "" {
				t.Fatalf("delimited spawn: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			row := getRow(t, home, fakeDir, result.InstanceID)
			if got := jsonOf(t, row["agent_args"]); got != jsonOf(t, tc.args) {
				t.Errorf("agent_args=%s; want %s", got, jsonOf(t, tc.args))
			}
			if row["agent"] != "claude" || row["relay_mode"] != "off" || jsonOf(t, row["labels"]) != `{"role":"b6ir-template"}` {
				t.Errorf("template settings lost: %v", row)
			}
			if tc.sessionName != "" && row["tmux_session_name"] != tc.sessionName {
				t.Errorf("tmux_session_name=%q; want string flag value %q", row["tmux_session_name"], tc.sessionName)
			}
		})
	}
}

// b.hb2: extra ids must not pause the first live row or hide a malformed
// safety flag. A Codex row makes an accidental /exit visible to fake tmux.
func TestCLIStrayIDsLeaveLiveRowUntouched(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name, verb string
		extra      []string
	}{
		{"pause multiple ids", "pause", []string{"B", "C"}},
		{"status extra id", "status", []string{"B"}},
		{"pause hidden malformed flag", "pause", []string{"B", "--only-if-waiting=notabool"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, store.StateWaiting, apitest.WithAgent("codex"))
			faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "ready\n")))
			if err := os.WriteFile(filepath.Join(directorDir(home), "config.toml"), []byte("[pause]\ntimeout_seconds = 1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before := rowColumns(t, home, id)
			effects := observeNoActionEffects(t, home, id)
			args := append([]string{tc.verb, "--instance-id", id}, tc.extra...)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			if after := rowColumns(t, home, id); !reflect.DeepEqual(after, before) {
				t.Errorf("rejected %s changed the live row: before=%+v after=%+v", tc.verb, before, after)
			}
			for _, argv := range fakeTmuxInvocations(t, home) {
				if slices.Contains(argv, "send-keys") || slices.Contains(argv, "paste-buffer") {
					t.Errorf("rejected %s sent tmux input: %q", tc.verb, argv)
				}
			}
			assertStrayArguments(t, stdout, stderr, code, "B")
			effects.assertUnchanged(t, 0)
		})
	}
}

// b.hb2, b.x3a: positional text is a refusal, never an Enter-only send.
// Claude must name the supplied word rather than blame missing text. A valid
// --text plus a stray word must not admit a covered Message (b.hb2 T1).
func TestCLISendKeysRejectsPositionalText(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	type sendCase struct {
		args  []string
		stray string
	}
	for _, agent := range []string{"droid", "codex", "claude"} {
		cases := []sendCase{
			{[]string{"hello world"}, "hello world"},
			{[]string{"hello", "--no-enter"}, "hello"},
		}
		if agent == "claude" {
			cases = append(cases, sendCase{[]string{"--text", "hello world", "STRAY"}, "STRAY"})
		}
		for _, tc := range cases {
			t.Run(agent+"/"+tc.stray, func(t *testing.T) {
				home, id, socket := seedRowOnSocket(t, store.StateWaiting, apitest.WithAgent(agent))
				faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "ready\n")))
				before := rowColumns(t, home, id)
				effects := observeNoActionEffects(t, home, id)
				if tc.stray == "STRAY" && effects.busy.Coverage != store.BusySendCovered {
					t.Fatal("valid-text refusal case needs a covered Claude row")
				}
				args := append([]string{"send-keys", "--instance-id", id}, tc.args...)
				stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
				if after := rowColumns(t, home, id); !reflect.DeepEqual(after, before) {
					t.Errorf("rejected send changed row: before=%+v after=%+v", before, after)
				}
				// Refusal precedes even looking at tmux, so Enter, literal
				// typing and paste paths are all excluded.
				if calls := fakeTmuxInvocations(t, home); len(calls) != 0 {
					t.Errorf("rejected send called tmux: %q", calls)
				}
				assertStrayArguments(t, stdout, stderr, code, tc.stray)
				effects.assertUnchanged(t, 0)
			})
		}
	}
}

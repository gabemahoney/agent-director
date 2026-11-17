package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/droidfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

func TestEnvelopeParityDroidGuardRefusal(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "spawn", true: "reuse"}[reuse], func(t *testing.T) {
			home, id, _ := seedRowOnSocket(t, "ended", apitest.WithAgent("droid"))
			before := rowColumns(t, home, id)
			apitest.SeedErrLaunchGuardRefused(t, directorDir(home))
			args := []string{"spawn", "--agent", "droid", "--cwd", home}
			params := map[string]any{"agent": "droid", "cwd": home, "agent_args": []string{"--model", "model"}}
			if reuse {
				args = append(args, "--instance-id", id, "--reuse-finished")
				params["instance_id"], params["reuse_finished"] = id, true
			}
			args = append(args, "--", "--model", "model")
			srv := startServe(t, home)
			t.Cleanup(srv.kill)
			srv.initialize(t)
			stdout, stderr, code := runCLIWithHome(t, home, args...)
			cli := assertOnlyEnvelope(t, stdout, stderr, code, "ErrLaunchGuardRefused")
			mcp := srv.callTool(t, "spawn", jsonOf(t, params))
			const want = "Droid launch Guard refused: line 3: the entry is not an absolute or ~/ path; an operator must fix the Path List File named by [launch] droid_secret_paths_file or the Guard installation and environment (see docs/experimental.md#droid-secret-paths); no row, session, trust entry or launch file was written and nothing was launched"
			if cli.ErrDescription != want || mcp.Error == nil || mcp.Error.Data.ErrName != cli.ErrName || errnames.TrimNamePrefix(mcp.Error.Data.ErrName, mcp.Error.Message) != want {
				t.Fatalf("CLI=%+v MCP=%+v; want %q", cli, mcp.Error, want)
			}
			if !reflect.DeepEqual(rowColumns(t, home, id), before) {
				t.Fatal("refusal changed the old row")
			}
			if ids := srv.callToolOK(t, "list", `{}`); strings.Count(ids, `"instance_id"`) != 1 {
				t.Fatalf("refusal created a row: %s", ids)
			}
			for _, rel := range []string{"launch", "templates"} {
				if _, err := os.Stat(filepath.Join(directorDir(home), rel)); !os.IsNotExist(err) {
					t.Fatalf("refusal created %s: %v", rel, err)
				}
			}
			srv.stop(t)
		})
	}
}

func TestEnvelopeParityDroidRulesRefusal(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "spawn", true: "reuse"}[reuse], func(t *testing.T) {
			home, id, _ := seedRowOnSocket(t, "ended", apitest.WithAgent("droid"))
			before := rowColumns(t, home, id)
			apitest.SeedErrPermissionRulesRefused(t, directorDir(home))
			args := []string{"spawn", "--agent", "droid", "--cwd", home}
			params := map[string]any{"agent": "droid", "cwd": home, "agent_args": []string{"--model", "model"}}
			if reuse {
				args = append(args, "--instance-id", id, "--reuse-finished")
				params["instance_id"], params["reuse_finished"] = id, true
			}
			args = append(args, "--", "--model", "model")
			srv := startServe(t, home)
			t.Cleanup(srv.kill)
			srv.initialize(t)
			stdout, stderr, code := runCLIWithHome(t, home, args...)
			cli := assertOnlyEnvelope(t, stdout, stderr, code, "ErrPermissionRulesRefused")
			mcp := srv.callTool(t, "spawn", jsonOf(t, params))
			const want = "Droid Rule Set refused: rules[0] is an allow rule whose program is not tail, head or an absolute path to a program other than a shell, interpreter or command runner; an operator must fix the Rule Set File [launch] droid_rules_path names, or the droid program (see docs/experimental.md#droid-rules); no row, session, trust entry or launch file was written and nothing was launched"
			if cli.ErrDescription != want || mcp.Error == nil || mcp.Error.Data.ErrName != cli.ErrName || errnames.TrimNamePrefix(mcp.Error.Data.ErrName, mcp.Error.Message) != want {
				t.Fatalf("CLI=%+v MCP=%+v; want %q", cli, mcp.Error, want)
			}
			if !reflect.DeepEqual(rowColumns(t, home, id), before) {
				t.Fatal("refusal changed the old row")
			}
			if ids := srv.callToolOK(t, "list", `{}`); strings.Count(ids, `"instance_id"`) != 1 {
				t.Fatalf("refusal created a row: %s", ids)
			}
			for _, rel := range []string{"launch", "templates"} {
				if _, err := os.Stat(filepath.Join(directorDir(home), rel)); !os.IsNotExist(err) {
					t.Fatalf("refusal created %s: %v", rel, err)
				}
			}
			srv.stop(t)
		})
	}
}

// One table covers the missing Droid caller paths on both wire transports.
// The configured Rule Set is valid: caller policy must still answer first.
func TestEnvelopeParityDroidCallerRules(t *testing.T) {
	type refusal struct {
		name, permission, want string
		args                   []string
	}
	cases := []refusal{
		{"allow", "allow", "ErrPermissionsUnsupported", nil},
		{"deny", "deny", "ErrPermissionsUnsupported", nil},
		{"ask", "ask", "ErrPermissionsUnsupported", nil},
		{"settings", "", "ErrSpawnDeniedFlag", []string{"--settings", "/caller/settings.json"}},
		{"joined model", "", "ErrSpawnDeniedFlag", []string{"--model=model"}},
		{"duplicate model", "", "ErrSpawnDeniedFlag", []string{"--model", "model", "--model", "other"}},
		{"positional", "", "ErrSpawnDeniedFlag", []string{"prompt"}},
		{"rule field", "", "ErrTemplateMalformed", nil},
	}
	fakeDir := buildFakeTmux(t)
	for _, mode := range []string{"spawn", "reuse", "make-template", "template", "template reuse"} {
		for _, tc := range cases {
			// Inheritance uses the same argument validator: settings and a
			// permission entry suffice; other spellings are covered directly.
			inherited := strings.HasPrefix(mode, "template")
			if inherited && tc.name != "settings" && tc.name != "ask" && tc.name != "rule field" {
				continue
			}
			if !inherited && tc.name == "rule field" {
				continue
			}
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				home, id, _ := seedRowOnSocket(t, "ended", apitest.WithAgent("droid"))
				before := rowColumns(t, home, id)
				rules := apitest.SeedErrPermissionRulesRefused(t, directorDir(home))
				if err := os.WriteFile(rules, []byte(droidfix.RuleSet("operator")), 0o600); err != nil {
					t.Fatal(err)
				}
				verb, tool := "spawn", "spawn"
				argv := []string{"--agent", "droid", "--cwd", home}
				params := map[string]any{"agent": "droid", "cwd": home}
				if mode == "reuse" || mode == "template reuse" {
					argv = append(argv, "--instance-id", id, "--reuse-finished")
					params["instance_id"], params["reuse_finished"] = id, true
				}
				agentArgs := tc.args
				if agentArgs == nil {
					agentArgs = []string{"--model", "model"}
				}
				if inherited {
					fields := apitest.TemplateFields{Agent: "droid", AgentArgs: agentArgs}
					if tc.name == "rule field" {
						fields.RawKeys = []string{`droid_rules_path = "/caller/rules.json"`}
					}
					if tc.permission != "" {
						fields.Permissions = &config.TemplatePermissions{Ask: []string{"Bash"}}
					}
					seedTemplate(t, home, "caller", fields)
					argv = append(argv, "--template", "caller")
					params["template"] = "caller"
				} else {
					if tc.permission != "" {
						argv = append(argv, "--"+tc.permission, "Bash")
						params[tc.permission] = []string{"Bash"}
					}
					params["agent_args"] = agentArgs
					if mode == "make-template" {
						verb, tool = "make-template", "make_template"
						argv = append(argv, "--name", "refused")
						params["name"] = "refused"
						for _, a := range agentArgs {
							argv = append(argv, "--agent-args", a)
						}
					} else {
						argv = append(append(argv, "--"), agentArgs...)
					}
				}
				templatePath := filepath.Join(directorDir(home), "templates", "caller.toml")
				templateBefore, _ := os.ReadFile(templatePath)
				srv := startServe(t, home, "PATH="+fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_TMUX_LOG="+filepath.Join(home, "fake-tmux.log"))
				t.Cleanup(srv.kill)
				srv.initialize(t)
				out, stderr, code := runSpawnCLI(t, home, fakeDir, append([]string{verb}, argv...)...)
				cli := assertOnlyEnvelope(t, out, stderr, code, tc.want)
				mcp := srv.callTool(t, tool, jsonOf(t, params))
				if mcp.Error == nil || mcp.Error.Data.ErrName != tc.want || errnames.TrimNamePrefix(mcp.Error.Data.ErrName, mcp.Error.Message) != cli.ErrDescription {
					t.Fatalf("CLI=%+v MCP=%+v", cli, mcp.Error)
				}
				if !reflect.DeepEqual(rowColumns(t, home, id), before) || len(listIDs(t, home, fakeDir)) != 1 {
					t.Fatal("caller refusal changed rows")
				}
				assertInvocationKinds(t, home)
				if inherited {
					after, err := os.ReadFile(templatePath)
					if err != nil || string(after) != string(templateBefore) {
						t.Fatalf("refusal rewrote the inherited template: %v", err)
					}
				}
				for _, rel := range []string{"launch", "templates/refused.toml"} {
					if _, err := os.Stat(filepath.Join(directorDir(home), rel)); !os.IsNotExist(err) {
						t.Fatalf("caller refusal created %s: %v", rel, err)
					}
				}
				srv.stop(t)
			})
		}
	}
}

// PM-1: an MCP server keeps its configured path until restart, but reads
// the bytes at that path on every launch. CLI calls reload the path too.
func TestEnvelopeDroidRulesConfigLifetime(t *testing.T) {
	home := t.TempDir()
	rules := apitest.SeedErrPermissionRulesRefused(t, directorDir(home))
	srv := startServe(t, home)
	t.Cleanup(srv.kill)
	srv.initialize(t)
	params := jsonOf(t, map[string]any{"agent": "droid", "cwd": home, "agent_args": []string{"--model", "model"}})
	initial := srv.callTool(t, "spawn", params)
	if initial.Error == nil || !strings.Contains(initial.Error.Message, "rules[0]") {
		t.Fatalf("initial refusal=%+v", initial.Error)
	}
	if err := os.WriteFile(rules, []byte(`{"version":1,"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed := srv.callTool(t, "spawn", params)
	if refreshed.Error == nil || refreshed.Error.Data.ErrName != "ErrPermissionRulesRefused" || !strings.Contains(refreshed.Error.Message, "the file is not a rules object:") {
		t.Fatalf("file edit not observed: %+v", refreshed.Error)
	}
	content := fmt.Sprintf("[experimental]\ndroid = true\n[launch]\ndroid_rules_path = %q\n", filepath.Join(home, "missing.json")) + droidfix.NewGuard(t).ConfigLine()
	if err := os.WriteFile(filepath.Join(directorDir(home), "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	retained := srv.callTool(t, "spawn", params)
	if !reflect.DeepEqual(retained.Error, refreshed.Error) {
		t.Fatalf("running server reloaded config: %+v", retained.Error)
	}
	out, stderr, code := runCLIWithHome(t, home, "spawn", "--agent", "droid", "--cwd", home, "--", "--model", "model")
	cli := assertOnlyEnvelope(t, out, stderr, code, "ErrPermissionRulesRefused")
	if !strings.Contains(cli.ErrDescription, "the file is not a readable regular file of at most 1 MiB") {
		t.Fatalf("CLI did not reload path: %+v", cli)
	}
	srv.stop(t)
	srv = startServe(t, home)
	t.Cleanup(srv.kill)
	srv.initialize(t)
	if got := srv.callTool(t, "spawn", params); got.Error == nil || errnames.TrimNamePrefix(got.Error.Data.ErrName, got.Error.Message) != cli.ErrDescription {
		t.Fatalf("restarted server did not reload path: %+v", got.Error)
	}
	srv.stop(t)
}

// configDirValue is the literal both binaries' version results report as
// config_dir, whatever HOME is (SR-11.7): no path is resolved.
const configDirValue = "~/.config/agent-director"

func TestEnvelopeParityPaneOnly(t *testing.T) {
	for _, agent := range []string{"droid", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			id, err := apitest.SeedSpawn(filepath.Join(home, ".config", "agent-director", "state.db"), "", "pending", t.TempDir(), "off", "", true,
				apitest.WithAgent(agent), apitest.WithNoSubagentTracking(), apitest.WithLaunchStartedAt(time.Now().Add(-time.Hour).UnixMilli()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := apitest.BindReadSurfaceProcess(stateDB(home), id, os.Getpid()); err != nil {
				t.Fatal(err)
			}
			srv := startServe(t, home)
			t.Cleanup(srv.kill)
			srv.initialize(t)
			first := map[string]string{}
			for _, verb := range []string{"get", "status", "list"} {
				args, toolArgs := []string{verb, "--instance-id", id}, `{"instance_id":"`+id+`"}`
				if verb == "list" {
					args, toolArgs = []string{verb}, `{}`
				}
				fixture := apitest.ReadSurfaceCase{Params: map[string]any{"instance_id": id}}
				from := time.Now()
				out, stderr, code := runCLIWithHome(t, home, args...)
				if code != 0 || stderr != "" {
					t.Fatalf("%s: %d %s", verb, code, stderr)
				}
				var cli, mcp map[string]any
				out = apitest.NormalizeReadSurfaceProbes(t, out, from, time.Now(), first, fixture)
				if err := json.Unmarshal([]byte(out), &cli); err != nil {
					t.Fatal(err)
				}
				from = time.Now()
				mcpOut := srv.callToolOK(t, verb, toolArgs)
				mcpOut = apitest.NormalizeReadSurfaceProbes(t, mcpOut, from, time.Now(), first, fixture)
				if err := json.Unmarshal([]byte(mcpOut), &mcp); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(cli, mcp) {
					t.Fatalf("%s: CLI=%v MCP=%v", verb, cli, mcp)
				}
				row := cli
				if verb == "list" {
					row = cli["spawns"].([]any)[0].(map[string]any)
				}
				if row["integration_mode"] != "pane_only" {
					t.Fatalf("%s=%v", verb, row)
				}
			}
			srv.stop(t)
		})
	}
}

// TestEnvelopeParityVersion asserts the version envelope shape: exactly the
// three JSON string fields "version", "commit" and "config_dir" (version and
// commit may be empty in test builds; presence is the invariant; config_dir is
// the literal ~/.config/agent-director).
func TestEnvelopeParityVersion(t *testing.T) {
	stdout, stderr, code := runCLI(t, "version")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q; want 0 and empty", code, stderr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout)
	}
	_, v := parsed["version"].(string)
	_, c := parsed["commit"].(string)
	dir, d := parsed["config_dir"].(string)
	if !v || !c || !d || len(parsed) != 3 {
		t.Errorf("version output = %v; want exactly the string keys version, commit and config_dir", parsed)
	}
	if dir != configDirValue {
		t.Errorf("config_dir = %q; want the literal %q", dir, configDirValue)
	}
}

// TestVersionConfigDirIsTheSameForEveryHome: config_dir is the literal under
// an empty HOME and under another one, so it reads no home and no directory.
func TestVersionConfigDirIsTheSameForEveryHome(t *testing.T) {
	var outs []string
	for _, home := range []string{"", t.TempDir()} {
		stdout, stderr, code := runCLIWithHome(t, home, "version")
		if code != 0 || stderr != "" {
			t.Fatalf("HOME=%q: exit=%d stderr=%q; want 0 and empty", home, code, stderr)
		}
		var parsed struct {
			ConfigDir string `json:"config_dir"`
		}
		if err := json.Unmarshal([]byte(stdout), &parsed); err != nil || parsed.ConfigDir != configDirValue {
			t.Errorf("HOME=%q: version = %q (err %v); want config_dir %q", home, stdout, err, configDirValue)
		}
		outs = append(outs, stdout)
	}
	if outs[0] != outs[1] {
		t.Errorf("version differs by HOME: %q vs %q", outs[0], outs[1])
	}
}

// TestMCPVersionToolCarriesConfigDir: serve's `version` tool reply holds the
// same config_dir the verb prints.
func TestMCPVersionToolCarriesConfigDir(t *testing.T) {
	srv := startServe(t, t.TempDir())
	t.Cleanup(srv.kill)
	srv.initialize(t)

	text := srv.callToolOK(t, "version", `{}`)
	srv.stop(t)

	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("version tool text %q is not JSON: %v", text, err)
	}
	if res["config_dir"] != configDirValue || len(res) != 3 {
		t.Errorf("version tool result = %v; want exactly version, commit and config_dir %q", res, configDirValue)
	}
}

// TestEnvelopeParityListEmpty asserts the list envelope against an empty
// store is exactly {"spawns":[]}: a non-null empty array, never null or absent.
func TestEnvelopeParityListEmpty(t *testing.T) {
	stdout, stderr, code := runCLI(t, "list")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q; want 0 and empty", code, stderr)
	}
	if stdout != "{\"spawns\":[]}\n" {
		t.Errorf("stdout = %q; want {\"spawns\":[]}", stdout)
	}
}

// TestEnvelopeParitySpawnCwdNotFound asserts the spawn-failure envelope for a
// nonexistent cwd: exit code 1, empty stdout, and an ErrCwdNotFound envelope
// with a description.
func TestEnvelopeParitySpawnCwdNotFound(t *testing.T) {
	stdout, stderr, code := runCLI(t, withAgent("claude", "spawn", "--cwd", "/no/such/path")...)
	if env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrCwdNotFound"); env.ErrDescription == "" {
		t.Errorf("err_description empty; want a human-readable message")
	}
}

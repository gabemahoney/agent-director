package main_test

// spawn's flags through the built CLI and test/fake-tmux. Validation, the
// label scan, pre-trust and reuse themselves are pkg/api's spawn_*_test.go,
// as is the order of the agent checks; here only how the CLI maps --agent.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// spawnResult mirrors api.SpawnResult for the CLI tests.
type spawnResult struct {
	InstanceID string `json:"instance_id"`
}

// spawnOK runs spawn under home with args, requires exit 0 and returns the
// new row's id.
func spawnOK(t *testing.T, home, fakeDir string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, withAgent("claude", "spawn", args...)...)
	var res spawnResult
	if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil || res.InstanceID == "" {
		t.Fatalf("spawn %q: exit = %d, stdout = %q; want 0 and an id (stderr=%s)", args, code, stdout, stderr)
	}
	return res.InstanceID
}

// getRow runs `get` for id and returns its JSON object.
func getRow(t *testing.T, home, fakeDir, id string) map[string]any {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "get", "--instance-id", id)
	var row map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &row) != nil {
		t.Fatalf("get %s: exit = %d, stdout = %q (stderr=%q)", id, code, stdout, stderr)
	}
	return row
}

// TestSpawnCLIHappyPath: --cwd, --label, --extra-env, --tmux-session-name and
// the claude args after -- reach the row and the one create, which runs on
// the socket under the test's TMUX_TMPDIR with the launch's chained labels
// (SR-2.1, SR-3.5); the row is pending with the config's relay mode.
func TestSpawnCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	const name = "bot-claude-status"
	id := spawnOK(t, home, fakeDir, "--cwd", t.TempDir(), "--label", "role=worker", "--extra-env", "AD_CLI_EXTRA=set",
		"--tmux-session-name", name, "--", "--model", "opus")

	row := getRow(t, home, fakeDir, id)
	labels, _ := row["labels"].(map[string]any)
	if row["state"] != "pending" || row["agent"] != "claude" || row["relay_mode"] != "off" || row["tmux_session_name"] != name ||
		labels["role"] != "worker" || jsonOf(t, row["agent_args"]) != `["--model","opus"]` {
		t.Errorf("get = %v; want pending, agent claude, relay off, %s, label role=worker, agent_args [--model opus]", row, name)
	}

	socket := spawnSocket(t, home)
	token, rowSocket, storeID := launchIdentity(t, home, id)
	if len(token) != 16 || rowSocket != socket {
		t.Errorf("row token = %q, socket = %q; want a 16-hex token on %q", token, rowSocket, socket)
	}
	argv := assertInvocationKinds(t, home, "new-session")[0]
	if !slices.Equal(argv[:4], []string{"-u", "-S", socket, "new-session"}) {
		t.Errorf("create argv head = %q; want [-u -S %s new-session]", argv[:4], socket)
	}
	target := "=" + name + ":"
	chain := []string{
		";", "set-option", "-F", "-t", target, "@ad_owner", tmuxfix.ChainLabelValue(token, id, storeID),
		";", "set-option", "-p", "-F", "-t", target, "@ad_pane", tmuxfix.ChainPaneLabelValue(token),
	}
	if len(argv) < len(chain) || !slices.Equal(argv[len(argv)-len(chain):], chain) {
		t.Errorf("create argv = %q; want it to end with the chain %q", argv, chain)
	}
	for _, want := range []string{name, "--settings", "--model", "AGENT_DIRECTOR_INSTANCE_ID=" + id,
		"AGENT_DIRECTOR_LABEL_ROLE=worker", "AD_CLI_EXTRA=set"} {
		if !slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, want) }) {
			t.Errorf("create argv lacks %q: %q", want, argv)
		}
	}
	sessions := faketmuxfix.Tables{}.Read(t, socket).Sessions
	if len(sessions) != 1 || sessions[0].Label != tmuxfix.LabelValue(token, sessions[0].ID, id, storeID) {
		t.Errorf("fake sessions = %+v; want one session with this launch's label", sessions)
	}
}

// TestSpawnCLITmuxSessionNameSupplied: an omitted --tmux-session-name gives
// the <basename(cwd)>-<id[:8]> default, while --tmux-session-name= (supplied,
// empty) is ErrTmuxSessionNameEmpty naming the CLI flag (b.ro3) with no row
// and no create: the CLI tells the two apart (TmuxSessionNameSupplied).
func TestSpawnCLITmuxSessionNameSupplied(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	id := spawnOK(t, home, fakeDir, "--cwd", t.TempDir(), "--no-pre-trust")
	if name, _ := getRow(t, home, fakeDir, id)["tmux_session_name"].(string); !regexp.MustCompile(`^[A-Za-z0-9_-]+-[0-9a-f]{8}$`).MatchString(name) {
		t.Errorf("tmux_session_name = %q; want the <basename>-<id[:8]> default", name)
	}

	home = t.TempDir()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, withAgent("claude", "spawn", "--cwd", t.TempDir(), "--tmux-session-name=")...)
	env := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrTmuxSessionNameEmpty")
	const desc = "tmux_session_name (--tmux-session-name on the CLI) was supplied with an empty value"
	if !strings.Contains(env.ErrDescription, desc) {
		t.Errorf("err_description = %q; want it to carry %q", env.ErrDescription, desc)
	}
	assertInvocationKinds(t, home)
	if ids := listIDs(t, home, fakeDir); len(ids) != 0 {
		t.Errorf("rows = %v; want none", ids)
	}
}

// TestSpawnCLITemplateAgentArgs: with --template and no args after --, the
// template's agent_args reach the create (b.qjk: they were wiped); args after
// -- replace them wholesale.
func TestSpawnCLITemplateAgentArgs(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name       string
		extra      []string
		want, gone string
	}{
		{"template args when none follow --", nil, "--foo", ""},
		{"args after -- replace the template's", []string{"--", "--bar"}, "--bar", "--foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			seedTemplate(t, home, "tpl", apitest.TemplateFields{Agent: "claude", AgentArgs: []string{"--foo"}})
			id := spawnOK(t, home, fakeDir, append([]string{"--cwd", t.TempDir(), "--template", "tpl", "--no-pre-trust"}, tc.extra...)...)

			argv := assertInvocationKinds(t, home, "new-session")[0]
			if !slices.Contains(argv, tc.want) || (tc.gone != "" && slices.Contains(argv, tc.gone)) {
				t.Errorf("create argv = %q; want %s and not %q", argv, tc.want, tc.gone)
			}
			if got := jsonOf(t, getRow(t, home, fakeDir, id)["agent_args"]); got != `["`+tc.want+`"]` {
				t.Errorf("get.agent_args = %s; want [%s]", got, tc.want)
			}
		})
	}
}

// seedTemplate writes a template under home from fields.
func seedTemplate(t *testing.T, home, name string, fields apitest.TemplateFields) {
	t.Helper()
	if _, err := apitest.SeedTemplate(filepath.Join(directorDir(home), "templates"), name, apitest.TemplateBody(fields)); err != nil {
		t.Fatalf("SeedTemplate: %v", err)
	}
}

// TestSpawnCLIAgentArgsMismatch: a template for another agent gives
// ErrAgentArgsMismatch when the spawn passes nothing after -- (a bare -- counts
// as none on the CLI), with no row and no tmux call; args after -- replace the
// template's and the row records the spawn's agent.
func TestSpawnCLIAgentArgsMismatch(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name  string
		extra []string
		want  string // the err_name, "" for a launch
	}{
		{"nothing after --", nil, "ErrAgentArgsMismatch"},
		{"a bare --", []string{"--"}, "ErrAgentArgsMismatch"},
		{"args after --", []string{"--", "--bar"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			seedTemplate(t, home, "tpl", apitest.TemplateFields{Agent: "codex", AgentArgs: []string{"--foo"}})
			args := append(withAgent("claude", "spawn", "--cwd", t.TempDir(), "--template", "tpl", "--no-pre-trust"), tc.extra...)

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)

			if tc.want != "" {
				env := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, tc.want)
				if !strings.Contains(env.ErrDescription, `template "tpl"'s agent_args are for agent codex and this spawn's agent is claude`) {
					t.Errorf("err_description = %q; want it to name the template and both agents", env.ErrDescription)
				}
				assertInvocationKinds(t, home)
				if ids := listIDs(t, home, fakeDir); len(ids) != 0 {
					t.Errorf("rows = %v; want none", ids)
				}
				return
			}
			var res spawnResult
			if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil {
				t.Fatalf("exit = %d, stdout = %q; want 0 and an id (stderr=%s)", code, stdout, stderr)
			}
			row := getRow(t, home, fakeDir, res.InstanceID)
			if row["agent"] != "claude" || jsonOf(t, row["agent_args"]) != `["--bar"]` {
				t.Errorf("get = %v; want agent claude and agent_args [--bar]", row)
			}
		})
	}
}

// TestSpawnCLIAgentRefusals: --agent's invalid, empty and codex-with-permissions
// refusals reach the CLI as their envelopes, with no row and no tmux call.
func TestSpawnCLIAgentRefusals(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const noPerms = "a codex agent takes no allow, deny or ask entries"
	for _, tc := range []struct {
		name, errName, desc string
		flags               []string
	}{
		{"wrong case", "ErrAgentInvalid", `agent "Claude" (want claude or codex)`, []string{"--agent", "Claude"}},
		{"empty counts as not given", "ErrAgentMissing", "--agent on the CLI", []string{"--agent="}},
		{"codex allow", "ErrPermissionsUnsupported", noPerms, []string{"--agent", "codex", "--allow", "Bash"}},
		{"codex deny", "ErrPermissionsUnsupported", noPerms, []string{"--agent", "codex", "--deny", "Bash"}},
		{"codex ask", "ErrPermissionsUnsupported", noPerms, []string{"--agent", "codex", "--ask", "Bash"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, append([]string{"spawn", "--cwd", t.TempDir(), "--no-pre-trust"}, tc.flags...)...)

			env := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, tc.errName)
			if !strings.Contains(env.ErrDescription, tc.desc) {
				t.Errorf("err_description = %q; want it to carry %q", env.ErrDescription, tc.desc)
			}
			assertInvocationKinds(t, home)
			if ids := listIDs(t, home, fakeDir); len(ids) != 0 {
				t.Errorf("rows = %v; want none", ids)
			}
		})
	}
}

// TestMakeTemplateCLIAgent: make-template --agent writes the template's agent
// beside --agent-args, and a spawn from it with no --agent takes that agent
// and its args; --agent-args with no --agent writes no template.
func TestMakeTemplateCLIAgent(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	path := filepath.Join(directorDir(home), "templates", "tpl.toml")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "make-template", "--name", "tpl", "--agent-args", "--foo")
	assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrTemplateAgentMissing")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("template after the refusal: %v; want none written", err)
	}

	if _, stderr, code = runSpawnCLI(t, home, fakeDir, withAgent("claude", "make-template", "--name", "tpl", "--agent-args", "--foo")...); code != 0 {
		t.Fatalf("make-template --agent claude: exit = %d; stderr=%s", code, stderr)
	}
	if body, err := os.ReadFile(path); err != nil || !strings.Contains(string(body), `agent = "claude"`) || !strings.Contains(string(body), "--foo") {
		t.Fatalf("template = %q (err %v); want agent = \"claude\" and agent_args with --foo", body, err)
	}

	stdout, stderr, code = runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", t.TempDir(), "--template", "tpl", "--no-pre-trust")
	var res spawnResult
	if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil {
		t.Fatalf("spawn --template: exit = %d, stdout = %q; want 0 and an id (stderr=%s)", code, stdout, stderr)
	}
	if row := getRow(t, home, fakeDir, res.InstanceID); row["agent"] != "claude" || jsonOf(t, row["agent_args"]) != `["--foo"]` {
		t.Errorf("get = %v; want agent claude and agent_args [--foo]", row)
	}
}

// TestSpawnCLIReuseFinishedFlag: --reuse-finished with an explicit id reuses
// an ended row with no session left (get: pending, a launch start, retained prior
// sessions), while --reuse-finished=false leaves it colliding with
// ErrInstanceIdCollision and no create (SR-10.1, SR-10.2; AC-REUSE-13).
func TestSpawnCLIReuseFinishedFlag(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		flag  string
		reuse bool
	}{{"--reuse-finished", true}, {"--reuse-finished=false", false}} {
		t.Run(tc.flag, func(t *testing.T) {
			historyID := uuid.NewString()
			home, id, _ := seedRowOnSocket(t, store.StateEnded, apitest.WithLifeNumber(1),
				apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: historyID, Life: 1}))
			before := rowColumns(t, home, id)

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, withAgent("claude", "spawn", "--cwd", t.TempDir(), "--instance-id", id, tc.flag)...)

			if !tc.reuse {
				assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrInstanceIdCollision")
				if after := rowColumns(t, home, id); jsonOf(t, after) != jsonOf(t, before) {
					t.Errorf("row = %+v; want unchanged %+v", after, before)
				}
				assertInvocationKinds(t, home)
				return
			}
			if code != 0 {
				t.Fatalf("exit = %d; stderr=%s", code, stderr)
			}
			row := getRow(t, home, fakeDir, id)
			if prior, _ := row["prior_sessions"].([]any); row["state"] != "pending" || row["launch_started_at"] == nil || len(prior) != 1 || prior[0].(map[string]any)["session_id"] != historyID {
				t.Errorf("get = %v; want pending, a launch start and the retained prior session", row)
			}
			assertInvocationKinds(t, home, "list-sessions", "new-session")
		})
	}
}

// jsonOf is v encoded as JSON.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

// TestSpawnCLISwitchResumeOutput pins the CLI notice and fresh-start envelope.
func TestSpawnCLISwitchResumeOutput(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, args := range [][]string{{"--resume", "S1"}, nil} {
		t.Run(jsonOf(t, args), func(t *testing.T) {
			transcript := apitest.SeedJsonlUnder(t, t.TempDir(), t.TempDir(), "S1")
			home, id, _ := seedRowOnSocket(t, store.StateEnded, apitest.WithAgent("codex"), apitest.WithLifeNumber(1),
				apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "S1", JSONLPath: transcript, Life: 0}))
			argv := append([]string{"spawn", "--cwd", t.TempDir(), "--instance-id", id, "--reuse-finished", "--agent", "claude", "--no-pre-trust", "--"}, args...)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, argv...)
			var result map[string]json.RawMessage
			if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if string(result["instance_id"]) != jsonOf(t, id) || string(result["pre_trust"]) != `"skipped"` {
				t.Fatalf("result=%s", stdout)
			}
			if len(args) > 0 {
				if string(result["dropped_args"]) != jsonOf(t, args) {
					t.Errorf("drops=%s want %s", result["dropped_args"], jsonOf(t, args))
				}
				if stderr != "Dropped original arguments [\"--resume\" \"S1\"]: switching agents starts fresh and resumes nothing.\n" {
					t.Errorf("notice=%q", stderr)
				}
			} else if _, present := result["dropped_args"]; present || stderr != "" {
				t.Errorf("empty result=%s stderr=%q", stdout, stderr)
			}
			row := getRow(t, home, fakeDir, id)
			if row["agent"] != "claude" || row["state"] != "pending" || row["session_id"] != "" || jsonOf(t, row["prior_sessions"]) != "[]" {
				t.Errorf("fresh row=%v", row)
			}
			stored := rowColumns(t, home, id)
			if stored.AgentArgs != "[]" || stored.LifeNumber != int64(2) {
				t.Errorf("stored args=%v", stored.AgentArgs)
			}
			for _, invocation := range assertInvocationKinds(t, home, "list-sessions", "new-session") {
				if slices.Contains(invocation, "--resume") || slices.Contains(invocation, "S1") {
					t.Errorf("launch resumes old session: %q", invocation)
				}
			}
		})
	}
}

func TestSpawnCLIResumePolicyRefusals(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name, agent, bare string
		args              []string
	}{
		{"same agent short", "claude", "-r", []string{"-rS1"}},
		{"switch print cluster", "codex", "-p", []string{"-pc"}},
		{"switch value owned long", "codex", "--resume", []string{"--append-system-prompt", "--resume", "S1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id, _ := seedRowOnSocket(t, store.StateEnded, apitest.WithAgent(tc.agent))
			before := rowColumns(t, home, id)
			argv := append([]string{"spawn", "--agent", "claude", "--cwd", t.TempDir(), "--instance-id", id, "--reuse-finished", "--no-pre-trust", "--"}, tc.args...)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, argv...)
			envelope := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrSpawnDeniedFlag")
			if envelope.ErrDescription != tc.bare || strings.Contains(envelope.ErrDescription, "-rS1") {
				t.Errorf("description=%q want bare %q", envelope.ErrDescription, tc.bare)
			}
			if jsonOf(t, rowColumns(t, home, id)) != jsonOf(t, before) {
				t.Error("refusal changed row")
			}
			assertInvocationKinds(t, home)
		})
	}
}

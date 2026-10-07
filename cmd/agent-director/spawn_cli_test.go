package main_test

// spawn's flags through the built CLI and test/fake-tmux. Validation, the
// label scan, pre-trust and reuse themselves are pkg/api's spawn_*_test.go.

import (
	"encoding/json"
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
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// spawnOK runs spawn under home with args, requires exit 0 and returns the
// new row's id.
func spawnOK(t *testing.T, home, fakeDir string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, append([]string{"spawn"}, args...)...)
	var res spawnResult
	if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil || res.ClaudeInstanceID == "" {
		t.Fatalf("spawn %q: exit = %d, stdout = %q; want 0 and an id (stderr=%s)", args, code, stdout, stderr)
	}
	return res.ClaudeInstanceID
}

// getRow runs `get` for id and returns its JSON object.
func getRow(t *testing.T, home, fakeDir, id string) map[string]any {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "get", "--claude-instance-id", id)
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
	if row["state"] != "pending" || row["relay_mode"] != "off" || row["tmux_session_name"] != name ||
		labels["role"] != "worker" || jsonOf(t, row["claude_args"]) != `["--model","opus"]` {
		t.Errorf("get = %v; want pending, relay off, %s, label role=worker, claude_args [--model opus]", row, name)
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
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", t.TempDir(), "--tmux-session-name=")
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

// TestSpawnCLITemplateClaudeArgs: with --template and no args after --, the
// template's claude_args reach the create (b.qjk: they were wiped); args after
// -- replace them wholesale.
func TestSpawnCLITemplateClaudeArgs(t *testing.T) {
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
			if _, err := apitest.SeedTemplate(filepath.Join(directorDir(home), "templates"), "tpl", `claude_args = ["--foo"]`+"\n"); err != nil {
				t.Fatalf("SeedTemplate: %v", err)
			}
			id := spawnOK(t, home, fakeDir, append([]string{"--cwd", t.TempDir(), "--template", "tpl", "--no-pre-trust"}, tc.extra...)...)

			argv := assertInvocationKinds(t, home, "new-session")[0]
			if !slices.Contains(argv, tc.want) || (tc.gone != "" && slices.Contains(argv, tc.gone)) {
				t.Errorf("create argv = %q; want %s and not %q", argv, tc.want, tc.gone)
			}
			if got := jsonOf(t, getRow(t, home, fakeDir, id)["claude_args"]); got != `["`+tc.want+`"]` {
				t.Errorf("get.claude_args = %s; want [%s]", got, tc.want)
			}
		})
	}
}

// TestSpawnCLIReuseFinishedFlag: --reuse-finished with an explicit id reuses
// an ended row with no session left (get: pending, a launch start, no prior
// sessions), while --reuse-finished=false leaves it colliding with
// ErrInstanceIdCollision and no create (SR-10.1, SR-10.2; AC-REUSE-13).
func TestSpawnCLIReuseFinishedFlag(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		flag  string
		reuse bool
	}{{"--reuse-finished", true}, {"--reuse-finished=false", false}} {
		t.Run(tc.flag, func(t *testing.T) {
			home, id, _ := seedRowOnSocket(t, store.StateEnded, apitest.WithLifeNumber(1),
				apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: uuid.NewString(), Life: 1}))
			before := rowColumns(t, home, id)

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", t.TempDir(), "--claude-instance-id", id, tc.flag)

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
			if prior, _ := row["prior_sessions"].([]any); row["state"] != "pending" || row["launch_started_at"] == nil || len(prior) != 0 {
				t.Errorf("get = %v; want pending, a launch start and no prior sessions", row)
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

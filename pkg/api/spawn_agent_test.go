package api_test

// spawn_agent_test.go covers the agent a spawn is for (SR-1, SR-2.1, SR-5.5,
// SR-14.4): the refusals of an agent that is missing, invalid, wrongly owning
// inherited arguments, or given permission lists a codex spawn cannot take,
// and the order of those checks beside spawn's earlier and later ones, each
// leaving nothing written; the agent a row records; and the Codex launch that
// plain spawn, reuse and resume make for a codex agent.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The refusal descriptions, as the Catalog name and the verb's text give them.
const (
	agentMissingText       = "ErrAgentMissing: the call gives no agent and its template names none; pass agent claude or codex (--agent on the CLI), or put agent in the template"
	agentInvalidTextFmt    = "ErrAgentInvalid: agent %q (want claude or codex)"
	templateNoAgentText    = `ErrTemplateAgentMissing: t: agent_args are set but the template names no agent; add agent = "claude" or "codex", or remove agent_args`
	agentArgsMismatchStart = `ErrAgentArgsMismatch: template "t"'s agent_args are for agent %s and this spawn's agent is %s, so they are not inherited; spawn agent %s, or pass this call's own non-empty agent_args, which replace the template's`
	permissionsText        = "ErrPermissionsUnsupported: a codex agent takes no allow, deny or ask entries; set permission rules in the rules/ folder of the group's Codex folder and use Codex's own permission controls"
)

// readDirFiles returns the name and body of every file in dir, nil when dir is absent.
func readDirFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	files := map[string]string{}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		files[e.Name()] = string(body)
	}
	return files
}

// seedAgentTemplate writes f as the template "t" under env's home and returns the templates dir.
func seedAgentTemplate(t *testing.T, env spawnEnv, f apitest.TemplateFields) string {
	t.Helper()
	dir := filepath.Join(adConfigDir(env.home), "templates")
	if _, err := apitest.SeedTemplate(dir, "t", apitest.TemplateBody(f)); err != nil {
		t.Fatalf("SeedTemplate: %v", err)
	}
	return dir
}

// agentClientWithConfig is a Client on env's store and tmux fake whose config file holds body.
func agentClientWithConfig(t *testing.T, env spawnEnv, body string) *api.Client {
	t.Helper()
	cfgPath := filepath.Join(env.home, "agent-config.toml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := api.New(api.Options{StorePath: env.dbPath, ConfigPath: cfgPath, TmuxClient: env.rec})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, env.clock.Now)
	api.SetProcCheckerForTest(c, env.pc)
	return c
}

// TestSpawnAgentRefusals (SR-1.3, SR-2.1, SR-5.5; AC-SPN-01): each agent
// refusal, and each order row crossing it with a step before or after it, gives
// the one named error with its description and writes nothing: no row, no tmux
// call, no socket directory, no template, the pre-trust file as seeded.
func TestSpawnAgentRefusals(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	args := []string{"--model", "opus"}
	perms := func(allow, deny, ask []string) *api.Permissions {
		return &api.Permissions{Allow: allow, Deny: deny, Ask: ask}
	}
	allowOne := []string{"Bash(ls)"}
	home := map[string]string{"HOME": "/x"}
	cases := []struct {
		name  string
		tmpl  *apitest.TemplateFields // the template named "t" the call spawns from; nil: no template
		p     api.SpawnParams         // the call; CWD is a temp dir unless noCWD, Template "t" when tmpl is set
		noCWD bool
		want  string // the Catalog name
		text  string // the start of the description
	}{
		// The five refusals.
		{name: "no agent from the call or a template", want: "ErrAgentMissing", text: agentMissingText},
		{name: "no agent, the template names none either", tmpl: &apitest.TemplateFields{RelayMode: "on"},
			want: "ErrAgentMissing", text: agentMissingText},
		{name: "per-call agent not claude or codex", p: api.SpawnParams{Agent: "gemini"}, want: "ErrAgentInvalid",
			text: strings.Replace(agentInvalidTextFmt, "%q", `"gemini"`, 1)},
		{name: "cursor agent in another case", p: api.SpawnParams{Agent: "Cursor"}, want: "ErrAgentInvalid", text: strings.Replace(agentInvalidTextFmt, "%q", `"Cursor"`, 1)},
		{name: "per-call agent in another case", p: api.SpawnParams{Agent: "Claude"}, want: "ErrAgentInvalid",
			text: strings.Replace(agentInvalidTextFmt, "%q", `"Claude"`, 1)},
		{name: "invalid per-call agent over a valid template agent", tmpl: &apitest.TemplateFields{Agent: "claude"},
			p: api.SpawnParams{Agent: "gpt"}, want: "ErrAgentInvalid", text: strings.Replace(agentInvalidTextFmt, "%q", `"gpt"`, 1)},
		{name: "template with an invalid agent, valid call agent", tmpl: &apitest.TemplateFields{Agent: "gemini"},
			p: api.SpawnParams{Agent: "claude"}, want: "ErrTemplateMalformed", text: `ErrTemplateMalformed: t: agent="gemini" (want claude or codex)`},
		{name: "template agent_args with no agent, the call names claude", tmpl: &apitest.TemplateFields{AgentArgs: args},
			p: api.SpawnParams{Agent: "claude"}, want: "ErrTemplateAgentMissing", text: templateNoAgentText},
		{name: "template agent_args with no agent, the call names codex", tmpl: &apitest.TemplateFields{AgentArgs: args},
			p: api.SpawnParams{Agent: "codex"}, want: "ErrTemplateAgentMissing", text: templateNoAgentText},
		{name: "template agent_args with no agent, the call gives its own", tmpl: &apitest.TemplateFields{AgentArgs: args},
			p: api.SpawnParams{Agent: "claude", AgentArgs: []string{"--verbose"}}, want: "ErrTemplateAgentMissing", text: templateNoAgentText},
		{name: "claude template's arguments inherited by a codex spawn", tmpl: &apitest.TemplateFields{Agent: "claude", AgentArgs: args},
			p: api.SpawnParams{Agent: "codex"}, want: "ErrAgentArgsMismatch", text: fmt.Sprintf(agentArgsMismatchStart, "claude", "codex", "claude")},
		{name: "codex template's arguments inherited by a claude spawn", tmpl: &apitest.TemplateFields{Agent: "codex", AgentArgs: args},
			p: api.SpawnParams{Agent: "claude"}, want: "ErrAgentArgsMismatch", text: fmt.Sprintf(agentArgsMismatchStart, "codex", "claude", "codex")},
		{name: "allow entries on a codex spawn from the call", p: api.SpawnParams{Agent: "codex", Permissions: perms(allowOne, nil, nil)},
			want: "ErrPermissionsUnsupported", text: permissionsText},
		{name: "deny entries on a codex spawn from the call", p: api.SpawnParams{Agent: "codex", Permissions: perms(nil, allowOne, nil)},
			want: "ErrPermissionsUnsupported", text: permissionsText},
		{name: "ask entries on a codex spawn from the call", p: api.SpawnParams{Agent: "codex", Permissions: perms(nil, nil, allowOne)},
			want: "ErrPermissionsUnsupported", text: permissionsText},
		{name: "entries in a codex template", tmpl: &apitest.TemplateFields{Agent: "codex", Permissions: &config.TemplatePermissions{Allow: allowOne}},
			want: "ErrPermissionsUnsupported", text: permissionsText},
		{name: "entries in a claude template, the call names codex",
			tmpl: &apitest.TemplateFields{Agent: "claude", Permissions: &config.TemplatePermissions{Deny: allowOne}},
			p:    api.SpawnParams{Agent: "codex"}, want: "ErrPermissionsUnsupported", text: permissionsText},

		{name: "cursor allow entries", p: api.SpawnParams{Agent: "cursor", Permissions: perms(allowOne, nil, nil)}, want: "ErrPermissionsUnsupported", text: "ErrPermissionsUnsupported:"},
		{name: "cursor deny entries", p: api.SpawnParams{Agent: "cursor", Permissions: perms(nil, allowOne, nil)}, want: "ErrPermissionsUnsupported", text: "ErrPermissionsUnsupported:"},
		{name: "cursor ask template", tmpl: &apitest.TemplateFields{Agent: "cursor", Permissions: &config.TemplatePermissions{Ask: allowOne}}, want: "ErrPermissionsUnsupported", text: "ErrPermissionsUnsupported:"},
		{name: "cursor ownership", tmpl: &apitest.TemplateFields{Agent: "claude", AgentArgs: args}, p: api.SpawnParams{Agent: "cursor"}, want: "ErrAgentArgsMismatch", text: fmt.Sprintf(agentArgsMismatchStart, "claude", "cursor", "claude")},
		{name: "cursor denied arg before permission", p: api.SpawnParams{Agent: "cursor", AgentArgs: []string{"--resume=x"}, Permissions: perms(allowOne, nil, nil)}, want: "ErrSpawnDeniedFlag", text: "ErrSpawnDeniedFlag:"},
		// A refused Codex argument: the Codex half of the denied-argument check, with the same nothing-written result.
		{name: "a refused argument on a codex spawn", p: api.SpawnParams{Agent: "codex", AgentArgs: []string{"--remote", "ws://x"}},
			want: "ErrSpawnDeniedFlag", text: "ErrSpawnDeniedFlag: --remote: it attaches the agent to a remote or shared service"},

		// Order: the agent checks beside the steps around them.
		{name: "order: control-character id before no agent", p: api.SpawnParams{InstanceID: "id\tx"}, want: "ErrInvalidFlags",
			text: "ErrInvalidFlags: "},
		{name: "order: control-character id before a template with an invalid agent", tmpl: &apitest.TemplateFields{Agent: "gemini"},
			p: api.SpawnParams{Agent: "claude", InstanceID: "id\tx"}, want: "ErrInvalidFlags", text: "ErrInvalidFlags: "},
		{name: "order: template with an invalid agent before a missing cwd", tmpl: &apitest.TemplateFields{Agent: "gemini"}, noCWD: true,
			want: "ErrTemplateMalformed", text: `ErrTemplateMalformed: t: agent="gemini"`},
		{name: "order: template agent_args with no agent before a missing cwd", tmpl: &apitest.TemplateFields{AgentArgs: args}, noCWD: true,
			want: "ErrTemplateAgentMissing", text: templateNoAgentText},
		{name: "order: missing cwd before no agent", noCWD: true, want: "ErrCwdMissing", text: "ErrCwdMissing"},
		{name: "order: invalid relay_mode before no agent", p: api.SpawnParams{RelayMode: "maybe"}, want: "ErrRelayModeInvalid",
			text: "ErrRelayModeInvalid"},
		{name: "order: no agent before a reserved extra_env key", p: api.SpawnParams{ExtraEnv: home}, want: "ErrAgentMissing", text: agentMissingText},
		{name: "order: invalid agent before a reserved extra_env key", p: api.SpawnParams{Agent: "gemini", ExtraEnv: home}, want: "ErrAgentInvalid",
			text: strings.Replace(agentInvalidTextFmt, "%q", `"gemini"`, 1)},
		{name: "order: argument ownership before codex permissions", tmpl: &apitest.TemplateFields{Agent: "claude", AgentArgs: args},
			p: api.SpawnParams{Agent: "codex", Permissions: perms(allowOne, nil, nil)}, want: "ErrAgentArgsMismatch",
			text: fmt.Sprintf(agentArgsMismatchStart, "claude", "codex", "claude")},
		{name: "order: codex permissions before a reserved extra_env key",
			p:    api.SpawnParams{Agent: "codex", Permissions: perms(allowOne, nil, nil), ExtraEnv: home},
			want: "ErrPermissionsUnsupported", text: permissionsText},
		{name: "order: codex permissions before an invalid session name",
			p:    api.SpawnParams{Agent: "codex", Permissions: perms(allowOne, nil, nil), TmuxSessionName: "bad:name", TmuxSessionNameSupplied: true},
			want: "ErrPermissionsUnsupported", text: permissionsText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			trust := seedTrustConfig(t, env.home, trustLacksEntry)
			p := tc.p
			if !tc.noCWD {
				p.CWD = t.TempDir()
			}
			tdir := filepath.Join(adConfigDir(env.home), "templates")
			if tc.tmpl != nil {
				tdir = seedAgentTemplate(t, env, *tc.tmpl)
				p.Template = "t"
			}
			rows, templates := listIDs(t, env.c), readDirFiles(t, tdir)

			_, err := env.c.Spawn(p)

			assertOneName(t, err, tc.want)
			if !strings.HasPrefix(errText(err), tc.text) {
				t.Errorf("description %q\nwant it to start %q", errText(err), tc.text)
			}
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); len(ids) != len(rows) {
				t.Errorf("List ids = %q; want unchanged %q", ids, rows)
			}
			if got := readDirFiles(t, tdir); len(got) != len(templates) || got["t"] != templates["t"] {
				t.Errorf("templates = %v; want unchanged %v", got, templates)
			}
			trust.check(t, "", false, "after the refused spawn")
			if _, err := os.Lstat(filepath.Dir(env.socket)); !os.IsNotExist(err) {
				t.Errorf("socket directory %s: Lstat err = %v; want it not created", filepath.Dir(env.socket), err)
			}
		})
	}
}

// TestSpawnRecordsResolvedAgent (SR-1.2, SR-5.0): the row records the agent
// the spawn resolved to, the call's own over a template's, and get shows it.
func TestSpawnRecordsResolvedAgent(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name  string
		tmpl  *apitest.TemplateFields
		agent string // the call's
		want  string
	}{
		{"the call names claude", nil, "claude", "claude"},
		{"the call names none, the template claude", &apitest.TemplateFields{Agent: "claude"}, "", "claude"},
		{"the call and the template name claude", &apitest.TemplateFields{Agent: "claude"}, "claude", "claude"},
		{"the call's claude over a codex template", &apitest.TemplateFields{Agent: "codex"}, "claude", "claude"},
		{"the call's claude over a codex template holding no lists", &apitest.TemplateFields{Agent: "codex", Permissions: &config.TemplatePermissions{}},
			"claude", "claude"},
		{"the call names codex", nil, "codex", "codex"},
		{"the call names cursor", nil, "cursor", "cursor"},
		{"cursor template", &apitest.TemplateFields{Agent: "cursor"}, "", "cursor"},
		{"cursor over claude template", &apitest.TemplateFields{Agent: "claude"}, "cursor", "cursor"},
		{"claude over cursor template", &apitest.TemplateFields{Agent: "cursor"}, "claude", "claude"},
		{"the call names none, the template codex", &apitest.TemplateFields{Agent: "codex"}, "", "codex"},
		{"the call and the template name codex", &apitest.TemplateFields{Agent: "codex"}, "codex", "codex"},
		{"the call's codex over a claude template", &apitest.TemplateFields{Agent: "claude"}, "codex", "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			p := api.SpawnParams{Agent: tc.agent, CWD: t.TempDir(), NoPreTrust: true}
			if tc.tmpl != nil {
				seedAgentTemplate(t, env, *tc.tmpl)
				p.Template = "t"
			}

			res, err := env.c.Spawn(p)
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}

			cols, err := apitest.ReadSpawnColumns(env.dbPath, res.InstanceID)
			if err != nil || cols.Agent != tc.want {
				t.Errorf("row agent = %q (%v); want %q", cols.Agent, err, tc.want)
			}
			if row, err := env.c.Get(res.InstanceID); err != nil || row.Agent != tc.want {
				t.Errorf("get agent = %q (%v); want %q", row.Agent, err, tc.want)
			}
			if tc.want == "codex" {
				assertCodexLaunch(t, env.rec.SocketCallsOf(tmux.CallCreate), env.dbPath, res.InstanceID, codexProgram)
			} else if tc.want == "cursor" {
				creates := env.rec.SocketCallsOf(tmux.CallCreate)
				if len(creates) != 1 || len(creates[0].Command) < 2 || creates[0].Command[1] != "cursor-host" || creates[0].Envs["AGENT_DIRECTOR_CURSOR_STORE_PATH"] != env.dbPath {
					t.Fatalf("Cursor create missing gateway/store route: %+v", creates)
				}
			} else if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Command[0] != "claude" {
				t.Errorf("creates = %+v; want one, launching claude", creates)
			}
		})
	}
}

// TestSpawnExplicitEmptyAgentArgsAgainstMismatchedTemplate (SR-2.1): a call
// that supplies an empty agent_args list is not refused for the template's
// agent_args belonging to another agent, and inherits none of them; without
// the list the same call is ErrAgentArgsMismatch.
func TestSpawnExplicitEmptyAgentArgsAgainstMismatchedTemplate(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env := newSpawnEnv(t)
	seedAgentTemplate(t, env, apitest.TemplateFields{Agent: "codex", AgentArgs: []string{"--model", "gpt"}})
	p := api.SpawnParams{Agent: "claude", CWD: t.TempDir(), Template: "t", NoPreTrust: true}

	_, refused := env.c.Spawn(p)
	assertOneName(t, refused, "ErrAgentArgsMismatch")
	p.AgentArgs = []string{}
	res, err := env.c.Spawn(p)

	if err != nil {
		t.Fatalf("Spawn with an empty agent_args: %v", err)
	}
	if cols, err := apitest.ReadSpawnColumns(env.dbPath, res.InstanceID); err != nil || cols.AgentArgs != "[]" {
		t.Errorf("row agent_args = %v (%v); want []", cols.AgentArgs, err)
	}
	if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || strings.Contains(strings.Join(creates[0].Command, " "), "--model") {
		t.Errorf("creates = %+v; want one, launching without the template's arguments", creates)
	}
}

// storedAgentArgs is the agent_args list cols holds, decoded.
func storedAgentArgs(t *testing.T, cols apitest.SpawnColumns) []string {
	t.Helper()
	raw, _ := cols.AgentArgs.(string)
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatalf("agent_args %v is not a JSON list: %v", cols.AgentArgs, err)
	}
	return args
}

// storedExtraEnv is the extra_env map cols holds, decoded.
func storedExtraEnv(t *testing.T, cols apitest.SpawnColumns) map[string]string {
	t.Helper()
	raw, _ := cols.ExtraEnv.(string)
	var env map[string]string
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("extra_env %v is not a JSON object: %v", cols.ExtraEnv, err)
	}
	return env
}

// TestSpawnCodexLaunches (SR-5.1, SR-5.5, SR-14.5): a codex spawn that passes
// every check, with no lists (disable_ask_user on, empty lists and a codex
// template included), is not ErrPermissionsUnsupported and launches: one
// create running codex, a pending row recording agent codex, the arguments as
// given (the prompt after `--` included) and the extra_env stored, and the
// folder trust of a Codex launch reported as pre_trust ok.
func TestSpawnCodexLaunches(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	prompt := []string{"--model", "x", "--", "fix the bug"}
	cases := []struct {
		name     string
		tmpl     *apitest.TemplateFields
		config   string // the config file's body
		p        api.SpawnParams
		wantArgs []string // the stored agent_args
		wantPass []string // the pass-through arguments before any `--`
		wantEnv  map[string]string
	}{
		{name: "no lists", p: api.SpawnParams{Agent: "codex"}, wantArgs: []string{}},
		{name: "disable_ask_user on, no lists", config: "[defaults]\ndisable_ask_user = true\n", p: api.SpawnParams{Agent: "codex"}, wantArgs: []string{}},
		{name: "empty lists", p: api.SpawnParams{Agent: "codex", Permissions: &api.Permissions{Allow: []string{}, Deny: []string{}, Ask: []string{}}},
			wantArgs: []string{}},
		{name: "arguments and a prompt after the double dash", p: api.SpawnParams{Agent: "codex", AgentArgs: prompt}, wantArgs: prompt,
			wantPass: []string{"--model", "x"}},
		{name: "extra_env", p: api.SpawnParams{Agent: "codex", ExtraEnv: map[string]string{"TEAM": "core"}}, wantArgs: []string{},
			wantEnv: map[string]string{"TEAM": "core"}},
		{name: "a codex template", tmpl: &apitest.TemplateFields{Agent: "codex", AgentArgs: []string{"--model", "x"}}, p: api.SpawnParams{},
			wantArgs: []string{"--model", "x"}, wantPass: []string{"--model", "x"}},
		{name: "a codex template, the call names codex", tmpl: &apitest.TemplateFields{Agent: "codex"}, p: api.SpawnParams{Agent: "codex"}, wantArgs: []string{}},
		{name: "a codex template with empty lists", tmpl: &apitest.TemplateFields{Agent: "codex", Permissions: &config.TemplatePermissions{}}, p: api.SpawnParams{},
			wantArgs: []string{}},
		{name: "a claude template, the call names codex and its own empty arguments", tmpl: &apitest.TemplateFields{Agent: "claude", AgentArgs: []string{"--model", "x"}},
			p: api.SpawnParams{Agent: "codex", AgentArgs: []string{}}, wantArgs: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			c := env.c
			if tc.config != "" {
				c = agentClientWithConfig(t, env, tc.config)
			}
			p := tc.p
			p.CWD = t.TempDir()
			if tc.tmpl != nil {
				seedAgentTemplate(t, env, *tc.tmpl)
				p.Template = "t"
			}

			res, err := c.Spawn(p)

			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			l := assertCodexLaunch(t, env.rec.SocketCallsOf(tmux.CallCreate), env.dbPath, res.InstanceID, codexProgram)
			if res.PreTrust != "ok" {
				t.Errorf("pre_trust = %q; want ok (a Codex launch writes no trust file)", res.PreTrust)
			}
			cols, err := apitest.ReadSpawnColumns(env.dbPath, res.InstanceID)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			if got := storedAgentArgs(t, cols); !slices.Equal(got, tc.wantArgs) {
				t.Errorf("stored agent_args = %q; want %q", got, tc.wantArgs)
			}
			if !slices.Equal(l.PassThrough(), tc.wantPass) {
				t.Errorf("pass-through arguments = %q; want %q", l.PassThrough(), tc.wantPass)
			}
			if wantAfter := slices.Index(tc.wantArgs, "--"); (wantAfter >= 0) != l.HasDashDash() ||
				(wantAfter >= 0 && !slices.Equal(l.AfterDashDashArgs(), tc.wantArgs[wantAfter+1:])) {
				t.Errorf("argv %q; want the prompt after the first `--` as %q", l.Argv, tc.wantArgs)
			}
			if env := storedExtraEnv(t, cols); tc.wantEnv != nil && env["TEAM"] != tc.wantEnv["TEAM"] {
				t.Errorf("stored extra_env = %v; want it to hold %v", env, tc.wantEnv)
			}
		})
	}
}

// TestSpawnCodexPathFromConfig (SR-5.0): [launch] codex_path is the program a
// codex spawn runs, one unchanged argument, over the program named codex.
func TestSpawnCodexPathFromConfig(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env := newSpawnEnv(t)
	path := filepath.Join(env.home, "my tools", "codex")
	c := agentClientWithConfig(t, env, "[launch]\ncodex_path = "+strconv.Quote(path)+"\n")

	res, err := c.Spawn(api.SpawnParams{Agent: "codex", CWD: t.TempDir()})

	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	assertCodexLaunch(t, env.rec.SocketCallsOf(tmux.CallCreate), env.dbPath, res.InstanceID, path)
}

// TestSpawnReuseAsCodex (SR-5.0, SR-5.1, SR-10): a reuse as codex onto a
// finished row, a claude or a codex one, each with an end report, launches
// codex: the row is pending with agent codex, the new arguments and extra_env,
// the end report cleared, and subagent_tracking 0 whatever the row had.
func TestSpawnReuseAsCodex(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, rowAgent := range []string{"claude", "codex"} {
		t.Run("a "+rowAgent+" row", func(t *testing.T) {
			e := newReuseEnv(t)
			opts := agentRowOpts(rowAgent, apitest.WithEndReportedAt(e.clock.Now().Add(-time.Hour).UnixMilli()))
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, Opts: opts})
			if cols := e.columns(t, r.ID); cols.EndReportedAt == nil || cols.Agent != rowAgent {
				t.Fatalf("seeded row: agent %v, end report %v; want %s, one set", cols.Agent, cols.EndReportedAt, rowAgent)
			}
			p := reuseParams(t, r, reuseRequest{Args: []string{"--model", "gpt", "--", "go on"}, Env: map[string]string{"TEAM": "core"}})
			p.Agent = "codex"
			e.agentOnCreate(r.ID, agentAlive)

			_, logs, err := e.reuse(t, p)

			if err != nil {
				t.Fatalf("reuse as codex: %v (log %q)", err, logs)
			}
			assertReuseLifeAndHistory(t, e, r, "codex")
			l := assertCodexLaunch(t, e.rec.SocketCallsOf(tmux.CallCreate), e.dbPath, r.ID, codexProgram)
			if !slices.Equal(l.PassThrough(), []string{"--model", "gpt"}) || !slices.Equal(l.AfterDashDashArgs(), []string{"go on"}) {
				t.Errorf("launch %q; want --model gpt before the double dash and its prompt after", l.Argv)
			}
			cols := e.columns(t, r.ID)
			if got := storedAgentArgs(t, cols); !slices.Equal(got, []string{"--model", "gpt", "--", "go on"}) {
				t.Errorf("stored agent_args = %q; want the reuse's arguments", got)
			}
			if got := storedExtraEnv(t, cols); got["TEAM"] != "core" {
				t.Errorf("stored extra_env = %v; want TEAM core", got)
			}
			if cols.EndReportedAt != nil {
				t.Errorf("end_reported_at = %v; want it cleared by the reuse", cols.EndReportedAt)
			}
		})
	}
}

// TestSpawnReuseTakesTheNewAgent (SR-5.0, SR-10): a reuse resets a finished
// row to the new spawn's agent, whatever agent the row ran.
func TestSpawnReuseTakesTheNewAgent(t *testing.T) {
	t.Parallel()
	for _, rowAgent := range []string{"claude", "codex"} {
		t.Run("a "+rowAgent+" row reused as claude", func(t *testing.T) {
			e := newReuseEnv(t)
			opts := agentRowOpts(rowAgent)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, Opts: opts})
			if got := e.columns(t, r.ID).Agent; got != rowAgent {
				t.Fatalf("seeded row agent = %q; want %q", got, rowAgent)
			}

			e.reuseLaunch(t, r, agentAlive, reuseRequest{})
			assertReuseLifeAndHistory(t, e, r, "claude")

			if cols := e.columns(t, r.ID); cols.Agent != "claude" || cols.State != store.StatePending {
				t.Errorf("row after the reuse: agent %q, state %v; want claude, pending", cols.Agent, cols.State)
			}
			if cols := e.columns(t, r.ID); cols.SubagentTracking != int64(1) {
				t.Errorf("subagent_tracking = %v; want 1 for the claude launch, whatever the row had", cols.SubagentTracking)
			}
			if create := e.rec.SocketCallsOf(tmux.CallCreate); len(create) != 1 || create[0].Command[0] != "claude" {
				t.Errorf("creates = %+v; want one, launching claude", create)
			}
		})
	}
}

// TestResumeCodexRowLaunches (SR-5.0, SR-7.4, SR-14.5): a resume of a codex
// row, ended or missing, launches `codex resume` with the row's session id,
// moves the row to pending and keeps the row's agent and its stored arguments,
// refusing none of them.
func TestResumeCodexRowLaunches(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			rollout := codexfix.TempRolloutPath(t, "sess-codex-row", true)
			stored := []string{"--model", "gpt", "--", "carry on"}
			spec := e.resumableSpec(rlkSettled(e), agentGone, codexRowOpts(apitest.WithJsonlPath(rollout), apitest.WithRawAgentArgs(`["--model","gpt","--","carry on"]`))...)
			spec.State, spec.SessionID = state, "sess-codex-row"
			row := e.seedResumableRow(t, spec)

			res, err := e.resume(row.ID)

			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			l := assertCodexLaunch(t, e.rec.SocketCallsOf(tmux.CallCreate), e.dbPath, row.ID, codexProgram)
			if it, ok := l.Resume(); !ok || it.SessionID != "sess-codex-row" {
				t.Errorf("launch %q; want codex resume sess-codex-row", l.Argv)
			}
			if !slices.Equal(l.PassThrough(), []string{"--model", "gpt"}) || !slices.Equal(l.AfterDashDashArgs(), []string{"carry on"}) {
				t.Errorf("launch %q; want the stored arguments replayed", l.Argv)
			}
			if res.PreTrust != "ok" {
				t.Errorf("pre_trust = %q; want ok", res.PreTrust)
			}
			if got := storedAgentArgs(t, e.columns(t, row.ID)); !slices.Equal(got, stored) {
				t.Errorf("stored agent_args = %q; want %q kept", got, stored)
			}
		})
	}
}

// assertReuseLifeAndHistory distinguishes retained history from an agent switch.
func assertReuseLifeAndHistory(t *testing.T, e *killEnv, r reuseRow, agent string) {
	t.Helper()
	life := reuseLife
	var prior []getWantPrior
	if r.Spawn.Agent == agent {
		prior = rlfRetained(r)
	} else {
		life++
	}
	rlvAssertLife(t, e, r.ID, life)
	rlfAssertListed(t, rlfGet(t, e, r.ID), "no_session", prior...)
	assertReuseRequestsDeleted(t, e, r)
}

func assertReuseRequestsDeleted(t *testing.T, e *killEnv, r reuseRow) {
	t.Helper()
	row := rlfGet(t, e, r.ID)
	if len(row.UnprovenRequests) != 0 || len(row.PermissionRequests) != 0 {
		t.Errorf("reuse retained permission requests: %+v", row)
	}
	for _, token := range []string{r.OpenRequest, r.DecidedRequest} {
		_, err := api.GetPermission(e.st, api.RelayView{}, api.GetPermissionParams{RequestToken: token})
		if !errors.Is(err, api.ErrPermissionRequestNotFound) {
			t.Errorf("old token %s: %v; want ErrPermissionRequestNotFound", token, err)
		}
	}
}

// TestSpawnReuseSwitchBackForgets keeps the child spawned by the caller environment visible.
func TestSpawnReuseSwitchBackForgets(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID.
	e := newReuseEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{})
	setenvIfChanged(t, "AGENT_DIRECTOR_INSTANCE_ID", r.ID)
	c, _ := e.client(t)
	child, err := c.Spawn(api.SpawnParams{Agent: "claude", CWD: r.CWD, NoPreTrust: true})
	if err != nil {
		t.Fatalf("child spawn: %v", err)
	}
	childBefore := e.columns(t, child.InstanceID)
	assertChildUnchanged := func() {
		t.Helper()
		rows, err := c.List(api.ListParams{Parent: r.ID})
		if err != nil || len(rows.Spawns) != 1 || rows.Spawns[0].InstanceID != child.InstanceID {
			t.Fatalf("list parent: %+v, %v", rows, err)
		}
		if after := e.columns(t, child.InstanceID); !reflect.DeepEqual(after, childBefore) {
			t.Errorf("child changed: %+v; want %+v", after, childBefore)
		}
	}
	assertChildUnchanged()
	setenvIfChanged(t, "AGENT_DIRECTOR_INSTANCE_ID", "")
	p := reuseParams(t, r, reuseRequest{})
	p.Agent = "codex"
	e.agentOnCreate(r.ID, agentAlive)
	if _, _, err := e.reuse(t, p); err != nil {
		t.Fatalf("switch: %v", err)
	}
	assertReuseLifeAndHistory(t, e, r, "codex")
	assertChildUnchanged()
	rlfEndLife(t, e, r.ID)
	r, _ = e.reuseLaunch(t, r, agentAlive, reuseRequest{})
	rlvAssertLife(t, e, r.ID, reuseLife+2)
	rlfAssertListed(t, rlfGet(t, e, r.ID), "no_session")
	assertChildUnchanged()
}

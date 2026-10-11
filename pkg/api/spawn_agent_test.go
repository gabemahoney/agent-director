package api_test

// spawn_agent_test.go covers the agent a spawn is for (SR-1, SR-2.1, SR-5.5,
// SR-14.4): the refusals of an agent that is missing, invalid, wrongly owning
// inherited arguments, or given permission lists a codex spawn cannot take,
// and the order of those checks beside spawn's earlier and later ones, each
// leaving nothing written; the agent a row records; and the Codex composition
// stop that plain spawn, reuse and resume reach for a codex agent.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
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
	codexStopText          = "a Codex launch is not available in this build"
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
	dir := filepath.Join(env.home, ".agent-director", "templates")
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
			tdir := filepath.Join(env.home, ".agent-director", "templates")
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
			if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Command[0] != "claude" {
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

// TestSpawnCodexStopsAtComposition (SR-5.5, SR-14.5): a codex spawn that
// passes every check, with no lists (disable_ask_user on, empty lists and a
// codex template included), is not ErrPermissionsUnsupported and stops at
// composition: ErrInternal with no row, no tmux call and the pre-trust file as seeded.
func TestSpawnCodexStopsAtComposition(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name   string
		tmpl   *apitest.TemplateFields
		config string // the config file's body
		p      api.SpawnParams
	}{
		{name: "no lists", p: api.SpawnParams{Agent: "codex"}},
		{name: "disable_ask_user on, no lists", config: "[defaults]\ndisable_ask_user = true\n", p: api.SpawnParams{Agent: "codex"}},
		{name: "empty lists", p: api.SpawnParams{Agent: "codex", Permissions: &api.Permissions{Allow: []string{}, Deny: []string{}, Ask: []string{}}}},
		{name: "a codex template", tmpl: &apitest.TemplateFields{Agent: "codex", AgentArgs: []string{"--model", "x"}}, p: api.SpawnParams{}},
		{name: "a codex template, the call names codex", tmpl: &apitest.TemplateFields{Agent: "codex"}, p: api.SpawnParams{Agent: "codex"}},
		{name: "a codex template with empty lists", tmpl: &apitest.TemplateFields{Agent: "codex", Permissions: &config.TemplatePermissions{}}, p: api.SpawnParams{}},
		{name: "a claude template, the call names codex and its own empty arguments", tmpl: &apitest.TemplateFields{Agent: "claude", AgentArgs: []string{"--model", "x"}},
			p: api.SpawnParams{Agent: "codex", AgentArgs: []string{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			trust := seedTrustConfig(t, env.home, trustLacksEntry)
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

			_, err := c.Spawn(p)

			assertOneName(t, err, "")
			if desc := errText(err); !strings.Contains(desc, codexStopText) || strings.Contains(desc, "ErrPermissionsUnsupported") {
				t.Errorf("err = %q; want the Codex composition stop alone", desc)
			}
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); len(ids) != 0 {
				t.Errorf("List ids = %q; want no row", ids)
			}
			trust.check(t, "", false, "after the stopped spawn")
		})
	}
}

// TestSpawnReuseAsCodexStopsAtComposition (SR-5.5, SR-10): a reuse as codex
// onto a finished row stops at composition after the one lookup, leaving the
// row, its history and requests, the trust file and the trail as they were.
func TestSpawnReuseAsCodexStopsAtComposition(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, rowAgent := range []string{"claude", "codex"} {
		t.Run("a "+rowAgent+" row", func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, Opts: []apitest.SpawnOption{apitest.WithAgent(rowAgent)}})
			p := reuseParams(t, r, reuseRequest{})
			p.Agent = "codex"
			before := e.snapshotReuse(t, r)

			_, _, err := e.reuse(t, p)

			assertOneName(t, err, "")
			if !strings.Contains(errText(err), codexStopText) {
				t.Errorf("err = %q; want it to say %q", err, codexStopText)
			}
			e.assertWroteNothing(t, before)
			if cols := e.columns(t, r.ID); cols.Agent != rowAgent {
				t.Errorf("row agent = %q; want %q as it was", cols.Agent, rowAgent)
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
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, Opts: []apitest.SpawnOption{apitest.WithAgent(rowAgent)}})
			if got := e.columns(t, r.ID).Agent; got != rowAgent {
				t.Fatalf("seeded row agent = %q; want %q", got, rowAgent)
			}

			e.reuseLaunch(t, r, agentAlive, reuseRequest{})

			if cols := e.columns(t, r.ID); cols.Agent != "claude" || cols.State != store.StatePending {
				t.Errorf("row after the reuse: agent %q, state %v; want claude, pending", cols.Agent, cols.State)
			}
			if create := e.rec.SocketCallsOf(tmux.CallCreate); len(create) != 1 || create[0].Command[0] != "claude" {
				t.Errorf("creates = %+v; want one, launching claude", create)
			}
		})
	}
}

// TestResumeCodexRowStopsAtComposition (SR-5.0, SR-14.5): a resume of a codex
// row stops at composition after its lookup: ErrInternal, the row, history and
// requests as they were, and no create.
func TestResumeCodexRowStopsAtComposition(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			spec := e.resumableSpec(rlkSettled(e), agentGone, apitest.WithAgent("codex"))
			spec.State = state
			row := e.seedResumableRow(t, spec)
			before := e.snapshotResume(t, row)

			_, err := e.resume(row.ID)

			assertOneName(t, err, "")
			if !strings.Contains(errText(err), codexStopText) {
				t.Errorf("err = %q; want it to say %q", err, codexStopText)
			}
			e.assertResumeWroteNothing(t, before)
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
				t.Errorf("creates = %d; want none", n)
			}
			if cols := e.columns(t, row.ID); cols.Agent != "codex" {
				t.Errorf("row agent = %q; want codex as it was", cols.Agent)
			}
		})
	}
}

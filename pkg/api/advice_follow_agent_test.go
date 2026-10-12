package api_test

// advice_follow_agent_test.go follows, literally, the advice the agent
// refusals give (SR-4.4, SR-14.4): trigger the refusal, pin its description,
// do what it says as an automated caller would, and check the promised
// outcome. A claude and a codex agent that pass every check both launch
// (SR-5.1, SR-5.5). The refusals
// and their order are covered in spawn_agent_test.go and
// make_template_agent_test.go.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// advAgentFollow is one followed spawn: the agent the advice leads to.
type advAgentFollow struct {
	name  string
	agent string // the agent the followed spawn launches
}

// advAgentFollows are the two agents a caller can follow the advice with.
var advAgentFollows = []advAgentFollow{{"claude", "claude"}, {"codex", "codex"}}

// advAgentSpawn runs p on env, expecting the followed spawn's outcome for agent: one create launching it and a
// pending row recording it.
func advAgentSpawn(t *testing.T, env spawnEnv, p api.SpawnParams, agent string) {
	t.Helper()
	res, err := env.c.Spawn(p)
	if err != nil {
		t.Fatalf("followed spawn: %v; want it launched", err)
	}
	if agent == "codex" {
		assertCodexLaunch(t, env.rec.SocketCallsOf(tmux.CallCreate), env.dbPath, res.InstanceID, codexProgram)
		return
	}
	if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Command[0] != "claude" {
		t.Errorf("creates = %+v; want one, launching claude", creates)
	}
	if cols, err := apitest.ReadSpawnColumns(env.dbPath, res.InstanceID); err != nil || cols.Agent != agent || cols.State != store.StatePending {
		t.Errorf("row {agent %v, state %v}, %v; want pending, agent %s", cols.Agent, cols.State, err, agent)
	}
}

// TestAdviceFollow_A16_AgentMissingPassAgent: a spawn naming no agent whose template names none is refused;
// passing agent claude or codex, as the advice says, launches; so does putting the
// agent in the template.
// A16 (SR-4.4): "pass agent claude or codex (--agent on the CLI), or put agent in the template"
func TestAdviceFollow_A16_AgentMissingPassAgent(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertGoDoc(t, "spawn.go", "Spawn", "ErrAgentMissing: the call gives no Agent and its template names none")
	adviceAssertManifest(t, "spawn", "agent", "claude", "codex")
	for _, f := range advAgentFollows {
		for _, viaTemplate := range []bool{false, true} {
			name := f.name + " passed by the call"
			if viaTemplate {
				name = f.name + " put in the template"
			}
			t.Run(name, func(t *testing.T) {
				env := newSpawnEnv(t)
				p := api.SpawnParams{CWD: t.TempDir(), NoPreTrust: true}
				if viaTemplate {
					seedAgentTemplate(t, env, apitest.TemplateFields{RelayMode: "on"})
					p.Template = "t"
				}

				_, refused := env.c.Spawn(p)

				adviceAssertAdvice(t, refused, api.ErrAgentMissing, agentMissingText)
				assertNoTmuxCalls(t, env.rec)
				if viaTemplate {
					seedAgentTemplate(t, env, apitest.TemplateFields{RelayMode: "on", Agent: f.agent})
				} else {
					p.Agent = f.agent
				}
				advAgentSpawn(t, env, p, f.agent)
			})
		}
	}
}

// TestAdviceFollow_A17_AgentInvalidNameClaudeOrCodex: a spawn naming an agent that is neither claude nor codex
// (any other word, or another case) is refused with its value quoted; the call again with claude or codex, as
// the advice says, launches.
// A17 (SR-4.4): "agent \"gemini\" (want claude or codex)"
func TestAdviceFollow_A17_AgentInvalidNameClaudeOrCodex(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	for _, bad := range []string{"gemini", "Claude", "CODEX"} {
		for _, f := range advAgentFollows {
			t.Run(bad+" then "+f.name, func(t *testing.T) {
				env := newSpawnEnv(t)
				p := api.SpawnParams{Agent: bad, CWD: t.TempDir(), NoPreTrust: true}

				_, refused := env.c.Spawn(p)

				adviceAssertAdvice(t, refused, api.ErrAgentInvalid, fmt.Sprintf(agentInvalidTextFmt, bad))
				assertNoTmuxCalls(t, env.rec)
				p.Agent = f.agent
				advAgentSpawn(t, env, p, f.agent)
			})
		}
	}
}

// TestAdviceFollow_A18_AgentArgsMismatchOwnArgsOrTemplateAgent: a template's agent_args for one agent are not
// inherited by a spawn of the other; each remedy the advice gives works: spawn the template's agent, or pass the
// call's own non-empty agent_args.
// A18 (SR-4.4): "spawn agent claude, or pass this call's own non-empty agent_args, which replace the template's"
func TestAdviceFollow_A18_AgentArgsMismatchOwnArgsOrTemplateAgent(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertGoDoc(t, "spawn.go", "Spawn", "ErrAgentArgsMismatch: the template's agent_args are for another agent")
	remedies := []struct {
		name  string
		agent string // the agent the remedy spawns
		args  []string
	}{
		{"spawn the template's agent", "claude", nil},
		{"the call's own arguments", "codex", []string{"--model", "gpt"}},
	}
	for _, r := range remedies {
		t.Run(r.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			seedAgentTemplate(t, env, apitest.TemplateFields{Agent: "claude", AgentArgs: []string{"--model", "opus"}})
			p := api.SpawnParams{Agent: "codex", CWD: t.TempDir(), Template: "t", NoPreTrust: true}

			_, refused := env.c.Spawn(p)

			adviceAssertAdvice(t, refused, api.ErrAgentArgsMismatch, fmt.Sprintf(agentArgsMismatchStart, "claude", "codex", "claude"))
			assertNoTmuxCalls(t, env.rec)
			p.Agent, p.AgentArgs = r.agent, r.args
			advAgentSpawn(t, env, p, r.agent)
		})
	}
}

// TestAdviceFollow_A19_TemplateAgentMissingAddAgentOrDropArgs: a template with agent_args and no agent is
// refused at spawn, whatever agent the call names; adding agent to the template, or removing agent_args, as the
// advice says, lets the spawn through.
// A19 (SR-4.4): "add agent = \"claude\" or \"codex\", or remove agent_args"
func TestAdviceFollow_A19_TemplateAgentMissingAddAgentOrDropArgs(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	broken := apitest.TemplateFields{AgentArgs: []string{"--model", "opus"}}
	fixes := []struct {
		name  string
		agent string
		fixed apitest.TemplateFields
	}{
		{"add claude", "claude", apitest.TemplateFields{Agent: "claude", AgentArgs: broken.AgentArgs}},
		{"add codex", "codex", apitest.TemplateFields{Agent: "codex", AgentArgs: broken.AgentArgs}},
		{"remove agent_args, the call names claude", "claude", apitest.TemplateFields{}},
		{"remove agent_args, the call names codex", "codex", apitest.TemplateFields{}},
	}
	for _, fix := range fixes {
		t.Run(fix.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			seedAgentTemplate(t, env, broken)
			p := api.SpawnParams{Agent: fix.agent, CWD: t.TempDir(), Template: "t", NoPreTrust: true}

			_, refused := env.c.Spawn(p)

			adviceAssertAdvice(t, refused, api.ErrTemplateAgentMissing, templateNoAgentText)
			assertNoTmuxCalls(t, env.rec)
			seedAgentTemplate(t, env, fix.fixed)
			advAgentSpawn(t, env, p, fix.agent)
		})
	}
}

// TestAdviceFollow_A20_MakeTemplateAgentMissingAddAgentOrDropArgs: make-template with agent_args and no agent is
// refused and writes no templates dir; passing agent claude or codex, or removing agent_args, as the advice
// says, saves the template.
// A20 (SR-4.4): "pass agent claude or codex, or remove agent_args"
func TestAdviceFollow_A20_MakeTemplateAgentMissingAddAgentOrDropArgs(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	adviceAssertGoDoc(t, "make_template.go", "MakeTemplate", "ErrTemplateAgentMissing")
	fixes := []struct {
		name string
		fix  func(p *api.MakeTemplateParams)
		want config.TemplateFile
	}{
		{"pass claude", func(p *api.MakeTemplateParams) { p.Agent = "claude" }, config.TemplateFile{Agent: "claude", AgentArgs: []string{"--model", "x"}}},
		{"pass codex", func(p *api.MakeTemplateParams) { p.Agent = "codex" }, config.TemplateFile{Agent: "codex", AgentArgs: []string{"--model", "x"}}},
		{"remove agent_args", func(p *api.MakeTemplateParams) { p.AgentArgs = nil }, config.TemplateFile{}},
	}
	for _, fix := range fixes {
		t.Run(fix.name, func(t *testing.T) {
			home := withTempHome(t)
			p := api.MakeTemplateParams{Name: "adv-agent", AgentArgs: []string{"--model", "x"}}

			_, refused := api.MakeTemplate(p)

			adviceAssertAdvice(t, refused, api.ErrTemplateAgentMissing, mtAgentMissingTxt)
			if entries := readDirFiles(t, filepath.Join(adConfigDir(home), "templates")); entries != nil {
				t.Errorf("templates dir holds %v after the refusal; want none created", entries)
			}
			fix.fix(&p)
			if _, err := api.MakeTemplate(p); err != nil {
				t.Fatalf("followed make-template: %v; want it saved", err)
			}
			got, err := config.LoadTemplate("adv-agent")
			if err != nil || got.Agent != fix.want.Agent || strings.Join(got.AgentArgs, " ") != strings.Join(fix.want.AgentArgs, " ") {
				t.Errorf("LoadTemplate = %+v, %v; want agent %q, args %q", got, err, fix.want.Agent, fix.want.AgentArgs)
			}
		})
	}
}

// TestAdviceFollow_A21_PermissionsUnsupportedDropTheLists: a codex spawn carrying allow, deny or ask entries is
// refused; the same spawn without the lists, as the advice says (the rules and Codex's own controls are the
// operator's, outside any call), launches codex.
// A21 (SR-5.5): "a codex agent takes no allow, deny or ask entries; set permission rules in the rules/ folder of the group's Codex folder and use Codex's own permission controls"
func TestAdviceFollow_A21_PermissionsUnsupportedDropTheLists(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertGoDoc(t, "spawn.go", "Spawn", "ErrPermissionsUnsupported: a codex spawn carries Permissions")
	entry := []string{"Bash(ls)"}
	cases := []struct {
		name string
		p    api.SpawnParams
		tmpl *apitest.TemplateFields
		undo func(t *testing.T, p *api.SpawnParams, env spawnEnv)
	}{
		{"allow on the call", api.SpawnParams{Agent: "codex", Permissions: &api.Permissions{Allow: entry}}, nil,
			func(_ *testing.T, p *api.SpawnParams, _ spawnEnv) { p.Permissions = nil }},
		{"deny on the call", api.SpawnParams{Agent: "codex", Permissions: &api.Permissions{Deny: entry}}, nil,
			func(_ *testing.T, p *api.SpawnParams, _ spawnEnv) { p.Permissions = nil }},
		{"ask on the call", api.SpawnParams{Agent: "codex", Permissions: &api.Permissions{Ask: entry}}, nil,
			func(_ *testing.T, p *api.SpawnParams, _ spawnEnv) { p.Permissions = nil }},
		{"entries in the codex template", api.SpawnParams{}, &apitest.TemplateFields{Agent: "codex", Permissions: &config.TemplatePermissions{Allow: entry}},
			func(t *testing.T, _ *api.SpawnParams, env spawnEnv) {
				seedAgentTemplate(t, env, apitest.TemplateFields{Agent: "codex"})
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			p := tc.p
			p.CWD, p.NoPreTrust = t.TempDir(), true
			if tc.tmpl != nil {
				seedAgentTemplate(t, env, *tc.tmpl)
				p.Template = "t"
			}

			_, refused := env.c.Spawn(p)

			adviceAssertAdvice(t, refused, api.ErrPermissionsUnsupported, permissionsText)
			assertNoTmuxCalls(t, env.rec)
			tc.undo(t, &p, env)
			advAgentSpawn(t, env, p, "codex")
		})
	}
}

// TestAdviceFollow_A22_MakeTemplatePermissionsUnsupportedDropTheLists: make-template of a codex template with
// allow, deny or ask entries is refused and writes no templates dir; without the lists the template saves.
// A22 (SR-5.5): "a codex agent takes no allow, deny or ask entries; ..."
func TestAdviceFollow_A22_MakeTemplatePermissionsUnsupportedDropTheLists(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	entry := []string{"Bash(jq)"}
	for name, perms := range map[string]*api.MakeTemplatePermissions{
		"allow": {Allow: entry}, "deny": {Deny: entry}, "ask": {Ask: entry},
	} {
		t.Run(name, func(t *testing.T) {
			home := withTempHome(t)
			p := api.MakeTemplateParams{Name: "adv-perms", Agent: "codex", Permissions: perms}

			_, refused := api.MakeTemplate(p)

			adviceAssertAdvice(t, refused, api.ErrPermissionsUnsupported, permissionsText)
			if entries := readDirFiles(t, filepath.Join(adConfigDir(home), "templates")); entries != nil {
				t.Errorf("templates dir holds %v after the refusal; want none created", entries)
			}
			p.Permissions = nil
			if _, err := api.MakeTemplate(p); err != nil {
				t.Fatalf("followed make-template: %v; want it saved", err)
			}
			if got, err := config.LoadTemplate("adv-perms"); err != nil || got.Agent != "codex" {
				t.Errorf("LoadTemplate = %+v, %v; want a codex template", got, err)
			}
		})
	}
}

// TestAdviceFollow_B13_CodexPendingRowSpawnWithReuse: resume of a pending codex row with no session id is refused
// naming agent codex in its spawn-again step; find-missing past the grace period marks it missing; the spawn
// the advice describes, with the reuse opt-in and agent codex, launches the row's next life as codex.
// B13 (SR-4.4): "... spawn the id again with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and agent codex (--agent codex on the CLI); nothing was written"
func TestAdviceFollow_B13_CodexPendingRowSpawnWithReuse(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	r := reuseRow{resumeRow: e.seedOnServer(t, killRowSpec{State: store.StatePending, Agent: agentGone, NoSession: true,
		Opts: codexRowOpts(apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli()))}, e.seedRow)}
	id := r.ID
	before := e.columns(t, id)

	_, refused := e.resume(id)

	adviceAssertAdvice(t, refused, api.ErrSpawnNotResumable, advResumeNoSessionAdvice("codex"))
	e.assertRowUnchanged(t, id, before)
	if st := adviceAwaitFinished(t, c, e.clock, id, nil); st != store.StateMissing {
		t.Fatalf("get %s past the pending grace period: state %q; want missing", id, st)
	}
	if row, err := c.Get(id); err != nil || row.SessionID != "" || row.Agent != "codex" {
		t.Fatalf("get %s once missing = session id %q, agent %q, %v; want none, codex", id, row.SessionID, row.Agent, err)
	}
	missing := e.columns(t, id)
	creates := len(e.rec.SocketCallsOf(tmux.CallCreate))

	p := reuseParams(t, r, reuseRequest{})
	p.Agent = "codex"
	_, _, err := e.reuse(t, p)

	if err != nil {
		t.Fatalf("the followed spawn: %v; want it launched", err)
	}
	assertCodexLaunch(t, e.rec.SocketCallsOf(tmux.CallCreate)[creates:], e.dbPath, id, codexProgram)
	if cols := e.columns(t, id); cols.LifeNumber != advSpawnNextLife(missing) {
		t.Errorf("life = %v after the followed spawn; want the next life %v", cols.LifeNumber, advSpawnNextLife(missing))
	}
}

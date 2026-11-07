package api_test

// advice_follow_agent_test.go follows, literally, the advice the agent
// refusals give (SR-4.4, SR-14.4): trigger the refusal, pin its description,
// do what it says as an automated caller would, and check the promised
// outcome. A supported agent that pass every check both launch
// (SR-5.1, SR-5.5). The refusals
// and their order are covered in spawn_agent_test.go and
// make_template_agent_test.go.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// A27: "enable that flag before creating its launch or template".
func TestAdviceFollow_A27_EnablePaneOnlyAgent(t *testing.T) {
	// Serial: it sets HOME for configuration and launch files.
	for _, agent := range []string{"droid"} {
		for _, template := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/template=%t", agent, template), func(t *testing.T) {
				e := newSpawnEnv(t)
				p := api.SpawnParams{Agent: agent, CWD: t.TempDir(), AgentArgs: []string{"--model", "model"}}
				call := func(c *api.Client) error {
					if template {
						_, err := c.MakeTemplate(api.MakeTemplateParams{Name: "lane", Agent: agent})
						return err
					}
					_, err := c.Spawn(p)
					return err
				}
				adviceAssertAdvice(t, call(e.c), api.ErrExperimentalAgentDisabled, "enable that flag before creating its launch or template")
				c := agentClientWithConfig(t, e, "[experimental]\n"+agent+" = true\n")
				if err := call(c); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// A28: follow each model refusal's instruction to supply one split subscription model.
func TestAdviceFollow_A28_PaneOnlyModelAdvice(t *testing.T) {
	// Serial: it sets HOME for launch files.
	for _, agent := range []string{"droid"} {
		for _, tc := range []struct {
			name   string
			args   []string
			want   error
			advice string
		}{
			{"missing", nil, api.ErrModelMissing, "supply exactly one explicit subscription model"},
			{"joined", []string{"--model=model"}, spawn.ErrSpawnDeniedFlag, "supply --model and its ID as separate arguments"},
			{"positional", []string{"--model", "model", "task"}, spawn.ErrSpawnDeniedFlag, "remove this argument and any following value; supply only --model ID"},
			{"duplicate", []string{"--model", "model", "--model", "second"}, spawn.ErrSpawnDeniedFlag, "supply exactly one --model ID"},
			{"option value", []string{"--model", "--verbose"}, spawn.ErrSpawnDeniedFlag, "--model requires a nonempty ID that is not an option; supply --model ID"},
			{"custom", []string{"--model", "custom:private"}, spawn.ErrSpawnDeniedFlag, "supply a subscription model ID"},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				e := newSpawnEnv(t)
				c := agentClientWithConfig(t, e, "[experimental]\n"+agent+" = true\n")
				p := api.SpawnParams{Agent: agent, CWD: t.TempDir(), AgentArgs: tc.args}
				err := e.checkCreationWrites(t, func() error {
					_, err := c.Spawn(p)
					return err
				})
				adviceAssertAdvice(t, err, tc.want, tc.advice)
				p.AgentArgs = []string{"--model", "subscription-model"}
				if _, err = c.Spawn(p); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// A29: remove an unsupported capability, permission entry or environment key.
func TestAdviceFollow_A29_PaneOnlySettings(t *testing.T) {
	// Serial: it sets HOME for launch files.
	for _, agent := range []string{"droid"} {
		for _, tc := range []struct {
			name, advice string
			want         error
			set, follow  func(*api.SpawnParams)
		}{
			{"relay", "omit this capability and supervise the owned pane", api.ErrAgentCapabilityUnsupported, func(p *api.SpawnParams) { p.RelayMode = "on" }, func(p *api.SpawnParams) { p.RelayMode = "" }},
			{"permissions", "remove those entries and handle native permission prompts through the pane", api.ErrPermissionsUnsupported, func(p *api.SpawnParams) { p.Permissions = &api.Permissions{Allow: []string{"Bash"}} }, func(p *api.SpawnParams) { p.Permissions = nil }},
			{"environment", "remove that key from extra_env", spawn.ErrReservedEnvKey, func(p *api.SpawnParams) { p.ExtraEnv = map[string]string{"FACTORY_API_KEY": "sentinel"} }, func(p *api.SpawnParams) { delete(p.ExtraEnv, "FACTORY_API_KEY") }},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				e := newSpawnEnv(t)
				c := agentClientWithConfig(t, e, "[experimental]\n"+agent+" = true\n")
				p := api.SpawnParams{Agent: agent, CWD: t.TempDir(), AgentArgs: []string{"--model", "model"}}
				tc.set(&p)
				err := e.checkCreationWrites(t, func() error {
					_, err := c.Spawn(p)
					return err
				})
				adviceAssertAdvice(t, err, tc.want, tc.advice)
				tc.follow(&p)
				if _, err = c.Spawn(p); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// E21: "omit this capability and supervise the owned pane"; manifest says allow_pending.
func TestAdviceFollow_E21_PaneOnlySupervision(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "get", "integration_mode", "Plain send-keys requires allow_pending and returns an empty manual result")
	for _, agent := range []string{"droid"} {
		t.Run(agent, func(t *testing.T) {
			e := newKillEnv(t)
			e.rowAgent = agent
			r := e.seedPending(t, pendingFresh, pendingOurs)
			c, _ := e.client(t)
			_, err := c.Pause(context.Background(), api.PauseParams{InstanceID: r.ID})
			adviceAssertAdvice(t, err, api.ErrAgentCapabilityUnsupported, "omit this capability and supervise the owned pane")
			e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "native prompt")
			pane, err := c.ReadPane(api.ReadPaneParams{InstanceID: r.ID})
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.SendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "answer", AllowPending: true, ExpectPaneSHA256: pane.PaneSHA256})
			if err != nil || jsonOf(t, res) != "{}" {
				t.Fatalf("manual supervision=%+v, %v", res, err)
			}
		})
	}
}

// advAgentFollow is one followed spawn: the agent the advice leads to.
type advAgentFollow struct {
	name  string
	agent string // the agent the followed spawn launches
}

// advAgentFollows are the agents a caller can follow the advice with; Droid
// cases enable its flag before requesting advice.
var advAgentFollows = []advAgentFollow{{"claude", "claude"}, {"codex", "codex"}, {"cursor", "cursor"}, {"droid", "droid"}}

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
	if agent == "droid" {
		creates := env.rec.SocketCallsOf(tmux.CallCreate)
		if len(creates) != 1 || len(creates[0].Command) != 3 || creates[0].Command[0] != "droid" || creates[0].Command[1] != "--settings" {
			t.Fatalf("followed Droid advice did not launch its settings overlay: %+v", creates)
		}
	} else if agent == "cursor" {
		creates := env.rec.SocketCallsOf(tmux.CallCreate)
		if len(creates) != 1 || len(creates[0].Command) < 2 || creates[0].Command[1] != "cursor-host" {
			t.Fatalf("followed Cursor advice did not launch gateway: %+v", creates)
		}
	} else {
		if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Command[0] != "claude" {
			t.Errorf("creates = %+v; want one, launching claude", creates)
		}
	}
	if cols, err := apitest.ReadSpawnColumns(env.dbPath, res.InstanceID); err != nil || cols.Agent != agent || cols.State != store.StatePending {
		t.Errorf("row {agent %v, state %v}, %v; want pending, agent %s", cols.Agent, cols.State, err, agent)
	}
}

// TestAdviceFollow_A16_AgentMissingPassAgent: a spawn naming no agent whose template names none is refused;
// passing a listed agent, as the advice says, launches; so does putting the
// agent in the template. Droid is listed only in the enabled cases.
// A16 (SR-4.4): "pass agent claude, codex or cursor (--agent on the CLI), or put agent in the template"
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
				want := agentMissingText
				if f.agent == "droid" {
					env.c = agentClientWithConfig(t, env, "[experimental]\ndroid = true\n")
					p.AgentArgs, p.RelayMode = []string{"--model", "model"}, "off"
					want = strings.Replace(want, "claude, codex or cursor", "claude, codex, cursor or droid", 1)
				}
				if viaTemplate {
					seedAgentTemplate(t, env, apitest.TemplateFields{RelayMode: "on"})
					p.Template = "t"
				}

				_, refused := env.c.Spawn(p)

				adviceAssertAdvice(t, refused, api.ErrAgentMissing, want)
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

// TestAdviceFollow_A17_AgentInvalidNameClaudeOrCodex: an unrecognized agent is
// refused with its value quoted; retrying with a listed agent launches.
// The enabled cases list Droid; the default cases omit it.
// A17 (SR-4.4): "agent \"gemini\" (want claude, codex or cursor)"
func TestAdviceFollow_A17_AgentInvalidNameClaudeOrCodex(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	for _, bad := range []string{"gemini", "Claude", "CODEX"} {
		for _, f := range advAgentFollows {
			t.Run(bad+" then "+f.name, func(t *testing.T) {
				env := newSpawnEnv(t)
				p := api.SpawnParams{Agent: bad, CWD: t.TempDir(), NoPreTrust: true}
				want := fmt.Sprintf(agentInvalidTextFmt, bad)
				if f.agent == "droid" {
					env.c = agentClientWithConfig(t, env, "[experimental]\ndroid = true\n")
					p.AgentArgs = []string{"--model", "model"}
					want = strings.Replace(want, "claude, codex or cursor", "claude, codex, cursor or droid", 1)
				}

				_, refused := env.c.Spawn(p)

				adviceAssertAdvice(t, refused, api.ErrAgentInvalid, want)
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
// advice says, lets the spawn through. The enabled Droid case additionally
// preserves raw support even though template-load advice has no loaded config.
// A19 (SR-4.4): "add agent = \"claude\", \"codex\" or \"cursor\", or remove agent_args"
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
		{"add cursor", "cursor", apitest.TemplateFields{Agent: "cursor", AgentArgs: broken.AgentArgs}},
		{"add droid", "droid", apitest.TemplateFields{Agent: "droid", AgentArgs: broken.AgentArgs}},
		{"remove agent_args, the call names claude", "claude", apitest.TemplateFields{}},
		{"remove agent_args, the call names codex", "codex", apitest.TemplateFields{}},
		{"remove agent_args, the call names cursor", "cursor", apitest.TemplateFields{}},
	}
	for _, fix := range fixes {
		t.Run(fix.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			seedAgentTemplate(t, env, broken)
			p := api.SpawnParams{Agent: fix.agent, CWD: t.TempDir(), Template: "t", NoPreTrust: true}
			if fix.agent == "droid" {
				env.c = agentClientWithConfig(t, env, "[experimental]\ndroid = true\n")
			}

			_, refused := env.c.Spawn(p)

			adviceAssertAdvice(t, refused, api.ErrTemplateAgentMissing, templateNoAgentText)
			assertNoTmuxCalls(t, env.rec)
			seedAgentTemplate(t, env, fix.fixed)
			advAgentSpawn(t, env, p, fix.agent)
		})
	}
}

// TestAdviceFollow_A20_MakeTemplateAgentMissingAddAgentOrDropArgs: make-template with agent_args and no agent is
// refused and writes no templates dir; passing agent claude, codex or cursor, or removing agent_args, as the advice
// says, saves the template. The enabled Droid case preserves raw support;
// package-level missing-agent validation does not load the experimental flag.
// A20 (SR-4.4): "pass agent claude, codex or cursor, or remove agent_args"
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
		{"pass cursor", func(p *api.MakeTemplateParams) { p.Agent = "cursor" }, config.TemplateFile{Agent: "cursor", AgentArgs: []string{"--model", "x"}}},
		{"pass droid", func(p *api.MakeTemplateParams) { p.Agent = "droid" }, config.TemplateFile{Agent: "droid", AgentArgs: []string{"--model", "x"}}},
		{"remove agent_args", func(p *api.MakeTemplateParams) { p.AgentArgs = nil }, config.TemplateFile{}},
	}
	for _, fix := range fixes {
		t.Run(fix.name, func(t *testing.T) {
			home := withTempHome(t)
			p := api.MakeTemplateParams{Name: "adv-agent", AgentArgs: []string{"--model", "x"}}
			if fix.want.Agent == "droid" {
				writeConfig(t, filepath.Join(adConfigDir(home), "config.toml"), "[experimental]\ndroid = true\n")
			}

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
	adviceAssertGoDoc(t, "spawn.go", "Spawn", "ErrPermissionsUnsupported: a codex, cursor or pane-only spawn carries Permissions")
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

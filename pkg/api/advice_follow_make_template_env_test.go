package api_test

// advice_follow_make_template_env_test.go follows, literally, the advice
// make-template's refusals of an extra_env key give (b.66q): spawn's A14 and A15
// texts, followed at make-template. The refusals themselves are covered in
// make_template_test.go.

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// advTemplateHomeDoc is A14 as MakeTemplate's Go doc words it.
const advTemplateHomeDoc = "Set the agent's absolute folder variable instead to give it its own config: CLAUDE_CONFIG_DIR for claude, CODEX_HOME for codex, and the message names both when Agent is empty"

// advTemplateHomeBoth is the alternative a make-template with no agent gets: both folder variables.
const advTemplateHomeBoth = "to give the agent its own config, set an absolute CLAUDE_CONFIG_DIR (Claude Code) or CODEX_HOME (Codex) in extra_env instead"

// TestAdviceFollow_A14_MakeTemplateReservedHomeSetConfigDir: the same template with HOME removed and the
// advised folder variable set saves and its spawn launches; with no agent the advice names both variables and the template saves with either.
// A14 (b.66q): "remove \"HOME\" from extra_env, and to give the agent its own Claude Code config, set an absolute CLAUDE_CONFIG_DIR in extra_env instead" (Codex: "its own Codex config, set an absolute CODEX_HOME"; no agent: "its own config, set an absolute CLAUDE_CONFIG_DIR (Claude Code) or CODEX_HOME (Codex)")
func TestAdviceFollow_A14_MakeTemplateReservedHomeSetConfigDir(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertManifest(t, "make-template", "extra_env", advSpawnHomeParam)
	adviceAssertGoDoc(t, "make_template.go", "MakeTemplate", advTemplateHomeDoc)
	agents := append(append([]advHomeAgent{}, advHomeAgents...), advHomeAgent{"", "CLAUDE_CONFIG_DIR", advTemplateHomeBoth})
	for _, a := range agents {
		t.Run("agent "+a.agent, func(t *testing.T) {
			env, cfg := newSpawnEnv(t), seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			adviceFollowTemplateEnvKey(t, env, cfg, a, "HOME", cfg.dir, `"HOME" sets HOME, which is reserved`, advSpawnHome(a))
		})
	}
}

// TestAdviceFollow_A15_MakeTemplateInvalidEnvKeyNameTheVariable: the same
// template with the key removed and CLAUDE_CONFIG_DIR as its own key saves and launches.
// A15 (b.66q): "remove \"CLAUDE_CONFIG_DIR=...\" from extra_env, and give each variable its own name as the key (...) and its value as the value"
func TestAdviceFollow_A15_MakeTemplateInvalidEnvKeyNameTheVariable(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env, cfg := newSpawnEnv(t), seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	bad := "CLAUDE_CONFIG_DIR=" + cfg.dir
	adviceFollowTemplateEnvKey(t, env, cfg, advHomeAgents[0], bad, "", strconv.Quote(bad)+" is not a valid env-var name: it contains '='",
		"remove "+strconv.Quote(bad)+" from extra_env, and "+advEnvKeyOwnName)
}

// adviceFollowTemplateEnvKey: make-template of a's template (a.agent "": no agent) with key=value is
// ErrReservedEnvKey, its description carrying phrase (which refusal it is) and advice; without key and with
// a.variable=cfg.dir it saves, and a spawn with it launches a claude agent with that variable, pre-trusting
// cfg, launches a codex agent with that variable, and for no agent (a spawn naming claude) launches claude.
func adviceFollowTemplateEnvKey(t *testing.T, env spawnEnv, cfg trustConfig, a advHomeAgent, key, value, phrase, advice string) {
	t.Helper()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	p := api.MakeTemplateParams{Name: "adv-env", Agent: a.agent, ExtraEnv: map[string]string{key: value}}

	_, err = env.c.MakeTemplate(p)

	adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, advice)
	adviceAssertPhrase(t, err, phrase)
	delete(p.ExtraEnv, key)
	p.ExtraEnv[a.variable] = cfg.dir
	if _, err := env.c.MakeTemplate(p); err != nil {
		t.Fatalf("make-template with %s as its own key = %v; want it saved", a.variable, err)
	}
	id := "adv-tplenv-" + uuid.NewString()[:8]
	sp := api.SpawnParams{CWD: cwd, InstanceID: id, Template: p.Name}
	if a.agent == "" {
		sp.Agent = "claude" // a template naming no agent: the spawn names it
	}
	res, err := env.c.Spawn(sp)
	if a.agent == "codex" {
		if err != nil {
			t.Fatalf("a spawn with the saved template = %v; want it launched", err)
		}
		creates := env.rec.SocketCallsOf(tmux.CallCreate)
		assertCodexLaunch(t, creates, env.dbPath, id, codexProgram)
		if creates[0].Envs["CODEX_HOME"] != cfg.dir || res.PreTrust != "ok" {
			t.Errorf("create env CODEX_HOME %q, pre_trust %q; want %q, ok", creates[0].Envs["CODEX_HOME"], res.PreTrust, cfg.dir)
		}
		return
	}
	advSpawnLaunched(t, env.dbPath, id, nil, res, err)
	if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Envs["CLAUDE_CONFIG_DIR"] != cfg.dir {
		t.Errorf("creates = %+v; want one, its session env CLAUDE_CONFIG_DIR %q", creates, cfg.dir)
	}
	cfg.check(t, cwd, true, "after the followed make-template")
}

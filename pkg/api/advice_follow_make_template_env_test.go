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

// advTemplateHomeDoc is A14 as (*Client).MakeTemplate's Go doc words it.
const advTemplateHomeDoc = "Set an absolute CLAUDE_CONFIG_DIR instead to give the agent its own Claude Code config"

// TestAdviceFollow_A14_MakeTemplateReservedHomeSetConfigDir: the same template
// with HOME removed and an absolute CLAUDE_CONFIG_DIR set saves and launches.
// A14 (b.66q): "remove \"HOME\" from extra_env, and to give the agent its own Claude Code config, set an absolute CLAUDE_CONFIG_DIR in extra_env instead"
func TestAdviceFollow_A14_MakeTemplateReservedHomeSetConfigDir(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertManifest(t, "make-template", "extra_env", advSpawnHomeParam)
	adviceAssertGoDoc(t, "make_template.go", "MakeTemplate", advTemplateHomeDoc)
	env, cfg := newSpawnEnv(t), seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	adviceFollowTemplateEnvKey(t, env, cfg, "HOME", cfg.dir, `"HOME" sets HOME, which is reserved`, advSpawnHome)
}

// TestAdviceFollow_A15_MakeTemplateInvalidEnvKeyNameTheVariable: the same
// template with the key removed and CLAUDE_CONFIG_DIR as its own key saves and launches.
// A15 (b.66q): "remove \"CLAUDE_CONFIG_DIR=...\" from extra_env, and give each variable its own name as the key (...) and its value as the value"
func TestAdviceFollow_A15_MakeTemplateInvalidEnvKeyNameTheVariable(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env, cfg := newSpawnEnv(t), seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	bad := "CLAUDE_CONFIG_DIR=" + cfg.dir
	adviceFollowTemplateEnvKey(t, env, cfg, bad, "", strconv.Quote(bad)+" is not a valid env-var name: it contains '='",
		"remove "+strconv.Quote(bad)+" from extra_env, and "+advEnvKeyOwnName)
}

// adviceFollowTemplateEnvKey: make-template with key=value is ErrReservedEnvKey, its description carrying
// phrase (which refusal it is) and advice; without key and with CLAUDE_CONFIG_DIR=cfg.dir it saves, and a
// spawn with it launches with that variable, pre-trusting cfg.
func adviceFollowTemplateEnvKey(t *testing.T, env spawnEnv, cfg trustConfig, key, value, phrase, advice string) {
	t.Helper()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	p := api.MakeTemplateParams{Name: "adv-env", ExtraEnv: map[string]string{key: value}}

	_, err = env.c.MakeTemplate(p)

	adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, advice)
	adviceAssertPhrase(t, err, phrase)
	delete(p.ExtraEnv, key)
	p.ExtraEnv["CLAUDE_CONFIG_DIR"] = cfg.dir
	if _, err := env.c.MakeTemplate(p); err != nil {
		t.Fatalf("make-template with CLAUDE_CONFIG_DIR as its own key = %v; want it saved", err)
	}
	id := "adv-tplenv-" + uuid.NewString()[:8]
	res, err := env.c.Spawn(api.SpawnParams{CWD: cwd, ClaudeInstanceID: id, Template: p.Name})
	advSpawnLaunched(t, env.dbPath, id, nil, res, err)
	if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Envs["CLAUDE_CONFIG_DIR"] != cfg.dir {
		t.Errorf("creates = %+v; want one, its session env CLAUDE_CONFIG_DIR %q", creates, cfg.dir)
	}
	cfg.check(t, cwd, true, "after the followed make-template")
}

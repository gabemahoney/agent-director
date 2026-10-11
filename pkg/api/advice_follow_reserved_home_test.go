package api_test

// advice_follow_reserved_home_test.go follows, literally, the advice spawn's
// and resume's refusals of HOME in extra_env give (b.nas; A14, B11): trigger
// the refusal, pin the advice, do what it says, check the promised outcome.
// The refusals themselves are covered in reserved_home_test.go.

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The advice A14 and B11 pin, exactly as the description, manifest or Go doc words it.
const (
	advHomeConfigDir   = "to give the agent its own Claude Code config, set an absolute CLAUDE_CONFIG_DIR in extra_env instead"
	advSpawnHome       = `remove "HOME" from extra_env, and ` + advHomeConfigDir
	advSpawnHomeDoc    = "so set an absolute CLAUDE_CONFIG_DIR instead to give the agent its own Claude Code config"
	advSpawnHomeParam  = "set an absolute CLAUDE_CONFIG_DIR to give the agent its own Claude Code config"
	advResumeHome      = "nothing was written and nothing was launched; to run the agent again, spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and an extra_env without HOME (a reused id starts a new life with no memory of this conversation), and " + advHomeConfigDir
	advResumeHomeGoDoc = "Recourse: spawn again with the same id, opting in to reuse (SpawnParams.ReuseFinished), with an ExtraEnv without HOME (an absolute CLAUDE_CONFIG_DIR gives the agent its own Claude Code config)"
)

// TestAdviceFollow_A14_ReservedHomeSetConfigDir: the same spawn with HOME removed
// and an absolute CLAUDE_CONFIG_DIR set launches, pre-trusting that directory.
// A14 (b.nas): "remove \"HOME\" from extra_env, and to give the agent its own Claude Code config, set an absolute CLAUDE_CONFIG_DIR in extra_env instead"
func TestAdviceFollow_A14_ReservedHomeSetConfigDir(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertManifest(t, "spawn", "extra_env", advSpawnHomeParam)
	adviceAssertGoDoc(t, "spawn.go", "Spawn", advSpawnHomeDoc)
	env := newSpawnEnv(t)
	cfg := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	id := "adv-home-" + uuid.NewString()[:8]
	p := api.SpawnParams{CWD: cwd, ClaudeInstanceID: id, ExtraEnv: map[string]string{"HOME": cfg.dir, "TEAM": "core"}}

	_, err = env.c.Spawn(p)

	adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, advSpawnHome)
	delete(p.ExtraEnv, "HOME")
	p.ExtraEnv["CLAUDE_CONFIG_DIR"] = cfg.dir
	res, err := env.c.Spawn(p)
	advSpawnLaunched(t, env.dbPath, id, nil, res, err)
	if res.PreTrust != "ok" {
		t.Errorf("pre_trust = %q; want ok", res.PreTrust)
	}
	cfg.check(t, cwd, true, "after the followed spawn")
}

// TestAdviceFollow_B11_ReservedHomeSpawnWithReuse: the reuse that keeps HOME is
// refused alike and writes nothing; the one without it launches the next life.
// B11 (b.nas): "to run the agent again, spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and an extra_env without HOME (...)"
func TestAdviceFollow_B11_ReservedHomeSpawnWithReuse(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "resume.go", "Resume", advResumeHomeGoDoc)
	e := newKillEnv(t)
	home := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e),
		Opts: []apitest.SpawnOption{apitest.WithExtraEnv(map[string]string{"HOME": home.dir})}})
	before := e.snapshotResume(t, r.resumeRow)

	_, err := e.resume(r.ID)

	adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, advResumeHome)
	e.assertResumeWroteNothing(t, before)

	kept := e.snapshotReuse(t, r)
	_, _, err = e.reuse(t, reuseParams(t, r, reuseRequest{Env: map[string]string{"HOME": home.dir}}))
	assertOneSentinel(t, err, spawn.ErrReservedEnvKey)
	e.assertWroteNothing(t, kept)

	res, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{})) // extra env: r's absolute CLAUDE_CONFIG_DIR, no HOME
	if err != nil {
		t.Fatalf("spawn of %s with the reuse opt-in and no HOME = %v (log %q); want it launched", r.ID, err, logs)
	}
	advSpawnLaunched(t, e.dbPath, r.ID, advSpawnNextLife(before.cols), res, err)
	r.Trust.check(t, r.CWD, true, "after the followed reuse")
	home.check(t, "", false, "after the followed reuse")
}

package api_test

// advice_follow_invalid_env_key_test.go follows, literally, the advice spawn's
// and resume's refusals of a malformed extra_env key give (b.vpb; A15, B12):
// trigger the refusal, pin the advice, do what it says, check the promised
// outcome. The refusals themselves are covered in invalid_env_key_test.go.

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The advice A15 and B12 pin, exactly as the description or Go doc words it.
const (
	advEnvKeyOwnName       = "give each variable its own name as the key (not empty, with no '=' and no NUL byte) and its value as the value"
	advResumeEnvKeyReuse   = "nothing was written and nothing was launched; to run the agent again, spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and an extra_env without "
	advResumeEnvKeyGoDoc   = "Recourse: spawn again with the same id, opting in to reuse (SpawnParams.ReuseFinished), with an ExtraEnv without that key"
	advResumeEnvKeyNewLife = " (a reused id starts a new life with no memory of this conversation), and " + advEnvKeyOwnName
)

// TestAdviceFollow_A15_InvalidEnvKeyNameTheVariable: the same spawn with the key
// removed and CLAUDE_CONFIG_DIR as its own key launches with that variable set,
// pre-trusting that directory.
// A15 (b.vpb): "remove \"CLAUDE_CONFIG_DIR=...\" from extra_env, and give each variable its own name as the key (...) and its value as the value"
func TestAdviceFollow_A15_InvalidEnvKeyNameTheVariable(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env := newSpawnEnv(t)
	own := seedTrustConfig(t, env.home, trustLacksEntry)
	cfg := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	id := "adv-envkey-" + uuid.NewString()[:8]
	bad := "CLAUDE_CONFIG_DIR=" + cfg.dir
	p := api.SpawnParams{CWD: cwd, ClaudeInstanceID: id, ExtraEnv: map[string]string{bad: "", "TEAM": "core"}}

	_, err = env.c.Spawn(p)

	adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, "remove "+strconv.Quote(bad)+" from extra_env, and "+advEnvKeyOwnName)
	adviceAssertPhrase(t, err, strconv.Quote(bad)+" is not a valid env-var name: it contains '='")
	delete(p.ExtraEnv, bad)
	p.ExtraEnv["CLAUDE_CONFIG_DIR"] = cfg.dir
	res, err := env.c.Spawn(p)
	advSpawnLaunched(t, env.dbPath, id, nil, res, err)
	if res.PreTrust != "ok" {
		t.Errorf("pre_trust = %q; want ok", res.PreTrust)
	}
	creates := env.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) != 1 || creates[0].Envs["CLAUDE_CONFIG_DIR"] != cfg.dir {
		t.Fatalf("creates = %+v; want one, its session env CLAUDE_CONFIG_DIR %q", creates, cfg.dir)
	}
	cfg.check(t, cwd, true, "after the followed spawn")
	own.check(t, "", false, "after the followed spawn")
}

// TestAdviceFollow_B12_InvalidEnvKeySpawnWithReuse: the reuse that keeps the key
// is refused alike and writes nothing; the one naming CLAUDE_CONFIG_DIR as its
// own key launches the next life, pre-trusting that directory.
// B12 (b.vpb): "to run the agent again, spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and an extra_env without \"CLAUDE_CONFIG_DIR=...\" (...)"
func TestAdviceFollow_B12_InvalidEnvKeySpawnWithReuse(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "resume.go", "Resume", advResumeEnvKeyGoDoc)
	e := newKillEnv(t)
	cfg := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	bad := "CLAUDE_CONFIG_DIR=" + cfg.dir
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e),
		Opts: []apitest.SpawnOption{apitest.WithExtraEnv(map[string]string{bad: ""})}})
	before := e.snapshotResume(t, r.resumeRow)

	_, err := e.resume(r.ID)

	malformed := strconv.Quote(bad) + " is not a valid env-var name: it contains '='"
	adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, advResumeEnvKeyReuse+strconv.Quote(bad)+advResumeEnvKeyNewLife)
	adviceAssertPhrase(t, err, malformed)
	e.assertResumeWroteNothing(t, before)

	kept := e.snapshotReuse(t, r)
	_, _, err = e.reuse(t, reuseParams(t, r, reuseRequest{Env: map[string]string{bad: ""}}))
	assertOneSentinel(t, err, spawn.ErrReservedEnvKey)
	adviceAssertPhrase(t, err, malformed)
	e.assertWroteNothing(t, kept)

	res, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{Env: map[string]string{"CLAUDE_CONFIG_DIR": cfg.dir}}))
	if err != nil {
		t.Fatalf("spawn of %s with the reuse opt-in and CLAUDE_CONFIG_DIR as its own key = %v (log %q); want it launched", r.ID, err, logs)
	}
	advSpawnLaunched(t, e.dbPath, r.ID, advSpawnNextLife(before.cols), res, err)
	cfg.check(t, r.CWD, true, "after the followed reuse")
}

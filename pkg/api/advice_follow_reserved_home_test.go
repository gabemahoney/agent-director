package api_test

// advice_follow_reserved_home_test.go follows, literally, the advice spawn's
// and resume's refusals of HOME in extra_env give (b.nas; A14, B11): trigger
// the refusal, pin the advice, do what it says, check the promised outcome.
// The refusals themselves are covered in reserved_home_test.go.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// advHomeAgent is an agent A14 and B11 are followed for: the folder variable its advice names to set
// instead of HOME, and the alternative's text as the description words it.
type advHomeAgent struct {
	agent, variable, alternative string
}

// advHomeAgents are the agents the advice names a folder variable for, claude and codex.
var advHomeAgents = []advHomeAgent{
	{"claude", "CLAUDE_CONFIG_DIR", "to give the agent its own Claude Code config, set an absolute CLAUDE_CONFIG_DIR in extra_env instead"},
	{"codex", "CODEX_HOME", "to give the agent its own Codex config, set an absolute CODEX_HOME in extra_env instead"},
}

// The advice A14 and B11 pin, exactly as the manifest or Go doc words it (the description's depends on the agent).
const (
	advSpawnHomeDoc    = "so set an absolute CLAUDE_CONFIG_DIR (a claude agent) or CODEX_HOME (a codex agent) instead to give the agent its own config"
	advSpawnHomeParam  = "set an absolute CLAUDE_CONFIG_DIR (Claude Code) or CODEX_HOME (Codex) to give the agent its own config"
	advResumeHomeGoDoc = "Recourse: spawn again with the same id, opting in to reuse (SpawnParams.ReuseFinished) and passing the row's agent (SpawnParams.Agent), with an ExtraEnv without HOME (an absolute CLAUDE_CONFIG_DIR gives a Claude Code agent its own config, an absolute CODEX_HOME a Codex agent"
)

// advSpawnHome is A14's advice for a spawn of a: remove HOME and set a's folder variable.
func advSpawnHome(a advHomeAgent) string {
	return `remove "HOME" from extra_env, and ` + a.alternative
}

// advResumeHome is B11's advice for a row of a: the spawn-again step with the reuse opt-in and a's agent word,
// an extra_env without HOME, and a's folder variable.
func advResumeHome(a advHomeAgent) string {
	return "nothing was written and nothing was launched; to run the agent again, spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and agent " +
		a.agent + " (--agent " + a.agent + " on the CLI) and an extra_env without HOME (a reused id starts a new life with no memory of this conversation), and " + a.alternative
}

// advAssertCodexStop fails unless err is the Codex composition stop (a followed spawn of a codex agent that
// passed every check).
func advAssertCodexStop(t *testing.T, err error, what string) {
	t.Helper()
	assertOneName(t, err, "ErrInternal")
	if err == nil || !strings.Contains(err.Error(), codexStopText) {
		t.Errorf("%s = %v; want the Codex composition stop %q", what, err, codexStopText)
	}
}

// advAssertOnlyLookups fails unless the only tmux calls rec saw are lookups: a spawn that names its id looks
// for a session of that id before composing, and the Codex stop that follows creates nothing.
func advAssertOnlyLookups(t *testing.T, rec *tmuxfix.Recorder) {
	t.Helper()
	if n := len(rec.Calls()); n != 0 {
		t.Errorf("name-based tmux calls = %d; want none", n)
	}
	for _, k := range callKinds(rec) {
		if k != tmux.CallLookup {
			t.Errorf("tmux calls = %v; want lookups only (nothing created)", callKinds(rec))
			return
		}
	}
}

// TestAdviceFollow_A14_ReservedHomeSetConfigDir: the same spawn with HOME removed and the agent's absolute
// folder variable set launches a claude agent, pre-trusting that directory; a codex agent reaches the
// Codex composition stop, writing nothing.
// A14 (b.nas): "remove \"HOME\" from extra_env, and to give the agent its own Claude Code config, set an absolute CLAUDE_CONFIG_DIR in extra_env instead" (Codex: "its own Codex config, set an absolute CODEX_HOME")
func TestAdviceFollow_A14_ReservedHomeSetConfigDir(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertManifest(t, "spawn", "extra_env", advSpawnHomeParam)
	adviceAssertGoDoc(t, "spawn.go", "Spawn", advSpawnHomeDoc)
	for _, a := range advHomeAgents {
		t.Run(a.agent, func(t *testing.T) {
			env := newSpawnEnv(t)
			cfg := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks: %v", err)
			}
			id := "adv-home-" + uuid.NewString()[:8]
			p := api.SpawnParams{Agent: a.agent, CWD: cwd, InstanceID: id, ExtraEnv: map[string]string{"HOME": cfg.dir, "TEAM": "core"}}

			_, err = env.c.Spawn(p)

			adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, advSpawnHome(a))
			delete(p.ExtraEnv, "HOME")
			p.ExtraEnv[a.variable] = cfg.dir
			res, err := env.c.Spawn(p)
			if a.agent == "codex" {
				advAssertCodexStop(t, err, "the followed spawn")
				advAssertOnlyLookups(t, env.rec)
				cfg.check(t, "", false, "after the followed spawn")
				return
			}
			advSpawnLaunched(t, env.dbPath, id, nil, res, err)
			if res.PreTrust != "ok" {
				t.Errorf("pre_trust = %q; want ok", res.PreTrust)
			}
			cfg.check(t, cwd, true, "after the followed spawn")
		})
	}
}

// TestAdviceFollow_B11_ReservedHomeSpawnWithReuse: the reuse that keeps HOME is refused alike and writes
// nothing; the one without it launches a claude row's next life, and a codex row's reaches the Codex
// composition stop, writing nothing.
// B11 (b.nas): "to run the agent again, spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and agent claude (--agent claude on the CLI) and an extra_env without HOME (...)"
func TestAdviceFollow_B11_ReservedHomeSpawnWithReuse(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "resume.go", "Resume", advResumeHomeGoDoc)
	for _, a := range advHomeAgents {
		t.Run(a.agent+" row", func(t *testing.T) {
			e := newKillEnv(t)
			home := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e),
				Opts: []apitest.SpawnOption{apitest.WithAgent(a.agent), apitest.WithExtraEnv(map[string]string{"HOME": home.dir})}})
			before := e.snapshotResume(t, r.resumeRow)

			_, err := e.resume(r.ID)

			adviceAssertAdvice(t, err, spawn.ErrReservedEnvKey, advResumeHome(a))
			e.assertResumeWroteNothing(t, before)

			kept := e.snapshotReuse(t, r)
			p := reuseParams(t, r, reuseRequest{Env: map[string]string{"HOME": home.dir}})
			p.Agent = a.agent
			_, _, err = e.reuse(t, p)
			assertOneSentinel(t, err, spawn.ErrReservedEnvKey)
			e.assertWroteNothing(t, kept)

			p = reuseParams(t, r, reuseRequest{}) // extra env: r's absolute CLAUDE_CONFIG_DIR, no HOME
			p.Agent = a.agent
			res, logs, err := e.reuse(t, p)
			if a.agent == "codex" {
				advAssertCodexStop(t, err, "the followed spawn")
				e.assertWroteNothing(t, kept)
				return
			}
			if err != nil {
				t.Fatalf("spawn of %s with the reuse opt-in and no HOME = %v (log %q); want it launched", r.ID, err, logs)
			}
			advSpawnLaunched(t, e.dbPath, r.ID, advSpawnNextLife(before.cols), res, err)
			r.Trust.check(t, r.CWD, true, "after the followed reuse")
			home.check(t, "", false, "after the followed reuse")
		})
	}
}

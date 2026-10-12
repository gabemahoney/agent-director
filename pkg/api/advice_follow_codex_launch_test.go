package api_test

// advice_follow_codex_launch_test.go follows, literally, the two texts a Codex
// launch gives a caller: the description of a refused Codex argument (SR-5.2;
// A23) and the one warning a Codex launch prints when its pre-trust failed
// (SR-5.6; A24): trigger it, pin the text, do what it says as an automated
// caller would, and check the promised outcome. The refusals themselves are
// covered in spawn_agent_test.go and internal/spawn's Codex arguments tests, the
// pre-trust outcomes in spawn_pretrust_test.go.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestAdviceFollow_A23_CodexDeniedFlagDropTheArgument: a codex spawn whose agent_args hold an argument agent-director
// must own is refused with ErrSpawnDeniedFlag, the description naming the argument as written (bare, without its
// value) and why; the agent_args text lists every kind. The call again without that argument, the rest kept, as
// the description says, launches codex with the others replayed.
// A23 (SR-5.2): "ErrSpawnDeniedFlag: --remote: it attaches the agent to a remote or shared service"
func TestAdviceFollow_A23_CodexDeniedFlagDropTheArgument(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	adviceAssertManifest(t, "spawn", "agent_args", "For a codex agent, refused (ErrSpawnDeniedFlag): a Codex subcommand in command position",
		"--remote, --remote-auth-token-env", "-c/--config keys at, above or under hooks, tui.disable_paste_burst or features.hooks", "--disable hooks")
	cases := []struct {
		name    string
		refused []string // the arguments to drop, placed after the kept ones
		text    string   // the description's text after the sentinel
	}{
		{"--remote", []string{"--remote", "ws://x"}, "--remote: it attaches the agent to a remote or shared service"},
		{"--remote-auth-token-env", []string{"--remote-auth-token-env=TOK"}, "--remote-auth-token-env: it attaches the agent to a remote or shared service"},
		{"a hooks key", []string{"-c", "hooks.state={}"}, "-c hooks.state: "},
		{"a key under hooks", []string{"--config=hooks.Stop=[]"}, "--config hooks.Stop: "},
		{"disable_paste_burst", []string{"-c", "tui.disable_paste_burst=true"}, "-c tui.disable_paste_burst: "},
		{"--disable hooks", []string{"--disable", "hooks"}, "--disable hooks: agent-director needs Codex's hooks on for every launch"},
		{"a subcommand", []string{"resume"}, "codex subcommand resume: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			kept := []string{"--model", "gpt"}
			p := api.SpawnParams{Agent: "codex", CWD: t.TempDir(), NoPreTrust: true}
			if tc.name == "a subcommand" {
				p.AgentArgs = append(slices.Clone(tc.refused), kept...) // a subcommand is in command position only first
			} else {
				p.AgentArgs = append(slices.Clone(kept), tc.refused...)
			}

			_, refused := env.c.Spawn(p)

			adviceAssertAdvice(t, refused, spawn.ErrSpawnDeniedFlag, "ErrSpawnDeniedFlag: "+tc.text)
			assertNoTmuxCalls(t, env.rec)
			p.AgentArgs = kept
			res, err := env.c.Spawn(p)
			if err != nil {
				t.Fatalf("followed spawn: %v; want it launched", err)
			}
			l := assertCodexLaunch(t, env.rec.SocketCallsOf(tmux.CallCreate), env.dbPath, res.InstanceID, codexProgram)
			if !slices.Equal(l.PassThrough(), kept) {
				t.Errorf("pass-through arguments = %q; want the kept %q", l.PassThrough(), kept)
			}
		})
	}
}

// TestAdviceFollow_A24_CodexPreTrustWarningAbsoluteCodexHome: a codex spawn whose extra_env CODEX_HOME is not an
// absolute path launches with pre_trust failed and prints the one warning naming the value, which says the agent
// may stop at Codex's folder-trust prompt; the same spawn with an absolute CODEX_HOME, which the reported failure
// points to, reports ok and prints nothing, and the launch sets that folder.
// A24 (SR-5.6): "pre-trust failed (CODEX_HOME \"rel\" is not an absolute path); the agent may stop at Codex's folder-trust prompt"
func TestAdviceFollow_A24_CodexPreTrustWarningAbsoluteCodexHome(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR and the working directory with t.Setenv, and redirects stderr.
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("no spawn verb in the manifest")
	}
	for _, f := range v.ResultFields {
		if f.Name != "pre_trust" {
			continue
		}
		for _, phrase := range []string{"for a codex agent, extra_env sets CODEX_HOME to a path that is not absolute",
			"the agent may stop at its folder-trust prompt (Claude Code's or Codex's)"} {
			if !strings.Contains(f.Description, phrase) {
				t.Errorf("spawn result pre_trust text %q\nwant it to carry %q", f.Description, phrase)
			}
		}
	}
	env := newSpawnEnv(t)
	folder := seedRelativeCodexFolder(t)
	cwd := t.TempDir()

	var res api.SpawnResult
	var err error
	warn := captureStderr(t, func() {
		res, err = env.c.Spawn(api.SpawnParams{Agent: "codex", CWD: cwd, ExtraEnv: map[string]string{"CODEX_HOME": "rel"}})
	})

	if err != nil {
		t.Fatalf("spawn with a relative CODEX_HOME: %v; want it launched", err)
	}
	if res.PreTrust != "failed" {
		t.Errorf("pre_trust = %q; want failed", res.PreTrust)
	}
	if want := fmt.Sprintf(codexFailedWarning, "rel"); warn != want {
		t.Errorf("warning = %q; want %q", warn, want)
	}
	assertCodexLaunch(t, env.rec.SocketCallsOf(tmux.CallCreate), env.dbPath, res.InstanceID, codexProgram)

	// Follow it: the absolute path of the folder.
	before := len(env.rec.SocketCallsOf(tmux.CallCreate))
	warn = captureStderr(t, func() {
		res, err = env.c.Spawn(api.SpawnParams{Agent: "codex", CWD: cwd, ExtraEnv: map[string]string{"CODEX_HOME": folder.dir}})
	})

	if err != nil {
		t.Fatalf("followed spawn: %v; want it launched", err)
	}
	if res.PreTrust != "ok" {
		t.Errorf("followed pre_trust = %q; want ok", res.PreTrust)
	}
	if warn != "" {
		t.Errorf("followed warning = %q; want none", warn)
	}
	creates := env.rec.SocketCallsOf(tmux.CallCreate)[before:]
	assertCodexLaunch(t, creates, env.dbPath, res.InstanceID, codexProgram)
	if got := creates[0].Envs["CODEX_HOME"]; got != folder.dir {
		t.Errorf("launch CODEX_HOME = %q; want %q", got, folder.dir)
	}
}

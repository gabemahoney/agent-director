package spawn

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// launchProgramPaths are the [launch] claude_path / codex_path values the
// program tests set, by agent name; "unset" is the empty path.
var launchProgramPaths = []struct {
	name string
	path func(agent string) string
}{
	{"unset", func(string) string { return "" }},
	{"absolute path", func(a string) string { return "/opt/" + a + "/bin/" + a }},
	{"spaces", func(a string) string { return "/opt/with space/" + a }},
	{"quotes and a flag-like tail", func(a string) string { return `/opt/q"uote's/` + a + " --verbose" }},
}

// setLaunchPath sets the config's launch path for agent.
func setLaunchPath(cfg *config.Config, agent, path string) {
	if agent == store.AgentCodex {
		cfg.Launch.CodexPath = path
	} else {
		cfg.Launch.ClaudePath = path
	}
}

// TestLaunchProgram (SR-5.0): a launch's argv starts with the agent's
// [launch] path when set, as one unchanged argument, else with the program
// named claude or codex; the path is read from the config at each launch and
// never stored on the row. Reuse composes through ComposeLaunch, as spawn
// does (pkg/api/spawn_reuse.go).
func TestLaunchProgram(t *testing.T) {
	launches := []struct {
		name string
		// next is the argument after the program, by agent.
		next   map[string]string
		create func(t *testing.T, agent, path string) (argv []string, rowText string)
	}{
		{"spawn", map[string]string{"claude": "--settings", "codex": "--no-daemon"}, func(t *testing.T, agent, path string) ([]string, string) {
			e := newLaunchEnv(t)
			e.r.Agent, e.r.AgentArgs = agent, nil
			setLaunchPath(&e.cfg, agent, path)
			id := e.mustLaunch()
			return e.onlyCreate().Command, fmt.Sprintf("%+v", e.row(id))
		}},
		{"reuse", map[string]string{"claude": "--settings", "codex": "--no-daemon"}, func(t *testing.T, agent, path string) ([]string, string) {
			e := newLaunchEnv(t)
			e.r.Agent, e.r.AgentArgs = agent, nil
			setLaunchPath(&e.cfg, agent, path)
			c, err := ComposeLaunch(e.r, e.cfg)
			if err != nil {
				t.Fatalf("ComposeLaunch: %v", err)
			}
			return c.Create.Command, fmt.Sprintf("%+v", c.Row)
		}},
		{"resume", map[string]string{"claude": "--resume", "codex": "resume"}, func(t *testing.T, agent, path string) ([]string, string) {
			e := newRelaunchEnv(t)
			e.row.Agent, e.row.AgentArgs = agent, nil
			setLaunchPath(&e.cfg, agent, path)
			if out := e.relaunch("session-program-1"); out.Kind != CreateLabelled {
				t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
			}
			return e.onlyCreate().Command, fmt.Sprintf("%+v", e.row)
		}},
	}
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		for _, l := range launches {
			for _, p := range launchProgramPaths {
				path := p.path(agent)
				want := path
				if path == "" {
					want = agent
				}
				t.Run(agent+"/"+l.name+"/"+p.name, func(t *testing.T) {
					argv, rowText := l.create(t, agent, path)
					if len(argv) < 2 || argv[0] != want || argv[1] != l.next[agent] {
						t.Errorf("create argv starts %q; want program %q (one argument) then %q", argv[:min(2, len(argv))], want, l.next[agent])
					}
					if path != "" && strings.Contains(rowText, path) {
						t.Errorf("row = %s; want nothing about the launch path on it", rowText)
					}
				})
			}
		}
	}
}

// TestResumeProgramFollowsTheConfigAtResume (SR-5.0): a row resumed after
// [launch] claude_path or codex_path changed is launched with the new path,
// and one resumed after the key is removed with the program on PATH again.
func TestResumeProgramFollowsTheConfigAtResume(t *testing.T) {
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		t.Run(agent, func(t *testing.T) {
			e := newRelaunchEnv(t)
			e.row.Agent = agent
			program := func() string {
				c, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: "s1", Socket: e.socket, Token: relaunchToken, StoreID: relaunchStoreID}, e.cfg)
				if err != nil {
					t.Fatalf("ComposeRelaunch: %v", err)
				}
				return c.Create.Command[0]
			}
			setLaunchPath(&e.cfg, agent, "/opt/one/"+agent)
			if got := program(); got != "/opt/one/"+agent {
				t.Errorf("program with the first path = %q", got)
			}
			setLaunchPath(&e.cfg, agent, "/opt/two dir/"+agent)
			if got := program(); got != "/opt/two dir/"+agent {
				t.Errorf("program after the path changed = %q; want the new one", got)
			}
			setLaunchPath(&e.cfg, agent, "")
			if got := program(); got != agent {
				t.Errorf("program after the key was removed = %q; want %q", got, agent)
			}
		})
	}
}

// TestComposeRefusesUnknownAgents: ComposeLaunch and ComposeRelaunch stop
// before composing for an empty or unknown row agent (store.ErrRowAgentInvalid,
// quoting the value) and return the zero value.
func TestComposeRefusesUnknownAgents(t *testing.T) {
	composers := []struct {
		name    string
		compose func(t *testing.T, agent string) error
	}{
		{"ComposeLaunch", func(t *testing.T, agent string) error {
			e := newLaunchEnv(t)
			e.r.Agent = agent
			c, err := ComposeLaunch(e.r, e.cfg)
			if err != nil && (len(c.Create.Command) != 0 || c.Row.InstanceID != "") {
				t.Errorf("ComposeLaunch returned %+v with its error; want the zero value", c)
			}
			return err
		}},
		{"ComposeRelaunch", func(t *testing.T, agent string) error {
			e := newRelaunchEnv(t)
			e.row.Agent = agent
			c, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: "s1", Socket: e.socket, Token: relaunchToken, StoreID: relaunchStoreID}, e.cfg)
			if err != nil && (len(c.Create.Command) != 0 || c.PreTrust != "") {
				t.Errorf("ComposeRelaunch returned %+v with its error; want the zero value", c)
			}
			return err
		}},
	}
	agents := []struct{ name, agent, want string }{
		{"empty", "", `: ""`},
		{"unknown", "gemini", `: "gemini"`},
		{"wrong case", "Codex", `: "Codex"`},
	}
	for _, c := range composers {
		for _, a := range agents {
			t.Run(c.name+"/"+a.name, func(t *testing.T) {
				err := c.compose(t, a.agent)
				if !errors.Is(err, store.ErrRowAgentInvalid) || !strings.Contains(err.Error(), a.want) {
					t.Fatalf("%s(agent %q) err = %v; want ErrRowAgentInvalid carrying %q", c.name, a.agent, err, a.want)
				}
			})
		}
	}
}

// TestCodexLaunchWritesNoClaudeJSON: a codex launch inserts one pending row,
// makes one create and leaves ~/.claude.json untouched (no Claude pre-trust).
func TestCodexLaunchWritesNoClaudeJSON(t *testing.T) {
	const seed = `{"projects":{}}`
	e := newLaunchEnv(t)
	stub := withStubClaudeJSON(t)
	seedFile(t, stub, seed)
	e.r.Agent = store.AgentCodex

	id, outcome := e.mustLaunchOutcome()

	if row := e.row(id); row.State != store.StatePending || row.Agent != store.AgentCodex {
		t.Errorf("row = {state %q, agent %q}; want pending codex", row.State, row.Agent)
	}
	if outcome != PreTrustOK {
		t.Errorf("pre-trust outcome = %q; want ok", outcome)
	}
	e.onlyCreate()
	if got := mustReadFile(t, stub); string(got) != seed {
		t.Errorf("claude.json = %q; want untouched (no pre-trust of a Claude file)", got)
	}
}

// TestReuseRetryNamesTheAgent (SR-4.4): the held-name and launch-timeout retry
// advice names the row's agent, or points to get when none is known.
func TestReuseRetryNamesTheAgent(t *testing.T) {
	const optIn = "a retry with this id uses the reuse opt-in reuse_finished (--reuse-finished on the CLI) and "
	for agent, tail := range map[string]string{
		"claude": "agent claude (--agent claude on the CLI)",
		"codex":  "agent codex (--agent codex on the CLI)",
		"":       "the row's agent, which get shows",
	} {
		if got := ReuseRetry(agent); got != optIn+tail {
			t.Errorf("ReuseRetry(%q) = %q; want %q", agent, got, optIn+tail)
		}
	}
}

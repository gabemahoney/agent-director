package spawn

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestClaudeLaunchProgram (SR-5.0): a Claude launch's argv starts with
// [launch] claude_path when set, as one unchanged argument, else with claude;
// the path is read from the config at each launch and never stored on the row.
func TestClaudeLaunchProgram(t *testing.T) {
	paths := []struct{ name, path, want string }{
		{"unset", "", "claude"},
		{"absolute path", "/opt/claude/bin/claude", "/opt/claude/bin/claude"},
		{"spaces", "/opt/with space/claude", "/opt/with space/claude"},
		{"quotes and a flag-like tail", `/opt/q"uote's/claude --verbose`, `/opt/q"uote's/claude --verbose`},
	}
	launches := []struct {
		name   string
		next   string // the argument after the program
		create func(t *testing.T, claudePath string) (argv []string, rowText string)
	}{
		{"spawn", "--settings", func(t *testing.T, claudePath string) ([]string, string) {
			e := newLaunchEnv(t)
			e.cfg.Launch.ClaudePath = claudePath
			id := e.mustLaunch()
			return e.onlyCreate().Command, fmt.Sprintf("%+v", e.row(id))
		}},
		{"resume", "--resume", func(t *testing.T, claudePath string) ([]string, string) {
			e := newRelaunchEnv(t)
			e.cfg.Launch.ClaudePath = claudePath
			if out := e.relaunch("session-program-1"); out.Kind != CreateLabelled {
				t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
			}
			return e.onlyCreate().Command, fmt.Sprintf("%+v", e.row)
		}},
	}
	for _, l := range launches {
		for _, p := range paths {
			t.Run(l.name+"/"+p.name, func(t *testing.T) {
				argv, rowText := l.create(t, p.path)
				if len(argv) < 2 || argv[0] != p.want || argv[1] != l.next {
					t.Errorf("create argv starts %q; want program %q (one argument) then %q", argv[:min(2, len(argv))], p.want, l.next)
				}
				if p.path != "" && strings.Contains(rowText, p.path) {
					t.Errorf("row = %s; want nothing about the launch path on it", rowText)
				}
			})
		}
	}
}

// TestComposeStopsForUnlaunchableAgents: ComposeLaunch and ComposeRelaunch
// stop before composing for a codex agent (a Codex launch is not available in
// this build, an uncatalogued error) and for an empty or unknown row agent
// (store.ErrRowAgentInvalid, quoting the value).
func TestComposeStopsForUnlaunchableAgents(t *testing.T) {
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
			req, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: "s1", Socket: e.socket, Token: relaunchToken, StoreID: relaunchStoreID}, e.cfg)
			if err != nil && len(req.Command) != 0 {
				t.Errorf("ComposeRelaunch returned %+v with its error; want the zero value", req)
			}
			return err
		}},
	}
	agents := []struct {
		name, agent, want string
		rowInvalid        bool
	}{
		{"codex", "codex", "a Codex launch is not available in this build", false},
		{"empty", "", `: ""`, true},
		{"unknown", "gemini", `: "gemini"`, true},
		{"wrong case", "Claude", `: "Claude"`, true},
	}
	for _, c := range composers {
		for _, a := range agents {
			t.Run(c.name+"/"+a.name, func(t *testing.T) {
				err := c.compose(t, a.agent)
				if err == nil || errors.Is(err, store.ErrRowAgentInvalid) != a.rowInvalid || !strings.Contains(err.Error(), a.want) {
					t.Fatalf("%s(agent %q) err = %v; want one carrying %q (ErrRowAgentInvalid: %v)", c.name, a.agent, err, a.want, a.rowInvalid)
				}
				if !a.rowInvalid && err.Error() != a.want {
					t.Errorf("err = %q; want exactly %q", err.Error(), a.want)
				}
			})
		}
	}
}

// TestLaunchStopsForCodexBeforeAnyWrite: a codex launch stops at composition,
// so no row is inserted, no cwd is pre-trusted and no tmux session is created.
func TestLaunchStopsForCodexBeforeAnyWrite(t *testing.T) {
	const seed = `{"projects":{}}`
	e := newLaunchEnv(t)
	stub := withStubClaudeJSON(t)
	seedFile(t, stub, seed)
	e.r.Agent = "codex"

	_, _, err := e.launch()

	if err == nil || err.Error() != "a Codex launch is not available in this build" {
		t.Fatalf("Launch err = %v; want the Codex-unavailable error", err)
	}
	if _, err := e.s.GetSpawn(e.r.InstanceID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("GetSpawn after the stop = %v; want ErrSpawnNotFound (no row written)", err)
	}
	if calls := e.rec.SocketCalls(); len(calls) != 0 {
		t.Errorf("tmux calls = %+v; want none", calls)
	}
	if got := mustReadFile(t, stub); string(got) != seed {
		t.Errorf("claude.json = %q; want untouched (no pre-trust)", got)
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

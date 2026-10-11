package api_test

// launch_path_test.go covers the launch program of a Claude launch (SR-7.6):
// the [launch] claude_path the Client was configured with is the first element
// of the create argv for spawn, reuse and resume alike, and `claude` when it is
// unset; the rest of the argv is as it was. The path forms and their refusals
// are internal/config's.

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// launchPathArgs are the agent_args a launch-path test passes (spawn, reuse) or finds on its row (resume).
var launchPathArgs = []string{"--model", "x"}

// launchPathVerbs are the Claude launches, each running one on c over e and returning its create's argv
// and the argv's elements from the second on that it must keep.
var launchPathVerbs = []struct {
	name string
	run  func(t *testing.T, e *killEnv, c *api.Client) (argv, rest []string)
}{
	{"spawn", func(t *testing.T, e *killEnv, c *api.Client) ([]string, []string) {
		if _, err := c.Spawn(api.SpawnParams{Agent: "claude", CWD: t.TempDir(), AgentArgs: launchPathArgs, NoPreTrust: true}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		return onlyCreateArgv(t, e), launchPathArgs
	}},
	{"reuse", func(t *testing.T, e *killEnv, c *api.Client) ([]string, []string) {
		r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
		if _, err := c.Spawn(reuseParams(t, r, reuseRequest{Args: launchPathArgs})); err != nil {
			t.Fatalf("Spawn (reuse of %s): %v", r.ID, err)
		}
		return onlyCreateArgv(t, e), launchPathArgs
	}},
	{"resume", func(t *testing.T, e *killEnv, c *api.Client) ([]string, []string) {
		r := seedLaunchPathResumable(t, e)
		if _, err := c.Resume(api.ResumeParams{InstanceID: r.ID}); err != nil {
			t.Fatalf("Resume(%s): %v", r.ID, err)
		}
		return onlyCreateArgv(t, e), []string{"--resume", launchPathSession}
	}},
}

// launchPathSession is the session id the resumable rows of these tests record.
const launchPathSession = "sess-launchpath"

// seedLaunchPathResumable seeds an ended Claude row, its agent gone, recording launchPathSession.
func seedLaunchPathResumable(t *testing.T, e *killEnv) resumeRow {
	t.Helper()
	spec := e.resumableSpec(rlkSettled(e), agentGone)
	spec.SessionID = launchPathSession
	return e.seedResumableRow(t, spec)
}

// onlyCreateArgv returns the argv of the one create e's Recorder saw.
func onlyCreateArgv(t *testing.T, e *killEnv) []string {
	t.Helper()
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) != 1 {
		t.Fatalf("creates = %d; want 1", len(creates))
	}
	return creates[0].Command
}

// launchClient is a Client on e whose config file holds claude_path path (the
// key is left out when path is "").
func launchClient(t *testing.T, e *killEnv, path string) *api.Client {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteKeysConfig(t, cfgPath, apitest.ConfigKeys{ClaudePath: path})
	c, _, err := e.clientFor(t, cfgPath)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	return c
}

// assertLaunchArgv fails unless argv starts with program, then the flags rest
// names in order (the session or settings flags the verb puts there), and the
// program appears nowhere else.
func assertLaunchArgv(t *testing.T, argv []string, program string, rest []string) {
	t.Helper()
	if len(argv) < 1+len(rest) || argv[0] != program || !slices.Contains(argv[1:], "--settings") {
		t.Fatalf("create argv = %q; want %q first and --settings after it", argv, program)
	}
	for _, want := range rest {
		if !slices.Contains(argv[1:], want) {
			t.Errorf("create argv = %q; want %q kept", argv, want)
		}
	}
	if program != "claude" && slices.Contains(argv[1:], program) {
		t.Errorf("create argv = %q; want the program only first", argv)
	}
}

// TestLaunchClaudePathIsTheFirstElement (SR-7.6): spawn, reuse and resume
// launch `claude` with no [launch] claude_path and the configured path in its
// place with it, the other elements as they were.
func TestLaunchClaudePathIsTheFirstElement(t *testing.T) {
	t.Parallel()
	for _, verb := range launchPathVerbs {
		for _, tc := range []struct{ name, path, want string }{
			{"unset", "", "claude"},
			{"an absolute path", "/opt/agents/claude-wrapper", "/opt/agents/claude-wrapper"},
		} {
			t.Run(verb.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newReuseEnv(t)
				argv, rest := verb.run(t, e, launchClient(t, e, tc.path))
				assertLaunchArgv(t, argv, tc.want, rest)
			})
		}
	}
}

// TestLaunchClaudePathReadAtEachLaunch (SR-7.6): the path is read when a launch
// is made, not stored on a row: a resume from a Client configured with another
// path (or none) launches that one, whatever launched the Client's earlier rows.
func TestLaunchClaudePathReadAtEachLaunch(t *testing.T) {
	t.Parallel()
	e := newReuseEnv(t)
	for _, path := range []string{"/opt/agents/first", "/opt/agents/second", ""} {
		r := seedLaunchPathResumable(t, e)
		before := len(e.rec.SocketCallsOf(tmux.CallCreate))

		if _, err := launchClient(t, e, path).Resume(api.ResumeParams{InstanceID: r.ID}); err != nil {
			t.Fatalf("Resume(%s) with claude_path %q: %v", r.ID, path, err)
		}

		creates := e.rec.SocketCallsOf(tmux.CallCreate)
		if len(creates) != before+1 {
			t.Fatalf("creates = %d; want %d", len(creates), before+1)
		}
		want := path
		if want == "" {
			want = "claude"
		}
		assertLaunchArgv(t, creates[before].Command, want, []string{"--resume", launchPathSession})
	}
}

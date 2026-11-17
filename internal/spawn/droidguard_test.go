package spawn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/probe"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/droidfix"
)

func guardError(line int, kind string) string {
	where := ""
	if line > 0 {
		where = fmt.Sprintf("line %d: ", line)
	}
	return "ErrLaunchGuardRefused: Droid launch Guard refused: " + where + kind + "; an operator must fix the Path List File named by [launch] droid_secret_paths_file or the Guard installation and environment (see docs/experimental.md#droid-secret-paths); no row, session, trust entry or launch file was written and nothing was launched"
}

func assertGuardError(t *testing.T, err error, line int, kind string) {
	t.Helper()
	if !errors.Is(err, ErrLaunchGuardRefused) || err.Error() != guardError(line, kind) {
		t.Fatalf("error=%v; want %s", err, guardError(line, kind))
	}
}

// SR-1.3: physical lines, lexical normalization and exact sanitized refusals.
func TestDroidGuardPathList(t *testing.T) {
	for _, tc := range []struct {
		name, input, home, kind string
		line                    int
		want                    []string
	}{
		{name: "normalized", input: "# comment\n \t\n/invented/a/../vault\n~/private/./box\n/invented/λ\n", home: "/test-home", want: []string{"/invented/vault", "/test-home/private/box", "/invented/λ"}},
		{name: "empty", input: "# comment\n\t\n", home: "/test-home", kind: "the Path List has no entries"},
		{name: "encoding", input: "/private-\xff", home: "/test-home", kind: "the Path List File is not valid UTF-8"},
		{name: "relative HOME", input: "~/private", home: "relative", kind: "HOME is not a usable absolute path"},
		{name: "empty HOME", input: "/private", kind: "HOME is not a usable absolute path"},
		{name: "control HOME", input: "~/private", home: "/private\n", kind: "HOME is not a usable absolute path"},
		{name: "invalid HOME encoding", input: "~/private", home: "/private-\xff", kind: "HOME is not a usable absolute path"},
		{name: "leading whitespace", input: "# comment\n\n /private", home: "/test-home", line: 3, kind: "the entry has leading or trailing whitespace"},
		{name: "trailing whitespace", input: "/private\u00a0", home: "/test-home", line: 1, kind: "the entry has leading or trailing whitespace"},
		{name: "CRLF", input: "/private\r\n", home: "/test-home", line: 1, kind: "the entry has leading or trailing whitespace"},
		{name: "embedded control", input: "/private\x00value", home: "/test-home", line: 1, kind: "the entry has a control character"},
		{name: "relative", input: "# comment\n\nPRIVATE_ENTRY", home: "/test-home", line: 3, kind: "the entry is not an absolute or ~/ path"},
		{name: "named tilde", input: "~someone/private", home: "/test-home", line: 1, kind: "the entry is not an absolute or ~/ path"},
		{name: "bare tilde", input: "~", home: "/test-home", line: 1, kind: "the entry is not an absolute or ~/ path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDroidSecretPaths([]byte(tc.input), tc.home)
			if tc.kind != "" {
				assertGuardError(t, err, tc.line, tc.kind)
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%q err=%v; want %q", got, err, tc.want)
			}
		})
	}
}

// SR-3.3: real sh receives the exact command. Expected argv is literal data,
// never built by the production quoting helper. Listed paths need not exist.
func TestDroidGuardCommand(t *testing.T) {
	for _, tc := range []struct {
		mode droidfix.Mode
		exit int
	}{
		{droidfix.Pass, 0}, {droidfix.Fail, 2}, {droidfix.Signal, 2}, {droidfix.Text, 2}, {droidfix.Newlines, 2}, {droidfix.NUL, 2}, {droidfix.Flood, 2}, {"missing", 2}, {"not-executable", 2},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			g := droidfix.NewGuard(t)
			g.Path = filepath.Join(filepath.Dir(g.Path), "guard ' ; $(touch INJECTED) & program")
			if tc.mode == "missing" || tc.mode == "not-executable" {
				if tc.mode == "not-executable" {
					mustDo(t, os.WriteFile(g.Path, []byte("#!/bin/sh\nexit 0\n"), 0600))
				}
			} else {
				mustDo(t, g.Set(tc.mode))
			}
			entries := []string{"/invented/quote's", "/invented/space here", "/invented/$(touch INJECTED);`touch INJECTED`&*"}
			command, err := composeDroidGuardCommand(g.Path, entries)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, "sh", "-c", command)
			c.Dir = t.TempDir()
			c.Stdin = strings.NewReader("payload")
			var out, diag bytes.Buffer
			c.Stdout, c.Stderr = &out, &diag
			err = c.Run()
			exit := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != tc.exit || out.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), diag.String())
			}
			if tc.mode != "missing" && tc.mode != "not-executable" {
				calls := g.Calls(t)
				want := append([]string{"tool-guard", "--"}, entries...)
				if len(calls) != 1 || !reflect.DeepEqual(calls[0].Args, want) || string(calls[0].Input) != "payload" {
					t.Fatalf("calls=%+v; want argv=%q", calls, want)
				}
				if !strings.HasPrefix(diag.String(), droidfix.FailOutput+"\n") {
					t.Fatalf("stderr lost program bytes: %q", diag.String())
				}
				if tc.mode != droidfix.Signal && diag.String() != droidfix.FailOutput+"\n" {
					t.Fatalf("stderr changed: %q", diag.String())
				}
			}
			files, err := os.ReadDir(c.Dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("shell metacharacters executed: %v %v", files, err)
			}
		})
	}
}

func TestDroidGuardCommandLimit(t *testing.T) {
	base, err := composeDroidGuardCommand("/guard", []string{"/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{131071, 131072, 131073} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			command, err := composeDroidGuardCommand("/guard", []string{"/" + strings.Repeat("x", n-len(base))})
			if n < 131072 {
				if err != nil || len(command) != n {
					t.Fatalf("len=%d err=%v", len(command), err)
				}
			} else {
				assertGuardError(t, err, 0, "the Guard command exceeds the single-argument length limit")
			}
		})
	}
}

// SR-2.2: actual probe/wrapper interaction; no duplicate tool decision matrix.
func TestDroidGuardSelfCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode droidfix.Mode
		kind string
	}{
		{"pair", droidfix.GuardPass, ""},
		{"AskUser allowed", droidfix.Pass, "the Guard self-check failed"},
		{"TodoWrite blocked", droidfix.Fail, "the Guard self-check failed"},
		{"stdout", droidfix.Newlines, "the Guard self-check failed"},
		{"bounded stderr", droidfix.StderrFlood, "the Guard self-check output failed its bounds"},
		{"missing program", "missing", "the Guard self-check failed"},
		{"missing sh", droidfix.GuardPass, "sh is unavailable on agent-director's PATH"},
		{"expired deadline", droidfix.GuardPass, "the Guard self-check exceeded its deadline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for _, key := range []string{"AGENT_DIRECTOR_INSTANCE_ID", "TMUX", "TMUX_PANE", "FACTORY_API_KEY", "OPENROUTER_API_KEY"} {
				t.Setenv(key, "synthetic-probe-private")
			}
			g := droidfix.NewGuard(t)
			if tc.mode == "missing" {
				mustDo(t, os.Remove(g.Path))
			} else {
				mustDo(t, g.Set(tc.mode))
			}
			t.Cleanup(SetDroidGuardProgramForTest(func() (string, error) { return g.Path, nil }))
			cfg := config.Default()
			cfg.Launch.DroidSecretPathsFile = g.List
			if tc.name == "missing sh" {
				t.Setenv("PATH", t.TempDir())
			}
			deadline := time.Second
			if tc.name == "expired deadline" {
				deadline = 0
			}
			got, err := resolveDroidGuard(rulesLaunch(map[string]string{"PATH": t.TempDir()}), cfg, deadline)
			if tc.kind != "" {
				assertGuardError(t, err, 0, tc.kind)
				if got.droidGuard != nil {
					t.Fatal("refusal retained checked data")
				}
				return
			}
			if err != nil || got.droidGuard == nil {
				t.Fatalf("resolve=%+v %v", got, err)
			}
			calls := g.Calls(t)
			for _, call := range calls {
				for _, key := range []string{"AGENT_DIRECTOR_INSTANCE_ID", "TMUX", "TMUX_PANE", "FACTORY_API_KEY", "OPENROUTER_API_KEY"} {
					if _, ok := call.Env[key]; ok {
						t.Errorf("probe retained %s", key)
					}
				}
				if call.Env["PATH"] != os.Getenv("PATH") {
					t.Errorf("probe used caller PATH: %q", call.Env["PATH"])
				}
			}
			if len(calls) != 2 || string(calls[0].Input) != `{"hook_event_name":"PreToolUse","tool_name":"AskUser","tool_input":{}}` || string(calls[1].Input) != `{"hook_event_name":"PreToolUse","tool_name":"TodoWrite","tool_input":{}}` {
				t.Fatalf("self-check calls=%+v", calls)
			}
		})
	}
}

// Cancel only after the Guard has started; deadline classification is covered
// by TestDroidGuardSelfCheck/expired_deadline, independently of startup speed.
func TestDroidGuardCancelledProbeStopsProgram(t *testing.T) {
	g := droidfix.NewGuard(t)
	started := g.Started(t)
	mustDo(t, g.Set(droidfix.Hang))
	command, err := composeDroidGuardCommand(g.Path, []string{"/invented/vault"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- checkDroidGuard(ctx, "/bin/sh", command) }()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("probe exited before start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("probe never started")
	}
	pid := g.PID(t, 0)
	cancel()
	select {
	case err := <-result:
		assertGuardError(t, err, 0, "the Guard self-check exceeded its deadline")
	case <-time.After(5 * time.Second):
		t.Fatal("probe cleanup did not finish")
	}
	_, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || alive {
		t.Fatalf("Guard process %d survived cancellation: alive=%v known=%v", pid, alive, known)
	}
}

// The probe itself can fail or emit output before the wrapper runs.
func TestDroidGuardProbeRunnerFailure(t *testing.T) {
	for _, tc := range []struct{ name, shell, command, kind string }{
		{"cannot start", "/invented/missing-sh", "exit 0", "the Guard self-check failed"},
		{"probe stdout", "/bin/sh", "printf x; exit 2", "the Guard self-check output failed its bounds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertGuardError(t, checkDroidGuard(context.Background(), tc.shell, tc.command), 0, tc.kind)
		})
	}
}

// The same bounded reader serves Rules and Path Lists; check the exact size
// boundary through the Guard, without opening the listed FIFO or missing path.
func TestDroidGuardBoundedListAndOtherAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := droidfix.NewGuard(t)
	t.Cleanup(SetDroidGuardProgramForTest(func() (string, error) { return g.Path, nil }))
	cfg := config.Default()
	cfg.Launch.DroidSecretPathsFile = g.List
	sentinel := filepath.Join(t.TempDir(), "listed-fifo")
	mustDo(t, syscall.Mkfifo(sentinel, 0600))
	content := sentinel + "\n#"
	content += strings.Repeat("x", (1<<20)-len(content))
	mustDo(t, os.WriteFile(g.List, []byte(content), 0600))
	if _, err := ResolveDroidGuard(rulesLaunch(nil), cfg); err != nil {
		t.Fatal(err)
	}
	mustDo(t, os.WriteFile(g.List, []byte(content+"x"), 0600))
	_, err := ResolveDroidGuard(rulesLaunch(nil), cfg)
	assertGuardError(t, err, 0, "the Path List File is not a readable regular file of at most 1 MiB")
	called := false
	t.Cleanup(SetDroidGuardProgramForTest(func() (string, error) { called = true; return "", errors.New("must not select") }))
	for _, agent := range []string{"claude", "codex", "cursor", "opencode"} {
		r := Resolved{Agent: agent}
		got, err := ResolveDroidGuard(r, cfg)
		if err != nil || !reflect.DeepEqual(got, r) || called {
			t.Fatalf("%s reached Guard: %+v %v", agent, got, err)
		}
	}
}

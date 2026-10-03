package tmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Call-mechanics tests of the production exec runner (SR-2.3, SR-2.4,
// SR-3.12): each test runs a small sh script as the tmux binary, never tmux.

// execTolerance bounds scheduling slack on top of a call's timeout and wait.
const execTolerance = 500 * time.Millisecond

// execCutShort is the FirstLine of a data call whose pipes the wait closed.
const execCutShort = "output cut short: its pipes stayed open past the pipe-close wait"

// execToken is a well-formed 16-hex label token for the create.
const execToken = "0123456789abcdef"

// execFake writes an sh script run as the tmux binary; in body, $D is its
// private dir holding "out" and "err"; pids appended to $D/gc die at cleanup.
func execFake(t *testing.T, stdout, stderr, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	for name, data := range map[string]string{"out": stdout, "err": stderr} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin = filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nD='" + dir + "'\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, pid := range execPids(filepath.Join(dir, "gc")) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return bin, dir
}

// execPids reads the pids written one per line to path (none if absent).
func execPids(path string) []int {
	data, _ := os.ReadFile(path)
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// execCreate runs the create with fixed arguments.
func execCreate(c *tmux.Client, socket string) (tmux.CreateReply, error) {
	return c.NewSession(socket, "agent-x", "/tmp", nil, []string{"true"}, execToken, "id-1", tmuxfix.StoreID)
}

// roomyTimeouts gives every class 3 s, far past the wait, with pipe-close wait w.
func roomyTimeouts(w time.Duration) tmux.Timeouts {
	return tmux.Timeouts{Query: 3 * time.Second, Action: 3 * time.Second, Create: 3 * time.Second, WaitDelay: w}
}

// execCallError returns err as a *tmux.CallError, failing the test otherwise.
func execCallError(t *testing.T, err error) *tmux.CallError {
	t.Helper()
	var ce *tmux.CallError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v (%T), want *tmux.CallError", err, err)
	}
	return ce
}

// A hung client is killed at its class timeout and reaped; the error names
// the call and the timeout in seconds, and the wait bounds held pipes.
func TestExecTimeoutTerminatesAndReaps(t *testing.T) {
	to := tmux.Timeouts{Query: 100 * time.Millisecond, Action: 200 * time.Millisecond,
		Create: 300 * time.Millisecond, WaitDelay: 50 * time.Millisecond}
	calls := []struct {
		name    string
		call    tmux.Call
		timeout time.Duration
		seconds string
		run     func(c *tmux.Client, socket string) error
	}{
		{"lookup", tmux.CallLookup, to.Query, "0.1 s", func(c *tmux.Client, s string) error { _, err := c.Lookup(s); return err }},
		{"pane kill", tmux.CallKillPane, to.Action, "0.2 s", func(c *tmux.Client, s string) error { return c.KillPane(s, "%1") }},
		{"create", tmux.CallCreate, to.Create, "0.3 s", func(c *tmux.Client, s string) error { _, err := execCreate(c, s); return err }},
	}
	hangs := []struct{ name, prefix string }{
		{"hang", ""},
		{"hang holding pipes", `sleep 5 & echo $! >> "$D/gc"` + "\n"},
	}
	for _, tc := range calls {
		for _, h := range hangs {
			t.Run(tc.name+"/"+h.name, func(t *testing.T) {
				bin, dir := execFake(t, "", "", h.prefix+`echo $$ > "$D/pid"`+"\nexec sleep 5")
				start := time.Now()
				err := tc.run(tmux.New(bin, to), "/tmp/tmux-1000/default")
				elapsed := time.Since(start)

				ce := execCallError(t, err)
				if ce.Failure != tmux.FailTimeout || ce.Call != tc.call || ce.Timeout != tc.timeout || ce.ExitStatus != -1 {
					t.Errorf("CallError = %+v, want FailTimeout on %q, Timeout %v, ExitStatus -1", *ce, tc.call, tc.timeout)
				}
				if msg := ce.Error(); !strings.Contains(msg, "tmux "+string(tc.call)) || !strings.Contains(msg, "no answer within "+tc.seconds) {
					t.Errorf("Error() = %q, want the call wording and %q", msg, tc.seconds)
				}
				if elapsed < tc.timeout || elapsed > tc.timeout+to.WaitDelay+execTolerance {
					t.Errorf("returned after %v, want between the timeout %v and it plus wait plus tolerance", elapsed, tc.timeout)
				}
				pids := execPids(filepath.Join(dir, "pid"))
				if len(pids) != 1 {
					t.Fatalf("child pid not recorded: %v", pids)
				}
				if err := syscall.Kill(pids[0], 0); !errors.Is(err, syscall.ESRCH) {
					t.Errorf("child %d still exists after the call (kill 0: %v), want terminated and reaped", pids[0], err)
				}
			})
		}
	}
}

// An exit 0 whose pipes a grandchild holds returns after the pipe-close wait:
// success for actions, FailUnrecognized for data calls, reply-driven for create.
func TestExecPipeCloseWaitPerCallKind(t *testing.T) {
	const wait = 50 * time.Millisecond
	fullReply := tmux.CreateReply{SessionID: "$1", ServerPID: 123, ServerStart: 456, PaneID: "%2", PanePID: 789}
	lookupLine := tmuxfix.SessionLine("$1", 100, 200, 300, "agent-x", "") + "\n"
	paneLine := tmuxfix.PaneLine("$1", 0, 0, "%1", 42, "") + "\n"
	cutShort := tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply-cut-short").Stdout
	createRun := func(c *tmux.Client, s string) (any, error) { return execCreate(c, s) }
	noValue := func(err error) (any, error) { return nil, err }
	cases := []struct {
		name     string
		zeroWait bool
		stdout   string
		run      func(c *tmux.Client, socket string) (any, error)
		want     any  // the value on success, when checked
		wantCut  bool // FailUnrecognized with the cut-short FirstLine
	}{
		{name: "pane kill", run: func(c *tmux.Client, s string) (any, error) { return noValue(c.KillPane(s, "%1")) }},
		{name: "session kill", run: func(c *tmux.Client, s string) (any, error) { return noValue(c.KillSessionID(s, "$1")) }},
		{name: "text", run: func(c *tmux.Client, s string) (any, error) { return noValue(c.SendKeysPane(s, "%1", "hi", false)) }},
		{name: "text and Enter", run: func(c *tmux.Client, s string) (any, error) { return noValue(c.SendKeysPane(s, "%1", "hi", true)) }},
		{name: "key send", run: func(c *tmux.Client, s string) (any, error) { return noValue(c.SendKeyPane(s, "%1", "C-u")) }},
		{name: "label by id", run: func(c *tmux.Client, s string) (any, error) {
			return noValue(c.SetLabel(s, "$1", "%1", execToken, "id-1", tmuxfix.StoreID))
		}},
		{name: "lookup", stdout: lookupLine, wantCut: true,
			run: func(c *tmux.Client, s string) (any, error) { return c.Lookup(s) }},
		{name: "pane listing", stdout: paneLine, wantCut: true,
			run: func(c *tmux.Client, s string) (any, error) { return c.ListPanes(s) }},
		{name: "capture", stdout: "pane text\n", wantCut: true,
			run: func(c *tmux.Client, s string) (any, error) { return c.CapturePaneID(s, "%1", 10, false) }},
		{name: "create full reply", stdout: tmuxfix.CreateReplyLine(fullReply), run: createRun, want: fullReply},
		{name: "create partial reply", stdout: cutShort, run: createRun, wantCut: true},
		{name: "create no reply", run: createRun, wantCut: true},
		{name: "zero wait still returns/pane kill", zeroWait: true,
			run: func(c *tmux.Client, s string) (any, error) { return noValue(c.KillPane(s, "%1")) }},
		{name: "zero wait still returns/lookup", zeroWait: true, stdout: lookupLine, wantCut: true,
			run: func(c *tmux.Client, s string) (any, error) { return c.Lookup(s) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := wait
			if tc.zeroWait {
				w = 0
			}
			to := roomyTimeouts(w)
			bin, _ := execFake(t, tc.stdout, "", `sleep 5 & echo $! >> "$D/gc"`+"\ncat \"$D/out\"\nexit 0")
			start := time.Now()
			got, err := tc.run(tmux.New(bin, to), "/tmp/tmux-1000/default")
			// Two invocations at most (text then Enter), each ending at its wait.
			if elapsed := time.Since(start); elapsed > 2*w+execTolerance {
				t.Errorf("returned after %v, want within the pipe-close wait %v (+ tolerance)", elapsed, w)
			}
			if !tc.wantCut {
				if err != nil {
					t.Fatalf("error = %v, want success", err)
				}
				if tc.want != nil && !reflect.DeepEqual(got, tc.want) {
					t.Errorf("reply = %+v, want %+v", got, tc.want)
				}
				return
			}
			ce := execCallError(t, err)
			if ce.Failure != tmux.FailUnrecognized || ce.FirstLine != execCutShort || ce.ExitStatus != 0 {
				t.Errorf("CallError = %+v, want FailUnrecognized, FirstLine %q, ExitStatus 0", *ce, execCutShort)
			}
		})
	}
}

// Standard output and standard error are captured apart: replies are
// recognised from stderr only and data is read from stdout only.
func TestExecStreamsSeparate(t *testing.T) {
	to := roomyTimeouts(50 * time.Millisecond)
	noServer := tmuxfix.NoServer("/tmp/x")
	cases := []struct {
		name           string
		stdout, stderr string
		exit           int
		run            func(c *tmux.Client, socket string) (string, error)
		wantText       string
		wantFail       tmux.Failure
		wantSocket     string
		wantLine       string
	}{
		{name: "reply on stderr, data on stdout", stdout: tmuxfix.SessionLine("$1", 100, 200, 300, "agent-x", "") + "\n",
			stderr: noServer.Stderr, exit: noServer.Exit,
			run:      func(c *tmux.Client, s string) (string, error) { _, err := c.Lookup(s); return "", err },
			wantFail: tmux.FailNoServer, wantSocket: noServer.Socket},
		{name: "reply on stdout only", stdout: noServer.Stderr, stderr: "something else\n", exit: 1,
			run:      func(c *tmux.Client, s string) (string, error) { return "", c.KillPane(s, "%1") },
			wantFail: tmux.FailUnrecognized, wantLine: "something else"},
		{name: "exit 0 data excludes stderr", stdout: "pane text\n", stderr: "warning\n",
			run:      func(c *tmux.Client, s string) (string, error) { return c.CapturePaneID(s, "%1", 10, false) },
			wantText: "pane text\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin, _ := execFake(t, tc.stdout, tc.stderr, "cat \"$D/out\"\ncat \"$D/err\" >&2\nexit "+strconv.Itoa(tc.exit))
			got, err := tc.run(tmux.New(bin, to), "/tmp/tmux-1000/default")
			if tc.wantFail == 0 {
				if err != nil || got != tc.wantText {
					t.Fatalf("got (%q, %v), want (%q, nil)", got, err, tc.wantText)
				}
				return
			}
			ce := execCallError(t, err)
			if ce.Failure != tc.wantFail || ce.Socket != tc.wantSocket || ce.FirstLine != tc.wantLine || ce.ExitStatus != tc.exit {
				t.Errorf("CallError = %+v, want %v, Socket %q, FirstLine %q, ExitStatus %d",
					*ce, tc.wantFail, tc.wantSocket, tc.wantLine, tc.exit)
			}
		})
	}
}

// A binary that cannot be run, by any path shape, gives FailUnavailable.
func TestExecBinaryNotRunIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, "not-executable")
	notProgram := filepath.Join(dir, "not-a-program")
	for path, mode := range map[string]os.FileMode{notExec: 0o644, notProgram: 0o755} {
		if err := os.WriteFile(path, []byte("no shebang, not a program\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	to := roomyTimeouts(50 * time.Millisecond)
	for name, bin := range map[string]string{
		"absolute path missing": filepath.Join(dir, "missing"),
		"relative path missing": "./no-such-tmux-binary",
		"bare name not on PATH": "no-such-tmux-binary-e4",
		"not executable":        notExec,
		"not a program":         notProgram,
		"a directory":           dir,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tmux.New(bin, to).Lookup("/tmp/tmux-1000/default")
			ce := execCallError(t, err)
			if ce.Failure != tmux.FailUnavailable || ce.ExitStatus != -1 {
				t.Errorf("CallError = %+v, want FailUnavailable, ExitStatus -1", *ce)
			}
		})
	}
}

// The child's environment is the caller's minus every AGENT_DIRECTOR_*
// variable, with locale variables exactly as they were and nothing added.
func TestExecEnvironmentStripped(t *testing.T) {
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "id-parent")
	t.Setenv("AGENT_DIRECTOR_EXTRA", "x")
	t.Setenv("AGENT_DIRECTORX", "kept")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LC_CTYPE", "")
	os.Unsetenv("LC_CTYPE") // restored by t.Setenv's cleanup

	bin, dir := execFake(t, "", "", `cat /proc/$$/environ > "$D/env"`)
	to := roomyTimeouts(50 * time.Millisecond)
	if err := tmux.New(bin, to).KillPane("/tmp/tmux-1000/default", "%1"); err != nil {
		t.Fatalf("KillPane: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")

	var want []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AGENT_DIRECTOR_") {
			want = append(want, kv)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("child environment:\n got %q\nwant %q", got, want)
	}
	for _, kv := range []string{"AGENT_DIRECTORX=kept", "LANG=C.UTF-8", "LC_ALL=de_DE.UTF-8"} {
		if !slices.Contains(got, kv) {
			t.Errorf("child environment lacks %q", kv)
		}
	}
}

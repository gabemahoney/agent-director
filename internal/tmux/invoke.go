package tmux

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// This file holds the call mechanics of the socket-taking call set (SRD
// SR-2.2 to SR-2.4, SR-3.12, SR-13.1): the argv prefix, the command-list
// argv with its separators and final-";" escape (b.ukw), the clean client
// environment, the per-class timeout, the pipe-close wait, the runner seam and
// the shaping of a run into a typed failure.

// clientEnvPrefix names the variables removed from every tmux client's
// environment (SR-2.1, SR-3.12).
const clientEnvPrefix = "AGENT_DIRECTOR_"

// Invocation is one socket-taking call as the runner seam receives it. Every
// field is observable, so argv, environment and bound assertions need no real
// tmux.
type Invocation struct {
	// Binary is the program run: the configured binary, or "tmux" on PATH.
	Binary string
	// Args is the full argv after the binary: "-u", "-S", <socket>, then the
	// call's command(s).
	Args []string
	// Env is the client's whole environment: the process environment with
	// every AGENT_DIRECTOR_* variable removed, nothing added.
	Env []string
	// Timeout is the call class's timeout (SR-13.1).
	Timeout time.Duration
	// WaitDelay is the pipe-close wait (SR-2.4).
	WaitDelay time.Duration
}

// RunStatus says how a run ended.
type RunStatus int

// The ways a run ends. The zero value is not a valid status.
const (
	// RunExited: the process exited on its own; ExitStatus holds its status.
	// A non-zero exit whose pipes had to be closed by the pipe-close wait is
	// reported this way too.
	RunExited RunStatus = iota + 1
	// RunPipesCut: the process exited 0, but its output pipes stayed open
	// (held by a process that inherited them) and were closed by the
	// pipe-close wait; the output may be incomplete (SR-2.4).
	RunPipesCut
	// RunTimedOut: the timeout expired; the process was terminated and
	// reaped.
	RunTimedOut
	// RunNotStarted: the binary could not be run (SR-2.6).
	RunNotStarted
)

// RunResult is what the runner seam returns for one Invocation.
type RunResult struct {
	// Status says how the run ended.
	Status RunStatus
	// ExitStatus is the exit status for RunExited and RunPipesCut, -1
	// otherwise.
	ExitStatus int
	// Stdout and Stderr are captured separately (SR-2.3).
	Stdout, Stderr []byte
}

// Runner is the seam every socket-taking call runs through. The production
// runner execs the binary directly; replay tests substitute one that returns
// recorded standard output, standard error and exit status.
type Runner func(Invocation) RunResult

// execRunner is the production Runner: a direct exec (no shell) with the
// given environment, terminated and reaped when the timeout expires, whose
// pipe wait is bounded by WaitDelay counted from the exit or termination.
// A WaitDelay of zero or less closes the pipes as soon as the process ends
// (exec.Cmd treats zero as "wait forever", which SR-2.4 forbids).
func execRunner(inv Invocation) RunResult {
	ctx, cancel := context.WithTimeout(context.Background(), inv.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	cmd.Env = inv.Env
	cmd.WaitDelay = max(inv.WaitDelay, time.Nanosecond)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := RunResult{ExitStatus: -1, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	state := cmd.ProcessState
	switch {
	case state == nil:
		// Start failed (not found, not executable, wrong format), or the
		// deadline passed before the process could be started.
		if ctx.Err() != nil && !isExecError(err) {
			res.Status = RunTimedOut
		} else {
			res.Status = RunNotStarted
		}
	case !state.Exited() && ctx.Err() != nil:
		res.Status = RunTimedOut
	case err == nil:
		res.Status, res.ExitStatus = RunExited, 0
	case errors.Is(err, exec.ErrWaitDelay) && state.ExitCode() == 0:
		res.Status, res.ExitStatus = RunPipesCut, 0
	default:
		res.Status, res.ExitStatus = RunExited, state.ExitCode()
	}
	return res
}

// isExecError reports whether err is a failure to run the binary itself.
func isExecError(err error) bool {
	var execErr *exec.Error
	var pathErr *os.PathError
	return errors.As(err, &execErr) || errors.As(err, &pathErr)
}

// clientEnv returns environ with every AGENT_DIRECTOR_* variable removed and
// nothing else changed (SR-2.2, SR-3.12). The result is never nil: a nil
// exec.Cmd.Env would inherit the whole process environment.
func clientEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, clientEnvPrefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// timeoutFor returns the call's class timeout (SR-13.1).
func (c *Client) timeoutFor(call Call) time.Duration {
	switch call {
	case CallLookup, CallListPanes:
		return c.timeouts.Query
	case CallCreate:
		return c.timeouts.Create
	}
	return c.timeouts.Action
}

// cmdSeparator is tmux's command separator, passed as its own argv element.
// Only commandArgv puts one in an argv.
const cmdSeparator = ";"

// commandArgv returns the argv of a tmux command list: the elements of each
// command in cmds, every one passed through escapeFinalSemicolon, with a
// standalone cmdSeparator element between one command and the next. It is
// the only way an argv element ending in ";" reaches tmux, so no element a
// caller supplies (a session name, a cwd, an -e entry, a command or claude
// argument, a send-keys text) can end its command and start another, which
// would run a tmux command of the caller's choosing (b.ukw).
func commandArgv(cmds ...[]string) []string {
	n := len(cmds)
	for _, cmd := range cmds {
		n += len(cmd)
	}
	argv := make([]string, 0, n)
	for i, cmd := range cmds {
		if i > 0 {
			argv = append(argv, cmdSeparator)
		}
		for _, a := range cmd {
			argv = append(argv, escapeFinalSemicolon(a))
		}
	}
	return argv
}

// escapeFinalSemicolon returns text with one backslash inserted before its
// final ";" when it ends in ";" (`;` becomes `\;`, `a;` becomes `a\;`, `a\;`
// becomes `a\\;`), and any other text unchanged. tmux's command parser reads
// every argv element that ends in ";" as a command separator, before any
// option parsing and so even after "--": the ";" is dropped and the
// element's text before it, if any, is the command's last argument. An
// element ending in `\;` is instead one argument with that backslash
// removed, so the escaped text reaches the command exactly as written (tmux
// 3.2a to 3.5a). A ";" anywhere else in the text is not special. Applied by
// commandArgv to every element of every command.
func escapeFinalSemicolon(text string) string {
	if !strings.HasSuffix(text, cmdSeparator) {
		return text
	}
	return text[:len(text)-len(cmdSeparator)] + `\` + cmdSeparator
}

// invoke runs one socket-taking call: "-u", "-S", socket, then the argv of
// the command list cmds (commandArgv), with the clean client environment,
// the call's class timeout and the pipe-close wait. The socket is not
// escaped: it is -S's option argument, which tmux's own option parsing
// consumes before the command parser sees the argv.
func (c *Client) invoke(call Call, socket string, cmds ...[]string) RunResult {
	argv := append([]string{"-u", "-S", socket}, commandArgv(cmds...)...)
	run := c.runCall
	if run == nil {
		run = execRunner
	}
	return run(Invocation{
		Binary:    c.binaryPath(),
		Args:      argv,
		Env:       clientEnv(os.Environ()),
		Timeout:   c.timeoutFor(call),
		WaitDelay: c.timeouts.WaitDelay,
	})
}

// runFailure shapes a run that did not end with exit 0 into its *CallError:
// the binary not run is FailUnavailable, a timeout is FailTimeout with the
// effective value, and a non-zero exit is recognised from the first line of
// standard error only. It returns nil for an exit 0, RunPipesCut included:
// what a cut-short exit 0 means depends on the call (see cutShort).
func (c *Client) runFailure(call Call, res RunResult) *CallError {
	e := &CallError{Call: call, ExitStatus: res.ExitStatus, HadStdout: len(res.Stdout) > 0}
	switch {
	case res.Status == RunNotStarted:
		e.Failure = FailUnavailable
	case res.Status == RunTimedOut:
		e.Failure, e.Timeout = FailTimeout, c.timeoutFor(call)
	case res.ExitStatus != 0:
		e.Failure, e.Socket, e.FirstLine = recognizeReply(call, res.Stderr)
	default:
		return nil
	}
	return e
}

// cutShort is the failure of a data call (lookup, pane listing, capture) that
// exited 0 with its pipes closed by the pipe-close wait: its output may be
// incomplete, so it is an unrecognised reply (SR-2.4).
func cutShort(call Call, res RunResult) *CallError {
	return &CallError{Call: call, Failure: FailUnrecognized, FirstLine: firstLineOutputCut,
		ExitStatus: res.ExitStatus, HadStdout: len(res.Stdout) > 0}
}

// runAction runs a call whose output is not used (kills, sends, label by
// id): exit 0 is success, with or without the pipe-close wait (SR-2.4).
func (c *Client) runAction(call Call, socket string, cmds ...[]string) error {
	if e := c.runFailure(call, c.invoke(call, socket, cmds...)); e != nil {
		return e
	}
	return nil
}

// runData runs a data call (lookup, pane listing, capture) and returns its
// standard output only after an exit 0 whose pipes closed on their own
// (SR-2.3, SR-2.4).
func (c *Client) runData(call Call, socket string, cmds ...[]string) ([]byte, error) {
	res := c.invoke(call, socket, cmds...)
	if e := c.runFailure(call, res); e != nil {
		return nil, e
	}
	if res.Status == RunPipesCut {
		return nil, cutShort(call, res)
	}
	return res.Stdout, nil
}

// unparseable is the failure of a data call whose exit-0 output out does not
// parse. out may be empty: a lookup always prints its server identity line,
// so its empty output does not parse (b.47f).
func unparseable(call Call, out []byte, firstLine string) *CallError {
	return &CallError{Call: call, Failure: FailUnrecognized, FirstLine: firstLine, HadStdout: len(out) > 0}
}

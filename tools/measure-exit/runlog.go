package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// actionKind classifies every agent-director and tmux call in the run log,
// so the operator can audit that only the allowed actions occurred.
type actionKind string

const (
	// actionVersion: the preflight's version reads.
	actionVersion actionKind = "version"
	// actionRead: read-only calls (get, list, tmux list-sessions/list-panes).
	actionRead actionKind = "read"
	// actionSpawn: agent-director spawn.
	actionSpawn actionKind = "spawn"
	// actionMeasured: a sample's one measured action (a pane kill, pause,
	// the natural-exit keystrokes).
	actionMeasured actionKind = "measured"
	// actionDrive: an RN-9 drive action to the harness's own row (send-keys,
	// a permission answer, pause).
	actionDrive actionKind = "drive"
	// actionTeardown: the version probe's pane kill of its one agent once
	// its hook is recorded (no time is measured from it).
	actionTeardown actionKind = "teardown"
)

// runLogFile is the run log's name in the results directory.
const runLogFile = "run-log.jsonl"

// Per-call bounds. pause itself waits for its row to end (30 s by default),
// so agent-director calls get a wide bound; tmux calls are quick.
const (
	agentDirectorCallTimeout = 3 * time.Minute
	tmuxCallTimeout          = 15 * time.Second
	// maxLoggedOutput bounds the stderr excerpt kept in an error.
	maxLoggedOutput = 512
)

// runLogEntry is one line of the run log. It carries argv, never an
// environment value; credential values are scrubbed from argv too.
type runLogEntry struct {
	Seq      int        `json:"seq"`
	Time     string     `json:"time"`
	Case     string     `json:"case"`
	Kind     actionKind `json:"kind"`
	Argv     []string   `json:"argv"`
	ExitCode int        `json:"exit_code"`
	Millis   int64      `json:"duration_ms"`
}

// runLog appends one JSON line per call. It is safe for concurrent use.
type runLog struct {
	mu  sync.Mutex
	w   io.Writer
	seq int
	scr scrubber
}

// newRunLog writes to w, scrubbing with scr.
func newRunLog(w io.Writer, scr scrubber) *runLog {
	return &runLog{w: w, scr: scr}
}

// record appends one entry; a write failure is returned so the run stops
// rather than continue unaudited.
func (l *runLog) record(at time.Time, caseID string, kind actionKind, argv []string, exitCode int, d time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	clean := make([]string, len(argv))
	for i, a := range argv {
		clean[i] = l.scr.scrub(a)
	}
	b, err := json.Marshal(runLogEntry{
		Seq: l.seq, Time: at.UTC().Format(time.RFC3339Nano), Case: caseID, Kind: kind,
		Argv: clean, ExitCode: exitCode, Millis: d.Milliseconds(),
	})
	if err != nil {
		return err
	}
	_, err = l.w.Write(append(b, '\n'))
	return err
}

// execFunc runs argv with env and returns its stdout, stderr and exit code.
// err is non-nil only when the command could not run to an exit status (not
// found, timed out); a non-zero exit is reported through exitCode.
type execFunc func(ctx context.Context, argv, env []string) (stdout, stderr []byte, exitCode int, err error)

// osExec is the production execFunc.
func osExec(ctx context.Context, argv, env []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is built by the driver
	cmd.Env = env
	cmd.Stdin = nil
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), errOut.Bytes(), 0, nil
	case ctx.Err() != nil:
		return out.Bytes(), errOut.Bytes(), -1, fmt.Errorf("timed out: %w", ctx.Err())
	case errors.As(err, &exitErr):
		return out.Bytes(), errOut.Bytes(), exitErr.ExitCode(), nil
	default:
		return out.Bytes(), errOut.Bytes(), -1, err
	}
}

// invoker runs every agent-director and tmux call of a run: with the child
// environment, a bound, and one run-log entry per call.
type invoker struct {
	log           *runLog
	exec          execFunc
	clock         clock
	scr           scrubber
	agentDirector string
	tmux          string
	// env is the child environment (environment.childEnv); extraEnv calls
	// add to it per call.
	env []string
}

// callResult is one finished call.
type callResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// callError is a call that did not exit 0. Its text carries the argv's verb
// and a scrubbed, bounded stderr excerpt.
type callError struct {
	what     string
	exitCode int
	stderr   string
	cause    error
}

func (e *callError) Error() string {
	msg := e.what
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	} else {
		msg += fmt.Sprintf(": exit %d", e.exitCode)
	}
	if e.stderr != "" {
		msg += ": " + e.stderr
	}
	return msg
}

func (e *callError) Unwrap() error { return e.cause }

// run executes argv under the bound, logs it and returns its result; a
// non-zero exit is a *callError (with the result still returned).
func (inv *invoker) run(caseID string, kind actionKind, timeout time.Duration, env []string, argv []string) (callResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := inv.clock.Now()
	stdout, stderr, code, err := inv.exec(ctx, argv, env)
	d := inv.clock.Now().Sub(start)
	if lerr := inv.log.record(start, caseID, kind, argv, code, d); lerr != nil {
		return callResult{}, fmt.Errorf("run log: %w", lerr)
	}
	res := callResult{stdout: stdout, stderr: stderr, exitCode: code}
	if err != nil || code != 0 {
		return res, &callError{what: inv.scr.scrub(argvLabel(argv)), exitCode: code, stderr: inv.excerpt(stderr), cause: err}
	}
	return res, nil
}

// agentDirectorCall runs one agent-director verb.
func (inv *invoker) agentDirectorCall(caseID string, kind actionKind, args ...string) (callResult, error) {
	return inv.run(caseID, kind, agentDirectorCallTimeout, inv.env, append([]string{inv.agentDirector}, args...))
}

// tmuxCall runs one tmux command on socket, always with -S: no call ever
// reaches a default socket.
func (inv *invoker) tmuxCall(caseID string, kind actionKind, socket string, args ...string) (callResult, error) {
	if socket == "" {
		return callResult{}, errors.New("tmux call without a socket refused")
	}
	argv := append([]string{inv.tmux, "-S", socket}, args...)
	return inv.run(caseID, kind, tmuxCallTimeout, inv.env, argv)
}

// excerpt is a scrubbed, bounded, single-line form of command output.
func (inv *invoker) excerpt(b []byte) string {
	s := strings.Join(strings.Fields(inv.scr.scrub(string(b))), " ")
	if len(s) > maxLoggedOutput {
		s = s[:maxLoggedOutput] + "…"
	}
	return s
}

// argvLabel names a call by its program and first argument(s) for errors.
func argvLabel(argv []string) string {
	n := len(argv)
	if n > 4 {
		n = 4
	}
	return strings.Join(argv[:n], " ")
}

// openRunLog creates the run log in the results directory.
func openRunLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

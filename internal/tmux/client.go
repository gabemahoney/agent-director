package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// binaryName is the program agent-director invokes. Held as a var (not a
// const) so tests can swap it for a fake-tmux helper without monkey-patching
// the exec layer.
var binaryName = "tmux"

// runner is the unit of work the client uses to materialize an *exec.Cmd. A
// custom runner is the seam tests use to capture argv without launching real
// tmux. It returns combined stdout+stderr plus the exec error.
type runner func(name string, args ...string) (stdout []byte, err error)

// defaultRunner shells out to the real tmux binary on PATH. It is the
// runtime default; tests replace this with a fake.
func defaultRunner(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		// exec returns *exec.Error when the binary cannot be located —
		// caller wants ErrTmuxNotAvailable for that case so a missing
		// install is distinguishable from "tmux ran and failed".
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return out.Bytes(), fmt.Errorf("%w: %v", ErrTmuxNotAvailable, err)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// Client is the entry point callers hold to drive tmux. It carries two
// seams: run, used by the pre-Phase-1 name-based methods (combined output, no
// timeout), and runCall, used by the socket-taking call set (separate
// streams, per-class timeout, pipe-close wait). Tests inject either without
// touching the production exec path.
type Client struct {
	run      runner
	binary   string
	timeouts Timeouts
	runCall  Runner
}

// New builds the client (Appendix F.1). binary "" means tmux on PATH;
// otherwise it is the program to run (Options.TmuxCommand). t holds the
// effective per-class timeouts and the pipe-close wait of the socket-taking
// calls; pkg/api fills it and internal/tmux defines no defaults (SR-2.4). The
// name-based methods keep their behaviour: same binary, no timeout.
func New(binary string, t Timeouts) *Client {
	c := &Client{binary: binary, timeouts: t, runCall: execRunner}
	if binary == "" {
		c.run = defaultRunner
	} else {
		c.run = func(_ string, args ...string) ([]byte, error) {
			return defaultRunner(binary, args...)
		}
	}
	return c
}

// binaryPath is the program the socket-taking calls run.
func (c *Client) binaryPath() string {
	if c.binary != "" {
		return c.binary
	}
	return binaryName
}

// HasSession returns true when `tmux has-session -t name` exits 0. Any other
// exit (including the documented "can't find session" code) returns false
// with a nil error.
//
// It matches name by prefix, as tmux's `has-session -t <name>` does: a
// session whose name merely begins with name answers true. So no verb may
// newly adopt it (resume still calls it until it moves to the lookup), and
// it must never be used for a new lookup: the socket-taking Lookup finds a
// session by its label (SRD SR-2.1, SR-3.4). Its signature, meaning and
// error contract are unchanged.
func (c *Client) HasSession(name string) (bool, error) {
	_, err := c.run(binaryName, "has-session", "-t", name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrTmuxNotAvailable) {
		return false, err
	}
	// tmux returns a non-zero exit when the session is absent; that is the
	// expected "no" answer, not an error. Surface the bool, swallow the
	// exit-code-only error.
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false, nil
	}
	return false, err
}

// KillSession sends `tmux kill-session -t name`. A non-zero tmux exit is
// surfaced as ErrTmuxKillFailed; callers that consider "session already
// gone" a no-op should consult HasSession first.
func (c *Client) KillSession(name string) error {
	out, err := c.run(binaryName, "kill-session", "-t", name)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTmuxNotAvailable) {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrTmuxKillFailed, trimOutput(out), err)
}

// paneTarget is the canonical tmux pane address agent-director uses for
// every send-keys / capture-pane invocation. tmux session creation runs
// without a window/pane suffix, so the first pane is always at index 0
// inside window 0. Pinning this here keeps callers from constructing
// ad-hoc targets and accidentally hitting a sibling pane.
func paneTarget(name string) string { return name + ":0.0" }

// SendKeys delivers text to the named tmux session's first pane. The
// text call uses `-l` (literal) so any argv element that exactly matches
// a keysym (`Enter`, `Tab`, `C-c`, `BSpace`, …) or starts with `-` is
// treated as text rather than a key event. Without `-l`, a caller that
// composes `"Enter the password"` would have tmux fire a real Enter
// keypress on the first token and submit the buffer prematurely
// (worse: a Spawn-driving Claude composing a multi-token payload could
// accidentally fire C-c into the target).
//
// When pressEnter is true a SECOND tmux send-keys call (without `-l`)
// delivers a real `Enter` keystroke as the submit. The split into two
// calls is deliberate — the trailing Enter must be a real key event so
// Claude's input handler sees the submit, while the text payload must
// be literal so its bytes are never interpreted as keystrokes.
//
// `\r` stripping in text is the verb layer's responsibility (SRD §4.3,
// reference/send-keys-research.md).
//
// On a non-zero tmux exit the error chain contains ErrTmuxSendKeys plus
// the tmux stderr blob; on a missing tmux binary, ErrTmuxNotAvailable.
// If the literal-text call succeeds but the Enter call fails, the
// buffer is already typed into the pane — the caller observes the
// error and surfaces it, but the partial state is not rolled back
// (tmux exposes no atomic "type + submit" primitive).
func (c *Client) SendKeys(name, text string, pressEnter bool) error {
	if err := c.sendKeysCall(name, []string{"-l", text}); err != nil {
		return err
	}
	if !pressEnter {
		return nil
	}
	return c.sendKeysCall(name, []string{"Enter"})
}

// sendKeysCall is the per-invocation wire to tmux send-keys. Factored
// out so the literal-text and submit-Enter paths share identical error
// shaping. payload is appended after the -t target argv.
func (c *Client) sendKeysCall(name string, payload []string) error {
	args := append([]string{"send-keys", "-t", paneTarget(name)}, payload...)
	out, err := c.run(binaryName, args...)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTmuxNotAvailable) {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrTmuxSendKeys, trimOutput(out), err)
}

// CapturePane returns the last nLines lines of the named tmux session's
// first pane.
//
// When ansi is true the invocation is
// `tmux capture-pane -p -e -t <name>:0.0 -S -<n>`: `-e` tells tmux to
// emit ANSI escape sequences for SGR colors / cursor moves so the
// caller can re-render or inspect the original styling. When ansi is
// false `-e` is omitted and tmux returns the rendered text without
// escapes; the verb-layer ANSI-strip helper still runs on top to
// scrub any residual sequences that survive the default render.
//
// nLines is passed through verbatim — callers wanting "all available
// scrollback" set it to a large number. There is no upper cap; SRD §12
// explicitly leaves the bound to the caller.
//
// On a non-zero tmux exit the error chain contains ErrTmuxCaptureFailed
// plus the tmux stderr blob; on a missing tmux binary, ErrTmuxNotAvailable.
func (c *Client) CapturePane(name string, nLines int, ansi bool) (string, error) {
	scroll := "-" + strconv.Itoa(nLines)
	args := []string{"capture-pane", "-p"}
	if ansi {
		args = append(args, "-e")
	}
	args = append(args, "-t", paneTarget(name), "-S", scroll)
	out, err := c.run(binaryName, args...)
	if err != nil {
		if errors.Is(err, ErrTmuxNotAvailable) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s: %v", ErrTmuxCaptureFailed, trimOutput(out), err)
	}
	return string(out), nil
}

// sortedEnvFlags returns env entries as KEY=VAL strings in a deterministic
// order so the argv NewSession composes is stable across runs (important
// for tests asserting exact argv slices).
func sortedEnvFlags(envs map[string]string) []string {
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+envs[k])
	}
	return out
}

// trimOutput crops a tmux stderr blob to a single line and bounded length so
// error envelopes don't carry kilobytes of pane content.
func trimOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sort"
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
// seams: run, used by the name-based HasSession (combined output, no
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
// name-based HasSession keeps its behaviour: same binary, no timeout.
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
// session whose name merely begins with name answers true. So no verb uses
// it (resume, its last user, moved to the lookup), and it must never be used
// for a new lookup: the socket-taking Lookup finds a
// session by its label (SRD SR-2.1, SR-3.4). Its signature, meaning and
// error contract are unchanged. Its argv goes through commandArgv, so a name
// ending in ";" is passed as written.
func (c *Client) HasSession(name string) (bool, error) {
	_, err := c.run(binaryName, commandArgv([]string{"has-session", "-t", name})...)
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

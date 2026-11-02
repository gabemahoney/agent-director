package tmux

import (
	"path/filepath"
	"strings"
	"time"
)

// PaneTerminalRaw reports whether the terminal of the pane paneID on socket
// takes raw input: canonical mode, echo and CR-to-NL translation all off
// (b.qcf). Raw mode alone does not show that the program is ready for input.
// With those flags set, keys are echoed and line-edited by the terminal, and
// an Enter reaches the program as LF. It reads the pane's terminal device,
// display-message -p -t <pane id> '#{pane_tty}', then that device's mode,
// opened read-only without becoming this process's controlling terminal; it
// never reads from or writes to the terminal. maxWait bounds the tmux call,
// including its pipe-close wait, without increasing either configured bound.
// A nonpositive maxWait refuses without running tmux. A failed call
// is *CallError (CallPaneTerminal), a reply that is not a clean absolute
// /dev/ path is FailUnrecognized, and a terminal that cannot be opened or
// whose mode cannot be read is that error.
func (c *Client) PaneTerminalRaw(socket, paneID string, maxWait time.Duration) (bool, error) {
	if maxWait <= 0 {
		return false, &CallError{Call: CallPaneTerminal, Failure: FailTimeout, ExitStatus: -1}
	}
	// A per-call copy keeps concurrent users' configured timeouts unchanged
	// and makes runFailure report the actual timeout of this invocation.
	bounded := *c
	bounded.timeouts.Action = min(c.timeouts.Action, maxWait)
	bounded.timeouts.WaitDelay = min(c.timeouts.WaitDelay, maxWait-bounded.timeouts.Action)
	out, err := bounded.runData(CallPaneTerminal, socket, []string{"display-message", "-p", "-t", paneID, "#{pane_tty}"})
	if err != nil {
		return false, err
	}
	tty := strings.TrimSuffix(string(out), "\n")
	if !strings.HasPrefix(tty, "/dev/") || filepath.Clean(tty) != tty {
		return false, unparseable(CallPaneTerminal, out, "pane terminal is not an absolute /dev/ path")
	}
	return terminalRaw(tty)
}

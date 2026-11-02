package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// PaneTerminalTmux is the optional terminal-mode read that ordinary Claude
// fallback input on a pending row waits on (b.qcf). Existing SendKeysTmux
// implementations remain source-compatible, but must implement this
// capability to deliver that input.
type PaneTerminalTmux interface {
	// PaneTerminalRaw reports whether the terminal of the pane paneID on
	// socket takes raw input: canonical mode, echo and CR-to-NL translation
	// all off. maxWait bounds the tmux read, including its pipe-close wait;
	// a nonpositive bound refuses without running tmux. It never writes to
	// the terminal.
	PaneTerminalRaw(socket, paneID string, maxWait time.Duration) (bool, error)
}

var _ PaneTerminalTmux = (*tmux.Client)(nil)

// waitTerminalRaw holds pending Claude fallback input until the agent's
// terminal takes raw input. Typed while a starting agent's terminal is still
// cooked, the text is echoed and line-edited there and its Enter reaches the
// agent as LF, a newline in its input box rather than a submit (b.qcf). The
// mode is read every submissionPollInterval for up to inputReadyTimeout on the
// call's clock; a failed read, or no raw mode by then, is not_started with
// nothing typed, so the same call can be made again later. A raw reply must
// arrive before the deadline, and the same budget includes revalidating the
// launch, pane and guards before any journal entry or input. Refusal details
// use the last successfully read row, without changing the pinned launch.
// Keep that state after success for later refusals and hook observation.
func (r *sendKeysRun) waitTerminalRaw(paneID string, params SendKeysParams) error {
	lastRow := r.row
	r.terminalState = lastRow.State
	end := r.j.now().Add(inputReadyTimeout)
	for n := 0; n <= int(inputReadyTimeout/submissionPollInterval); n++ {
		remaining := end.Sub(r.j.now())
		if remaining <= 0 {
			break
		}
		raw, err := r.terminal.PaneTerminalRaw(r.socket, paneID, remaining)
		if err != nil {
			return deliveryNotStarted(lastRow, "the agent's terminal mode could not be read: "+err.Error()+"; nothing was typed, so the same send-keys can be retried later")
		}
		remaining = end.Sub(r.j.now())
		if remaining <= 0 {
			break
		}
		if raw {
			row := r.row
			row.Identity = r.adoption.Identity
			id := deliveryIdentity(row, r.s.StoreID())
			row, err = r.revalidateInputRow(id, min(time.Second, remaining))
			if row.InstanceID != "" {
				lastRow = row
				r.terminalState = row.State
			}
			if err != nil {
				return deliveryNotStarted(lastRow, err.Error()+"; nothing was typed, so the same send-keys can be retried later")
			}
			if !r.j.now().Before(end) {
				break
			}
			// Pin the server found before the wait, even when its adoption
			// could not be written. A replacement server must not qualify.
			row.Identity = r.adoption.Identity
			if err := r.revalidateInputPane(row, paneID, id.PID, params); err != nil {
				return deliveryNotStarted(lastRow, err.Error()+"; nothing was typed, so the same send-keys can be retried later")
			}
			if !r.j.now().Before(end) {
				break
			}
			return nil
		}
		r.sleepInput(min(submissionPollInterval, remaining))
	}
	return deliveryNotStarted(lastRow, "the agent's terminal did not take raw input with its launch and guards verified within 12 seconds; nothing was typed, so the same send-keys can be retried later")
}

package api

import (
	"context"
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
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

// claudeSessionStartSettle leaves time for Claude's composer after SessionStart.
// In b.qcf's live Claude Code 2.1.309 measurements, input at raw onset or up to
// 30 ms after SessionStart submitted 0/6 times; at 0.59 s or later, 6/6. The
// chosen 1.5 s margin starts when this call first observes waiting.
const claudeSessionStartSettle = 1500 * time.Millisecond

// inputCancelledReason is pending Claude input's not_started reason when the
// call's context ends before input, during the wait or its admission.
const inputCancelledReason = "the call was cancelled or reached its deadline before input"

// waitTerminalRaw holds pending Claude fallback input until the agent's
// terminal takes raw input, SessionStart has made the same launch waiting,
// and claudeSessionStartSettle has passed since this call first saw waiting.
// A cooked terminal turns Enter into LF; raw mode and SessionStart alone can
// still precede Claude's composer accepting input (b.qcf). Polls are spaced by
// submissionPollInterval for up to inputReadyTimeout on the call's clock, or
// to the call context's earlier deadline. Once raw, only the row needs polling
// until the settle ends; readiness then needs a fresh raw read followed by
// row, pane and guard revalidation. Row and relay reads go through the wait's
// scoped store (inputStore) and the adopted launch identity. A failed read,
// changed launch, non-waiting state after SessionStart, cancellation or
// context expiry, or any unmet condition at the deadline is not_started with
// nothing typed, so the same call can be made again later. The deadline and
// the context are checked before each read, after each sleep (an injected
// Sleep ignores the context) and before accepting readiness, including the
// final pane ownership and guard revalidation. It runs before input admission
// and never checks the unadmitted input guard, which predates SessionStart.
// Refusal details retain the last successfully read row state, also for later
// refusals and hook observation, without changing the pinned launch.
func (r *sendKeysRun) waitTerminalRaw(paneID string, params SendKeysParams) error {
	lastRow := r.row
	r.terminalState = lastRow.State
	row := r.row
	row.Identity = r.adoption.Identity
	id := deliveryIdentity(row, r.s.StoreID())
	end := r.j.now().Add(inputReadyTimeout)
	ctxEnd := false
	if r.env.Context != nil {
		if d, ok := r.env.Context.Deadline(); ok && d.Before(end) {
			end, ctxEnd = d, true
		}
	}
	cancelled := func() bool {
		return r.env.Context != nil && r.env.Context.Err() != nil || ctxEnd && !r.j.now().Before(end)
	}
	refuse := func(reason string) error {
		if cancelled() {
			reason = inputCancelledReason
		}
		return deliveryNotStarted(lastRow, reason+"; nothing was typed, so the same send-keys can be retried later")
	}
	s, cancel, err := r.inputStore(end)
	if err != nil {
		return refuse(err.Error())
	}
	defer cancel()
	var settleUntil time.Time
	raw := false
	reason := "the agent's terminal did not take raw input within 12 seconds"
	for n := 0; n <= int(inputReadyTimeout/submissionPollInterval); n++ {
		if cancelled() {
			break
		}
		now := r.j.now()
		remaining := end.Sub(now)
		if remaining <= 0 {
			break
		}
		// While SessionStart or its settle is outstanding, a known-raw
		// terminal cannot authorize input. Recheck it once those conditions
		// can be met, rather than spend tmux reads while only the row matters.
		checkedRaw := !raw || !settleUntil.IsZero() && !now.Before(settleUntil)
		if checkedRaw {
			var err error
			raw, err = r.terminal.PaneTerminalRaw(r.socket, paneID, remaining)
			if err != nil {
				return refuse("the agent's terminal mode could not be read: " + err.Error())
			}
		}
		remaining = end.Sub(r.j.now())
		if remaining <= 0 {
			reason = "the agent's terminal raw-input check did not finish within 12 seconds"
			break
		}
		if cancelled() {
			break
		}
		row, err := r.revalidateInputRow(s, id, min(time.Second, remaining))
		if row.InstanceID != "" {
			lastRow = row
			r.terminalState = row.State
		}
		if err != nil {
			return refuse(err.Error())
		}
		if row.State == store.StatePending && !settleUntil.IsZero() {
			return refuse("the ordinary composer returned to pending before input")
		}
		now = r.j.now()
		if row.State == store.StateWaiting && settleUntil.IsZero() {
			settleUntil = now.Add(claudeSessionStartSettle)
		}
		switch {
		case !raw:
			reason = "the agent's terminal did not take raw input within 12 seconds"
		case settleUntil.IsZero():
			reason = "the agent did not report in with SessionStart within 12 seconds"
		case now.Before(settleUntil):
			reason = "the 1.5 second settle after observing SessionStart did not finish within 12 seconds"
		default:
			reason = "the agent's terminal raw-input check after the SessionStart settle did not finish within 12 seconds"
		}
		remaining = end.Sub(now)
		if remaining <= 0 {
			reason = "the agent's launch and SessionStart state could not be verified within 12 seconds"
			break
		}
		if raw && checkedRaw && !settleUntil.IsZero() && !now.Before(settleUntil) {
			reason = "the agent's launch, pane ownership and guards could not be verified within 12 seconds after terminal readiness and the SessionStart settle"
			// Pin the server found before the wait, even when its adoption
			// could not be written. A replacement server must not qualify.
			row.Identity = r.adoption.Identity
			if cancelled() {
				break
			}
			if err := r.revalidateInputPane(s, row, paneID, id.PID, params, end); err != nil {
				if errors.Is(err, errInputDeadline) {
					break
				}
				return refuse(err.Error())
			}
			if !r.j.now().Before(end) || cancelled() {
				break
			}
			return nil
		}
		r.sleepInput(min(submissionPollInterval, remaining))
	}
	return refuse(reason)
}

// terminalStateReader is input admission's row reader after waitTerminalRaw:
// a successful read becomes the last observed row state (terminalState).
type terminalStateReader struct {
	SendKeysStore
	state *string
}

// GetSpawnWithin reads the row and records its state when the read succeeds.
func (s terminalStateReader) GetSpawnWithin(instanceID string, maxWait time.Duration) (Spawn, error) {
	row, err := s.SendKeysStore.GetSpawnWithin(instanceID, maxWait)
	if err == nil {
		*s.state = row.State
	}
	return row, err
}

// terminalRefusal gives a pending Claude admission refusal the last row state
// this call read (terminalState) in place of the pending row admission compares
// against, as every refusal after waitTerminalRaw does. Cancellation or context
// expiry during admission becomes the wait's not_started refusal. Other errors,
// and every error off that path, pass unchanged.
func (r *sendKeysRun) terminalRefusal(err error) error {
	if r.terminal == nil {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = deliveryNotStarted(r.row, inputCancelledReason+"; nothing was typed, so the same send-keys can be retried later")
	}
	de, ok := err.(*DetailedError)
	if !ok {
		return err
	}
	d, ok := de.Details.(SubmissionUnconfirmedDetails)
	if !ok {
		return err
	}
	d.RowState = r.terminalState
	return &DetailedError{Err: de.Err, Details: d}
}

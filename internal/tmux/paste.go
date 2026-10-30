package tmux

import (
	"errors"
	"os"
	"strings"

	"github.com/google/uuid"
)

// PasteKeysPane delivers one literal paste, then one Enter, without retries.
// -p asks tmux to frame the paste only when the application enabled bracketed
// paste; it is not a readiness or submission acknowledgement. -r preserves LF.
// A private named buffer avoids the shared default buffer and clipboard, and
// stdin keeps payloads out of argv parsing and its size limit.
func (c *Client) PasteKeysPane(socket, paneID, text string) (err error) {
	// paste-buffer silently ignores a pane in copy mode or with input disabled.
	// Inspect tmux facts, never the application's screen. As with other pane
	// input, callers must exclude concurrent operators changing the pane.
	out, err := c.runData(CallSendText, socket, []string{"display-message", "-p", "-t", paneID, "#{pane_in_mode}\t#{pane_input_off}"})
	if err != nil {
		return err
	}
	if strings.TrimSuffix(string(out), "\n") != "0\t0" {
		return unparseable(CallSendText, out, "pane is in a mode, input is disabled, or paste state is unavailable")
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return unparseable(CallSendText, nil, "cannot allocate a private paste buffer")
	}
	buffer := "ad-input-" + id.String()
	pasted := false
	defer func() {
		if !pasted {
			// A failed or timed-out load/paste may have created the buffer.
			// Delete only this request's name; never replay input for cleanup.
			cleanup := c.runAction(CallSendText, socket, []string{"delete-buffer", "-b", buffer})
			if cleanup != nil {
				err = errors.Join(err, cleanup)
			}
		}
	}()
	res := c.invokeInput(CallSendText, socket, clientEnv(os.Environ()), []byte(text), []string{"load-buffer", "-b", buffer, "-"})
	if failure := c.runFailure(CallSendText, res); failure != nil {
		return failure
	}
	if err = c.runAction(CallSendText, socket, []string{"paste-buffer", "-p", "-r", "-d", "-b", buffer, "-t", paneID}); err != nil {
		return err
	}
	pasted = true // -d deleted our buffer; even a failed Enter must not replay it.
	return c.runAction(CallSendEnter, socket, []string{"send-keys", "-t", paneID, "Enter"})
}

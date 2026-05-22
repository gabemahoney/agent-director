package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// ReadPane captures the last N lines of a tracked Spawn's tmux pane.
// NLines=0 falls back to DefaultReadPaneLines (25). No upper cap.
func (c *Client) ReadPane(params ReadPaneParams) (ReadPaneResult, error) {
	if err := c.checkClosed(); err != nil {
		return ReadPaneResult{}, err
	}
	return internalapi.ReadPane(c.st, c.tmuxClient, params)
}

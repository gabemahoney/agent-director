package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// SendKeys sends text into a tracked Spawn's tmux pane. `\r` bytes are
// stripped; `\n` bytes are preserved; a single Enter is always appended.
func (c *Client) SendKeys(params SendKeysParams) (SendKeysResult, error) {
	if err := c.checkClosed(); err != nil {
		return SendKeysResult{}, err
	}
	return internalapi.SendKeys(c.st, c.tmuxClient, params)
}
